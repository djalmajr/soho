package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
	textutil "github.com/djalmajr/soho/internal/text"
)

var inlineConfigComment = regexp.MustCompile(`[ \t]#.*$`)
var setupPromptConfig = regexp.MustCompile(`^(multi_role=|role\.[A-Za-z0-9_-]+\.kind=|lane\.[A-Za-z0-9_-]+\.kind=)`)
var userTeamKey = regexp.MustCompile(`^(multi_role|role_[A-Za-z0-9_]+_kind|lane_[A-Za-z0-9_]+_kind)$`)

// readOrRefuse reads a project instruction or Claude settings file for the
// setup writers: only a missing file counts as absent. Any other error (an
// unreadable file, a directory in place of the file, ENOTDIR above it)
// refuses with exit 4 before any write, because a merge that could not read
// the file would overwrite what it could not see.
// ReadTextFile normalizes CRLF, so CRLF-only differences are unchanged by design.
func readOrRefuse(file string, plan bool) *string {
	raw, err := platform.ReadTextFile(file)
	if err == nil {
		return &raw
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	reason := err.Error()
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		reason = pathErr.Err.Error()
	}
	prefix, suffix := "setup: ", " (file left untouched)"
	if plan {
		prefix, suffix = "setup --plan: ", " (setup would refuse it and leave the file untouched)"
	}
	platform.DieFriction(fmt.Sprintf("%scould not read %s: %s%s", prefix, file, reason, suffix), 4)
	return nil
}

func setupWriteBlock(file string) string {
	content := readOrRefuse(file, false)
	result := setuptext.SetupBlockResult(content)
	if result == nil {
		platform.DieFriction(fmt.Sprintf("setup: %s has an unpaired soho marker; fix it by hand and run setup again (file left untouched)", file), 4)
	}
	if content != nil && *content == *result {
		return "unchanged"
	}
	had := content != nil && (strings.Contains(*content, setuptext.SetupStart) || strings.Contains(*content, setuptext.LegacySetupStart))
	if err := platform.AtomicWrite(file, *result); err != nil {
		platform.DieFriction(fmt.Sprintf("setup: could not write %s (file left untouched)", file), 4)
	}
	if had {
		return "updated"
	}
	return "written"
}

func setupWriteHooks(file string, guards ...bool) bool {
	content := readOrRefuse(file, false)
	merged := setuptext.SettingsHooksResult(content)
	if len(guards) > 0 && guards[0] {
		merged = setuptext.SettingsHooksGuardedResult(content)
	}
	if merged == nil {
		platform.DieFriction("could not merge hooks into "+file, 4)
	}
	if content != nil && *content == *merged {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		platform.DieFriction("could not merge hooks into "+file, 4)
	}
	if err := platform.AtomicWrite(file, *merged); err != nil {
		platform.DieFriction("could not merge hooks into "+file, 4)
	}
	return true
}

func printBlockResult(file string) {
	result := setupWriteBlock(file)
	if result == "unchanged" {
		_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s\n", file)
		return
	}
	_, _ = fmt.Fprintf(platform.Stdout, "block %s: %s\n", result, file)
}

func projectNeedsConfigPrompt(file string) bool {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		return true
	}
	for _, rawLine := range strings.Split(raw, "\n") {
		line := strings.TrimSpace(inlineConfigComment.ReplaceAllString(rawLine, ""))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if setupPromptConfig.MatchString(line) {
			return false
		}
	}
	return true
}

func setupTargetExisting(root string) string {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		file := filepath.Join(root, name)
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		text, err := platform.ReadTextFile(file)
		if err == nil && (strings.Contains(text, setuptext.SetupStart) || strings.Contains(text, setuptext.LegacySetupStart)) {
			return file
		}
	}
	return ""
}

func setupIsSymlink(file string) bool {
	info, err := os.Lstat(file)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func markedLines(text string) string {
	var out []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, setuptext.SetupStart) {
			inside = true
		}
		if inside {
			out = append(out, line)
		}
		if strings.Contains(line, setuptext.SetupEnd) {
			inside = false
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// Managed hook comparisons use ReadTextFile: CRLF-only differences are unchanged by design.
func writeHooksSection(root string, env platform.Env, local, guard bool, cache *setupTracking) []string {
	var ready []string
	settings := claudeSettingsPath(root, local)
	if local {
		old := readOrRefuse(settings, false)
		next := setuptext.SettingsHooksResult(old)
		if guard {
			next = setuptext.SettingsHooksGuardedResult(old)
		}
		skipped := trackedLocalChange(root, claudeLocalHookRel, env, old, next, cache)
		changed := false
		if !skipped {
			changed = setupWriteHooks(settings, guard)
		}
		ready = append(ready, claudeLocalHookRel)
		if skipped {
			printSharedHooksDestinationSkipped(root, env, cache)
		} else {
			migrateSharedHooks(root, env, cache)
		}
		if changed {
			_, _ = fmt.Fprintf(platform.Stdout, "hooks written: %s (UserPromptSubmit reminder, SessionStart doctor)\n", settings)
		} else if !skipped {
			_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s\n", settings)
		}
	} else {
		before := readOrRefuse(settings, false)
		if setuptext.SettingsHooksResult(before) == nil {
			platform.DieFriction("could not merge hooks into "+settings, 4)
		}
		after, note := canonicalClaudeHooks(root, before, false, guard)
		warnTrackedCanonical(root, settings, env, before, after, cache)
		if note != "" {
			_, _ = fmt.Fprintln(platform.Stdout, note)
		}
		if after != nil && before != nil && *after == *before && note == "" {
			_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s\n", settings)
		} else if note == "" || (after != nil && (before == nil || *after != *before)) {
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				platform.DieFriction("could not merge hooks into "+settings, 4)
			}
			if err := platform.AtomicWrite(settings, *after); err != nil {
				platform.DieFriction("could not merge hooks into "+settings, 4)
			}
			_, _ = fmt.Fprintf(platform.Stdout, "hooks written: %s (UserPromptSubmit reminder, SessionStart doctor)\n", settings)
			noteUntrackedHookFile(settings, env, false)
		}
	}
	if !local {
		file := filepath.Join(root, filepath.FromSlash(piInboundRel))
		var before *string
		if raw, err := platform.ReadTextFile(file); err == nil {
			before = &raw
		} else if !os.IsNotExist(err) {
			before = nil
			file = ""
		}
		if file != "" {
			warnTrackedCanonical(root, file, env, before, setuptext.PiInboundResult(before), cache)
		}
	}
	if writePiInboundExtension(root, env, local, cache) {
		ready = append(ready, piInboundRel)
	}
	codex := filepath.Join(root, filepath.FromSlash(codexHookRel))
	var content *string
	if raw, err := platform.ReadTextFile(codex); err == nil {
		content = &raw
	} else if !os.IsNotExist(err) {
		core.Warn("could not read hooks from "+codex+"; file left untouched", "", "setup")
		return ready
	}
	merged := setuptext.CodexHooksResult(content)
	if guard {
		merged = setuptext.CodexHooksGuardedResult(content)
	}
	if !local {
		warnTrackedCanonical(root, codex, env, content, merged, cache)
	}
	if local && trackedLocalChange(root, codexHookRel, env, content, merged, cache) {
		ready = append(ready, codexHookRel)
	} else if merged == nil {
		core.Warn("could not merge hooks into "+codex+"; file left untouched", "", "setup")
	} else if content != nil && *content == *merged {
		_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s\n", codex)
		ready = append(ready, codexHookRel)
	} else {
		if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
			platform.DieFriction("could not merge hooks into "+codex, 4)
		}
		if err := platform.AtomicWrite(codex, *merged); err != nil {
			platform.DieFriction("could not merge hooks into "+codex, 4)
		}
		_, _ = fmt.Fprintf(platform.Stdout, "hooks written: %s (UserPromptSubmit reminder + SessionStart doctor, codex)\n", codex)
		noteUntrackedHookFile(codex, env, local)
		ready = append(ready, codexHookRel)
	}
	// The generated hooks are the portable PATH commands 'soho hook
	// reminder' / 'soho hook doctor': they resolve the native binary
	// through PATH at hook time. Check that PATH with the shared platform
	// resolution over the explicit env (never the deleted skill scripts
	// paths, never the process-global HOME). Resolution only: the binary
	// is not executed here and PATH is not modified.
	if _, found := platform.FindExecutable("soho", env, platform.Current()); !found {
		core.Warn("SessionStart hook cannot resolve soho in PATH; add the native soho binary to PATH so the generated hooks ('soho hook reminder', 'soho hook doctor') can run", "", "setup")
	}
	return ready
}

// noteUntrackedHookFile tells a shared (non --local) setup that a hook file it
// just wrote is not shared yet: it prints one line when Git, queried through
// the shared reference runner (cleaned GIT_* environment, 5 s bound), lists the
// file as untracked and not ignored. A file that is tracked, ignored or outside
// a work tree, and any Git failure (missing git, timeout, non-zero status),
// print nothing: the note is advisory and never fails setup.
func noteUntrackedHookFile(file string, env platform.Env, local bool) {
	if local {
		return
	}
	res := dispatch.ReferenceGit(env, "-C", filepath.Dir(file), "ls-files", "--others", "--exclude-standard", "--", filepath.Base(file))
	if res.NotFound || res.TimedOut || res.Error != "" || res.Status == nil || *res.Status != 0 || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	_, _ = fmt.Fprintf(platform.Stdout, "note: %s is untracked; commit it to share the hooks, or run 'setup --local' to keep them out of the repository\n", file)
}

// ReadTextFile makes CRLF-only differences unchanged by design.
func writePiInboundExtension(root string, env platform.Env, local bool, cache *setupTracking) bool {
	file := filepath.Join(root, filepath.FromSlash(piInboundRel))
	var content *string
	if raw, err := platform.ReadTextFile(file); err == nil {
		content = &raw
	} else if !os.IsNotExist(err) {
		core.Warn("could not read extension from "+file+"; file left untouched", "", "setup")
		return false
	}
	next := setuptext.PiInboundResult(content)
	if local && trackedLocalChange(root, piInboundRel, env, content, next, cache) {
		return true
	}
	if next == nil {
		core.Warn("could not install extension into "+file+"; file has no soho marker and was left untouched", "", "setup")
		return false
	}
	if content != nil && *content == *next {
		_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s\n", file)
		return true
	}
	if content == nil || *content != *next {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			platform.DieFriction("could not install extension into "+file, 4)
		}
		if err := platform.AtomicWrite(file, *next); err != nil {
			platform.DieFriction("could not install extension into "+file, 4)
		}
	}
	_, _ = fmt.Fprintf(platform.Stdout, "extension ready: %s (input + session, pi)\n", file)
	return true
}

func planExtraHooks(root string, guard, local bool, env platform.Env, cache *setupTracking) {
	codex := filepath.Join(root, filepath.FromSlash(codexHookRel))
	var before *string
	raw, err := platform.ReadTextFile(codex)
	if err == nil {
		before = &raw
	} else if !os.IsNotExist(err) {
		core.Warn("could not read hooks from "+codex+"; file left untouched", "", "setup")
	}
	if err == nil || os.IsNotExist(err) {
		after := setuptext.CodexHooksResult(before)
		if guard {
			after = setuptext.CodexHooksGuardedResult(before)
		}
		if !local {
			warnTrackedCanonical(root, codex, env, before, after, cache)
		}
		if local && trackedLocalChange(root, codexHookRel, env, before, after, cache) {
		} else if after == nil {
			core.Warn("could not merge hooks into "+codex+"; file left untouched", "", "setup")
		} else if before != nil && *before == *after {
			fmt.Fprintf(platform.Stdout, "# no change: %s\n", codex)
		} else {
			fmt.Fprintf(platform.Stdout, "# would merge into %s/.codex/hooks.json: UserPromptSubmit reminder + SessionStart doctor (codex)\n", root)
		}
	}
	pi := filepath.Join(root, filepath.FromSlash(piInboundRel))
	var old *string
	raw, err = platform.ReadTextFile(pi)
	if err == nil {
		old = &raw
	} else if !os.IsNotExist(err) {
		core.Warn("could not read extension from "+pi+"; file left untouched", "", "setup")
		return
	}
	next := setuptext.PiInboundResult(old)
	if !local {
		warnTrackedCanonical(root, pi, env, old, next, cache)
	}
	if local && trackedLocalChange(root, piInboundRel, env, old, next, cache) {
	} else if next == nil {
		core.Warn("could not install extension into "+pi+"; file has no soho marker and was left untouched", "", "setup")
	} else if old != nil && *old == *next {
		fmt.Fprintf(platform.Stdout, "# no change: %s\n", pi)
	} else {
		fmt.Fprintf(platform.Stdout, "# would write %s/.pi/extensions/soho-inbound.ts: input + session extension (pi)\n", root)
	}
}

// BuildVersion is the version the CLI was built with ("dev" outside a
// release). The plugin migration installs the soho plugin at this tag.
var BuildVersion = "dev"

func CmdSetup(args []string, ctx *core.Config, env platform.Env, cwd string) {
	cache := &setupTracking{}
	if containsSetupArg(args, "--roles") {
		cmdSetupRoles(args, env, cwd)
		return
	}
	wantPlan, wantProbe := false, false
	for _, arg := range args {
		wantPlan = wantPlan || arg == "--plan"
		wantProbe = wantProbe || arg == "--probe"
	}
	if wantPlan && wantProbe {
		platform.DieFriction("setup: --probe and --plan are exclusive", 2)
	}
	implicitLocal := false
	if !wantProbe && !containsSetupArg(args, "--detect") {
		implicitLocal = implicitLocalSetup(platform.ProjectRoot(env, cwd), args, ctx, env, cache)
		// This refusal must precede migrations, presets and hook writes.
		refuseImplicitCanonicalSetup(args, ctx, env, cwd, implicitLocal)
	}
	if wantPlan {
		cmdPlan(withoutSetupArg(args, "--plan"), ctx, env, cwd, cache, implicitLocal)
		planLocal := containsSetupArg(args, "--local") || (!containsSetupArg(args, "--target") && (core.Cfg(ctx, "setup_target", "canonical", env) == "local" || implicitLocal))
		if !containsSetupArg(args, "--no-hooks") {
			planExtraHooks(platform.ProjectRoot(env, cwd), containsSetupArg(args, "--guard-hooks"), planLocal, env, cache)
			if containsSetupArg(args, "--guard-hooks") {
				_, _ = fmt.Fprintln(platform.Stdout, "# guarded Codex hooks: "+setuptext.GuardedHookCodexReminder()+"; "+setuptext.GuardedHookCodexDoctor())
			}
			if planLocal {
				root := platform.ProjectRoot(env, cwd)
				rels := append(localRels(root, ctx, env, cwd), localHookRels(root, env, cache)...)
				if plan := preflightLocalExcludes(root, rels, env, "setup", false, false); plan != nil && plan.path != "" {
					if added := missingExcludeEntries(plan.before, plan.needed); len(added) > 0 {
						_, _ = fmt.Fprintf(platform.Stdout, "# would ensure %s ignores: %s\n", plan.path, strings.Join(added, ", "))
					} else {
						_, _ = fmt.Fprintf(platform.Stdout, "# no change: %s\n", plan.path)
					}
				}
			}
		}
		return
	}
	if wantProbe {
		CmdProbe(withoutSetupArg(args, "--probe"), ctx, env, cwd)
		return
	}
	var target, panes, argTarget string
	var lanes, roles []string
	local, hooks, dry, detect, guard := false, true, false, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--target", "--panes", "--lane", "--role":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				platform.DieFriction("setup: "+a+" expects a value", 2)
			}
			i++
			v := args[i]
			if a == "--target" {
				target = v
				argTarget = v
			} else if a == "--panes" {
				panes = v
			} else if a == "--role" {
				roles = append(roles, v)
			} else {
				lanes = append(lanes, v)
			}
		case "--set", "--user-set", "--session-set":
			hint := "soho config set <key> <value>"
			if a == "--user-set" {
				hint += " --user"
			} else if a == "--session-set" {
				hint = "soho session set <key> <value>"
			}
			platform.DieFriction(fmt.Sprintf("setup: %s is a --plan option (it previews a value without writing); to write it, run '%s'", a, hint), 2)
		case "--guard-hooks":
			guard = true
		case "--no-hooks":
			hooks = false
		case "--dry-run":
			dry = true
		case "--local":
			local = true
		case "--detect":
			detect = true
		default:
			platform.DieFriction(fmt.Sprintf("setup: unknown option '%s'", a), 2)
		}
	}
	if detect {
		CmdDetect(ctx, env, cwd)
		return
	}
	root := platform.ProjectRoot(env, cwd)
	// The block, hooks, excludes and preset all land under the project root;
	// refuse them inside the skill before anything is written.
	if skill := core.StatePathInSkill(env, root); skill != "" {
		platform.DieFriction(core.StateInSkillMessage("setup", root, skill), 2)
	}
	// The argument-only checks come first: --local with --target, --panes,
	// --lane and --role. Nothing on disk may change before they can refuse.
	// The check reads the raw --target argument (argTarget): the target
	// defaulting below gives target a value in every run, and that is not
	// a conflict with --local.
	if implicitLocal {
		_, _ = fmt.Fprintln(platform.Stderr, implicitLocalNotice)
	}
	modeLocal := implicitLocal || resolveSetupMode(local, argTarget, ctx, env, "setup")
	if panes != "" && panes != "2" && panes != "3" && panes != "4" {
		platform.DieFriction("setup: --panes must be 2, 3 or 4", 2)
	}
	parsed := make([]core.LaneSpec, 0, len(lanes))
	for _, spec := range lanes {
		lane, err := core.SetupLaneSpec(spec)
		if err != nil {
			platform.DieFriction(err.Error(), 2)
		}
		parsed = append(parsed, lane)
	}
	roleSpecs := make([]core.LaneSpec, 0, len(roles))
	for _, spec := range roles {
		role, err := core.SetupRoleSpec(spec)
		if err != nil {
			platform.DieFriction(err.Error(), 2)
		}
		roleSpecs = append(roleSpecs, role)
	}
	if target == "" {
		target = setupTargetExisting(root)
		if target == "" {
			agents, claude := filepath.Join(root, "AGENTS.md"), filepath.Join(root, "CLAUDE.md")
			if info, err := os.Stat(agents); err == nil && info.Mode().IsRegular() {
				target = agents
			} else if info, err := os.Stat(claude); err == nil && info.Mode().IsRegular() && !setupIsSymlink(claude) {
				target = claude
			} else {
				target = agents
			}
		}
	}
	target = setupTargetPath(root, target)
	conf := core.ConfigFileFor("project", env, cwd) // Every no-effect refusal (an unwritable state dir or exclude file, a
	// tracked local instruction file, an unreadable or marker-broken
	// destination, an unmergeable settings file) runs before the name
	// migrations can rename anything, and again after them: the migration
	// can change the answer (setup_target=local in an old-named config).
	dest := target
	if modeLocal {
		dest = localTarget(root)
	}
	// The first pass is strictly read-only (active=false): it never probes
	// the git exclude directory, so a refusal there precedes no write at
	// all. The second pass may probe (active=!dry), and only as the last
	// preflight step, after the destination and settings are validated.
	before, preview := setupPreflight(root, ctx, env, cwd, modeLocal, dest, hooks, false, false, cache)
	// Migrate the herdr-soho and herdr-agents names. Everything after reads
	// and writes only the soho names.
	for _, line := range migrateNames(ctx, env, cwd, dry) {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	for _, line := range migratePlugin(env, BuildVersion, dry) {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	modeLocal = implicitLocal || resolveSetupMode(local, argTarget, ctx, env, "setup")
	dest = target
	if modeLocal {
		dest = localTarget(root)
	}
	before, preview = setupPreflight(root, ctx, env, cwd, modeLocal, dest, hooks, !dry, true, cache)
	if dry {
		if panes != "" {
			_, _ = fmt.Fprintf(platform.Stdout, "# would set panes=%s and the preset lanes in %s\n", panes, conf)
		}
		for _, spec := range lanes {
			_, _ = fmt.Fprintf(platform.Stdout, "# would apply --lane %s\n", textutil.QuoteControls(spec))
		}
		for _, spec := range roles {
			_, _ = fmt.Fprintf(platform.Stdout, "# would apply --role %s\n", textutil.QuoteControls(spec))
		}
	}
	if dry {
		_, _ = fmt.Fprintf(platform.Stdout, "# would write to %s\n", dest)
		legacyOnly := before != nil && strings.Contains(*before, setuptext.LegacySetupStart) && !strings.Contains(*before, setuptext.SetupStart)
		if legacyOnly {
			_, _ = fmt.Fprint(platform.Stdout, markedLines(*preview))
		} else {
			_, _ = fmt.Fprint(platform.Stdout, setuptext.SetupBlock())
		}
		if hooks {
			if guard {
				_, _ = fmt.Fprintln(platform.Stdout, "# guarded hooks: "+setuptext.GuardedHookReminder()+"; "+setuptext.GuardedHookDoctor()+"; "+setuptext.GuardedHookCodexReminder()+"; "+setuptext.GuardedHookCodexDoctor())
			}
			settings := claudeSettingsPath(root, modeLocal)
			var after *string
			var note string
			skippedLocalHooks := false
			if !modeLocal {
				before := readOrRefuse(settings, false)
				after, note = canonicalClaudeHooks(root, before, false)
				if note != "" {
					_, _ = fmt.Fprintln(platform.Stdout, note)
				}
				if note == "" || (after != nil && (before == nil || *after != *before)) {
					_, _ = fmt.Fprintf(platform.Stdout, "\n# would merge into %s: UserPromptSubmit + SessionStart hooks\n", settings)
				}
			} else {
				before := readOrRefuse(settings, false)
				after = setuptext.SettingsHooksResult(before)
				if guard {
					after = setuptext.SettingsHooksGuardedResult(before)
				}
				skippedLocalHooks = trackedLocalChange(root, claudeLocalHookRel, env, before, after, cache)
				if skippedLocalHooks {
					printSharedHooksDestinationSkipped(root, env, cache)
				} else {
					_, _ = fmt.Fprintf(platform.Stdout, "\n# would merge into %s: UserPromptSubmit + SessionStart hooks\n", settings)
				}
			}
			if modeLocal && !skippedLocalHooks {
				if shared, after := sharedHooksMigration(root); after != nil {
					if warning := sharedHooksTrackingWarning(cache.status(root, ".claude/settings.json", env)); warning != "" {
						_, _ = fmt.Fprintln(platform.Stdout, "# "+warning)
					} else {
						if sharedHooksMigrationEmpties(after) {
							_, _ = fmt.Fprintf(platform.Stdout, "# would remove %s (it holds only soho-managed hooks)\n", shared)
						} else {
							_, _ = fmt.Fprintf(platform.Stdout, "# would remove soho-managed hooks from %s\n", shared)
						}
					}
				}
			}
			_, _ = fmt.Fprintf(platform.Stdout, "# would merge into %s/.codex/hooks.json: UserPromptSubmit reminder + SessionStart doctor (codex)\n", root)
			_, _ = fmt.Fprintf(platform.Stdout, "# would write %s/.pi/extensions/soho-inbound.ts: input + session extension (pi)\n", root)
		}
		if modeLocal {
			rels := localRels(root, ctx, env, cwd)
			if hooks {
				rels = append(rels, localHookRels(root, env, cache)...)
			}
			if plan := preflightLocalExcludes(root, rels, env, "setup", false, false); plan != nil && plan.path != "" {
				added := missingExcludeEntries(plan.before, plan.needed)
				if len(added) > 0 {
					_, _ = fmt.Fprintf(platform.Stdout, "# would ensure %s ignores: %s\n", plan.path, strings.Join(added, ", "))
				}
			}
		}
		return
	}
	if !modeLocal {
		if panes != "" {
			applySetupPreset(conf, panes, parsed, env, cwd)
		} else if len(parsed) > 0 {
			// --lane without --panes applies the lanes on their own.
			writeSetupAssignments(conf, "lane", parsed, env, cwd)
		}
		writeSetupAssignments(conf, "role", roleSpecs, env, cwd)
	}
	if modeLocal {
		rels := localRels(root, ctx, env, cwd)
		path, added := ensureLocalExcludes(root, rels, env, "setup")
		if _, note := trackedClaudeBlock(root, env, cache); note != "" {
			fmt.Fprintln(platform.Stdout, note)
		} else if !trackedLocalChange(root, localInstructionFile, env, before, preview, cache) {
			printBlockResult(dest)
		}
		if len(added) > 0 {
			_, _ = fmt.Fprintf(platform.Stdout, "local excludes updated: %s (%s)\n", path, strings.Join(added, ", "))
		}
		excludeChanged := len(added) > 0
		if hooks {
			ready := writeHooksSection(root, env, true, guard, cache)
			hookPath, hookAdded := ensureLocalExcludes(root, untrackedLocalHooks(root, ready, env, cache), env, "setup")
			if len(hookAdded) > 0 {
				excludeChanged = true
				_, _ = fmt.Fprintf(platform.Stdout, "local excludes updated: %s (%s)\n", hookPath, strings.Join(hookAdded, ", "))
			}
		}
		if path != "" && !excludeChanged {
			_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s\n", path)
		}
		if panes != "" {
			applySetupPreset(conf, panes, parsed, env, cwd)
		} else if len(parsed) > 0 {
			writeSetupAssignments(conf, "lane", parsed, env, cwd)
		}
		writeSetupAssignments(conf, "role", roleSpecs, env, cwd)
		stateShown := classifyLocalState(root, ctx, env, cwd).shown
		if path == "" {
			_, _ = fmt.Fprintf(platform.Stdout, "state dir not ignored: outside a git work tree, no git exclusion was installed for %s\n", stateShown)
		} else if len(rels) > 1 {
			_, _ = fmt.Fprintf(platform.Stdout, "state dir ignored: %s\n", stateShown)
		} else {
			_, _ = fmt.Fprintf(platform.Stdout, "state dir outside the repository: %s (no repository Git exclusion is needed)\n", stateShown)
		}
		_, _ = fmt.Fprintln(platform.Stdout, "note: Claude Code reads CLAUDE.local.md; Claude Code, Codex and pi run the peer checks. Codex, pi, Grok, Cursor and agy need a separate local instruction route.")
		promptConfigNotice(ctx, conf, true)
		return
	}
	warnTrackedCanonical(root, dest, env, before, preview, cache)
	printBlockResult(dest)
	claude := filepath.Join(root, "CLAUDE.md")
	if info, err := os.Stat(claude); err == nil && info.Mode().IsRegular() && !setupIsSymlink(claude) && dest != claude {
		text, readErr := platform.ReadTextFile(claude)
		if readErr == nil && strings.Contains(text, setuptext.LegacySetupStart) && !strings.Contains(text, setuptext.SetupStart) {
			core.Warn("CLAUDE.md exists separately and still has the legacy herdr-soho block: run 'setup --target CLAUDE.md' too", "", "setup")
		} else if readErr == nil && !strings.Contains(text, setuptext.SetupStart) {
			core.Warn("CLAUDE.md exists separately and has no block: run 'setup --target CLAUDE.md' too, or make CLAUDE.md a symlink to AGENTS.md", "", "setup")
		}
	}
	if hooks {
		writeHooksSection(root, env, false, guard, cache)
		if !guard {
			_, _ = fmt.Fprintln(platform.Stdout, "soho: setup: the versioned Claude Code and Codex hooks call soho; anyone who opens a clone in those assistants without soho installed (a teammate, a contributor, an assistant run in CI) sees a hook error on every prompt; 'soho setup --guard-hooks' (needs sh) keeps those hooks quiet there, and 'soho setup --local' keeps them out of the repository")
		}
	}
	writeCanonicalStateIgnore(ctx, env, cwd, cache)
	_, _ = fmt.Fprintf(platform.Stdout, "state dir ignored: %s/\n", core.StateDirSetting(ctx, env, cwd))
	_, _ = fmt.Fprintln(platform.Stdout, "note: Claude Code, Codex and pi run the peer checks; Grok, Cursor and agy only read the instruction file.")
	promptConfigNotice(ctx, conf, false)
}

func containsSetupArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func withoutSetupArg(args []string, remove string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != remove {
			out = append(out, a)
		}
	}
	return out
}

// setupPreflight runs every setup refusal that cannot change the disk:
// the local state dir, the tracked-file check, the destination read and
// marker check, and the settings read and merge check. The git exclude
// check comes last, and only probes the exclude directory when active
// (the second pass, non-dry runs): a read or marker refusal is never
// preceded by a write, in either pass. It prints nothing, so it can run
// twice around the name migrations without duplicating output.
// postMigration marks the pass that runs after the migrations: its
// refusals may not promise that nothing changed.
func setupPreflight(root string, ctx *core.Config, env platform.Env, cwd string, modeLocal bool, dest string, hooks, active, postMigration bool, cache *setupTracking) (before, preview *string) {
	if modeLocal {
		refuseUnignorableStateDir(root, ctx, env, cwd, "setup", postMigration)
		refuseTrackedLocal(root, env, "setup", cache)
	}
	before = readOrRefuse(dest, false)
	preview = setuptext.SetupBlockResult(before)
	if preview == nil && !(modeLocal && protectedLocalFile(root, localInstructionFile, env, cache)) {
		markerNote := "file left untouched"
		if postMigration {
			markerNote += ", but files may already have changed"
		}
		platform.DieFriction(fmt.Sprintf("setup: %s has an unpaired soho marker; fix it by hand and run setup again (%s)", dest, markerNote), 4)
	}
	if hooks {
		settings := claudeSettingsPath(root, modeLocal)
		old := readOrRefuse(settings, false)
		if setuptext.SettingsHooksResult(old) == nil && !(modeLocal && protectedLocalFile(root, claudeLocalHookRel, env, cache)) {
			platform.DieFriction("could not merge hooks into "+settings, 4)
		}
		if !modeLocal {
			canonicalClaudeHooks(root, old, false)
		}
	}
	// The exclude check is the final preflight step: its write probe
	// (second pass only) and its refusals follow, never precede, the
	// destination and settings validation.
	if modeLocal {
		rels := localRels(root, ctx, env, cwd)
		if hooks {
			rels = append(rels, localHookRels(root, env, cache)...)
		}
		preflightLocalExcludes(root, rels, env, "setup", active, postMigration)
	}
	return before, preview
}

func applySetupPreset(conf, panes string, lanes []core.LaneSpec, env platform.Env, cwd string) {
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		platform.DieFriction(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", conf), 4)
	}
	for _, line := range core.ApplyLaneFile(conf, panes, env, cwd, lanes...) {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	writeSetupAssignments(conf, "lane", lanes, env, cwd)
}

func setupAssignmentPairs(scope string, specs []core.LaneSpec) [][2]string {
	var pairs [][2]string
	for _, spec := range specs {
		prefix := scope + "." + spec.Name + "."
		pairs = append(pairs, [2]string{prefix + "kind", spec.Kind})
		if spec.Model != "" {
			pairs = append(pairs, [2]string{prefix + "model", spec.Model})
		}
		if spec.Effort != "" {
			pairs = append(pairs, [2]string{prefix + "effort", spec.Effort})
		}
	}
	return pairs
}

func writeSetupAssignments(conf, scope string, specs []core.LaneSpec, env platform.Env, cwd string) {
	if len(specs) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		platform.DieFriction(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", conf), 4)
	}
	for _, pair := range setupAssignmentPairs(scope, specs) {
		core.ConfigWritePair(conf, pair[0], pair[1], env, cwd)
		_, _ = fmt.Fprintf(platform.Stdout, "set %s=%s\n", pair[0], textutil.QuoteControls(pair[1]))
	}
}

// UserLayerChoosesTeam reports whether the user's own configuration file
// already holds a team choice (the same keys projectNeedsConfigPrompt looks
// for in the project file). That choice is in effect in every project that
// does not override it, so setup has nothing to ask and a project is not
// on its first run (doctor.ProjectIsFirstRun calls this same function).
func UserLayerChoosesTeam(ctx *core.Config) bool {
	if ctx == nil {
		return false
	}
	for _, layer := range ctx.Layers {
		if layer.Source != "user" || layer.ReadError != nil {
			continue
		}
		// Entries holds the keys as the loader normalized them, so every
		// spelling the loader accepts (lane.build.kind, lane_build_kind,
		// multi-role) counts, and a commented-out line does not.
		for key := range layer.Entries {
			if userTeamKey.MatchString(key) {
				return true
			}
		}
	}
	return false
}

func promptConfigNotice(ctx *core.Config, conf string, local bool) {
	path := conf
	if !projectNeedsConfigPrompt(path) || UserLayerChoosesTeam(ctx) {
		return
	}
	flag := ""
	if local {
		flag = "--local "
	}
	subject := "project config " + path
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		subject = "this project has no config file (" + path + " does not exist), so it"
	}
	core.Warn(fmt.Sprintf("%s sets neither multi_role, any lane.<name>.kind, nor any role.<role>.kind. max_workers alone is not that choice. Orchestrator: run 'setup --detect', ask the user in their language how many panels (4 recommended, 3, or 2) and which assistant should build, review, research, and handle risky code, then run 'setup %s--panes 2|3|4 [--lane builders=kind:model:effort] [--lane reviewers=kind:model:effort] [--role researcher=kind:model:effort] [--role critical-implementer=kind:model:effort]'. If doctor reports a missing or legacy config, finish with 'doctor --fix --panes 2|3|4'.", subject, flag), "", "setup")
}

func writeCanonicalStateIgnore(ctx *core.Config, env platform.Env, cwd string, cache *setupTracking) {
	stateRoot := platform.StateProjectRoot(env, cwd)
	rel := core.StateGitignoreRel(stateRoot, core.StateRootPath(ctx, env, cwd))
	if rel == "" {
		return
	}
	result := platform.RunCli("git", []string{"-C", stateRoot, "rev-parse", "--is-inside-work-tree"}, platform.RunOptions{Env: env, Cwd: stateRoot})
	if result.Status == nil || *result.Status != 0 {
		return
	}
	if !core.GitignoreNeeds(stateRoot, rel, env) {
		_, _ = fmt.Fprintf(platform.Stdout, "unchanged: %s%s\n", filepath.Join(stateRoot, ".gitignore"), stateIgnoreSuffix(stateRoot, env, cwd))
		return
	}
	file := filepath.Join(stateRoot, ".gitignore")
	text, err := platform.ReadTextFile(file)
	if err != nil {
		text = ""
	}
	after := core.GitignoreAfter(text, rel)
	warnTrackedCanonical(stateRoot, file, env, &text, &after, cache)
	appendText := after[len(text):]
	if appendText != "" {
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
		if err != nil {
			platform.DieFriction(fmt.Sprintf("setup: could not write %s (file left untouched)", file), 4)
		}
		if _, err := f.WriteString(appendText); err != nil {
			_ = f.Close()
			platform.DieFriction(fmt.Sprintf("setup: could not write %s (file left untouched)", file), 4)
		}
		if err := f.Close(); err != nil {
			platform.DieFriction(fmt.Sprintf("setup: could not write %s (file left untouched)", file), 4)
		}
		_, _ = fmt.Fprintf(platform.Stdout, "added: %s%s\n", file, stateIgnoreSuffix(stateRoot, env, cwd))
	}
}

func stateIgnoreSuffix(stateRoot string, env platform.Env, cwd string) string {
	if filepath.Clean(stateRoot) != filepath.Clean(platform.ProjectRoot(env, cwd)) {
		return " (the state directory is shared by this repository's worktrees and lives in the main checkout)"
	}
	return ""
}
