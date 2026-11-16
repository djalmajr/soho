package gittree_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/testenv"
)

// Git mutates only this test's t.TempDir repository. Its environment is
// explicit; no Git configuration, identity, or session is inherited.
func diffFixtureGit(t *testing.T, f *diffFixture, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(f.env["PATH"], "git"), append([]string{"-C", f.tree}, args...)...)
	cmd.Env = []string{
		"PATH=" + testenv.HostPath(), "HOME=" + filepath.Dir(f.tree),
		"USERPROFILE=" + filepath.Dir(f.tree), "XDG_CONFIG_HOME=" + filepath.Dir(f.tree),
		"TMPDIR=" + filepath.Dir(f.tree), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q in %q: %v: %s", args, f.tree, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDiffReportCommittedPathsSortedAndWorkerCanEditThem(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `scripts/z.sh`\n- `scripts/a.sh`\n- `src/a.go`\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "scripts", "z.sh"), "committed z\n")
	diffWrite(t, filepath.Join(f.tree, "scripts", "a.sh"), "committed a\n")
	diffFixtureGit(t, f, "add", "scripts")
	diffFixtureGit(t, f, "commit", "-m", "orchestrator")
	diffWrite(t, filepath.Join(f.tree, "scripts", "z.sh"), "worker edit\n")
	value, _, stderr := f.wait(t)
	if !reflect.DeepEqual(value["committed_during_task"], []any{"scripts/a.sh", "scripts/z.sh"}) || !reflect.DeepEqual(value["outside_owned"], []any{"scripts/z.sh"}) {
		t.Errorf("overlap or sort: value=%v stderr=%q", value, stderr)
	}
	if !strings.Contains(stderr, "outside its Owned files: scripts/z.sh") || !reflect.DeepEqual(value["cited_unchanged"], []any{"src/a.go"}) {
		t.Errorf("citation or warning control: value=%v stderr=%q", value, stderr)
	}
}

func TestDiffReportCommitsDuringTask(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		outside, unrelated, clean bool
	}{
		{name: "owned worker"},
		{name: "outside worker", outside: true},
		{name: "non-ancestor", unrelated: true},
		{name: "commits only", clean: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/**\n", "## Files changed\n- `scripts/x.sh`\n- `src/b.go`\n", "implementer")
			diffWrite(t, filepath.Join(f.tree, "scripts", "x.sh"), "committed\n")
			diffFixtureGit(t, f, "add", "scripts/x.sh")
			diffFixtureGit(t, f, "commit", "-m", "orchestrator")
			if tc.unrelated {
				orphan := diffFixtureGit(t, f, "commit-tree", "HEAD^{tree}", "-m", "unrelated")
				diffFixtureGit(t, f, "update-ref", "HEAD", orphan)
			}
			if !tc.clean {
				diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "worker edit\n")
			}
			if tc.outside {
				diffWrite(t, filepath.Join(f.tree, "forbidden.go"), "worker outside\n")
			}
			value, _, stderr := f.wait(t)
			if tc.unrelated {
				if value["committed_during_task"] != nil || !reflect.DeepEqual(value["outside_owned"], []any{"scripts/x.sh"}) || !strings.Contains(stderr, "outside its Owned files: scripts/x.sh") {
					t.Fatalf("non-ancestor should retain original audit: value=%v stderr=%q", value, stderr)
				}
				return
			}
			if !reflect.DeepEqual(value["committed_during_task"], []any{"scripts/x.sh"}) {
				t.Errorf("committed paths = %v, want scripts/x.sh; value=%v stderr=%q", value["committed_during_task"], value, stderr)
			}
			if value["cited_unchanged"] != nil && !tc.clean {
				t.Errorf("committed or worker file cited as unchanged: %v", value["cited_unchanged"])
			}
			if !tc.clean {
				stat, err := os.ReadFile(value["diff_stat"].(string))
				if err != nil || strings.Contains(string(stat), "scripts/x.sh") || !strings.Contains(string(stat), "src/b.go") {
					t.Errorf("stat should contain worker, not committed changes: %q, %v", stat, err)
				}
			}
			if tc.outside {
				if !reflect.DeepEqual(value["outside_owned"], []any{"forbidden.go"}) || !strings.Contains(stderr, "outside its Owned files: forbidden.go") {
					t.Errorf("outside worker not reported: value=%v stderr=%q", value, stderr)
				}
			} else if value["outside_owned"] != nil || strings.Contains(stderr, "outside its Owned files") {
				t.Errorf("committed file counted as outside: value=%v stderr=%q", value, stderr)
			}
		})
	}
}
