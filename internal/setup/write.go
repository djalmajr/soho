package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

var inlineConfigComment = regexp.MustCompile(`[ \t]#.*$`)
var setupPromptConfig = regexp.MustCompile(`^(multi_role=|role\.[A-Za-z0-9_-]+\.kind=|lane\.[A-Za-z0-9_-]+\.kind=)`)

func setupWriteBlock(file string) string {
	var content *string
	if raw, err := platform.ReadTextFile(file); err == nil {
		content = &raw
	}
	result := setuptext.SetupBlockResult(content)
	if result == nil {
		platform.DieFriction(fmt.Sprintf("setup: produced an incomplete file for %s (file left untouched)", file), 4)
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

func setupWriteHooks(file string) {
	var content *string
	if raw, err := platform.ReadTextFile(file); err == nil {
		content = &raw
	}
	merged := setuptext.SettingsHooksResult(content)
	if merged == nil {
		platform.DieFriction("could not merge hooks into "+file, 4)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		platform.DieFriction("could not merge hooks into "+file, 4)
	}
	if err := platform.AtomicWrite(file, *merged); err != nil {
		platform.DieFriction("could not merge hooks into "+file, 4)
	}
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

func writeHooksSection(root string, env platform.Env, local bool) []string {
	var ready []string
	settings := claudeSettingsPath(root, local)
	setupWriteHooks(settings)
	if local {
		ready = append(ready, claudeLocalHookRel)
		migrateSharedHooks(root, env)
	}
	_, _ = fmt.Fprintf(platform.Stdout, "hooks written: %s (UserPromptSubmit reminder, SessionStart doctor)\n", settings)
	if writePiInboundExtension(root) {
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
	if merged := setuptext.CodexHooksResult(content); merged == nil {
		core.Warn("could not merge hooks into "+codex+"; file left untouched", "", "setup")
	} else {
		if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
			platform.DieFriction("could not merge hooks into "+codex, 4)
		}
		if err := platform.AtomicWrite(codex, *merged); err != nil {
			platform.DieFriction("could not merge hooks into "+codex, 4)
		}
		_, _ = fmt.Fprintf(platform.Stdout, "hooks written: %s (UserPromptSubmit inbound, codex)\n", codex)
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

func writePiInboundExtension(root string) bool {
	file := filepath.Join(root, filepath.FromSlash(piInboundRel))
	var content *string
	if raw, err := platform.ReadTextFile(file); err == nil {
		content = &raw
	} else if !os.IsNotExist(err) {
		core.Warn("could not read extension from "+file+"; file left untouched", "", "setup")
		return false
	}
	next := setuptext.PiInboundResult(content)
	if next == nil {
		core.Warn("could not install extension into "+file+"; file has no soho marker and was left untouched", "", "setup")
		return false
	}
	if content == nil || *content != *next {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			platform.DieFriction("could not install extension into "+file, 4)
		}
		if err := platform.AtomicWrite(file, *next); err != nil {
			platform.DieFriction("could not install extension into "+file, 4)
		}
	}
	_, _ = fmt.Fprintf(platform.Stdout, "extension ready: %s (input inbound, pi)\n", file)
	return true
}

// BuildVersion is the version the CLI was built with ("dev" outside a
// release). The plugin migration installs the soho plugin at this tag.
var BuildVersion = "dev"

func CmdSetup(args []string, ctx *core.Config, env platform.Env, cwd string) {
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
	if !wantProbe && !containsSetupArg(args, "--detect") {
		// This refusal must precede migrations, presets and hook writes.
		refuseImplicitCanonicalSetup(args, ctx, env, cwd)
	}
	if wantPlan {
		CmdPlan(withoutSetupArg(args, "--plan"), ctx, env, cwd)
		if !containsSetupArg(args, "--no-hooks") {
			_, _ = fmt.Fprintf(platform.Stdout, "# would merge into %s/.codex/hooks.json: UserPromptSubmit hook (codex)\n", platform.ProjectRoot(env, cwd))
			_, _ = fmt.Fprintf(platform.Stdout, "# would write %s/.pi/extensions/soho-inbound.ts: input extension (pi)\n", platform.ProjectRoot(env, cwd))
			if containsSetupArg(args, "--local") || (!containsSetupArg(args, "--target") && core.Cfg(ctx, "setup_target", "canonical", env) == "local") {
				root := platform.ProjectRoot(env, cwd)
				rels := append(localRels(root, ctx, env, cwd), localHookRels(root, env)...)
				if plan := preflightLocalExcludes(root, rels, env, "setup", false); plan != nil && plan.path != "" {
					if added := missingExcludeEntries(plan.before, plan.needed); len(added) > 0 {
						_, _ = fmt.Fprintf(platform.Stdout, "# would ensure %s ignores: %s\n", plan.path, strings.Join(added, ", "))
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
	var target, panes string
	var lanes []string
	local, hooks, dry, detect := false, true, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--target", "--panes", "--lane":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				platform.DieFriction("setup: "+a+" expects a value", 2)
			}
			i++
			v := args[i]
			if a == "--target" {
				target = v
			} else if a == "--panes" {
				panes = v
			} else {
				lanes = append(lanes, v)
			}
		case "--set", "--user-set", "--session-set":
			platform.DieFriction(fmt.Sprintf("setup: unknown option '%s'", a), 2)
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
	// Migrate the herdr-soho and herdr-agents names before anything else is
	// computed: everything after reads and writes only the soho names.
	for _, line := range migrateNames(ctx, env, cwd, dry) {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	for _, line := range migratePlugin(env, BuildVersion, dry) {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	modeLocal := resolveSetupMode(local, target, ctx, env, "setup")
	localFile := ""
	if modeLocal {
		localFile = localTarget(root)
		refuseUnignorableStateDir(root, ctx, env, cwd, "setup")
		refuseTrackedLocal(root, env, "setup")
		rels := localRels(root, ctx, env, cwd)
		if hooks {
			rels = append(rels, localHookRels(root, env)...)
		}
		preflightLocalExcludes(root, rels, env, "setup", !dry)
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
	conf := core.ConfigFileFor("project", env, cwd)
	dest := target
	if modeLocal {
		dest = localFile
	}
	var before *string
	if raw, err := platform.ReadTextFile(dest); err == nil {
		before = &raw
	}
	preview := setuptext.SetupBlockResult(before)
	if preview == nil {
		platform.DieFriction(fmt.Sprintf("setup: produced an incomplete file for %s (file left untouched)", dest), 4)
	}
	if hooks {
		settings := claudeSettingsPath(root, modeLocal)
		var old *string
		if raw, err := platform.ReadTextFile(settings); err == nil {
			old = &raw
		}
		if setuptext.SettingsHooksResult(old) == nil {
			platform.DieFriction("could not merge hooks into "+settings, 4)
		}
	}
	if panes != "" && dry {
		_, _ = fmt.Fprintf(platform.Stdout, "# would set panes=%s and the preset lanes in %s\n", panes, conf)
		for _, spec := range lanes {
			_, _ = fmt.Fprintf(platform.Stdout, "# would apply --lane %s\n", spec)
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
			_, _ = fmt.Fprintf(platform.Stdout, "\n# would merge into %s: UserPromptSubmit + SessionStart hooks\n", claudeSettingsPath(root, modeLocal))
			if modeLocal {
				if shared, after := sharedHooksMigration(root); after != nil {
					if warning := sharedHooksTrackingWarning(trackingStatus(root, ".claude/settings.json", env)); warning != "" {
						_, _ = fmt.Fprintln(platform.Stdout, "# "+warning)
					} else {
						_, _ = fmt.Fprintf(platform.Stdout, "# would remove soho-managed hooks from %s\n", shared)
					}
				}
			}
			_, _ = fmt.Fprintf(platform.Stdout, "# would merge into %s/.codex/hooks.json: UserPromptSubmit hook (codex)\n", root)
			_, _ = fmt.Fprintf(platform.Stdout, "# would write %s/.pi/extensions/soho-inbound.ts: input extension (pi)\n", root)
		}
		if modeLocal {
			rels := localRels(root, ctx, env, cwd)
			if hooks {
				rels = append(rels, localHookRels(root, env)...)
			}
			if plan := preflightLocalExcludes(root, rels, env, "setup", false); plan != nil && plan.path != "" {
				added := missingExcludeEntries(plan.before, plan.needed)
				if len(added) > 0 {
					_, _ = fmt.Fprintf(platform.Stdout, "# would ensure %s ignores: %s\n", plan.path, strings.Join(added, ", "))
				}
			}
		}
		return
	}
	if panes != "" && !modeLocal {
		applySetupPreset(conf, panes, parsed, env, cwd)
	}
	if modeLocal {
		rels := localRels(root, ctx, env, cwd)
		path, added := ensureLocalExcludes(root, rels, env, "setup")
		_, _ = fmt.Fprintf(platform.Stdout, "block %s: %s\n", setupWriteBlock(localFile), localFile)
		if len(added) > 0 {
			_, _ = fmt.Fprintf(platform.Stdout, "local excludes updated: %s (%s)\n", path, strings.Join(added, ", "))
		}
		if hooks {
			ready := writeHooksSection(root, env, true)
			hookPath, hookAdded := ensureLocalExcludes(root, untrackedLocalHooks(root, ready, env), env, "setup")
			if len(hookAdded) > 0 {
				_, _ = fmt.Fprintf(platform.Stdout, "local excludes updated: %s (%s)\n", hookPath, strings.Join(hookAdded, ", "))
			}
		}
		if panes != "" {
			applySetupPreset(conf, panes, parsed, env, cwd)
		}
		stateShown := classifyLocalState(root, ctx, env, cwd).shown
		if path == "" {
			_, _ = fmt.Fprintf(platform.Stdout, "state dir not ignored: outside a git work tree, no git exclusion was installed for %s\n", stateShown)
		} else if len(rels) > 1 {
			_, _ = fmt.Fprintf(platform.Stdout, "state dir ignored: %s\n", stateShown)
		} else {
			_, _ = fmt.Fprintf(platform.Stdout, "state dir outside the repository: %s (no repository Git exclusion is needed)\n", stateShown)
		}
		_, _ = fmt.Fprintln(platform.Stdout, "note: Claude Code reads CLAUDE.local.md; Claude Code, Codex and pi run the peer checks. Codex, pi, Grok, Cursor and agy need a separate local instruction route.")
		promptConfigNotice(conf, true)
		return
	}
	_, _ = fmt.Fprintf(platform.Stdout, "block %s: %s\n", setupWriteBlock(target), target)
	claude := filepath.Join(root, "CLAUDE.md")
	if info, err := os.Stat(claude); err == nil && info.Mode().IsRegular() && !setupIsSymlink(claude) && target != claude {
		text, readErr := platform.ReadTextFile(claude)
		if readErr == nil && strings.Contains(text, setuptext.LegacySetupStart) && !strings.Contains(text, setuptext.SetupStart) {
			core.Warn("CLAUDE.md exists separately and still has the legacy herdr-soho block: run 'setup --target CLAUDE.md' too", "", "setup")
		} else if readErr == nil && !strings.Contains(text, setuptext.SetupStart) {
			core.Warn("CLAUDE.md exists separately and has no block: run 'setup --target CLAUDE.md' too, or make CLAUDE.md a symlink to AGENTS.md", "", "setup")
		}
	}
	if hooks {
		writeHooksSection(root, env, false)
	}
	writeCanonicalStateIgnore(ctx, env, cwd)
	_, _ = fmt.Fprintf(platform.Stdout, "state dir ignored: %s/\n", core.StateDirSetting(ctx, env, cwd))
	_, _ = fmt.Fprintln(platform.Stdout, "note: Claude Code, Codex and pi run the peer checks; Grok, Cursor and agy only read the instruction file.")
	promptConfigNotice(conf, false)
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

func applySetupPreset(conf, panes string, lanes []core.LaneSpec, env platform.Env, cwd string) {
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		platform.DieFriction(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", conf), 4)
	}
	for _, line := range core.ApplyLaneFile(conf, panes, env, cwd) {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	for _, lane := range lanes {
		core.ConfigWritePair(conf, "lane."+lane.Name+".kind", lane.Kind, env, cwd)
		_, _ = fmt.Fprintf(platform.Stdout, "set lane.%s.kind=%s\n", lane.Name, lane.Kind)
		if lane.Model != "" {
			core.ConfigWritePair(conf, "lane."+lane.Name+".model", lane.Model, env, cwd)
			_, _ = fmt.Fprintf(platform.Stdout, "set lane.%s.model=%s\n", lane.Name, lane.Model)
		}
		if lane.Effort != "" {
			core.ConfigWritePair(conf, "lane."+lane.Name+".effort", lane.Effort, env, cwd)
			_, _ = fmt.Fprintf(platform.Stdout, "set lane.%s.effort=%s\n", lane.Name, lane.Effort)
		}
	}
}

func promptConfigNotice(conf string, local bool) {
	path := conf
	if !projectNeedsConfigPrompt(path) {
		return
	}
	flag := ""
	if local {
		flag = "--local "
	}
	core.Warn(fmt.Sprintf("project config %s sets neither multi_role, any lane.<name>.kind, nor any role.<role>.kind. max_workers alone is not that choice. Orchestrator: run 'setup --detect', ask the user in their language how many agents at once (4 recommended, 3, or 2) and which detected assistant should implement, review, and research — do not say lane, kind, or panes to them — then run 'setup %s--panes 2|3|4 [--lane name=kind:model:effort]'. If doctor reports a missing or legacy config, finish with 'doctor --fix --panes 2|3|4'.", path, flag), "", "setup")
}

func writeCanonicalStateIgnore(ctx *core.Config, env platform.Env, cwd string) {
	stateRoot := platform.StateProjectRoot(env, cwd)
	rel := core.StateGitignoreRel(stateRoot, core.StateRootPath(ctx, env, cwd))
	if rel == "" {
		return
	}
	result := platform.RunCli("git", []string{"-C", stateRoot, "rev-parse", "--is-inside-work-tree"}, platform.RunOptions{Env: env, Cwd: stateRoot})
	if result.Status == nil || *result.Status != 0 || !core.GitignoreNeeds(stateRoot, rel, env) {
		return
	}
	file := filepath.Join(stateRoot, ".gitignore")
	text, err := platform.ReadTextFile(file)
	if err != nil {
		text = ""
	}
	appendText := core.GitignoreAfter(text, rel)[len(text):]
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
	}
}
