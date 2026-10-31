package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateProjectRootWithGitMalformedRequiredPaths(t *testing.T) {
	for _, name := range []string{"empty-git-dir", "empty-common-dir", "relative-toplevel"} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			root, err := StateProjectRootWithGit(cwd, func(args ...string) (string, int) {
				switch strings.Join(args, " ") {
				case "rev-parse --git-dir":
					if name == "empty-git-dir" {
						return "\n", 0
					}
					return filepath.Join(cwd, ".git") + "\n", 0
				case "rev-parse --git-common-dir":
					if name == "empty-common-dir" {
						return "\n", 0
					}
					return filepath.Join(cwd, ".git") + "\n", 0
				case "rev-parse --show-toplevel":
					if name == "relative-toplevel" {
						return ".\n", 0
					}
					return cwd + "\n", 0
				case "worktree list --porcelain":
					return "worktree " + filepath.Dir(cwd) + "\n", 0
				}
				return "", 1
			})
			if err == nil || root != "" {
				t.Fatalf("malformed resolution: root=%q err=%v, want empty root and error", root, err)
			}
		})
	}
}

func TestStateProjectRootWithGitCanonicalMetadataAlias(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "container", ".git", "nested")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(target, alias); err != nil {
		skipUnavailableSymlink(t, err)
	}
	root, err := StateProjectRootWithGit(base, func(args ...string) (string, int) {
		switch strings.Join(args, " ") {
		case "rev-parse --git-dir":
			return filepath.Join(alias, ".git", "worktrees", "worker") + "\n", 0
		case "rev-parse --git-common-dir":
			return filepath.Join(alias, ".git") + "\n", 0
		}
		return "", 128
	})
	if err == nil || root != "" {
		t.Fatalf("canonical metadata alias: root=%q err=%v, want empty root and error", root, err)
	}
}

func TestStateProjectRootWithGitCanonicalCommonContainment(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(base, "container")
	repo := filepath.Join(common, "repo")
	worker := filepath.Join(base, "worker")
	alias := filepath.Join(base, "alias")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	run := func(args ...string) {
		if out, code := cleanedGitRun(t, base, args...); code != 0 {
			t.Fatalf("git %q exit=%d out=%q", args, code, out)
		}
	}
	run("init", "-q", "--separate-git-dir="+common, repo)
	run("-C", repo, "config", "user.email", "test@example.invalid")
	run("-C", repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "tracked")
	run("-C", repo, "commit", "-qm", "fixture")
	run("-C", repo, "worktree", "add", "-q", "-b", "worker", worker)
	if err := os.Symlink(repo, alias); err != nil {
		skipUnavailableSymlink(t, err)
	}
	for _, configured := range []string{repo, alias} {
		run("--git-dir", common, "config", "core.worktree", configured)
		root, err := StateProjectRootWithGit(worker, func(args ...string) (string, int) {
			return cleanedGitRun(t, worker, args...)
		})
		if err == nil || root != "" {
			t.Fatalf("contained checkout %q: root=%q err=%v, want empty root and error", configured, root, err)
		}
	}
}
