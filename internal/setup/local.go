package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

const localInstructionFile = "CLAUDE.local.md"

const codexHookRel = ".codex/hooks.json"
const piInboundRel = ".pi/extensions/soho-inbound.ts"

type fileTracking uint8

const (
	trackingUnknown fileTracking = iota
	trackingTracked
	trackingUntracked
)

func trackingStatus(root, rel string, env platform.Env) fileTracking {
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		return trackingUnknown
	}
	cmd := exec.Command(git, "-C", root, "ls-files", "--error-unmatch", "--", rel)
	cmd.Env = append(env.List(), "LC_ALL=C", "LANG=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 && strings.HasPrefix(stderr.String(), fmt.Sprintf("error: pathspec '%s' did not match any file(s) known to git\n", rel)) {
			return trackingUntracked
		}
		return trackingUnknown
	}
	return trackingTracked
}

func hasCompleteSetupBlock(text string) bool {
	currentStarted, legacyStarted := false, false
	for _, line := range strings.Split(text, "\n") {
		if (currentStarted && strings.Contains(line, setuptext.SetupEnd)) || (legacyStarted && strings.Contains(line, setuptext.LegacySetupEnd)) {
			return true
		}
		currentStarted = currentStarted || strings.Contains(line, setuptext.SetupStart)
		legacyStarted = legacyStarted || strings.Contains(line, setuptext.LegacySetupStart)
	}
	return false
}

func refuseImplicitCanonicalSetup(args []string, ctx *core.Config, env platform.Env, cwd string) {
	if containsSetupArg(args, "--local") || containsSetupArg(args, "--target") || core.Cfg(ctx, "setup_target", "canonical", env) == "local" {
		return
	}
	root := platform.ProjectRoot(env, cwd)
	text, err := platform.ReadTextFile(localTarget(root))
	if err != nil || !hasCompleteSetupBlock(text) {
		return
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		file := filepath.Join(root, name)
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if text, err := platform.ReadTextFile(file); err == nil && hasCompleteSetupBlock(text) {
			return
		}
	}
	platform.DieFriction("setup: this project keeps the soho block in CLAUDE.local.md; run 'setup --local', 'setup --target <file>', or 'session set setup_target local' (no files were changed)", 2)
}

func untrackedLocalHooks(root string, rels []string, env platform.Env) []string {
	var untracked []string
	for _, rel := range rels {
		if trackingStatus(root, rel, env) == trackingUntracked {
			untracked = append(untracked, rel)
		}
	}
	return untracked
}

// Preview only hooks the writers can install or confirm. Excludes are applied
// after successful writes, using the writers' actual results instead.
func localHookRels(root string, env platform.Env) []string {
	var rels []string
	for _, hook := range []struct {
		rel   string
		merge func(*string) *string
	}{{claudeLocalHookRel, setuptext.SettingsHooksResult}, {codexHookRel, setuptext.CodexHooksResult}, {piInboundRel, setuptext.PiInboundResult}} {
		var content *string
		if raw, err := platform.ReadTextFile(filepath.Join(root, filepath.FromSlash(hook.rel))); err == nil {
			content = &raw
		} else if !os.IsNotExist(err) {
			continue
		}
		if hook.merge(content) != nil {
			rels = append(rels, hook.rel)
		}
	}
	return untrackedLocalHooks(root, rels, env)
}

func resolveSetupMode(local bool, target string, ctx *core.Config, env platform.Env, cmd string) bool {
	if local && target != "" {
		platform.DieFriction(cmd+": --local and --target are exclusive", 2)
	}
	if local {
		return true
	}
	return target == "" && core.Cfg(ctx, "setup_target", "canonical", env) == "local"
}

type stateClassification struct {
	kind, rel, shown, link string
	rootTarget             bool
	worktree               string
}

func localStatePath(ctx *core.Config, env platform.Env, cwd string) string {
	return filepath.Clean(core.StateRootPath(ctx, env, cwd))
}

func gitOutput(env platform.Env, root string, args ...string) (string, bool) {
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		return "", false
	}
	argv := append([]string{"-C", root}, args...)
	cmd := exec.Command(git, argv...)
	cmd.Env = env.List()
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = nil
	if cmd.Run() != nil {
		return "", false
	}
	return strings.TrimSpace(out.String()), true
}

func gitWorktrees(root string, env platform.Env) []string {
	out, ok := gitOutput(env, root, "worktree", "list", "--porcelain")
	if !ok {
		return nil
	}
	var roots []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			p := strings.TrimPrefix(line, "worktree ")
			if abs, err := filepath.Abs(p); err == nil {
				roots = append(roots, filepath.Clean(abs))
			} else {
				roots = append(roots, filepath.Clean(p))
			}
		}
	}
	return roots
}

func sameDirectory(a, b string) bool {
	sa, ea := os.Lstat(a)
	sb, eb := os.Lstat(b)
	return ea == nil && eb == nil && sa.IsDir() && sb.IsDir() && os.SameFile(sa, sb)
}

func isWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func existingReal(p string) (string, string) {
	for cur := p; ; cur = filepath.Dir(cur) {
		physical, err := filepath.EvalSymlinks(cur)
		if err == nil {
			rel, relErr := filepath.Rel(cur, p)
			if relErr == nil {
				if rel == "." {
					rel = ""
				}
				return filepath.Clean(physical), rel
			}
		}
		up := filepath.Dir(cur)
		if up == cur {
			return "", ""
		}
	}
}

func internalSymlinkPath(d string, roots []string) (string, bool) {
	vol := filepath.VolumeName(d)
	cur := vol + string(filepath.Separator)
	rest := strings.TrimPrefix(d, cur)
	parts := strings.Split(rest, string(filepath.Separator))
	for i, part := range parts {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(cur))
		if err != nil {
			continue
		}
		linkFile := filepath.Join(parent, part)
		for _, root := range roots {
			rootReal, err := filepath.EvalSymlinks(root)
			if err != nil || !isWithin(rootReal, linkFile) {
				continue
			}
			if i == len(parts)-1 {
				target, err := filepath.EvalSymlinks(cur)
				if err != nil {
					return "", false // broken link's entry is the git object
				}
				inside := false
				for _, candidate := range roots {
					candidateReal, e := filepath.EvalSymlinks(candidate)
					if e == nil && isWithin(candidateReal, target) {
						inside = true
						if target == candidateReal {
							return cur, true
						}
					}
				}
				if !inside {
					return "", false
				}
			}
			return cur, false
		}
	}
	return "", false
}

func classifyLocalState(root string, ctx *core.Config, env platform.Env, cwd string) stateClassification {
	d := localStatePath(ctx, env, cwd)
	rootAbs, _ := filepath.Abs(root)
	roots := []string{filepath.Clean(rootAbs)}
	if _, ok := gitOutput(env, root, "rev-parse", "--is-inside-work-tree"); ok {
		for _, wt := range gitWorktrees(root, env) {
			found := false
			for _, have := range roots {
				if have == wt {
					found = true
				}
			}
			if !found {
				roots = append(roots, wt)
			}
		}
	}
	// A final symlink outside the repository is itself a git object and is
	// safely hidden by its lexical path. A target within any repository worktree
	// cannot be covered by ignoring that link entry.
	for _, candidate := range roots {
		if d == candidate || !isWithin(candidate, d) {
			continue
		}
		info, err := os.Lstat(d)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, targetErr := filepath.EvalSymlinks(d)
		if targetErr == nil {
			for _, other := range roots {
				otherReal, e := filepath.EvalSymlinks(other)
				if e == nil && isWithin(otherReal, target) {
					return stateClassification{kind: "symlink", shown: d, rootTarget: target == otherReal}
				}
			}
		}
		rel, _ := filepath.Rel(candidate, d)
		if candidate == rootAbs {
			rel = filepath.ToSlash(rel)
			return stateClassification{kind: "inside", rel: rel, shown: rel + "/"}
		}
		return stateClassification{kind: "sibling", rel: filepath.ToSlash(rel), shown: d, worktree: candidate}
	}
	// Resolve the deepest existing ancestor so /tmp aliases and explicit outside
	// aliases classify by their physical destination while retaining missing tails.
	phys, tail := existingReal(d)
	if phys == "" {
		return stateClassification{kind: "external", shown: d}
	}
	best := ""
	for _, candidate := range roots {
		candidateReal, err := filepath.EvalSymlinks(candidate)
		if err == nil && isWithin(candidateReal, phys) && len(candidateReal) > len(best) {
			best = candidateReal
		}
	}
	if best == "" {
		if link, rootTarget := internalSymlinkPath(d, roots); link != "" {
			return stateClassification{kind: "symlink", shown: d, link: link, rootTarget: rootTarget}
		}
		return stateClassification{kind: "external", shown: d}
	}
	if sameDirectory(phys, best) && tail == "" {
		return stateClassification{kind: "root", shown: d}
	}
	var candidate string
	for _, item := range roots {
		r, err := filepath.EvalSymlinks(item)
		if err == nil && r == best {
			candidate = item
			break
		}
	}
	if candidate == "" {
		candidate = best
	}
	if link, rootTarget := internalSymlinkPath(d, roots); link != "" {
		return stateClassification{kind: "symlink", shown: d, link: link, rootTarget: rootTarget}
	}
	rootReal, _ := filepath.EvalSymlinks(root)
	relBase, err := filepath.Rel(best, phys)
	if err != nil || relBase == "." {
		relBase = ""
	}
	if tail != "" {
		if relBase != "" {
			relBase = filepath.Join(relBase, tail)
		} else {
			relBase = tail
		}
	}
	relBase = filepath.Clean(relBase)
	if candidateReal, _ := filepath.EvalSymlinks(candidate); candidateReal == rootReal {
		return stateClassification{kind: "inside", rel: filepath.ToSlash(relBase), shown: filepath.ToSlash(relBase) + "/"}
	}
	return stateClassification{kind: "sibling", rel: filepath.ToSlash(relBase), shown: d, worktree: candidate}
}

func localRels(root string, ctx *core.Config, env platform.Env, cwd string) []string {
	rels := []string{localInstructionFile}
	c := classifyLocalState(root, ctx, env, cwd)
	if c.kind == "inside" || c.kind == "sibling" {
		rels = append(rels, c.rel)
	}
	return rels
}

func refuseUnignorableStateDir(root string, ctx *core.Config, env platform.Env, cwd, cmdName string) {
	c := classifyLocalState(root, ctx, env, cwd)
	if c.kind == "root" {
		platform.DieFriction(fmt.Sprintf("%s: refusing to use state path '%s' because it is the root of a git work tree; a work tree root cannot be named in the git exclude file (no files were changed; point SOHO_DIR or state_dir at a directory inside — or outside — the work tree)", cmdName, c.shown), 4)
	}
	if c.kind == "symlink" {
		reason := "it is a symlink to a directory inside the work tree and ignoring the symlink entry would not cover the path its target occupies"
		if c.rootTarget {
			reason = "it is a symlink to the root of a git work tree and the effective state directory is the work tree root, which cannot be named in the git exclude file"
		} else if c.link != "" {
			reason = fmt.Sprintf("the path traverses the symlink '%s' below the work tree root and git cannot ignore a path through a symlink", c.link)
		}
		platform.DieFriction(fmt.Sprintf("%s: refusing to use state path '%s' because %s (no files were changed; point SOHO_DIR or state_dir at a directory that does not pass through an in-repo symlink)", cmdName, c.shown, reason), 4)
	}
}

func refuseTrackedLocal(root string, env platform.Env, cmdName string) {
	file := filepath.Join(root, localInstructionFile)
	if info, err := os.Lstat(file); err == nil && info.Mode()&os.ModeSymlink != 0 {
		platform.DieFriction(fmt.Sprintf("%s: %s is a symlink; refusing to write local instructions through it (file left untouched; delete the link or replace it with a regular file, then re-run)", cmdName, localInstructionFile), 4)
	}
	if _, ok := gitOutput(env, root, "ls-files", "--error-unmatch", "--", localInstructionFile); ok {
		platform.DieFriction(fmt.Sprintf("%s: %s is tracked by git; refusing to overwrite it with local instructions (file left untouched)", cmdName, localInstructionFile), 4)
	}
}

func assertSafeLocalRels(rels []string, cmdName string) {
	for _, raw := range rels {
		norm := raw
		if platform.Current() == "win32" {
			norm = strings.TrimRight(strings.ReplaceAll(norm, `\`, `/`), "/")
		} else {
			norm = strings.TrimRight(norm, "/")
		}
		for _, seg := range strings.Split(norm, "/") {
			last, _ := utf8.DecodeLastRuneInString(seg)
			if unicode.IsSpace(last) || last == '\ufeff' {
				platform.DieFriction(fmt.Sprintf("%s: refusing to ignore state path '%s' via the git exclude file because a path segment ends in whitespace (git strips it from the exclude pattern, so the directory would stay untracked) (no files were changed; set SOHO_DIR or state_dir to a plain directory name such as .soho)", cmdName, raw), 4)
			}
		}
		unsafe := strings.ContainsAny(norm, "*?[\\]\n\r")
		for _, seg := range strings.Split(norm, "/") {
			if seg == "" || seg == "." || seg == ".." {
				unsafe = true
			}
		}
		if unsafe {
			platform.DieFriction(fmt.Sprintf("%s: refusing to ignore state path '%s' via the git exclude file because it contains git-ignore metacharacters or escapes the repo root (no files were changed; set SOHO_DIR or state_dir to a plain directory name such as .soho)", cmdName, raw), 4)
		}
	}
}

func excludePath(root string, env platform.Env) string {
	if p, ok := gitOutput(env, root, "rev-parse", "--git-path", "info/exclude"); ok && p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		return filepath.Clean(p)
	}
	if p, ok := gitOutput(env, root, "rev-parse", "--absolute-git-dir"); ok && p != "" {
		return filepath.Join(p, "info", "exclude")
	}
	return ""
}

func commonGitDir(root string, env platform.Env) string {
	gd, ok := gitOutput(env, root, "rev-parse", "--absolute-git-dir")
	if !ok || gd == "" {
		return ""
	}
	if raw, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		common := strings.TrimSpace(string(raw))
		if common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(gd, common)
			}
			return filepath.Clean(common)
		}
	}
	return filepath.Clean(gd)
}

func refuseSymlinkedExclude(root string, env platform.Env, cmdName string) {
	if reason := excludeRedirect(root, env); reason != "" {
		platform.DieFriction(cmdName+": "+reason, 4)
	}
}

// excludeRedirect reports why the git exclude file cannot be written safely:
// it is a symlink, lies outside the git metadata, or is reached through a
// symlinked directory. An empty string means it is safe to write.
func excludeRedirect(root string, env platform.Env) string {
	excl := excludePath(root, env)
	if excl == "" {
		return ""
	}
	if info, err := os.Lstat(excl); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Sprintf("the git exclude file at %s is a symlink; refusing to write local excludes through it (file left untouched; replace it with a regular file, then re-run)", excl)
	}
	common := commonGitDir(root, env)
	if common == "" {
		return ""
	}
	rel, err := filepath.Rel(common, excl)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Sprintf("the git exclude file at %s is not inside the git metadata at %s; refusing to write local excludes outside it (file left untouched)", excl, common)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := common
	for _, part := range parts[:max(0, len(parts)-1)] {
		cur = filepath.Join(cur, part)
		if info, err := os.Lstat(cur); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Sprintf("the git exclude file at %s is redirected through a symlink at %s (outside the git metadata); refusing to write local excludes through it (file left untouched; replace the symlink with a real directory, then re-run)", excl, cur)
		}
	}
	return ""
}

func insideWorkTree(root string, env platform.Env) bool {
	_, ok := gitOutput(env, root, "rev-parse", "--is-inside-work-tree")
	return ok
}

func checkedExcludeStatus(root, rel string, env platform.Env) (int, bool) {
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		return -1, false
	}
	cmd := exec.Command(git, "-C", root, "check-ignore", "-q", rel)
	cmd.Env = env.List()
	err := cmd.Run()
	if err == nil {
		return 0, true
	}
	if x, ok := err.(*exec.ExitError); ok {
		return x.ExitCode(), true
	}
	return -1, false
}

func excludeEntry(rel string) string {
	return "/" + strings.TrimRight(strings.ReplaceAll(rel, `\`, "/"), "/")
}

func excludeAfter(current string, entries []string) (string, []string) {
	have := map[string]bool{}
	for _, line := range strings.Split(current, "\n") {
		have[strings.TrimSuffix(line, "\r")] = true
	}
	added := []string{}
	out := current
	for _, entry := range entries {
		if have[entry] {
			continue
		}
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += entry + "\n"
		have[entry] = true
		added = append(added, entry)
	}
	return out, added
}

type localExcludePlan struct {
	path, before, after string
	needed              []string
}

func preflightLocalExcludes(root string, rels []string, env platform.Env, cmdName string, active bool) *localExcludePlan {
	assertSafeLocalRels(rels, cmdName)
	if !insideWorkTree(root, env) {
		return nil
	}
	excl := excludePath(root, env)
	if excl == "" {
		platform.DieFriction(fmt.Sprintf("%s: cannot resolve the git exclude file under %s; local excludes are unwritable (file left untouched)", cmdName, root), 4)
	}
	refuseSymlinkedExclude(root, env, cmdName)
	needed := []string{}
	for _, rel := range rels {
		if status, ok := checkedExcludeStatus(root, rel, env); ok && status == 0 {
			continue
		}
		needed = append(needed, excludeEntry(rel))
	}
	if len(needed) == 0 {
		return &localExcludePlan{path: excl}
	}
	current, err := platform.ReadTextFile(excl)
	if os.IsNotExist(err) {
		current, err = "", nil
	}
	if err != nil {
		platform.DieFriction(fmt.Sprintf("%s: cannot read the git exclude file at %s (file left untouched)", cmdName, excl), 4)
	}
	probeDir := filepath.Dir(excl)
	for {
		st, e := os.Stat(probeDir)
		if e == nil && st.IsDir() {
			break
		}
		up := filepath.Dir(probeDir)
		if up == probeDir {
			platform.DieFriction(fmt.Sprintf("%s: cannot write the git exclude file at %s (no files were changed)", cmdName, excl), 4)
		}
		probeDir = up
	}
	if active {
		if f, err := os.CreateTemp(probeDir, ".soho-probe-"); err != nil {
			platform.DieFriction(fmt.Sprintf("%s: cannot write the git exclude file at %s (no files were changed)", cmdName, excl), 4)
		} else {
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
		}
	} else {
		if !localDirectoryWritable(probeDir) {
			platform.DieFriction(fmt.Sprintf("%s: cannot write the git exclude file at %s (no files were changed)", cmdName, excl), 4)
		}
	}
	after, _ := excludeAfter(current, needed)
	return &localExcludePlan{path: excl, before: current, after: after, needed: needed}
}

func planLocalExcludes(root string, ctx *core.Config, env platform.Env, cwd, cmdName string) *localExcludePlan {
	return preflightLocalExcludes(root, localRels(root, ctx, env, cwd), env, cmdName, false)
}

func localTarget(root string) string { return filepath.Join(root, localInstructionFile) }

func setupTargetPath(root, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(root, target)
}

func ensureLocalExcludes(root string, rels []string, env platform.Env, cmdName string) (string, []string) {
	pre := preflightLocalExcludes(root, rels, env, cmdName, true)
	if pre == nil {
		return "", nil
	}
	added := missingExcludeEntries(pre.before, pre.needed)
	if len(added) == 0 {
		return pre.path, nil
	}
	if err := os.MkdirAll(filepath.Dir(pre.path), 0o755); err != nil {
		platform.DieFriction(fmt.Sprintf("%s: could not write local excludes to %s (file left untouched)", cmdName, pre.path), 4)
	}
	if err := platform.AtomicWrite(pre.path, pre.after); err != nil {
		platform.DieFriction(fmt.Sprintf("%s: could not write local excludes to %s (file left untouched)", cmdName, pre.path), 4)
	}
	return pre.path, added
}

func missingExcludeEntries(current string, entries []string) []string {
	_, added := excludeAfter(current, entries)
	return added
}

func excludeAfterText(current string, entries []string) string {
	after, _ := excludeAfter(current, entries)
	return after
}
