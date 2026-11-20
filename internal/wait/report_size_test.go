package wait

import (
	"os"
	"strings"
	"testing"
)

const reportSizeWarningText = "soho: warning: report of 'worker' is 2.0 MB; keep long logs in a file beside it and cite short excerpts"

// makeSparseReport grows the fixture's report to an exact byte size without
// writing that many bytes. The short header keeps the report parseable; the
// remainder is a sparse tail in t.TempDir.
func makeSparseReport(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

func TestWaitOversizedReportWarnsOnce(t *testing.T) {
	t.Run("wait: a settled report over 1 MB warns and still settles done", func(t *testing.T) {
		m := newMetricsFixture(t, "", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		makeSparseReport(t, m.report, 2*1024*1024+1)
		out, errText, rc := m.run(t)
		if rc != 0 || !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("rc=%d out=%q err=%q", rc, out, errText)
		}
		if got := strings.Count(errText, reportSizeWarningText); got != 1 {
			t.Fatalf("size warning count=%d, want 1: %q", got, errText)
		}
	})
	t.Run("wait: another poll of the same report does not warn again", func(t *testing.T) {
		m := newMetricsFixture(t, "", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		makeSparseReport(t, m.report, 2*1024*1024+1)
		_, err1, rc1 := m.run(t)
		_, err2, rc2 := m.run(t)
		if rc1 != 0 || rc2 != 0 {
			t.Fatalf("rc1=%d rc2=%d", rc1, rc2)
		}
		if strings.Count(err1, reportSizeWarningText) != 1 {
			t.Fatalf("first poll: %q", err1)
		}
		if strings.Contains(err2, "keep long logs in a file beside it") {
			t.Fatalf("second poll warned again: %q", err2)
		}
	})
}

func TestReportSizeBoundaryDoesNotWarn(t *testing.T) {
	t.Run("exactly 1 MB", func(t *testing.T) {
		m := newMetricsFixture(t, "", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		makeSparseReport(t, m.report, 1024*1024)
		out, errText, rc := m.run(t)
		if rc != 0 || !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("rc=%d out=%q err=%q", rc, out, errText)
		}
		if strings.Contains(errText, "keep long logs in a file beside it") {
			t.Fatalf("boundary warned: %q", errText)
		}
	})
	t.Run("empty report stays without a size warning", func(t *testing.T) {
		m := newMetricsFixture(t, "", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		makeSparseReport(t, m.report, 0)
		_, errText, _ := m.run(t)
		if strings.Contains(errText, "keep long logs in a file beside it") {
			t.Fatalf("empty report warned: %q", errText)
		}
	})
	t.Run("missing report stays without a size warning", func(t *testing.T) {
		m := newMetricsFixture(t, "", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		if err := os.Remove(m.report); err != nil {
			t.Fatal(err)
		}
		_, errText, _ := m.run(t)
		if strings.Contains(errText, "keep long logs in a file beside it") {
			t.Fatalf("missing report warned: %q", errText)
		}
	})
}

func TestReportSizeWarningNamesOnlyTheAgent(t *testing.T) {
	warning, ok := ReportSizeWarning("hygiene-b", 2*1024*1024)
	if !ok || warning.Kind != WarningReportSize {
		t.Fatalf("warning=%+v ok=%v", warning, ok)
	}
	if warning.Message != "report of 'hygiene-b' is 2.0 MB; keep long logs in a file beside it and cite short excerpts" {
		t.Fatalf("message=%q", warning.Message)
	}
	if _, ok := ReportSizeWarning("hygiene-b", 1024*1024); ok {
		t.Fatal("the boundary reported as oversized")
	}
}
