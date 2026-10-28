package wait

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

type reportFilesFixture struct {
	*queuedProbeFixture
	tree, report, sidecar string
	at                    time.Time
}

func reportFilesWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func newReportFilesFixture(t *testing.T, state, role, owned string) *reportFilesFixture {
	t.Helper()
	f := &reportFilesFixture{queuedProbeFixture: newQueuedProbeFixture(t, state, "5", "", nil), tree: t.TempDir(), at: time.Unix(1_900_000_000, 0)}
	isolateReportEnv(t, f.env)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Keep PATH fixture-only while using the real Git binary for all reads.
	if err := os.Symlink(git, filepath.Join(f.bin, filepath.Base(git))); err != nil {
		t.Fatal(err)
	}
	f.env["GIT_CONFIG_NOSYSTEM"] = "1"
	f.env["GIT_CONFIG_GLOBAL"] = os.DevNull
	run := func(args ...string) string {
		t.Helper()
		r := platform.RunCli("git", append([]string{"-C", f.tree}, args...), platform.RunOptions{Env: f.env})
		if r.Status == nil || *r.Status != 0 {
			t.Fatalf("git %q: %+v", args, r)
		}
		return strings.TrimSpace(r.Stdout)
	}
	run("init", "-q")
	for _, name := range []string{"owned.txt", "other.txt", "z-owned.txt", "nested/child.txt"} {
		reportFilesWrite(t, filepath.Join(f.tree, name), "original\n")
	}
	run("add", "--", "owned.txt", "other.txt", "z-owned.txt", "nested/child.txt")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	head := run("rev-parse", "HEAD")
	f.report = filepath.Join(t.TempDir(), "report.md")
	reportFilesWrite(t, f.report, "report")
	if err := os.Chtimes(f.report, f.at, f.at); err != nil {
		t.Fatal(err)
	}
	f.sidecar = filepath.Join(f.sd, "briefs", "report.dispatch.json")
	b, err := json.Marshal(map[string]string{"git_tree": f.tree, "git_head": head, "role": role})
	if err != nil {
		t.Fatal(err)
	}
	reportFilesWrite(t, f.sidecar, string(b))
	reportFilesWrite(t, strings.TrimSuffix(f.sidecar, ".dispatch.json")+".brief.md", "# Brief\n"+owned)
	reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), f.report+"\n")
	f.env["SOHO_DIR"] = filepath.Dir(f.sd)
	f.env["HERDR_WORKSPACE_ID"] = "ws"
	return f
}

func (f *reportFilesFixture) change(t *testing.T, name string, delta time.Duration) {
	t.Helper()
	path := filepath.Join(f.tree, name)
	reportFilesWrite(t, path, "changed\n")
	at := f.at.Add(delta)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func (f *reportFilesFixture) identity(t *testing.T) string {
	t.Helper()
	info, err := os.Stat(f.report)
	if err != nil {
		t.Fatal(err)
	}
	return reportIdentity(f.report, info)
}

func TestReportFilesWaitCases(t *testing.T) {
	for _, tc := range []struct {
		name, role, owned, file, state, want string
		delta                                time.Duration
	}{
		{"tracked owned", "implementer", "## Owned files\n- `owned.txt`\n", "owned.txt", "idle", "pending", time.Second},
		{"done pane", "implementer", "## Owned files\n- `owned.txt`\n", "owned.txt", "done", "pending", time.Second},
		{"older owned", "implementer", "## Owned files\n- `owned.txt`\n", "owned.txt", "idle", "done", -time.Second},
		{"equal mtime", "implementer", "## Owned files\n- `owned.txt`\n", "owned.txt", "idle", "done", 0},
		{"outside owned", "implementer", "## Owned files\n- `owned.txt`\n", "other.txt", "idle", "done", time.Second},
		{"untracked owned", "implementer", "## Owned files\n- `new file.txt`\n", "new file.txt", "idle", "pending", time.Second},
		{"no owned", "implementer", "", "other.txt", "idle", "pending", time.Second},
		{"read only", "reviewer", "", "owned.txt", "idle", "done", time.Second},
		{"gone", "implementer", "", "owned.txt", "gone", "done", time.Second},
		{"unavailable", "implementer", "", "owned.txt", "unavailable", "done", time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReportFilesFixture(t, tc.state, tc.role, tc.owned)
			f.change(t, tc.file, tc.delta)
			if got := f.probe(f.report); got != "pending" {
				t.Fatal(got)
			}
			if got := f.probe(f.report); got != tc.want {
				t.Fatalf("got=%s want=%s tree=%s", got, tc.want, f.tree)
			}
			if tc.want == "done" {
				if got, _ := readWaitFile(f.sd, "worker", "report-settled"); got != f.identity(t) {
					t.Fatalf("settled=%q", got)
				}
			}
		})
	}
}

func TestReportFilesHoldCapAndIdentity(t *testing.T) {
	f := newReportFilesFixture(t, "idle", "implementer", "## Owned files\n- `*-owned.txt`\n- `owned.txt`\n")
	f.change(t, "z-owned.txt", time.Second)
	f.change(t, "owned.txt", time.Second)
	now := f.at.Add(time.Minute)
	oldNow, oldErr := platform.Now, platform.Stderr
	var stderr bytes.Buffer
	platform.Now, platform.Stderr = func() time.Time { return now }, &stderr
	t.Cleanup(func() { platform.Now, platform.Stderr = oldNow, oldErr })
	for i := 0; i < 3; i++ {
		if got := f.probe(f.report); got != "pending" {
			t.Fatal(got)
		}
	}
	want := "soho: wait: 'worker' changed files after writing its report (owned.txt, 2 in total); waiting for it to finish\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q", stderr.String())
	}
	now = now.Add(600 * time.Second)
	if got := f.probe(f.report); got != "pending" {
		t.Fatal(got)
	}
	now = now.Add(time.Second)
	if got := f.probe(f.report); got != "done" {
		t.Fatal(got)
	}
	want += "soho: wait: 'worker' kept changing files after its report for 601s; treating the report as final\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q", stderr.String())
	}
	for _, name := range []string{"report-stable-at", "report-files-held"} {
		if _, err := os.Stat(f.marker(name)); !os.IsNotExist(err) {
			t.Fatalf("left %s: %v", name, err)
		}
	}
	if got := f.probe(f.report); got != "done" {
		t.Fatal(got)
	}
	// A different identity clears the latch and begins a fresh hold.
	if err := os.Chtimes(f.report, f.at.Add(-time.Second), f.at.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := f.probe(f.report); got != "pending" {
		t.Fatal(got)
	}
	if _, err := os.Stat(f.marker("report-settled")); !os.IsNotExist(err) {
		t.Fatalf("stale settled: %v", err)
	}
}

func TestReportFilesSettledSkipsLaterEdits(t *testing.T) {
	f := newReportFilesFixture(t, "idle", "implementer", "## Owned files\n- `owned.txt`\n")
	f.change(t, "owned.txt", -time.Second)
	f.probe(f.report)
	if got := f.probe(f.report); got != "done" {
		t.Fatal(got)
	}
	f.change(t, "owned.txt", time.Second)
	// Every new wait clears its size sample; settlement must survive that too.
	removeWaitFile(f.sd, "worker", "size")
	if got := f.probe(f.report); got != "done" {
		t.Fatalf("settled report reopened: %s", got)
	}
}

func TestReportFilesFailOpen(t *testing.T) {
	for _, mode := range []string{"git failure", "missing sidecar", "stat failure", "invalid head"} {
		t.Run(mode, func(t *testing.T) {
			f := newReportFilesFixture(t, "idle", "implementer", "")
			f.change(t, "owned.txt", time.Second)
			switch mode {
			case "git failure":
				reportFilesWrite(t, f.sidecar, fmt.Sprintf(`{"git_tree":%q,"git_head":%q,"role":"implementer"}`, t.TempDir(), strings.Repeat("a", 40)))
			case "missing sidecar":
				f.report = filepath.Join(t.TempDir(), "other.md")
				reportFilesWrite(t, f.report, "report")
			case "stat failure":
				if runtime.GOOS == "windows" {
					// Windows reports a file used as a directory as path-not-found,
					// which Go maps to ErrNotExist; the skip path is covered by
					// TestReportFilesDeletedPathKeepsHold.
					t.Skip("no portable non-ErrNotExist Lstat failure on Windows")
				}
				if err := os.Remove(filepath.Join(f.tree, "nested", "child.txt")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(f.tree, "nested")); err != nil {
					t.Fatal(err)
				}
				reportFilesWrite(t, filepath.Join(f.tree, "nested"), "parent is now a file\n")
				if _, err := os.Lstat(filepath.Join(f.tree, "nested", "child.txt")); err == nil || errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("need a non-ErrNotExist stat failure: %v", err)
				} else {
					t.Logf("non-ErrNotExist stat failure: %v", err)
				}
			case "invalid head":
				reportFilesWrite(t, f.sidecar, fmt.Sprintf(`{"git_tree":%q,"git_head":"invalid","role":"implementer"}`, f.tree))
			}
			f.probe(f.report)
			if got := f.probe(f.report); got != "done" {
				t.Fatal(got)
			}
		})
	}
}

func TestReportFilesDeletedPathKeepsHold(t *testing.T) {
	f := newReportFilesFixture(t, "idle", "implementer", "## Owned files\n- `*.txt`\n")
	if err := os.Remove(filepath.Join(f.tree, "other.txt")); err != nil {
		t.Fatal(err)
	}
	f.change(t, "owned.txt", time.Second)
	oldErr := platform.Stderr
	var stderr bytes.Buffer
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = oldErr })
	for i := 0; i < 2; i++ {
		if got := f.probe(f.report); got != "pending" {
			t.Fatalf("probe %d=%s stderr=%s", i, got, stderr.String())
		}
	}
	want := "soho: wait: 'worker' changed files after writing its report (owned.txt, 1 in total); waiting for it to finish\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q want=%q", stderr.String(), want)
	}
	t.Log(stderr.String())
}

func reportWaitInventory(t *testing.T, sd string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(filepath.Join(sd, "wait"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		out[d.Name()] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStatusReportFilesReadOnly(t *testing.T) {
	for _, mode := range []string{"working", "newer", "older", "final", "disabled", "expired", "settled", "settled working", "gone", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			pane := "idle"
			if mode == "working" || mode == "settled working" {
				pane = "working"
			}
			if mode == "gone" || mode == "unavailable" {
				pane = mode
			}
			f := newReportFilesFixture(t, pane, "implementer", "## Owned files\n- `owned.txt`\n")
			f.change(t, "owned.txt", time.Second)
			want, cause := "done", ""
			switch mode {
			case "working", "settled working":
				want, cause = "working", "report draft: pane working"
			case "newer":
				want, cause = "working", "report draft: files changed after it"
			case "older":
				f.change(t, "owned.txt", -time.Second)
			case "final":
				writeWaitFile(f.sd, "worker", "report-working-final", f.identity(t)+"\n")
			case "disabled":
				f.ctx.Entries["report_working_max"] = core.ConfigEntry{Value: "0"}
			case "expired":
				writeWaitFile(f.sd, "worker", "report-stable-at", fmt.Sprintf("%d\t%s\n", platform.Now().Unix()-601, f.identity(t)))
			}
			if strings.HasPrefix(mode, "settled") {
				writeWaitFile(f.sd, "worker", "report-settled", f.identity(t)+"\n")
			}
			writeWaitFile(f.sd, "worker", "report-files-held", "stale identity\n")
			before := reportWaitInventory(t, f.sd)
			t.Logf("wait before: %#v", before)
			s := notInRosterFixture{cwd: f.tree, state: filepath.Dir(f.sd), ctx: f.ctx, env: f.env}
			for i := 0; i < 2; i++ {
				rc, out, stderr := s.run(t, []string{"worker"})
				if rc != 0 || !strings.Contains(out, "worker\t"+want+"\t"+f.report+"\t") || stderr != "" || cause != "" && !strings.Contains(out, cause) {
					t.Fatalf("rc=%d out=%q stderr=%q want=%s/%s", rc, out, stderr, want, cause)
				}
			}
			after := reportWaitInventory(t, f.sd)
			t.Logf("wait after: %#v", after)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("wait changed: before=%#v after=%#v", before, after)
			}
		})
	}
}
