package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const snapshotHeader = "owner\tworktree\tsha\tpatch\tcreated\n"

var snapshotSlug = regexp.MustCompile(`[^a-z0-9]+`)
var snapshotSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

// Test interleaving point immediately before an exclusive artifact create.
var snapshotBeforeArtifact func(*os.Root, string) error

func openSnapshotRoot(path string) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("unsafe snapshot directory %s", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("snapshot directory changed while opening: %s", path)
	}
	return root, nil
}

func snapshotGit(env platform.Env, cwd string, args ...string) (string, error) {
	return snapshotGitIndex(env, cwd, "", args...)
}
func snapshotGitIndex(env platform.Env, cwd, index string, args ...string) (string, error) {
	clean := platform.Env{}
	for k, v := range env {
		if k != "GIT_DIR" && k != "GIT_WORK_TREE" && k != "GIT_COMMON_DIR" && k != "GIT_INDEX_FILE" {
			clean[k] = v
		}
	}
	if index != "" {
		clean["GIT_INDEX_FILE"] = index
	}
	r := platform.RunCli("git", args, platform.RunOptions{Env: clean, Cwd: cwd, TimeoutMs: 5000})
	if r.Status == nil || *r.Status != 0 || r.Error != "" || r.TimedOut {
		return "", fmt.Errorf("git %s: %s %v (timed out: %t)", strings.Join(args, " "), strings.TrimSpace(r.Stderr), r.Error, r.TimedOut)
	}
	return r.Stdout, nil
}
func snapshotMain(env platform.Env, cwd string) (string, error) {
	dir, err := snapshotGit(env, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir, err = filepath.EvalSymlinks(strings.TrimSpace(dir))
	if err != nil {
		return "", err
	}
	if filepath.Base(dir) != ".git" {
		return "", fmt.Errorf("repository has no main .git directory")
	}
	return filepath.Dir(dir), nil
}
func ensureReviewSnapshotDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return os.MkdirAll(path, 0700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe snapshot directory %s", path)
	}
	return nil
}
func cmdReviewSnapshot(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	fail := func(code int, err error) int {
		fmt.Fprintln(platform.Stderr, "soho: review-snapshot:", err)
		return code
	}
	if len(args) == 0 {
		return fail(2, fmt.Errorf("usage: review-snapshot <worktree> [--for <agent>] [--name <slug>] [--base <rev>]"))
	}
	sourceArg := args[0]
	if !filepath.IsAbs(sourceArg) {
		sourceArg = filepath.Join(cwd, sourceArg)
	}
	src, err := filepath.Abs(sourceArg)
	if err != nil {
		return fail(2, err)
	}
	owner, name, base := "", filepath.Base(src), "HEAD"
	ownerSpecified := false
	for i := 1; i < len(args); i++ {
		if i+1 >= len(args) {
			return fail(2, fmt.Errorf("missing option value"))
		}
		v := args[i+1]
		switch args[i] {
		case "--for":
			owner = v
			ownerSpecified = true
		case "--name":
			name = v
		case "--base":
			base = v
		default:
			return fail(2, fmt.Errorf("unknown option %s", args[i]))
		}
		i++
	}
	if strings.ContainsAny(src+owner+name+base, "\t\r\n") {
		return fail(2, fmt.Errorf("invalid control character"))
	}
	slug := strings.Trim(snapshotSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return fail(2, fmt.Errorf("empty snapshot slug"))
	}
	sd := core.StateDirPath(ctx, env, cwd)
	if ownerSpecified && (owner == "" || core.RosterLine(sd, owner) == "") {
		return fail(2, fmt.Errorf("agent %q is not in the roster", owner))
	}
	main, err := snapshotMain(env, src)
	if err != nil {
		return fail(4, err)
	}
	baseOut, err := snapshotGit(env, src, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return fail(4, err)
	}
	base = strings.TrimSpace(baseOut)
	temp, err := os.MkdirTemp(env["TMPDIR"], "soho-review-index-")
	if err != nil {
		return fail(4, err)
	}
	defer os.RemoveAll(temp)
	index := filepath.Join(temp, "index")
	for _, argv := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"}} {
		if _, err = snapshotGitIndex(env, src, index, argv...); err != nil {
			return fail(4, err)
		}
	}
	tree, err := snapshotGitIndex(env, src, index, "write-tree")
	if err != nil {
		return fail(4, err)
	}
	commit, err := snapshotGit(env, src, "commit-tree", strings.TrimSpace(tree), "-p", base, "-m", "soho review snapshot "+slug)
	if err != nil {
		return fail(4, err)
	}
	sha := strings.TrimSpace(commit)
	if !snapshotSHA.MatchString(sha) {
		return fail(4, fmt.Errorf("invalid commit identity"))
	}
	label := slug + "-" + sha[:7]
	path := filepath.Join(main, ".worktrees", "snap-"+label)
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return fail(2, fmt.Errorf("snapshot path already exists: %s", path))
	}
	if err = ensureReviewSnapshotDir(filepath.Dir(path)); err != nil {
		return fail(4, err)
	}
	patchDir := filepath.Join(sd, "snapshots")
	if err = ensureReviewSnapshotDir(patchDir); err != nil {
		return fail(4, err)
	}
	patch, err := snapshotGit(env, src, "diff", "--binary", base, sha)
	if err != nil {
		return fail(4, err)
	}
	patchPath := filepath.Join(patchDir, label+".patch")
	artifacts, err := openSnapshotRoot(patchDir)
	if err != nil {
		return fail(4, err)
	}
	defer artifacts.Close()
	for _, name := range []string{label + ".patch", label + ".patch.sha256"} {
		if _, err = artifacts.Lstat(name); err == nil {
			return fail(2, fmt.Errorf("snapshot artifact already exists: %s", filepath.Join(patchDir, name)))
		} else if !os.IsNotExist(err) {
			return fail(4, err)
		}
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(patch)))
	unlock, err := core.LockCopies(sd)
	if err != nil {
		return fail(4, err)
	}
	defer unlock()
	rows, err := readReviewSnapshots(sd)
	if err != nil {
		return fail(4, err)
	}
	for _, row := range rows {
		if row[1] == path {
			return fail(2, fmt.Errorf("snapshot already registered"))
		}
	}
	if _, err = snapshotGit(env, src, "worktree", "add", "--detach", path, sha); err != nil {
		return fail(4, err)
	}
	// Keep identities from successful exclusive opens, never from preflight.
	// A replaced or otherwise unidentifiable artifact is left alone.
	type ownedArtifact struct {
		name string
		info os.FileInfo
	}
	var created []ownedArtifact
	rollback := func(cause error) int {
		for _, artifact := range created {
			current, statErr := artifacts.Lstat(artifact.name)
			if statErr == nil && os.SameFile(artifact.info, current) {
				_ = artifacts.Remove(artifact.name)
			}
		}
		_, removeErr := snapshotGit(env, src, "worktree", "remove", "--force", path)
		code := fail(4, cause)
		if removeErr != nil {
			fmt.Fprintf(platform.Stderr, "soho: review-snapshot: rollback left worktree %s: %v\n", path, removeErr)
		}
		return code
	}
	for _, artifact := range []struct{ path, body string }{{patchPath, patch}, {patchPath + ".sha256", sum + "  " + patchPath + "\n"}} {
		name := filepath.Base(artifact.path)
		if snapshotBeforeArtifact != nil {
			if err = snapshotBeforeArtifact(artifacts, name); err != nil {
				return rollback(err)
			}
		}
		file, openErr := artifacts.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return rollback(openErr)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return rollback(statErr)
		}
		created = append(created, ownedArtifact{name, info})
		_, err = file.WriteString(artifact.body)
		closeErr := file.Close()
		if err != nil {
			return rollback(err)
		}
		if closeErr != nil {
			return rollback(closeErr)
		}
	}
	rows = append(rows, []string{owner, path, sha, patchPath, time.Now().UTC().Format(time.RFC3339)})
	if err = writeReviewSnapshots(sd, rows); err != nil {
		return rollback(err)
	}
	returnCode := 0
	if err = json.NewEncoder(platform.Stdout).Encode(map[string]string{"snapshot": path, "base": base, "sha": sha, "patch": patchPath, "patch_sha256": sum, "owner": owner}); err != nil {
		returnCode = 4
	}
	return returnCode
}
func readReviewSnapshots(sd string) ([][]string, error) {
	data, err := core.ReadScratchReport(filepath.Join(sd, "snapshots.tsv"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows := [][]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if i == 0 {
			if line != strings.TrimSuffix(snapshotHeader, "\n") {
				return nil, fmt.Errorf("invalid snapshot registry header")
			}
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("invalid snapshot registry row")
		}
		rows = append(rows, fields)
	}
	return rows, nil
}
func writeReviewSnapshots(sd string, rows [][]string) error {
	var b strings.Builder
	b.WriteString(snapshotHeader)
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t"))
		b.WriteByte('\n')
	}
	return core.WriteScratchFile(filepath.Join(sd, "snapshots.tsv"), b.String())
}
func validateReviewSnapshot(sd string, row []string, env platform.Env, cwd string) (bool, error) {
	main, err := snapshotMain(env, cwd)
	if err != nil {
		return false, err
	}
	path, sha := row[1], row[2]
	if !snapshotSHA.MatchString(sha) {
		return false, fmt.Errorf("invalid snapshot sha")
	}
	if filepath.Dir(path) != filepath.Join(main, ".worktrees") || !strings.HasPrefix(filepath.Base(path), "snap-") || !strings.HasSuffix(path, "-"+sha[:7]) {
		return false, fmt.Errorf("outside snapshot prefix")
	}
	if err = snapshotDirExisting(filepath.Dir(path)); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return false, err
	}
	if !missing && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return false, fmt.Errorf("not a snapshot directory")
	}
	expected := filepath.Join(sd, "snapshots", strings.TrimPrefix(filepath.Base(path), "snap-")+".patch")
	if row[3] != expected {
		return false, fmt.Errorf("outside snapshot patch directory")
	}
	if err = snapshotDirExisting(filepath.Dir(expected)); err != nil {
		return false, err
	}
	listing, err := snapshotGit(env, cwd, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	for _, record := range strings.Split(listing, "\x00\x00") {
		fields := strings.Split(record, "\x00")
		if len(fields) > 0 && strings.HasPrefix(fields[0], "worktree ") && snapshotPathsEqual(strings.TrimPrefix(fields[0], "worktree "), path, runtime.GOOS == "windows") {
			if missing {
				return false, fmt.Errorf("missing snapshot worktree is still registered")
			}
			hasHead, detached := false, false
			for _, field := range fields {
				hasHead = hasHead || field == "HEAD "+sha
				detached = detached || field == "detached"
			}
			if hasHead && detached {
				return true, nil
			}
		}
	}
	if missing {
		// Git removal already completed; the retained row can finish artifacts.
		return false, nil
	}
	return false, fmt.Errorf("not a detached snapshot worktree of this repository")
}
func snapshotDirExisting(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe snapshot parent")
	}
	return nil
}
func cleanupReviewSnapshots(sd, owner string, gc, yes bool, env platform.Env, cwd string) bool {
	// Even acquiring and removing a lock changes the state directory's mtime.
	if core.Nowrite(env) {
		return false
	}
	unlock, err := core.LockCopies(sd)
	if err != nil {
		var locked *core.CopiesLockedError
		if gc && errors.As(err, &locked) {
			// The copies sweep owns the command's lock refusal and exit code.
			return false
		}
		fmt.Fprintln(platform.Stderr, "soho: snapshot registry:", err)
		return false
	}
	rows, err := readReviewSnapshots(sd)
	unlock()
	if err != nil {
		fmt.Fprintln(platform.Stderr, "soho: snapshot registry:", err)
		return false
	}
	activity := false
	for _, row := range rows {
		if gc && row[0] == "" {
			fmt.Fprintf(platform.Stdout, "kept snapshot %s (orchestrator-owned)\n", row[1])
			activity = true
			continue
		}
		if gc && core.RosterLine(sd, row[0]) != "" || !gc && row[0] != owner {
			continue
		}
		activity = true
		if !yes {
			fmt.Fprintf(platform.Stdout, "would remove snapshot %s\n", row[1])
			continue
		}
		if err := removeReviewSnapshot(sd, row, env, cwd); err != nil {
			fmt.Fprintf(platform.Stderr, "soho: kept snapshot %s: %v\n", row[1], err)
		} else {
			fmt.Fprintf(platform.Stdout, "removed snapshot %s\n", row[1])
		}
	}
	return activity
}

// Test interleaving point immediately before Git removes the snapshot worktree.
var snapshotCleanupBeforeRemove func()

// Test interleaving point after root identity validation, before rooted removal.
var snapshotCleanupAfterIdentity func()

func removeReviewSnapshot(sd string, row []string, env platform.Env, cwd string) error {
	unlock, err := core.LockCopies(sd)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := readReviewSnapshots(sd)
	if err != nil {
		return err
	}
	for i, current := range rows {
		if strings.Join(current, "\t") != strings.Join(row, "\t") {
			continue
		}
		removeWorktree, err := validateReviewSnapshot(sd, row, env, cwd)
		if err != nil {
			return err
		}
		artifactDir := filepath.Dir(row[3])
		artifacts, err := openSnapshotRoot(artifactDir)
		if err != nil {
			return err
		}
		defer artifacts.Close()
		if removeWorktree {
			if snapshotCleanupBeforeRemove != nil {
				snapshotCleanupBeforeRemove()
			}
			if _, err = snapshotGit(env, cwd, "worktree", "remove", "--force", row[1]); err != nil {
				return err
			}
		}
		currentDir, err := os.Lstat(artifactDir)
		if err != nil {
			return err
		}
		openedDir, err := artifacts.Stat(".")
		if err != nil || !currentDir.IsDir() || !os.SameFile(currentDir, openedDir) {
			return fmt.Errorf("snapshot artifact directory changed during cleanup: %s", artifactDir)
		}
		if snapshotCleanupAfterIdentity != nil {
			snapshotCleanupAfterIdentity()
		}
		for _, name := range []string{filepath.Base(row[3]), filepath.Base(row[3]) + ".sha256"} {
			if err = artifacts.Remove(name); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return writeReviewSnapshots(sd, append(rows[:i], rows[i+1:]...))
	}
	return nil
}
func releaseReviewSnapshots(sd, agent string, keep bool, env platform.Env, cwd string) {
	if !keep {
		cleanupReviewSnapshots(sd, agent, false, true, env, cwd)
	}
}
func sweepReviewSnapshots(sd string, yes bool, env platform.Env, cwd string) bool {
	return cleanupReviewSnapshots(sd, "", true, yes, env, cwd)
}

// snapshotPathsEqual normalizes Git's slash spelling without resolving a leaf
// that may already be absent. windows also makes the platform rule testable.
func snapshotPathsEqual(a, b string, windows bool) bool {
	normalize := func(p string) string {
		if windows {
			p = strings.ReplaceAll(p, "\\", "/")
		}
		return filepath.Clean(filepath.FromSlash(p))
	}
	a, b = normalize(a), normalize(b)
	if windows {
		return strings.EqualFold(a, b)
	}
	return a == b
}
