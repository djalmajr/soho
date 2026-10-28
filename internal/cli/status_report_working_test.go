package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestWaitStatusReportWorking(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n"
	f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		f.env[key] = t.TempDir()
	}
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("partial report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "worker")
	if code != 0 || !strings.Contains(out, "worker\tworking\t"+report) || !strings.Contains(out, "report draft: pane working") || stderr != "" {
		t.Fatalf("status code=%d out=%s stderr=%s", code, out, stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil || len(calls) == 0 || strings.Join(calls[0].Argv, " ") != "agent get worker" {
		t.Fatalf("status did not query the pane: calls=%v err=%v", calls, err)
	}
	if _, err := os.Stat(filepath.Join(f.state, "wait", "worker.report-stable-at")); !os.IsNotExist(err) {
		t.Fatalf("status created a stability clock: %v", err)
	}
	code, out, stderr = f.runWait(t, "worker", "--timeout", "100")
	if code != 9 || !strings.Contains(out, `"status":"timeout"`) || !strings.Contains(out, `"state":"pending"`) {
		t.Fatalf("wait code=%d out=%s stderr=%s", code, out, stderr)
	}
	f.env["SOHO_REPORT_WORKING_MAX"] = "0"
	code, out, stderr = f.runWait(t, "worker", "--timeout", "1000")
	if code != 0 || !strings.Contains(out, `"status":"done"`) {
		t.Fatalf("disabled wait code=%d out=%s stderr=%s", code, out, stderr)
	}
}

func TestStatusReportWorkingNowrite(t *testing.T) {
	nr := newNowriteReads(t, true)
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}}); err != nil {
		t.Fatal(err)
	}
	nr.f.env["PATH"], nr.f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	code, out, stderr := nr.runNowrite(t, "status", "worker")
	if code != 0 || !strings.Contains(out, "worker\tworking\t"+nr.report) || !strings.Contains(out, "report draft: pane working") || stderr != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
}

// The review probe uses separate public waits, with time advancing between
// them while the same report and the same working pane remain in place.
func TestReview486RepeatedWaitCap(t *testing.T) {
	f, report := repeatedReportWaitFixture(t)
	clock := time.Unix(2_000_000_000, 0)
	old := platform.Now
	platform.Now = func() time.Time { r := clock; clock = clock.Add(time.Second); return r }
	t.Cleanup(func() { platform.Now = old })
	code, out, stderr := f.runWait(t, "worker", "--timeout", "10000")
	timer := filepath.Join(f.state, "wait", "worker.report-stable-at")
	first, err := os.ReadFile(timer)
	if err != nil || code != 9 {
		t.Fatalf("first wait code=%d out=%s stderr=%s timer=%q err=%v", code, out, stderr, first, err)
	}
	var at int64
	if _, err := fmt.Sscan(string(first), &at); err != nil {
		t.Fatal(err)
	}
	clock = time.Unix(at+601, 0)
	code, out, stderr = f.runWait(t, "worker", "--timeout", "10000")
	t.Logf("same report %s, pane continuously working; timer=%q second wait starts=%d code=%d out=%q stderr=%q", report, first, at+601, code, out, stderr)
	if code != 0 || !strings.Contains(out, `"status":"done"`) || !strings.Contains(stderr, "treating the report as final") {
		t.Fatal("a new wait reset the unchanged report's clock")
	}
	if _, err := os.Stat(timer); !os.IsNotExist(err) {
		t.Fatalf("done left timer: %v", err)
	}
	// A third wait must keep the completed result despite clearing worker.size.
	code, out, stderr = f.runWait(t, "worker", "--timeout", "10000")
	if code != 0 || !strings.Contains(out, `"status":"done"`) || stderr != "" {
		t.Fatalf("latched wait code=%d out=%s stderr=%s", code, out, stderr)
	}
}

func repeatedReportWaitFixture(t *testing.T) (*waitStatusFixture, string) {
	t.Helper()
	f := newWaitStatusFixture(t, "worker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		f.env[key] = t.TempDir()
	}
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("report body"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return f, report
}

func TestReportChangedBetweenWaits(t *testing.T) {
	for _, change := range []string{"size", "same size new mtime", "path"} {
		t.Run(change, func(t *testing.T) {
			f, report := repeatedReportWaitFixture(t)
			clock := time.Unix(2_000_000_000, 0)
			old := platform.Now
			platform.Now = func() time.Time { r := clock; clock = clock.Add(time.Second); return r }
			t.Cleanup(func() { platform.Now = old })
			code, out, stderr := f.runWait(t, "worker", "--timeout", "10000")
			if code != 9 {
				t.Fatalf("first wait code=%d out=%s stderr=%s", code, out, stderr)
			}
			timer := filepath.Join(f.state, "wait", "worker.report-stable-at")
			first, err := os.ReadFile(timer)
			if err != nil {
				t.Fatal(err)
			}
			var at int64
			if _, err := fmt.Sscan(string(first), &at); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(report)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "size":
				if err := os.WriteFile(report, []byte("a report with more bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "same size new mtime":
				if err := os.WriteFile(report, []byte("new content"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(report, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				changed, err := os.Stat(report)
				if err != nil || changed.Size() != info.Size() || changed.ModTime().Equal(info.ModTime()) {
					t.Fatalf("mtime fixture old=%v new=%v err=%v", info, changed, err)
				}
			case "path":
				next := filepath.Join(t.TempDir(), "report.md")
				raw, err := os.ReadFile(report)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(next, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(next, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(next+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			clock = time.Unix(at+601, 0)
			code, out, stderr = f.runWait(t, "worker", "--timeout", "10000")
			second, err := os.ReadFile(timer)
			var nextAt int64
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Sscan(string(second), &nextAt); err != nil {
				t.Fatal(err)
			}
			if code != 9 || !strings.Contains(out, `"state":"pending"`) || strings.Contains(stderr, "treating the report as final") || nextAt < at+601 {
				t.Fatalf("change=%s code=%d out=%s stderr=%s old timer=%q new timer=%q", change, code, out, stderr, first, second)
			}
			clock = time.Unix(nextAt+601, 0)
			code, out, stderr = f.runWait(t, "worker", "--timeout", "10000")
			if code != 0 || !strings.Contains(out, `"status":"done"`) || !strings.Contains(stderr, "treating the report as final") {
				t.Fatalf("new stable report code=%d out=%s stderr=%s", code, out, stderr)
			}
		})
	}
}
