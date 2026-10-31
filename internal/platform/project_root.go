package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// stateRootRunner runs one read-only Git invocation in cwd. The ordinary
// wrappers supply the environment-selected global runner; the strict entry
// supplies the caller's own already-cleaned closure for that cwd.
type stateRootRunner func(cwd string, args ...string) (string, int)

// stateRootResolve is the single shared common-directory interpretation: it
// compares the resolved --git-dir and --git-common-dir, takes the common
// .git's parent for linked worktrees, and otherwise the first nonbare
// worktree plus the optional core.worktree, keeping the existing containment
// and .git-segment guards and the native path rules. A non-nil error is the
// interpretation's refusal: the ordinary wrappers map it to their ProjectRoot
// fallback, the strict entry returns it. strict additionally fails the
// optional core.worktree read's empty-success and other non-absence outcomes,
// which the ordinary wrappers keep and let the guards decide, exactly as
// before. mainTree supplies the normal main tree's checkout root for the
// resolved branch.
func stateRootResolve(cwd string, run stateRootRunner, strict bool, mainTree func() (string, bool)) (root string, err error) {
	gitDirOut, gitStatus := run(cwd, "rev-parse", "--git-dir")
	commonOut, commonStatus := run(cwd, "rev-parse", "--git-common-dir")
	if gitStatus != 0 || commonStatus != 0 {
		return "", errors.New("state project root: the required Git resolution failed")
	}
	if strict && (strings.TrimSpace(gitDirOut) == "" || strings.TrimSpace(commonOut) == "") {
		return "", errors.New("state project root: a required Git directory resolution is empty")
	}
	gitPath := resolveFrom(cwd, strings.TrimSpace(gitDirOut))
	commonPath := resolveFrom(cwd, strings.TrimSpace(commonOut))
	defer func() {
		if strict && err == nil {
			root, err = strictCanonicalRoot(root, commonPath)
		}
	}()
	if gitPath == "" {
		return "", errors.New("state project root: the Git directory resolution is empty")
	}
	if gitPath == commonPath {
		root, ok := mainTree()
		if !ok {
			return "", errors.New("state project root: the normal main tree root resolution failed")
		}
		return root, nil
	}
	if filepath.Base(commonPath) == ".git" {
		root := filepath.Dir(commonPath)
		if hasGitPathSegment(root) {
			return "", errors.New("state project root: the common .git parent is inside Git metadata")
		}
		return root, nil
	}
	worktreesOut, worktreeStatus := run(cwd, "worktree", "list", "--porcelain")
	if worktreeStatus != 0 {
		return "", errors.New("state project root: the worktree listing failed")
	}
	first := strings.SplitN(strings.TrimRight(worktreesOut, "\n"), "\n\n", 2)[0]
	var firstPath string
	bare := false
	for _, line := range strings.Split(strings.ReplaceAll(first, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			firstPath = strings.TrimPrefix(line, "worktree ")
		}
		if line == "bare" {
			bare = true
		}
	}
	if firstPath == "" || bare {
		return "", errors.New("state project root: the worktree listing has no usable first worktree")
	}
	root = resolveFrom(cwd, firstPath)
	if isWithin(commonPath, root) {
		worktreeOut, status := run(cwd, "--git-dir", commonPath, "config", "--path", "core.worktree")
		switch {
		case status == 1:
			// a missing optional core.worktree is a legitimate absence
		case status == 0 && strings.TrimSpace(worktreeOut) != "":
			root = resolveFrom(commonPath, strings.TrimSpace(worktreeOut))
		case strict:
			return "", errors.New("state project root: the optional core.worktree query failed")
		}
	}
	if !isWithin(commonPath, root) && !hasGitPathSegment(root) {
		return root, nil
	}
	return "", errors.New("state project root: the candidate root is not a usable checkout")
}

// StateProjectRootWithGit resolves the strict uncached common-directory
// state root for cwd through the caller's own read-only Git closure: run
// takes that cwd's already-cleaned arguments and returns (stdout, exit code)
// with a failing integer for every non-success. The new code performs no
// process, environment or caller lookup and no caching itself. A required
// Git resolution failure, an empty or malformed path or porcelain data, or
// an unsafe root is an error with no cwd fallback; a successful
// normal-main-tree branch takes show-toplevel through the same closure. A
// missing optional core.worktree with Git exit 1 is a legitimate absence;
// other execution failures fail. The returned root is the canonical existing
// directory.
func StateProjectRootWithGit(cwd string, run func(args ...string) (string, int)) (string, error) {
	injected := func(_ string, args ...string) (string, int) { return run(args...) }
	mainTree := func() (string, bool) {
		out, status := run("rev-parse", "--show-toplevel")
		if status != 0 {
			return "", false
		}
		root, ok := projectRootFromToplevel(out)
		return root, ok && filepath.IsAbs(root)
	}
	root, err := stateRootResolve(cwd, injected, true, mainTree)
	if err != nil {
		return "", err
	}
	return root, nil
}

func strictCanonicalRoot(root, common string) (string, error) {
	canonical, err := canonicalExistingDir(root)
	if err != nil {
		return "", err
	}
	commonCanonical, err := filepath.EvalSymlinks(common)
	if err != nil {
		return "", fmt.Errorf("state project root: the common Git directory cannot be canonicalized: %v", err)
	}
	info, err := os.Stat(commonCanonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("state project root: the common Git directory is not an existing directory")
	}
	if isWithin(commonCanonical, canonical) {
		return "", errors.New("state project root: the canonical checkout root is inside the common Git directory")
	}
	return canonical, nil
}

// canonicalExistingDir resolves filesystem aliases and requires the result
// to be an existing directory, the strict entry's final guard.
func canonicalExistingDir(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("state project root: the checkout root is not absolute")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("state project root: %s is not an existing path: %v", root, err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("state project root: %s is not an existing directory", root)
	}
	if hasGitPathSegment(canonical) {
		return "", errors.New("state project root: the canonical checkout root is inside Git metadata")
	}
	return canonical, nil
}
