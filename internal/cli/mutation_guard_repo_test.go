package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func newMutationRepoFixture(t *testing.T) mutationCopyFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := mutationCopyFixture{root: root, source: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), tmp: filepath.Join(root, "tmp"), env: platform.Env{}}
	for _, entry := range testutil.CleanEnv(t) {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			f.env[k] = v
		}
	}
	f.env["HOME"], f.env["USERPROFILE"], f.env["XDG_CONFIG_HOME"], f.env["TMPDIR"] = f.home, f.home, filepath.Join(root, "config"), f.tmp
	f.env["GIT_CONFIG_GLOBAL"], f.env["GIT_CONFIG_SYSTEM"] = os.DevNull, os.DevNull
	f.env["GIT_AUTHOR_NAME"], f.env["GIT_COMMITTER_NAME"] = "fixture", "fixture"
	f.env["GIT_AUTHOR_EMAIL"], f.env["GIT_COMMITTER_EMAIL"] = "fixture@example.invalid", "fixture@example.invalid"
	for _, dir := range []string{f.home, f.tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gitRepoAll(t, f, f.source, "tracked.txt", "fixture\n")
	return f
}

func TestMutationGuardRepository(t *testing.T) {
	for _, name := range []string{"linked", "main", "equal-top", "outside", "non-repository", "symlink", "git-env"} {
		t.Run(name, func(t *testing.T) {
			f := newMutationRepoFixture(t)
			source, copyPath := f.source, filepath.Join(f.source, "sub", "copy")
			wantCode := 1
			overrides := platform.Env{}
			if name == "linked" || name == "equal-top" {
				source = filepath.Join(f.source, ".worktrees", "a")
				fixtureGit(t, f, f.source, "worktree", "add", "--detach", source)
				copyPath = filepath.Join(f.source, ".worktrees", "b")
				if name == "equal-top" {
					copyPath = f.source
				}
			}
			if name == "outside" || name == "non-repository" {
				copyPath, wantCode = filepath.Join(f.tmp, "copy"), 0
			}
			if name == "non-repository" {
				source = filepath.Join(f.root, "plain")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(copyPath, 0700); err != nil {
				t.Fatal(err)
			}
			if name == "symlink" {
				link := filepath.Join(f.tmp, "link")
				if err := os.Symlink(copyPath, link); err != nil {
					t.Fatal(err)
				}
				copyPath = link
			}
			if name == "git-env" {
				other := filepath.Join(f.root, "other")
				gitRepoAll(t, f, other, "other.txt", "other\n")
				overrides["GIT_DIR"] = filepath.Join(other, ".git")
				overrides["GIT_WORK_TREE"] = other
				overrides["GIT_COMMON_DIR"] = filepath.Join(other, ".git")
				overrides["GIT_INDEX_FILE"] = filepath.Join(other, ".git", "index")
			}
			g := guardFixture{root: f.root, source: source, copy: copyPath, env: f.env}
			code, out, stderr := runMutationGuardFixture(t, g, []string{copyPath, "--source", source}, overrides)
			line := "ok copy-outside-repository"
			if wantCode == 1 {
				line = "fail copy-outside-repository: the copy is inside the repository at " + f.source
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if code != wantCode || stderr != "" || len(lines) != 5 || lines[1] != line {
				t.Fatalf("code=%d want=%d stdout=%q stderr=%q", code, wantCode, out, stderr)
			}
			if wantCode == 0 && out != "ok copy-outside-source\nok copy-outside-repository\nok no-symlink-into-source\nok build-env\nok cargo-config\n" {
				t.Fatalf("unexpected successful checks: %q", out)
			}
			t.Logf("exit=%d\n%s", code, out)
		})
	}
}

func TestMutationCopyRepository(t *testing.T) {
	for _, name := range []string{"inside", "default"} {
		t.Run(name, func(t *testing.T) {
			f := newMutationRepoFixture(t)
			source := filepath.Join(f.source, ".worktrees", "a")
			fixtureGit(t, f, f.source, "worktree", "add", "--detach", source)
			args := []string{"--source", source}
			dest := filepath.Join(f.source, ".worktrees", "b")
			if name == "inside" {
				args = append(args, "--dest", dest)
			}
			code, out, stderr := runMutationCopyCmd(t, f, source, nil, args...)
			if name == "inside" {
				if code != 1 || !strings.Contains(stderr, "fail copy-outside-repository: the copy is inside the repository at "+f.source) || out != "" {
					t.Fatalf("exit=%d out=%q err=%q", code, out, stderr)
				}
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatalf("refused destination remains: %s: %v", dest, err)
				}
			} else {
				if code != 0 {
					t.Fatalf("exit=%d out=%q err=%q", code, out, stderr)
				}
				got := decodedCopy(t, out)
				if !inside(f.tmp, got.Copy) {
					t.Fatalf("default copy outside TMPDIR: %s", got.Copy)
				}
			}
			t.Logf("exit=%d stdout=%s stderr=%s", code, out, stderr)
		})
	}
}
