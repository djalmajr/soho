package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func repoTestEnv(t *testing.T) platform.Env {
	t.Helper()
	home := t.TempDir()
	return platform.Env{"PATH": os.Getenv("PATH"), "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": home, "TMPDIR": home, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull}
}

func repoTestGit(t *testing.T, env platform.Env, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env.List()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %q in %q: %v: %s", args, dir, err, out)
	}
}

func repoTestInit(t *testing.T, env platform.Env) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repoTestGit(t, env, dir, "init", "-q")
	return dir
}

func repoTestLinked(t *testing.T, env platform.Env, main string) string {
	t.Helper()
	repoTestGit(t, env, main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	worker := filepath.Join(t.TempDir(), "worker")
	repoTestGit(t, env, main, "worktree", "add", "-q", "-b", "worker", worker)
	return worker
}

func TestSameRepositoryRealGit(t *testing.T) {
	env := repoTestEnv(t)
	main := repoTestInit(t, env)
	worker := repoTestLinked(t, env, main)
	other := repoTestInit(t, env)
	nongit := t.TempDir()
	for _, tc := range []struct {
		name, left, right string
		want              RepositoryRelation
	}{
		{"same linked worktree", main, worker, RepositorySame},
		{"different repositories", main, other, RepositoryDifferent},
		{"non-git left", nongit, main, RepositoryUnknown},
		{"non-git right", main, nongit, RepositoryUnknown},
		{"missing directory", main, filepath.Join(nongit, "missing"), RepositoryUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameRepository(env, tc.left, tc.right); got != tc.want {
				t.Fatalf("SameRepository(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.want)
			}
		})
	}
	withoutGit := env.Clone()
	withoutGit["PATH"] = t.TempDir()
	if got := SameRepository(withoutGit, main, worker); got != RepositoryUnknown {
		t.Fatalf("missing git: got %v, want unknown", got)
	}
}
