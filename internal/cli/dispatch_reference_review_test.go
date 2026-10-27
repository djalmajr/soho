package cli

import (
	"fmt"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func review544Commit(t *testing.T, repo, git, input string, args ...string) string {
	t.Helper()
	c := exec.Command(git, append([]string{"-C", repo}, args...)...)
	root := t.TempDir()
	c.Env = platform.Env{"PATH": filepath.Dir(git), "HOME": root, "USERPROFILE": root, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}.List()
	c.Stdin = strings.NewReader(input)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("fixture git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func TestReview544WholeTreeExcludesGitlinks(t *testing.T) {
	repo, sha, git := cliReferenceRepo(t, "old\n")
	entries := review544Commit(t, repo, git, "", "ls-tree", sha)
	tree := review544Commit(t, repo, git, entries+"\n160000 commit "+sha+"\tmodule\n", "mktree")
	commit := review544Commit(t, repo, git, "gitlink fixture\n", "commit-tree", tree)
	target := t.TempDir()
	writeRoleFixture(t, filepath.Join(target, "seed.sql"), "new\n")
	writeRoleFixture(t, filepath.Join(target, "target-only.txt"), "out of scope\n")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before := referenceInventory(t, repo)
	diff, list, e := buildReferenceDiff([]dispatch.Reference{{Repo: repo, Commit: commit}}, target, platform.Env{"PATH": filepath.Dir(git), "HOME": tmp})
	children, _ := os.ReadDir(tmp)
	t.Logf("list=%t error=%v diff=%q temporary_entries=%d reference_unchanged=%t", list, e, diff, len(children), reflect.DeepEqual(before, referenceInventory(t, repo)))
	if len(children) != 0 {
		t.Fatal("temporary materialization leaked")
	}
	if !reflect.DeepEqual(before, referenceInventory(t, repo)) {
		t.Fatal("reference repository changed")
	}
	if e != nil || list || !strings.Contains(diff, "+new") || strings.Contains(diff, "target-only") || !strings.Contains(diff, "Skipped gitlink: module\n") {
		t.Fatal("whole-tree comparison must exclude Gitlinks and target-only files while comparing reference blobs")
	}
}
func TestReview544RejectLeadingDashPath(t *testing.T) {
	repo, _, git := cliReferenceRepo(t, "old\n")
	blob := review544Commit(t, repo, git, "data\n", "hash-object", "-w", "--stdin")
	tree := review544Commit(t, repo, git, "100644 blob "+blob+"\t-leading.go\n", "mktree")
	sha := review544Commit(t, repo, git, "dash fixture\n", "commit-tree", tree)
	f := roleDispatchFixture(t)
	f.env["PATH"] += string(os.PathListSeparator) + filepath.Dir(git)
	f.env["SOHO_BRIEF_LINT"] = "strict"
	body := fmt.Sprintf("# Goal\nReview.\n# Expected result\nReview.\n# Forbidden\nNo commit/push.\n# Report\nReview.\nReference: %s@%s:-leading.go\n", repo, sha)
	writeRoleFixture(t, f.brief, body)
	code, out, msg := run372Command(t, f, "lint", f.brief, "--role", "reviewer")
	t.Logf("path_ok=%t parsed=%+v strict_lint_exit=%d stdout=%q stderr=%q", dispatch.ReferencePathOK("-leading.go"), dispatch.DeclaredReferences(body), code, out, msg)
	if code != 2 {
		t.Fatal("strict lint must refuse the explicitly prohibited leading-dash path")
	}
}
