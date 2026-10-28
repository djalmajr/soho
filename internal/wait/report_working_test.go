package wait

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestReportWorkingCompletion(t *testing.T) {
	for _, state := range []string{"working", "idle", "done", "gone", "unavailable", "blocked"} {
		t.Run(state, func(t *testing.T) {
			f := newQueuedProbeFixture(t, state, "5", "", nil)
			isolateReportEnv(t, f.env)
			report := filepath.Join(t.TempDir(), "report.md")
			if err := os.WriteFile(report, []byte("partial report"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := f.probe(report); got != "pending" {
				t.Fatalf("first probe=%q", got)
			}
			want := "done"
			if state == "working" || state == "blocked" {
				want = "pending"
			}
			if got := f.probe(report); got != want {
				t.Fatalf("state=%s stable probe=%q want=%q env=%v", state, got, want, f.env)
			}
			if want == "done" {
				assertReportTimerAbsent(t, f)
			}
		})
	}
}

func isolateReportEnv(t *testing.T, env platform.Env) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		env[key] = t.TempDir()
	}
}

func assertReportTimerAbsent(t *testing.T, f *queuedProbeFixture) {
	t.Helper()
	if _, err := os.Stat(f.marker("report-stable-at")); !os.IsNotExist(err) {
		t.Fatalf("completion left timer: %v", err)
	}
}

func TestReportWorkingIdleTransition(t *testing.T) {
	f := newQueuedProbeFixture(t, "working", "5", "", nil)
	isolateReportEnv(t, f.env)
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if got := f.probe(report); got != "pending" {
			t.Fatalf("working probe %d=%s", i, got)
		}
	}
	// Rewrite only the fake's response, keeping the same agent and wait files.
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}}); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	if got := f.probe(report); got != "done" {
		t.Fatalf("idle probe=%s", got)
	}
	assertReportTimerAbsent(t, f)
}

func TestReportWorkingCapAndReset(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow, oldErr := platform.Now, platform.Stderr
	var stderr bytes.Buffer
	platform.Now = func() time.Time { return now }
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Now, platform.Stderr = oldNow, oldErr })
	f := newQueuedProbeFixture(t, "working", "5", "", nil)
	isolateReportEnv(t, f.env)
	report := filepath.Join(t.TempDir(), "report.md")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(report, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(want string) {
		t.Helper()
		if got := f.probe(report); got != want {
			t.Fatalf("now=%v got=%s want=%s stderr=%s", now, got, want, stderr.String())
		}
	}
	write("partial")
	probe("pending")
	probe("pending")
	info, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := reportStableAt(f.sd, "worker", reportIdentity(report, info)); !ok || at != now.Unix() {
		t.Fatalf("timer=%d valid=%v", at, ok)
	}
	now = now.Add(600 * time.Second)
	probe("pending")
	write("partial updated")
	probe("pending")
	assertReportTimerAbsent(t, f)
	probe("pending")
	now = now.Add(600 * time.Second)
	probe("pending")
	now = now.Add(time.Second)
	probe("done")
	probe("done")
	want := "soho: wait: 'worker' wrote its report but its pane is still working after 601s; treating the report as final\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q want=%q", stderr.String(), want)
	}
	assertReportTimerAbsent(t, f)
	// An empty report also resets the previous stability window.
	writeWaitFile(f.sd, "worker", "report-stable-at", "1\n")
	write("")
	f.probe(report)
	assertReportTimerAbsent(t, f)
}

func TestReportWorkingDisabled(t *testing.T) {
	f := newQueuedProbeFixture(t, "working", "5", "", map[string]string{"report-stable-at": "1\n"})
	isolateReportEnv(t, f.env)
	f.ctx.Entries["report_working_max"] = core.ConfigEntry{Value: "0"}
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := f.probe(report); got != "pending" {
		t.Fatal(got)
	}
	if got := f.probe(report); got != "done" {
		t.Fatal(got)
	}
	assertReportTimerAbsent(t, f)
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("disabled guard queried herdr: %v", calls)
	}
}

func TestStatusReportWorking(t *testing.T) {
	f := newNotInRosterFixture(t, "worker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
	isolateReportEnv(t, f.env)
	sd := filepath.Join(f.state, "ws")
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "last-report-worker"), []byte(report+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A malformed clock does not authorize completion; status leaves it
	// untouched while showing the working pane as a report draft.
	timer := filepath.Join(sd, "wait", "worker.report-stable-at")
	const original = "not a stability clock\n"
	if err := os.WriteFile(timer, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		rc, out, stderr := f.run(t, []string{"worker"})
		if rc != 0 || !strings.Contains(out, "worker\tworking\t"+report) || !strings.Contains(out, "report draft: pane working") || stderr != "" {
			t.Fatalf("probe=%d rc=%d out=%s stderr=%s", i, rc, out, stderr)
		}
	}
	raw, err := os.ReadFile(timer)
	if err != nil || string(raw) != original {
		t.Fatalf("status changed clock: %q err=%v", raw, err)
	}
	wantLog(t, f.callLog(t), "agent\x00get\x00worker", "agent\x00read\x00worker\x00--source\x00visible", "agent\x00get\x00worker", "agent\x00read\x00worker\x00--source\x00visible")
}
