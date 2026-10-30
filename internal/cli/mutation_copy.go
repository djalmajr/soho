package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
)

const mutationCopyUsage = "usage: mutation-copy [--source <dir>] [--dest <dir>] [--exclude <glob>]... [--link <relpath>=<target>]..."

// mutation-copy builds the throwaway mutation copy of the repository that the
// guard was written for, from the worktree's repository files only (never
// .git, never ignored paths), and validates it with the guard's own checks
// before it returns. A refusal (code 2) or a copy I/O error (code 4) removes
// the destination this command created; a failing guard removes it and exits
// with the guard's code.
func runMutationCopy(args []string, env platform.Env) int {
	cwd, err := os.Getwd()
	if err != nil {
		return mutationCopyUsageError()
	}
	parsed, ok := parseMutationCopyArgs(args)
	if !ok {
		return mutationCopyUsageError()
	}
	home := mutationCopyHome(env)

	// 1. Source.
	sourceRaw := parsed.source
	if sourceRaw == "" {
		toplevel, found := gitToplevel(cwd, env)
		if !found {
			return mutationCopyRefuse("source", cwd, "not a git worktree")
		}
		sourceRaw = toplevel
	}
	source, reason := mutationCopyResolveSource(sourceRaw, cwd, home, env)
	if reason != "" {
		// The refusal names the path actually attempted (the git toplevel or
		// the current directory when no --source is given), not the empty
		// parsed value.
		return mutationCopyRefuse("source", sourceRaw, reason)
	}
	ctx := core.LoadConfig(env, source)
	excludes := parsed.excludes
	if configured := core.Cfg(&ctx, "mutation_copy_exclude", "", env); configured != "" {
		for _, pattern := range strings.Split(configured, ",") {
			excludes = append(excludes, strings.TrimSpace(pattern))
		}
	}
	for _, pattern := range excludes {
		if !validMutationCopyExclude(pattern) {
			return mutationCopyUsageError()
		}
	}

	dest, destGiven, destCreated, reason := mutationCopyPrepareDest(parsed.dest, cwd, env, home, source)
	if reason != "" {
		if destCreated {
			_ = os.RemoveAll(dest)
		}
		return mutationCopyRefuse("destination", destGiven, reason)
	}
	// Refuse a repository destination before copying files, creating links
	// or writing an origin marker. Never remove a caller-owned directory.
	if detail := mutationRepositoryRefusal(dest, source, env); detail != "" {
		if destCreated {
			_ = os.RemoveAll(dest)
		}
		_, _ = fmt.Fprintln(platform.Stderr, "fail copy-outside-repository: "+detail)
		return 1
	}
	// The origin marker goes beside the copies this command creates in
	// TMPDIR (no --dest: gc never scans the other paths). It is written
	// right after the destination exists and before the file copy, so an
	// interrupted copy already carries it; a failure never changes the
	// command's stdout or exit code.
	markerPath := ""
	if parsed.dest == "" {
		markerPath = dest + ".soho-origin"
		if err := writeCopyOriginMarker(markerPath, source, env); err != nil {
			_, _ = fmt.Fprintf(platform.Stderr, "mutation-copy: origin marker not written (%s); gc will not remove this copy\n", err)
		}
	}
	cleanup := func() {
		if destCreated {
			_ = os.RemoveAll(dest)
			if markerPath != "" {
				_ = os.Remove(markerPath)
			}
		}
	}
	failIO := func(cause string) int {
		cleanup()
		_, _ = fmt.Fprintf(platform.Stderr, "mutation-copy: %s\n", cause)
		return 4
	}
	refuse := func(what, value, why string) int {
		cleanup()
		return mutationCopyRefuse(what, value, why)
	}

	// 2. Links: validate up front, create them after the copy.
	links := make([]mutationCopyLink, 0, len(parsed.links))
	explicitLinks := map[string]bool{}
	for _, raw := range parsed.links {
		rel, target, ok := strings.Cut(raw, "=")
		if !ok {
			return refuse("link", raw, "expected <relpath>=<target>")
		}
		if rel == "" {
			return refuse("link", raw, "relpath is empty")
		}
		if strings.HasPrefix(rel, "/") || (platform.Current() == "win32" && isWindowsAbsPath(rel)) {
			return refuse("link", raw, "relpath must be relative")
		}
		if rel == "." || hasDotDot(rel) {
			return refuse("link", raw, "relpath must not escape with '..'")
		}
		if target == "" {
			return refuse("link", raw, "target is empty")
		}
		if isFilesystemRoot(target) {
			return refuse("link", raw, "target is the filesystem root")
		}
		targetPath, err := canonicalPath(resolveRelative(cwd, target))
		if err != nil {
			return refuse("link", raw, "target cannot be resolved: "+err.Error())
		}
		info, statErr := os.Stat(targetPath)
		if statErr != nil || !info.IsDir() {
			return refuse("link", raw, "target does not exist or is not a directory")
		}
		if filepath.Dir(targetPath) == targetPath {
			return refuse("link", raw, "target is the filesystem root")
		}
		if home != "" && inside(targetPath, home) {
			return refuse("link", raw, "target is HOME or above it")
		}
		if inside(source, targetPath) {
			return refuse("link", raw, "target is inside the source; install the dependencies in the copy instead (an offline install from the package manager's store)")
		}
		// A target that is the copy, or an ancestor of it, would leave the copy
		// holding a link loop; refuse it like the implicit links.
		if mutationPathContains(targetPath, dest) {
			return refuse("link", raw, "target contains the copy")
		}
		links = append(links, mutationCopyLink{Path: filepath.Clean(rel), Target: targetPath})
		explicitLinks[filepath.Clean(rel)] = true
	}

	// 3. The repository's files, from the worktree's disk state.
	listArgs := []string{"-C", source, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."}
	filteredArgs := append([]string(nil), listArgs...)
	for _, pattern := range excludes {
		filteredArgs = append(filteredArgs, ":(exclude,glob)"+pattern)
	}
	result := platform.RunCli("git", filteredArgs, platform.RunOptions{Env: env, Cwd: source, TimeoutMs: 30_000})
	if result.NotFound {
		return failIO("git is not available on PATH")
	}
	if result.Status == nil || *result.Status != 0 {
		return failIO("git ls-files failed: " + strings.TrimSpace(result.Stderr))
	}
	excluded := 0
	if len(excludes) > 0 {
		// This listing is for cardinality only: never visit or log its paths.
		unfiltered := platform.RunCli("git", listArgs, platform.RunOptions{Env: env, Cwd: source, TimeoutMs: 30_000})
		if unfiltered.NotFound || unfiltered.Status == nil || *unfiltered.Status != 0 {
			return failIO("git ls-files count failed")
		}
		excluded = strings.Count(unfiltered.Stdout, "\x00") - strings.Count(result.Stdout, "\x00")
	}
	files := 0
	for _, rel := range strings.Split(result.Stdout, "\x00") {
		if rel == "" {
			continue
		}
		srcPath := filepath.Join(source, rel)
		dstPath := filepath.Join(dest, rel)
		// A directory component on the listed path may have been replaced by
		// a symlink to outside the source; lstat every source component before
		// reading through it.
		if component := mutationCopySourceDirSymlink(source, rel); component != "" {
			return refuse("symlink", component, "symlinked directory on a listed path")
		}
		info, err := os.Lstat(srcPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // tracked but deleted in the worktree
			}
			return failIO(err.Error())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// An explicit dependency link replaces the source link without
			// resolving or reading that source link's target.
			if explicitLinks[filepath.Clean(rel)] {
				continue
			}
			target, err := os.Readlink(srcPath)
			if err != nil {
				return failIO(err.Error())
			}
			if filepath.IsAbs(target) {
				// Index membership is independent of ignore rules: a trailing
				// slash ignore pattern does not hide a directory symlink.
				tracked := platform.RunCli("git", []string{"-C", source, "ls-files", "-z", "--cached", "--", ":(literal)" + rel}, platform.RunOptions{Env: env, Cwd: source, TimeoutMs: 30_000})
				if tracked.Status == nil || *tracked.Status != 0 {
					return failIO("git ls-files index check failed: " + strings.TrimSpace(tracked.Stderr))
				}
				if tracked.Stdout != "" {
					return refuse("symlink", rel, "absolute symlink")
				}
				reads, err := mutationGuardReadPaths(dest, env)
				if err != nil {
					return failIO("guard read paths cannot be resolved: " + err.Error())
				}
				for _, readPath := range reads.paths() {
					if mutationPathContains(dstPath, readPath) {
						return refuse("symlink", rel, "a path the guard reads")
					}
				}
				resolved, err := canonicalPath(target)
				if err != nil {
					return refuse("symlink", rel, "absolute symlink target cannot be resolved: "+err.Error())
				}
				if _, err := os.Stat(resolved); err != nil {
					return refuse("symlink", rel, "absolute symlink target does not exist: "+err.Error())
				}
				if mutationPathContains(source, resolved) || mutationPathContains(resolved, source) {
					return refuse("symlink", rel, "target contains the source")
				}
				if mutationPathContains(resolved, dest) {
					return refuse("symlink", rel, "target contains the copy")
				}
				// Recreate the link itself after copying regular files; never
				// visit its target's contents, and preserve its exact text.
				links = append(links, mutationCopyLink{Path: filepath.Clean(rel), Target: target, Implicit: true})
				continue
			}
			// The target is evaluated from the link's place in the copy: it
			// must land inside the copy, not in the source tree.
			resolved, err := canonicalPath(filepath.Join(filepath.Dir(dstPath), target))
			if err != nil {
				return refuse("symlink", rel, "target cannot be resolved: "+err.Error())
			}
			if !inside(dest, resolved) {
				return refuse("symlink", rel, "relative target outside the copy")
			}
			if component := mutationCopyDestDirSymlink(dest, rel); component != "" {
				return refuse("symlink", component, "symlinked directory on a listed path")
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return failIO(err.Error())
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return failIO(err.Error())
			}
			files++
			continue
		}
		if info.IsDir() {
			continue // ls-files lists files; directories appear through their files
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return failIO(err.Error())
		}
		if component := mutationCopyDestDirSymlink(dest, rel); component != "" {
			return refuse("symlink", component, "symlinked directory on a listed path")
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return failIO(err.Error())
		}
		if err := os.WriteFile(dstPath, data, info.Mode().Perm()); err != nil {
			return failIO(err.Error())
		}
		// WriteFile applies the umask; re-apply the on-disk mode.
		if err := os.Chmod(dstPath, info.Mode().Perm()); err != nil {
			return failIO(err.Error())
		}
		files++
	}

	// 4. The requested dependency links.
	for _, link := range links {
		dstPath := filepath.Join(dest, link.Path)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return failIO(err.Error())
		}
		if err := os.Symlink(link.Target, dstPath); err != nil {
			return failIO(err.Error())
		}
	}

	// 5. The guard's checks, in process.
	report := mutationGuardChecks(dest, source, env, nil)
	if report.failed() {
		cleanup()
		for _, line := range report.failingLines() {
			_, _ = fmt.Fprintln(platform.Stderr, line)
		}
		return 1
	}

	// 6. The result.
	encoded, err := json.Marshal(mutationCopyResult{
		Copy:     dest,
		Source:   source,
		Files:    files,
		Excluded: excluded,
		Links:    links,
	})
	if err != nil {
		return failIO(err.Error())
	}
	registerMutationCopy(env, source, dest)
	_, _ = fmt.Fprintln(platform.Stdout, string(encoded))
	return 0
}

// Mirror dispatch.SamePath's case folding, extending it to Darwin as required
// by this refusal policy, with component containment and no symlink resolution.
func mutationPathContains(parent, child string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	return inside(parent, child)
}

// registerMutationCopy records the finished copy in the per-workspace
// registry so the copies commands know who made it. It never changes the
// command's stdout or exit code: when the state dir cannot be resolved or
// the registry cannot be written, the copy stays unregistered, with a
// stderr warning. A registry the sandbox cannot write (a permission
// error) sends the row to the copy queue instead (queueCopy), and only a
// failed queue leaves the copy unregistered. A dying config or state
// resolution is caught the same way: the command must not die over the
// registration.
func registerMutationCopy(env platform.Env, source, dest string) {
	defer func() {
		if value := recover(); value != nil {
			if exitErr, ok := value.(*platform.ExitError); ok {
				mutationCopyNotRegistered(exitErr.Msg)
				return
			}
			panic(value)
		}
	}()
	cwd, err := os.Getwd()
	if err != nil {
		mutationCopyNotRegistered(err.Error())
		return
	}
	ctx := core.LoadConfig(env, cwd)
	sd := registryDestination(env)
	if sd == "" {
		sd, err = core.StateDirResolved(&ctx, env, cwd)
		if err != nil {
			mutationCopyNotRegistered(err.Error())
			return
		}
	}
	pane := herdr.New(env).Runtime.Caller().PaneID
	owner := rosterAgentForPane(sd, pane)
	if pane == "" {
		pane = "-"
	}
	if owner == "" {
		owner = "-"
	}
	task, report := copyTaskColumns(sd, owner)
	row := core.CopyRow{
		Path:    dest,
		Owner:   owner,
		Pane:    pane,
		Created: core.FrictionISO(platform.Now()),
		Source:  source,
		Origin:  "mutation-copy",
		Task:    task,
		Report:  report,
	}
	if err := upsertCopies(sd, row); err != nil {
		if err = queueCopy("mutation-copy", sd, row, err, env); err != nil {
			mutationCopyNotRegistered(err.Error())
		}
	}
}

func mutationCopyNotRegistered(cause string) {
	_, _ = fmt.Fprintf(platform.Stderr, "mutation-copy: copy not registered (%s); remove it yourself when done\n", cause)
}

type mutationCopyArgs struct {
	source   string
	dest     string
	links    []string
	excludes []string
}

func parseMutationCopyArgs(args []string) (mutationCopyArgs, bool) {
	var parsed mutationCopyArgs
	sourceSeen := false
	destSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--source" || arg == "--dest" || arg == "--link" || arg == "--exclude" {
			if i+1 >= len(args) || args[i+1] == "" {
				return mutationCopyArgs{}, false
			}
			value := args[i+1]
			i++
			switch arg {
			case "--source":
				if sourceSeen || strings.HasPrefix(value, "-") {
					return mutationCopyArgs{}, false
				}
				sourceSeen = true
				parsed.source = value
			case "--dest":
				if destSeen || strings.HasPrefix(value, "-") {
					return mutationCopyArgs{}, false
				}
				destSeen = true
				parsed.dest = value
			case "--link":
				parsed.links = append(parsed.links, value)
			case "--exclude":
				if !validMutationCopyExclude(value) {
					return mutationCopyArgs{}, false
				}
				parsed.excludes = append(parsed.excludes, value)
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return mutationCopyArgs{}, false
		}
	}
	return parsed, true
}

func validMutationCopyExclude(pattern string) bool {
	return pattern != "" && !strings.HasPrefix(pattern, ":")
}

type mutationCopyLink struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Implicit bool   `json:"implicit,omitempty"`
}

type mutationCopyResult struct {
	Copy     string             `json:"copy"`
	Source   string             `json:"source"`
	Files    int                `json:"files"`
	Excluded int                `json:"excluded"`
	Links    []mutationCopyLink `json:"links"`
}

func mutationCopyUsageError() int {
	_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", mutationCopyUsage)
	return 2
}

// writeOriginMarker writes the copy's origin marker file. Tests swap it to
// prove a write failure does not change the command's stdout or exit.
var writeOriginMarker = func(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// writeCopyOriginMarker writes the copy's origin marker beside it: the
// source's git common directory (project=, shared by every worktree of the
// repository), the source root as reported in the result JSON (source=) and
// the creation time in the registry's created format (created=) — one
// key=value line per field, in that order.
func writeCopyOriginMarker(markerPath, source string, env platform.Env) error {
	project, err := gitCommonDirResolved(source, env)
	if err != nil {
		return err
	}
	content := "project=" + project + "\nsource=" + source + "\ncreated=" + core.FrictionISO(platform.Now()) + "\n"
	return writeOriginMarker(markerPath, content)
}

// gitCommonDirResolved runs git rev-parse --path-format=absolute
// --git-common-dir in dir and resolves symlinks. Every worktree of a
// repository returns the same value (the main repository's .git), which
// makes it the project identity the origin marker and the gc compare. The
// error is non-nil when git cannot answer (missing git, not a repository,
// an unreadable result).
func gitCommonDirResolved(dir string, env platform.Env) (string, error) {
	result := platform.RunCli("git", []string{"-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir"}, platform.RunOptions{Env: env, Cwd: dir, TimeoutMs: 30_000})
	if result.NotFound {
		return "", errors.New("git is not available on PATH")
	}
	if result.Status == nil || *result.Status != 0 {
		return "", errors.New("git rev-parse --git-common-dir failed: " + strings.TrimSpace(result.Stderr))
	}
	value := strings.TrimSpace(result.Stdout)
	if value == "" {
		return "", errors.New("git rev-parse --git-common-dir returned no path")
	}
	return canonicalPath(value)
}

func mutationCopyRefuse(what, value, reason string) int {
	_, _ = fmt.Fprintf(platform.Stderr, "mutation-copy: refusing %s '%s': %s\n", what, value, reason)
	return 2
}

// mutationCopyResolveSource applies Abs + EvalSymlinks to the source value and
// checks the refusal rules; the reason is "" on success.
func mutationCopyResolveSource(raw, cwd, home string, env platform.Env) (string, string) {
	if raw == "" {
		return "", "source is empty"
	}
	if isFilesystemRoot(raw) {
		return "", "the filesystem root"
	}
	resolved, err := canonicalPath(resolveRelative(cwd, raw))
	if err != nil {
		return "", "cannot be resolved"
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", "not a directory"
	}
	if filepath.Dir(resolved) == resolved {
		return "", "the filesystem root"
	}
	if home != "" && inside(resolved, home) {
		return "", "HOME or above it"
	}
	if _, found := gitToplevel(resolved, env); !found {
		return "", "not a git worktree"
	}
	return resolved, ""
}

func mutationCopyHome(env platform.Env) string {
	home := ""
	if platform.Current() == "win32" {
		home = env.Get("USERPROFILE")
	} else {
		home = env.Get("HOME")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	resolved, err := canonicalPath(home)
	if err != nil {
		return home
	}
	return resolved
}

// mutationCopyPrepareDest resolves and creates the destination; on success it
// returns the path, the value as given, whether this command created it, and
// a "" reason.
func mutationCopyPrepareDest(raw, cwd string, env platform.Env, home, source string) (string, string, bool, string) {
	created := false
	if raw == "" {
		tmpDir := env.Get("TMPDIR")
		if tmpDir == "" {
			tmpDir = os.TempDir()
		}
		made, err := os.MkdirTemp(tmpDir, "soho-mutation-")
		if err != nil {
			return "", raw, false, "cannot be created: " + err.Error()
		}
		raw = made
		created = true
	}
	if raw == "" {
		return "", raw, created, "destination is empty"
	}
	if isFilesystemRoot(raw) {
		return "", raw, created, "the filesystem root"
	}
	resolved, err := canonicalPath(resolveRelative(cwd, raw))
	if err != nil {
		return "", raw, created, "cannot be resolved: " + err.Error()
	}
	if filepath.Dir(resolved) == resolved {
		return resolved, raw, created, "the filesystem root"
	}
	if home != "" && inside(resolved, home) {
		return resolved, raw, created, "HOME or above it"
	}
	if inside(source, resolved) {
		return resolved, raw, created, "destination is inside the source"
	}
	if inside(resolved, source) {
		return resolved, raw, created, "source is inside the destination"
	}
	info, err := os.Stat(resolved)
	if err == nil {
		if !info.IsDir() {
			return resolved, raw, created, "not a directory"
		}
		entries, readErr := os.ReadDir(resolved)
		if readErr != nil {
			return resolved, raw, created, "cannot be read: " + readErr.Error()
		}
		if len(entries) > 0 {
			return resolved, raw, created, "not empty"
		}
		return resolved, raw, created, ""
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return resolved, raw, created, "cannot be read: " + err.Error()
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return resolved, raw, created, "cannot be created: " + err.Error()
	}
	return resolved, raw, true, ""
}

func gitToplevel(dir string, env platform.Env) (string, bool) {
	result := platform.RunCli("git", []string{"-C", dir, "rev-parse", "--show-toplevel"}, platform.RunOptions{Env: env, Cwd: dir, TimeoutMs: 30_000})
	if result.NotFound || result.Status == nil || *result.Status != 0 {
		return "", false
	}
	value := strings.TrimSpace(result.Stdout)
	if value == "" {
		return "", false
	}
	// git prints forward slashes on Windows; report the native path.
	return filepath.Clean(filepath.FromSlash(value)), true
}

func hasDotDot(rel string) bool {
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

// isFilesystemRoot reports whether value is the filesystem root or a volume
// root (C:\ on Windows). It runs on the raw value, before resolution, because
// on Windows resolving "/" falls back to the current directory.
func isFilesystemRoot(value string) bool {
	cleaned := filepath.Clean(value)
	return filepath.VolumeName(cleaned)+string(filepath.Separator) == cleaned
}

// mutationCopySourceDirSymlink returns the relative slash-separated directory
// component of the listed rel path under source that is a symlink, or "" when
// none of the path's directory components is a symlink.
func mutationCopySourceDirSymlink(source, rel string) string {
	parts := strings.Split(rel, "/")
	dir := source
	component := ""
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		if component == "" {
			component = part
		} else {
			component += "/" + part
		}
		info, err := os.Lstat(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // the component is gone; the file check skips the path
			}
			return "" // the read below reports the real error
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return component
		}
	}
	return ""
}

// mutationCopyDestDirSymlink returns the destination's parent directory for
// the listed rel path (relative to dest, slash-separated) when that parent is
// a symlink, or "" when it is not.
func mutationCopyDestDirSymlink(dest, rel string) string {
	dirRel := "."
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dirRel = rel[:i]
	}
	info, err := os.Lstat(filepath.Join(dest, dirRel))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	return dirRel
}

func isWindowsAbsPath(value string) bool {
	return len(value) >= 2 && value[1] == ':'
}
