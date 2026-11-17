package wait

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// A review report that carries the findings/verdict header, with the given
// counts. The first non-empty line is the header, exactly as the role
// contract requires.
func reviewReportBody(p0, p1, p2, p3 int, verdict string) string {
	return "findings: " + strconv.Itoa(p0+p1+p2+p3) + " (P0 " + strconv.Itoa(p0) + ", P1 " + strconv.Itoa(p1) + ", P2 " + strconv.Itoa(p2) + ", P3 " + strconv.Itoa(p3) + ") | verdict: " + verdict + "\n\nbody\n"
}

// waitVerdictRun runs WaitFor to completion against a reviewer agent whose
// report already exists on disk, and returns the captured stdout, stderr and
// exit code.
func waitVerdictRun(t *testing.T, body string) (string, string, int) {
	t.Helper()
	f := newQueuedProbeFixture(t, "idle", "5", "done screen\n", nil)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tclaude\treviewer\t\t\t\t\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(f.sd, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["HERDR_WORKSPACE_ID"] = "ws"
	var stdout, stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &stdout, &stderr
	code := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, 5000, false, "")
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return stdout.String(), stderr.String(), code
}

func TestWaitVerdictEffective(t *testing.T) {
	t.Run("wait: a pass header with no open P0-P2 and no partial is verdict_effective pass", func(t *testing.T) {
		out, _, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 0, "pass"))
		if code != 0 || !strings.Contains(out, `"verdict":"pass"`) || !strings.Contains(out, `"P3":0},"verdict_effective":"pass"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a pass header with an open P2 is verdict_effective fail", func(t *testing.T) {
		// Mutation captured: dropping P2 from the fail conditions would print
		// pass on this report.
		out, _, code := waitVerdictRun(t, reviewReportBody(0, 0, 1, 0, "pass"))
		if code != 0 || !strings.Contains(out, `"verdict":"pass"`) || !strings.Contains(out, `"verdict_effective":"fail"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a fail header is verdict_effective fail", func(t *testing.T) {
		out, _, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 0, "fail"))
		if code != 0 || !strings.Contains(out, `"verdict":"fail"`) || !strings.Contains(out, `"verdict_effective":"fail"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a pass header with a partial item is verdict_effective fail after partial", func(t *testing.T) {
		// Mutation captured: ignoring partial in the fail conditions would
		// print pass on this report.
		out, _, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 0, "pass")+"| item | state |\n| --- | --- |\n| slice | [partial] |\n")
		if code != 0 || !strings.Contains(out, `"verdict":"pass"`) || !strings.Contains(out, `"partial":1,"verdict_effective":"fail"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a report without a header and without partial has no verdict_effective", func(t *testing.T) {
		out, _, code := waitVerdictRun(t, "# Report\n\nnothing marked\n")
		if code != 0 || !strings.Contains(out, `"status":"done"`) || strings.Contains(out, `"verdict_effective"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a report without a header but with a partial item is verdict_effective fail", func(t *testing.T) {
		out, _, code := waitVerdictRun(t, "# Report\n\n| item | state |\n| --- | --- |\n| slice | [partial] |\n")
		if code != 0 || strings.Contains(out, `"verdict":`) || !strings.Contains(out, `"partial":1,"verdict_effective":"fail"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

func TestWaitVerdictPassOpenFindings(t *testing.T) {
	t.Run("wait: a verdict pass with an open P2 warns and still exits 0", func(t *testing.T) {
		// Mutation captured: dropping the P2 from the open count silences the
		// warning for the pass-with-P2 report.
		out, errText, code := waitVerdictRun(t, reviewReportBody(0, 0, 1, 0, "pass"))
		want := "report of 'worker' says verdict pass with 1 open P0-P2 finding(s): read them before commit, push or release"
		if code != 0 || !strings.Contains(out, `"status":"done"`) || !strings.Contains(out, `"verdict":"pass"`) || !strings.Contains(errText, want) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errText)
		}
	})
	t.Run("wait: a verdict fail with an open P2 does not warn", func(t *testing.T) {
		out, errText, code := waitVerdictRun(t, reviewReportBody(0, 0, 1, 0, "fail"))
		if code != 0 || !strings.Contains(out, `"verdict":"fail"`) || strings.Contains(errText, "verdict pass with") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errText)
		}
	})
	t.Run("wait: a verdict pass with only P3 does not warn", func(t *testing.T) {
		// Mutation captured: counting P3 as open would warn on this report.
		out, errText, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 1, "pass"))
		if code != 0 || !strings.Contains(out, `"verdict":"pass"`) || strings.Contains(errText, "verdict pass with") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errText)
		}
	})
}

func TestWaitBlockedByScopeSummary(t *testing.T) {
	out, _, code := waitVerdictRun(t, "- [partial] blocked-by-scope: a.go:12 — edit one\n- [partial] blocked-by-scope: dir/a b.go:3 – edit two\n- [partial] blocked-by-scope: C:\\src\\a b.go:12 — edit three\n- [partial] blocked-by-scope: bad.go — no line\n- [partial] blocked-by-scope: bad.go:4 no dash\n")
	for _, want := range []string{"blocked by scope: a.go:12 — edit one", "blocked by scope: dir/a b.go:3 – edit two", `blocked by scope: C:\\src\\a b.go:12 — edit three`} {
		if !strings.Contains(out, want) {
			t.Fatalf("code=%d out=%q missing %q", code, out, want)
		}
	}
	if code != 0 || strings.Contains(out, "bad.go") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
