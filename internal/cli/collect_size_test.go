package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const collectSizeWarning = "soho: warning: report of 'alice' is 2.0 MB; keep long logs in a file beside it and cite short excerpts"

// collectSizeFixture points collect at a sparse report in the fixture's own
// temp dir; the report is never committed and no real 2 MB is written.
func collectSizeFixture(t *testing.T, size int64) (*statsFixture, string) {
	t.Helper()
	f := newCommandFixture(t)
	report := filepath.Join(f.reports, "alice-20261120T034936.md")
	if err := os.WriteFile(report, []byte("# Report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(report, size); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, report
}

// SOH-406: collect still prints the whole report and exits 0, but warns once
// when it is over 1 MB; the warning names the agent whose report it read.
func TestCollectOversizedReportWarnsOnce(t *testing.T) {
	f, report := collectSizeFixture(t, 2*1024*1024+1)
	out, errOut, code := f.invoke("collect", "alice")
	if code != 0 || !strings.Contains(out, "<!-- report: "+report+" -->") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if got := strings.Count(errOut, collectSizeWarning); got != 1 {
		t.Fatalf("size warning count=%d, want 1: %q", got, errOut)
	}
}

func TestCollectReportSizeBoundaryDoesNotWarn(t *testing.T) {
	t.Run("exactly 1 MB", func(t *testing.T) {
		f, _ := collectSizeFixture(t, 1024*1024)
		_, errOut, code := f.invoke("collect", "alice")
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if strings.Contains(errOut, "keep long logs in a file beside it") {
			t.Fatalf("boundary warned: %q", errOut)
		}
	})
	t.Run("empty report", func(t *testing.T) {
		f, _ := collectSizeFixture(t, 0)
		_, errOut, _ := f.invoke("collect", "alice")
		if strings.Contains(errOut, "keep long logs in a file beside it") {
			t.Fatalf("empty report warned: %q", errOut)
		}
	})
	t.Run("missing report", func(t *testing.T) {
		f, report := collectSizeFixture(t, 1024)
		if err := os.Remove(report); err != nil {
			t.Fatal(err)
		}
		_, errOut, _ := f.invoke("collect", "alice")
		if strings.Contains(errOut, "keep long logs in a file beside it") {
			t.Fatalf("missing report warned: %q", errOut)
		}
	})
}
