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

// implicitLocalNotice is the one stderr line printed when setup chooses the
// local setup in a repository that tracks no soho file.
const implicitLocalNotice = "setup: no soho file is tracked in this repository, so this wrote the local setup (files only you see); to write tracked files, run 'soho setup --target AGENTS.md'"

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

func hasGitMetadata(root string) bool {
	dir, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// setupTracking is scoped to a single setup invocation.
type setupTracking struct {
	values map[string]fileTracking
}

func (c *setupTracking) status(root, rel string, env platform.Env) fileTracking {
	if c == nil {
		return trackingStatus(root, rel, env)
	}
	if c.values == nil {
		c.values = make(map[string]fileTracking)
	}
	key := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if value, ok := c.values[key]; ok {
		return value
	}
	value := trackingStatus(root, rel, env)
	if !hasGitMetadata(root) && rel != ".claude/settings.json" {
		value = trackingUntracked
	}
	c.values[key] = value
	return value
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

// trackedSohoFile reports whether the repository tracks a file that belongs
// to a soho setup: a complete instruction block, one of the hooks setup
// writes, the project config, or a role file. Every status comes from the
// setup-wide tracking cache; an unresolvable status keeps the old behavior.
func trackedSohoFile(root string, env platform.Env, cache *setupTracking) bool {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		status := cache.status(root, name, env)
		if status == trackingUnknown {
			return true
		}
		if status != trackingTracked {
			continue
		}
		text, err := platform.ReadTextFile(filepath.Join(root, name))
		if err != nil || hasCompleteSetupBlock(text) {
			return true
		}
	}
	// The role directory is queried as one pathspec: git matches every file
	// below it, including files no longer present in the working tree.
	for _, rel := range []string{claudeLocalHookRel, codexHookRel, piInboundRel, ".agents/soho.conf", ".agents/soho-roles"} {
		switch cache.status(root, rel, env) {
		case trackingTracked:
			return true
		case trackingUnknown:
			return true
		}
	}
	return false
}

// implicitLocalSetup decides whether a setup call without an explicit target
// writes the local setup because this git repository tracks no soho file.
// An explicit setup_target (including canonical) keeps the configured choice.
func implicitLocalSetup(root string, args []string, ctx *core.Config, env platform.Env, cache *setupTracking) bool {
	if containsSetupArg(args, "--local") || containsSetupArg(args, "--target") {
		return false
	}
	if core.Cfg(ctx, "setup_target", "canonical", env) == "local" || core.ConfigExplicit(ctx, "setup_target", env) {
		return false
	}
	if !hasGitMetadata(root) {
		return false
	}
	return !trackedSohoFile(root, env, cache)
}

func refuseImplicitCanonicalSetup(args []string, ctx *core.Config, env platform.Env, cwd string, implicitLocal bool) {
	if implicitLocal {
		return
	}
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

func untrackedLocalHooks(root string, rels []string, env platform.Env, caches ...*setupTracking) []string {
	var cache *setupTracking
	if len(caches) > 0 {
		cache = caches[0]
	}
	var untracked []string
	for _, rel := range rels {
		if cache.status(root, rel, env) == trackingUntracked {
			untracked = append(untracked, rel)
		}
	}
	return untracked
}

// Preview only hooks the writers can install or confirm. Excludes are applied
// after successful writes, using the writers' actual results instead.
func localHookRels(root string, env platform.Env, cache *setupTracking) []string {
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
	return untrackedLocalHooks(root, rels, env, cache)
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

// changedNote is the state clause of the no-effect refusal messages: the
// pre-migration pass can promise that nothing changed; the post-migration
// pass, and the write-phase probes, may not, because the name migrations —
// or an earlier write — may already have changed files.
func changedNote(postMigration bool) string {
	if postMigration {
		return "files may already have changed"
	}
	return "no files were changed"
}

func refuseUnignorableStateDir(root string, ctx *core.Config, env platform.Env, cwd, cmdName string, postMigration bool) {
	c := classifyLocalState(root, ctx, env, cwd)
	if c.kind == "root" {
		platform.DieFriction(fmt.Sprintf("%s: refusing to use state path '%s' because it is the root of a git work tree; a work tree root cannot be named in the git exclude file (%s; point SOHO_DIR or state_dir at a directory inside — or outside — the work tree)", cmdName, c.shown, changedNote(postMigration)), 4)
	}
	if c.kind == "symlink" {
		reason := "it is a symlink to a directory inside the work tree and ignoring the symlink entry would not cover the path its target occupies"
		if c.rootTarget {
			reason = "it is a symlink to the root of a git work tree and the effective state directory is the work tree root, which cannot be named in the git exclude file"
		} else if c.link != "" {
			reason = fmt.Sprintf("the path traverses the symlink '%s' below the work tree root and git cannot ignore a path through a symlink", c.link)
		}
		platform.DieFriction(fmt.Sprintf("%s: refusing to use state path '%s' because %s (%s; point SOHO_DIR or state_dir at a directory that does not pass through an in-repo symlink)", cmdName, c.shown, reason, changedNote(postMigration)), 4)
	}
}

func refuseTrackedLocal(root string, env platform.Env, cmdName string, cache *setupTracking) {
	if _, note := trackedClaudeBlock(root, env, cache); note != "" {
		return
	}
	file := filepath.Join(root, localInstructionFile)
	if info, err := os.Lstat(file); err == nil && info.Mode()&os.ModeSymlink != 0 {
		platform.DieFriction(fmt.Sprintf("%s: %s is a symlink; refusing to write local instructions through it (file left untouched; delete the link or replace it with a regular file, then re-run)", cmdName, localInstructionFile), 4)
	}
}

func assertSafeLocalRels(rels []string, cmdName string, postMigration bool) {
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
				platform.DieFriction(fmt.Sprintf("%s: refusing to ignore state path '%s' via the git exclude file because a path segment ends in whitespace (git strips it from the exclude pattern, so the directory would stay untracked) (%s; set SOHO_DIR or state_dir to a plain directory name such as .soho)", cmdName, raw, changedNote(postMigration)), 4)
			}
		}
		unsafe := strings.ContainsAny(norm, "*?[\\]\n\r")
		for _, seg := range strings.Split(norm, "/") {
			if seg == "" || seg == "." || seg == ".." {
				unsafe = true
			}
		}
		if unsafe {
			platform.DieFriction(fmt.Sprintf("%s: refusing to ignore state path '%s' via the git exclude file because it contains git-ignore metacharacters or escapes the repo root (%s; set SOHO_DIR or state_dir to a plain directory name such as .soho)", cmdName, raw, changedNote(postMigration)), 4)
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
	out, ok := gitOutput(env, root, "rev-parse", "--is-inside-work-tree")
	return ok && strings.EqualFold(strings.TrimSpace(out), "true")
}

func explicitGitLayout(env platform.Env) bool {
	return env.Get("GIT_DIR") != "" || env.Get("GIT_WORK_TREE") != ""
}

func checkedExcludeStatus(root, rel string, env platform.Env) (int, bool) {
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		return -1, false
	}
	cmd := exec.Command(git, "-C", root, "check-ignore", "-q", "--", rel)
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
	ending := "\n"
	if strings.Contains(current, "\r\n") {
		ending = "\r\n"
	}
	added := []string{}
	out := current
	for _, entry := range entries {
		if have[entry] {
			continue
		}
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += ending
		}
		out += entry + ending
		have[entry] = true
		added = append(added, entry)
	}
	return out, added
}

type localExcludePlan struct {
	path, before, after string
	needed              []string
}

// createExcludeProbe is the real write test of the git exclude directory. The
// permission check before it only sees the mode bits, which on Windows do not
// show the ACL, so the second preflight pass still needs this write.
var createExcludeProbe = os.CreateTemp

// excludeLockAcquired observes a successful common-exclude lock acquisition.
// It exists so a contention fixture can prove the lock is actually taken.
var excludeLockAcquired = func() {}

func preflightLocalExcludes(root string, rels []string, env platform.Env, cmdName string, active, postMigration bool) *localExcludePlan {
	assertSafeLocalRels(rels, cmdName, postMigration)
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
	// Exact bytes: DecodeText/ReadTextFile would normalize CRLF and invalid
	// UTF-8, so the in-place writer would see a mismatch against the inode.
	raw, err := os.ReadFile(excl)
	if os.IsNotExist(err) {
		raw, err = nil, nil
	}
	current := string(raw)
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
			platform.DieFriction(fmt.Sprintf("%s: cannot write the git exclude file at %s (%s)", cmdName, excl, changedNote(postMigration)), 4)
		}
		probeDir = up
	}
	if active {
		if f, err := createExcludeProbe(probeDir, ".soho-probe-"); err != nil {
			platform.DieFriction(fmt.Sprintf("%s: cannot write the git exclude file at %s (%s)", cmdName, excl, changedNote(postMigration)), 4)
		} else {
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
		}
	} else {
		if !localDirectoryWritable(probeDir) {
			platform.DieFriction(fmt.Sprintf("%s: cannot write the git exclude file at %s (%s)", cmdName, excl, changedNote(postMigration)), 4)
		}
	}
	after, _ := excludeAfter(current, needed)
	return &localExcludePlan{path: excl, before: current, after: after, needed: needed}
}

func planLocalExcludes(root string, ctx *core.Config, env platform.Env, cwd, cmdName string) *localExcludePlan {
	return preflightLocalExcludes(root, localRels(root, ctx, env, cwd), env, cmdName, false, false)
}

func localTarget(root string) string { return filepath.Join(root, localInstructionFile) }

func setupTargetPath(root, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(root, target)
}

func ensureLocalExcludes(root string, rels []string, env platform.Env, cmdName string) (string, []string) {
	// The optimistic pass is read-only: the existing setup probe-count gate
	// admits exactly one active probe, taken inside the lock below.
	pre := preflightLocalExcludes(root, rels, env, cmdName, false, true)
	if pre == nil {
		return "", nil
	}
	if len(missingExcludeEntries(pre.before, pre.needed)) == 0 {
		return pre.path, nil // already ignored: keep the no-write plan pure
	}
	var resultPath string
	var added []string
	if reason := lockedCommonExclude(root, env, cmdName, false, func(file string) string {
		current := preflightLocalExcludes(root, rels, env, cmdName, true, true)
		if current == nil {
			return ""
		}
		resultPath = current.path
		added = missingExcludeEntries(current.before, current.needed)
		if len(added) == 0 {
			return ""
		}
		if err := writeSharedExclude(current.path, current.before, current.after, true); err != nil {
			return fmt.Sprintf("%s: could not update local excludes at %s: %v (verify the file)", cmdName, current.path, err)
		}
		return ""
	}); reason != "" {
		platform.DieFriction(reason, 4)
	}
	return resultPath, added
}

// lockedCommonExclude serializes one read/modify/replace of the common Git
// exclude for setup, init and migrations. The lock lives in the validated
// common Git metadata, so a repository with no info directory still has a
// parent, and every alias or linked worktree contends on the same file.
// The callback runs after a fresh redirect check and under the held lock.
func lockedCommonExclude(root string, env platform.Env, cmdName string, dry bool, fn func(file string) string) string {
	file := excludePath(root, env)
	if file == "" {
		return fmt.Sprintf("%s: cannot resolve the git exclude file under %s; local excludes are unwritable (file left untouched)", cmdName, root)
	}
	if reason := excludeRedirect(root, env); reason != "" {
		return reason
	}
	if dry {
		return fn(file)
	}
	// Create an absent validated metadata parent before taking the lock. A
	// shared repo may allow writes only in the existing info directory, so
	// the lock itself lives there, not in the common metadata root.
	parent := filepath.Dir(file)
	if info, err := os.Lstat(parent); os.IsNotExist(err) {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Sprintf("%s: could not create the git metadata directory at %s (%v; file left untouched)", cmdName, parent, err)
		}
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Sprintf("%s: the git metadata directory at %s is not a real directory (file left untouched)", cmdName, parent)
	}
	lockDir := excludeLockDir(root, env)
	if lockDir == "" {
		return fmt.Sprintf("%s: cannot resolve the common git dir for %s (file left untouched)", cmdName, file)
	}
	// All shared writers contend on the validated info directory itself; a
	// process-held lock releases on death and needs no lock inode or chown.
	unlock, err := core.LockSharedPath(sharedLockTarget(lockDir), "git exclude", 0o600)
	if err != nil {
		return fmt.Sprintf("%s: could not lock the git exclude file at %s (%v; file left untouched)", cmdName, file, err)
	}
	defer unlock()
	if reason := excludeRedirect(root, env); reason != "" {
		return reason
	}
	excludeLockAcquired()
	return fn(file)
}

// excludeLockDir is the process-held lock identity of one common exclude:
// the validated info directory itself. Linked worktrees resolve the same
// common directory, so every writer serializes on one OS lock.
func excludeLockDir(root string, env platform.Env) string {
	file := excludePath(root, env)
	if file == "" {
		return ""
	}
	parent := filepath.Dir(file)
	common := commonGitDir(root, env)
	if common == "" {
		return ""
	}
	rel, err := filepath.Rel(common, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	return parent
}

// sharedExcludeLockPerm follows the validated parent's real write access
// class: group read/write for a group-writable parent (whose group is then
// applied to the published inode), world read/write only when that is the
// parent's granting class, and private 0o600 otherwise.
func sharedExcludeLockPerm(path string) os.FileMode {
	perm := os.FileMode(0o600)
	if info, err := os.Stat(filepath.Dir(path)); err == nil {
		mode := info.Mode().Perm()
		switch {
		case mode&0o002 != 0:
			perm = 0o666
		case mode&0o020 != 0:
			perm = 0o660
		}
	}
	return perm
}

func missingExcludeEntries(current string, entries []string) []string {
	_, added := excludeAfter(current, entries)
	return added
}

func excludeAfterText(current string, entries []string) string {
	after, _ := excludeAfter(current, entries)
	return after
}

// trackedClaudeBlock finds the instruction file Claude reads, following its symlink.
func trackedClaudeBlock(root string, env platform.Env, cache *setupTracking) (string, string) {
	file := filepath.Join(root, "CLAUDE.md")
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", ""
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ""
	}
	if cache.status(root, "CLAUDE.md", env) != trackingTracked && cache.status(root, filepath.ToSlash(rel), env) != trackingTracked {
		return "", ""
	}
	raw, err := platform.ReadTextFile(resolved)
	if err != nil || !hasCompleteSetupBlock(raw) {
		return "", ""
	}
	next := setuptext.SetupBlockResult(&raw)
	hint := ""
	if next != nil && *next != raw {
		hint = " (its block is older: refresh it with 'soho setup' on a work branch)"
	}
	note := fmt.Sprintf("soho block already in %s (tracked); CLAUDE.local.md not written%s", resolved, hint)
	if local, err := platform.ReadTextFile(localTarget(root)); err == nil && hasCompleteSetupBlock(local) {
		note += "; warning: soho block is in both files; the local copy can be removed"
	}
	return resolved, note
}

func protectedLocalFile(root, rel string, env platform.Env, cache *setupTracking) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && cache.status(root, rel, env) != trackingUntracked
}

func trackedLocalChange(root, rel string, env platform.Env, before, after *string, cache *setupTracking) bool {
	if before == nil || (after != nil && *before == *after) {
		return false
	}
	file := filepath.Join(root, filepath.FromSlash(rel))
	switch cache.status(root, rel, env) {
	case trackingTracked:
		fmt.Fprintf(platform.Stdout, "%s is tracked; --local leaves it as is (run 'soho setup' on a work branch to refresh it)\n", file)
		return true
	case trackingUnknown:
		reason := "git lookup failed"
		if _, ok := platform.FindExecutable("git", env, platform.Current()); !ok {
			reason = "git not found"
		}
		fmt.Fprintf(platform.Stdout, "could not tell whether %s is tracked (%s); --local leaves it as is\n", file, reason)
		return true
	}
	return false
}

func warnTrackedCanonical(root, file string, env platform.Env, before, after *string, cache *setupTracking) {
	if after == nil || (before != nil && *before == *after) {
		return
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	status := cache.status(root, filepath.ToSlash(rel), env)
	if status == trackingUnknown {
		reason := "git lookup failed"
		if _, ok := platform.FindExecutable("git", env, platform.Current()); !ok {
			reason = "git not found"
		}
		fmt.Fprintf(platform.Stdout, "could not tell whether %s is tracked (%s)\n", file, reason)
		return
	}
	if status != trackingTracked {
		return
	}
	branch, ok := gitOutput(env, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if !ok {
		return
	}
	def, ok := gitOutput(env, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if ok {
		def = strings.TrimPrefix(def, "origin/")
	} else {
		if _, ok = gitOutput(env, root, "show-ref", "--verify", "--quiet", "refs/heads/main"); ok || branch == "main" {
			def = "main"
		} else {
			def = "master"
		}
	}
	if branch == def {
		if filepath.ToSlash(rel) == ".claude/settings.json" {
			fmt.Fprintf(platform.Stdout, "%s is tracked; commit .claude/settings.json on a work branch; add --guard-hooks if people without soho open this repository\n", file)
		} else {
			fmt.Fprintf(platform.Stdout, "%s is tracked; make this change on a work branch and open a PR\n", file)
		}
	}
}

// LocalStateSetupInEffect reports whether this project resolves to local
// setup: an explicit local target or the established implicit local setup.
func LocalStateSetupInEffect(root string, ctx *core.Config, env platform.Env) bool {
	return core.Cfg(ctx, "setup_target", "canonical", env) == "local" || ImplicitLocalSetupInEffect(root, ctx, env)
}

// EnsureLocalStateExclude hides the current state path through the common git
// dir's info/exclude when local setup is in effect, reusing setup's safe
// exclusion handling. It reports whether local mode applied; a non-repo,
// external or unsafe state path keeps its existing behavior and writes nothing.
func EnsureLocalStateExclude(root string, ctx *core.Config, env platform.Env, cwd, cmdName string) bool {
	if !LocalStateSetupInEffect(root, ctx, env) {
		return false
	}
	state := classifyLocalState(root, ctx, env, cwd)
	if state.kind != "inside" && state.kind != "sibling" {
		// A work-tree root or an in-repo symlink cannot be named in the
		// exclude file. Refuse with setup's own diagnostic in local init
		// instead of letting the legacy StateRoot append .gitignore.
		if state.kind == "root" || state.kind == "symlink" {
			refuseUnignorableStateDir(root, ctx, env, cwd, cmdName, false)
		}
		return false
	}
	// A sibling state checkout supplies its own ignore rules. The exclude
	// file itself still comes from the shared Git common directory.
	checkRoot := root
	if state.kind == "sibling" && state.worktree != "" {
		checkRoot = state.worktree
	}
	if !insideWorkTree(checkRoot, env) {
		if hasGitMetadata(checkRoot) || explicitGitLayout(env) {
			platform.DieFriction(fmt.Sprintf("%s: could not verify the Git work tree at %s before creating local state (no files were changed)", cmdName, checkRoot), 4)
		}
		// No applicable worktree: there is no exclusion to make or verify.
		return true
	}
	ensureLocalExcludes(checkRoot, []string{state.rel}, env, cmdName)
	// A trailing slash makes Git apply directory rules before the state exists.
	if status, ok := checkedExcludeStatus(checkRoot, state.rel+"/", env); !ok || status != 0 {
		platform.DieFriction(fmt.Sprintf("%s: refusing to create local state at '%s' because git still reports it unignored after updating the exclude file (a tracked negation or failed git check overrides the exclude; %s; remove the negation or move SOHO_DIR outside the work tree)", cmdName, state.shown, changedNote(true)), 4)
	}
	return true
}

// ImplicitLocalSetupInEffect reports whether plain setup in this repository
// wrote the local setup because no soho file is tracked. It shares the
// tracking rules with implicitLocalSetup and never writes. An explicitly
// configured setup_target (including canonical and local) keeps its own
// choice, and a git failure keeps the conservative tracked answer.
func ImplicitLocalSetupInEffect(root string, ctx *core.Config, env platform.Env) bool {
	if core.ConfigExplicit(ctx, "setup_target", env) {
		return false
	}
	if !hasGitMetadata(root) {
		return false
	}
	if trackedSohoFile(root, env, &setupTracking{}) {
		return false
	}
	text, err := platform.ReadTextFile(localTarget(root))
	return err == nil && (strings.Contains(text, setuptext.SetupStart) || strings.Contains(text, setuptext.LegacySetupStart))
}
