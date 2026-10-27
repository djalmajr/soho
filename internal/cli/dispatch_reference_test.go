package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
)

func cliReferenceRepo(t *testing.T, contents string) (string, string, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "reference repo 'quoted'")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": filepath.Dir(git), "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}
	run := func(input string, args ...string) string {
		c := exec.Command(git, append([]string{"-C", repo}, args...)...)
		c.Env = env.List()
		c.Stdin = strings.NewReader(input)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "init", "--quiet")
	blob := run(contents, "hash-object", "-w", "--stdin")
	tree := run("100644 blob "+blob+"\tseed.sql\n", "mktree")
	sha := run("fixture\n", "commit-tree", tree)
	return repo, sha, git
}

func referenceInventory(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		result[rel] = sha256.Sum256(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDispatchReferenceDiff(t *testing.T) {
	repo, sha, git := cliReferenceRepo(t, "DELETE;\nINSERT;\n")
	f := roleDispatchFixture(t)
	f.env["PATH"] += string(os.PathListSeparator) + filepath.Dir(git)
	worktree := filepath.Join(t.TempDir(), "work tree")
	if err := os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	writeRoleFixture(t, filepath.Join(worktree, "seed.sql"), "UPSERT;\n")
	writeRoleFixture(t, f.brief, fmt.Sprintf("Type: review\nWorktree: `%s`\nReference: %s@%s:seed.sql\nReview parity.\n", worktree, repo, sha))
	before := referenceInventory(t, repo)
	code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
	if code != 0 {
		t.Fatalf("dispatch rc=%d out=%s err=%s", code, out, msg)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	report, _ := dispatchOutputField(t, out, "report")
	file := filepath.Join(f.state, "ws", "briefs", strings.TrimSuffix(filepath.Base(report), ".md")+".reference.diff")
	diff := mustRead(t, file)
	for _, hunk := range []string{"-DELETE;", "-INSERT;", "+UPSERT;"} {
		if !strings.Contains(diff, hunk) {
			t.Fatalf("missing hunk %q in %s", hunk, diff)
		}
	}
	if !strings.Contains(mustRead(t, composed), "Reference diff for this review: "+file) {
		t.Fatal("prompt does not name diff")
	}
	if after := referenceInventory(t, repo); !reflect.DeepEqual(before, after) {
		t.Fatal("reference repository changed")
	}
	t.Log("review dispatch: expected diff hunk, prompt path, reference repository unchanged")
}

func TestReferenceLintCommandModes(t *testing.T) {
	repo, sha, git := cliReferenceRepo(t, "baseline\n")
	f := roleDispatchFixture(t)
	f.env["PATH"] += string(os.PathListSeparator) + filepath.Dir(git)
	const full = "# Goal\nMirror.\n# Expected result\nParity.\n# Forbidden\nNo commit/push.\n# Report\nParity.\n"
	for _, tc := range []struct {
		name, ref string
		mode      string
		want      int
	}{{"good", sha + ":seed.sql", "strict", 0}, {"missing-commit-warn", strings.Repeat("0", 40), "warn", 1}, {"missing-commit-strict", strings.Repeat("0", 40), "strict", 2}, {"missing-path", sha + ":absent", "strict", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			writeRoleFixture(t, f.brief, full+"Reference: "+repo+"@"+tc.ref+"\n")
			f.env["SOHO_BRIEF_LINT"] = tc.mode
			rc, out, msg := run372Command(t, f, "lint", f.brief, "--role", "reviewer")
			if rc != tc.want {
				t.Fatalf("lint rc=%d want=%d out=%s err=%s", rc, tc.want, out, msg)
			}
			t.Logf("lint %s rc=%d", tc.mode, rc)
		})
	}
	// Strict dispatch refuses before sending; warn dispatch still sends an
	// edit-role brief (the reviewer diff hook is not applicable to this role).
	for _, mode := range []string{"strict", "warn"} {
		t.Run("dispatch-"+mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			f.env["PATH"] += string(os.PathListSeparator) + filepath.Dir(git)
			f.env["SOHO_BRIEF_LINT"] = mode
			writeRoleFixture(t, f.brief, full+"# Owned files\nseed.sql\nType: backend\nReference: "+repo+"@"+strings.Repeat("0", 40)+"\n")
			rc, out, msg := f.run(t, "worker", f.brief, "--role", "implementer", "--no-wait")
			want := 0
			if mode == "strict" {
				want = 2
			}
			if rc != want {
				t.Fatalf("dispatch %s rc=%d want=%d out=%s err=%s", mode, rc, want, out, msg)
			}
			if !strings.Contains(msg, "commit does not exist") {
				t.Fatalf("missing reference diagnostic: %s", msg)
			}
			if mode == "strict" {
				assertRoleNoSend(t, f)
			}
		})
	}
}

func TestReferenceWholeTreeCapAndCleanup(t *testing.T) {
	for _, size := range []int{10, referenceDiffCap + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			repo, sha, git := cliReferenceRepo(t, strings.Repeat("x", size))
			root := t.TempDir()
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, root)
			}
			env := platform.Env{"PATH": filepath.Dir(git), "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root}
			before := referenceInventory(t, repo)
			diff, list, err := buildReferenceDiff([]dispatch.Reference{{Repo: repo, Commit: sha}}, t.TempDir(), env)
			if err != nil {
				t.Fatal(err)
			}
			if list != (size > referenceDiffCap) || !strings.Contains(diff, "seed.sql") || len(diff) > referenceDiffCap {
				t.Fatalf("list=%t size=%d output bytes=%d", list, size, len(diff))
			}
			if list && strings.Contains(diff, "-"+strings.Repeat("x", 20)) {
				t.Fatal("large tree was materialized as a diff")
			}
			files, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if strings.HasPrefix(f.Name(), "soho-reference-") {
					t.Fatalf("materialization not cleaned: %s", f.Name())
				}
			}
			if !reflect.DeepEqual(before, referenceInventory(t, repo)) {
				t.Fatal("reference repository changed")
			}
		})
	}
}

func TestReferenceBinaryMaterialization(t *testing.T) {
	contents := string([]byte{0xff, 0x00, 0xfe, 0x80, 0x0a})
	repo, sha, git := cliReferenceRepo(t, contents)
	root := t.TempDir()
	writeRoleFixture(t, filepath.Join(root, "seed.sql"), contents)
	env := platform.Env{"PATH": filepath.Dir(git), "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root}
	diff, list, err := buildReferenceDiff([]dispatch.Reference{{Repo: repo, Commit: sha, Paths: []string{"seed.sql"}}}, root, env)
	if err != nil || list || strings.Contains(diff, "diff --git") {
		t.Fatalf("identical binary changed: list=%t err=%v diff=%q", list, err, diff)
	}
}

func TestReferenceSelectedPathIgnoresUnrelatedGitlink(t *testing.T) {
	repo, sha, git := cliReferenceRepo(t, "old\n")
	root := t.TempDir()
	env := platform.Env{"PATH": filepath.Dir(git), "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}
	run := func(input string, args ...string) string {
		c := exec.Command(git, append([]string{"-C", repo}, args...)...)
		c.Env = env.List()
		c.Stdin = strings.NewReader(input)
		raw, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v %s", err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	entries := run("", "ls-tree", sha)
	tree := run(entries+"\n160000 commit "+sha+"\tunrelated-submodule\n", "mktree")
	commit := run("gitlink fixture\n", "commit-tree", tree)
	writeRoleFixture(t, filepath.Join(root, "seed.sql"), "new\n")
	if err := os.Mkdir(filepath.Join(root, "unrelated-submodule"), 0700); err != nil {
		t.Fatal(err)
	}
	writeRoleFixture(t, filepath.Join(root, "unrelated-submodule", "foreign.txt"), "must not compare\n")
	diff, list, err := buildReferenceDiff([]dispatch.Reference{{Repo: repo, Commit: commit, Paths: []string{"seed.sql"}}}, root, env)
	if err != nil || list || !strings.Contains(diff, "+new") {
		t.Fatalf("selected path: list=%t err=%v diff=%q", list, err, diff)
	}
	for _, paths := range [][]string{nil, {"unrelated-submodule"}, {"seed.sql", "unrelated-submodule"}} {
		diff, list, err := buildReferenceDiff([]dispatch.Reference{{Repo: repo, Commit: commit, Paths: paths}}, root, env)
		if err != nil || list || !strings.Contains(diff, "Skipped gitlink: unrelated-submodule\n") {
			t.Fatalf("paths=%v list=%t err=%v diff=%q", paths, list, err, diff)
		}
		if len(paths) == 1 && strings.Contains(diff, "diff --git") {
			t.Fatalf("gitlink was compared: %s", diff)
		}
	}
	// A gitlink can also collide with an ordinary target file. It must never
	// become a target-only addition merely because the reference was skipped.
	target := t.TempDir()
	writeRoleFixture(t, filepath.Join(target, "unrelated-submodule"), "foreign target file\n")
	diff, list, err = buildReferenceDiff([]dispatch.Reference{{Repo: repo, Commit: commit, Paths: []string{"unrelated-submodule"}}}, target, env)
	want := fmt.Sprintf("Reference: %s@%s\nSkipped gitlink: unrelated-submodule\n", repo, commit)
	if err != nil || list || diff != want {
		t.Fatalf("gitlink target-file collision: list=%t err=%v diff=%q want=%q", list, err, diff, want)
	}
}
