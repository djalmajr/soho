package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestReviewAuthorWarningRepository(t *testing.T) {
	home := t.TempDir()
	env := platform.Env{"PATH": os.Getenv("PATH"), "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": home, "TMPDIR": home, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %q in %q: %v: %s", args, dir, err, out)
		}
	}
	main, other, nongit := t.TempDir(), t.TempDir(), t.TempDir()
	git(main, "init", "-q")
	git(main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	worker := filepath.Join(t.TempDir(), "worker")
	git(main, "worktree", "add", "-q", "-b", "worker", worker)
	git(other, "init", "-q")
	for _, tc := range []struct {
		name, tree     string
		noLine, silent bool
	}{
		{"different repository", other, false, true},
		{"same linked repository", worker, false, false},
		{"unknown repository", nongit, false, false},
		{"missing worktree line", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sidecar := filepath.Join(dir, "review.dispatch.json")
			body := "Review.\n"
			if !tc.noLine {
				body = "Worktree: " + tc.tree + "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "review.brief.md"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			got := reviewAuthorWarning("source.md", "reviewer", sidecar, dispatchAuthorContext{env: env, cwd: main})
			want := "no author found for this review: no accepted edit-role brief declares Worktree: " + tc.tree + ", so 'soho stats --by author-model' will not count it"
			if tc.noLine {
				want = "brief source.md has no Worktree: line: this review cannot be tied to its author, so 'soho stats --by author-model' will not count it; add 'Worktree: <absolute path>' here and in the implementer's brief"
			} else if tc.silent {
				want = ""
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
