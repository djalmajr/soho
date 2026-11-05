package wait

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const findingMarksReviewReport = "findings: 3 (P0 0, P1 0, P2 0, P3 3) | verdict: pass\n\n" +
	"- [F1] [origin: impl] [class: missing-bound] a bound is missing\n" +
	"- [F2] [origin: brief] [class: doc-drift] the doc drifted\n" +
	"- a finding without marks\n"

const wantFindingMarksJSON = `"finding_marks":{"brief":1,"impl":1,"unknown":0,"unmarked":1}`

const findingMarksAllMarkedReport = "findings: 2 (P0 0, P1 0, P2 0, P3 2) | verdict: pass\n\n" +
	"- [F1] [origin: brief] [class: doc-drift] the doc drifted\n" +
	"- [F2] [origin: unknown] [class: follows-link] the code follows a link\n"

// TestWaitFindingMarksWarnsOnce covers the done case with findings and
// marks: the JSON key, the warning and the unchanged result (scenario 5),
// the once-per-report marker and the unwritable marker (scenario 7).
func TestWaitFindingMarksWarnsOnce(t *testing.T) {
	t.Run("wait: findings with two marks land in the JSON and warn for the rest", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsReviewerRoster, findingMarksReviewReport, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d out=%q err=%q", rc, out, errText)
		}
		if !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("status not done: %q", out)
		}
		if !strings.Contains(out, wantFindingMarksJSON) {
			t.Fatalf("finding_marks missing or wrong in the done line: %q", out)
		}
		if !strings.Contains(out, `"verdict":"pass"`) || !strings.Contains(out, `"verdict_effective":"pass"`) {
			t.Fatalf("the review result changed: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' has 1 finding(s) without an origin and a class") {
			t.Fatalf("missing the unmarked-finding warning: %q", errText)
		}
	})
	t.Run("wait: everything marked keeps the JSON and warns nothing", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsReviewerRoster, findingMarksAllMarkedReport, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"finding_marks":{"brief":1,"impl":0,"unknown":1,"unmarked":0}`) {
			t.Fatalf("finding_marks missing or wrong: %q", out)
		}
		if strings.Contains(errText, "without an origin and a class") {
			t.Fatalf("all-marked report warned: %q", errText)
		}
	})
	t.Run("wait: a header with zero findings and no marks keeps no key", func(t *testing.T) {
		body := "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\nno findings\n"
		f := newMetricsFixture(t, "off", metricsReviewerRoster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "finding_marks") {
			t.Fatalf("finding_marks without findings or marks: %q", out)
		}
		if strings.Contains(errText, "without an origin and a class") {
			t.Fatalf("warning without findings: %q", errText)
		}
	})
	t.Run("wait: a header with zero findings and one mark still carries the key", func(t *testing.T) {
		// The 0+1 case: the report declares no findings but carries a valid
		// mark (an extra note); the key is present with its zero unmarked,
		// and nothing warns.
		body := "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n- [F1] [origin: brief] [class: doc-drift] an extra note\n"
		f := newMetricsFixture(t, "off", metricsReviewerRoster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"finding_marks":{"brief":1,"impl":0,"unknown":0,"unmarked":0}`) {
			t.Fatalf("finding_marks missing or wrong: %q", out)
		}
		if strings.Contains(errText, "without an origin and a class") {
			t.Fatalf("warning on a fully marked 0+1 report: %q", errText)
		}
	})
	t.Run("wait: a report without a review header carries no key, even with a valid mark", func(t *testing.T) {
		// A research report can quote a finding line; without the header it is
		// not a review, and stats and metrics leave it out too.
		body := "verdict: pattern\n\n- [F1] [origin: impl] [class: error-swallowed] a quoted line\n"
		f := newMetricsFixture(t, "off", metricsReviewerRoster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "finding_marks") || strings.Contains(errText, "without an origin and a class") {
			t.Fatalf("a headerless report was treated as a review: out=%q err=%q", out, errText)
		}
	})
	t.Run("wait: a marker that names another report warns again", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsReviewerRoster, findingMarksReviewReport, metricsBriefBackend, "")
		if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.finding-marks-warned"), []byte("/another/report.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, errText, rc := f.run(t)
		if rc != 0 || !strings.Contains(errText, "without an origin and a class") {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
	})
	t.Run("wait: a second poll of the same report warns only once", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsReviewerRoster, findingMarksReviewReport, metricsBriefBackend, "")
		_, err1, rc1 := f.run(t)
		out2, err2, rc2 := f.run(t)
		if rc1 != 0 || rc2 != 0 {
			t.Fatalf("rc1=%d rc2=%d", rc1, rc2)
		}
		if !strings.Contains(err1, "without an origin and a class") {
			t.Fatalf("first poll did not warn: %q", err1)
		}
		if strings.Contains(err2, "without an origin and a class") {
			t.Fatalf("second poll warned again: %q", err2)
		}
		if !strings.Contains(out2, wantFindingMarksJSON) {
			t.Fatalf("second poll lost the finding_marks key: %q", out2)
		}
	})
	t.Run("wait: an unwritable marker does not abort the wait, the warning goes out and repeats", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsReviewerRoster, findingMarksReviewReport, metricsBriefBackend, "")
		// The marker path is a directory: the write cannot persist and the
		// marker read fails too, so the warning is eligible again each poll.
		if err := os.Mkdir(filepath.Join(f.sd, "wait", "worker.finding-marks-warned"), 0o700); err != nil {
			t.Fatal(err)
		}
		out1, err1, rc1 := f.run(t)
		if rc1 != 0 {
			t.Fatalf("rc=%d out=%q err=%q", rc1, out1, err1)
		}
		if !strings.Contains(out1, `"status":"done"`) || !strings.Contains(out1, wantFindingMarksJSON) {
			t.Fatalf("the done line changed: %q", out1)
		}
		if !strings.Contains(err1, "without an origin and a class") {
			t.Fatalf("the warning did not go out: %q", err1)
		}
		_, err2, rc2 := f.run(t)
		if rc2 != 0 {
			t.Fatalf("rc=%d", rc2)
		}
		if !strings.Contains(err2, "without an origin and a class") {
			t.Fatalf("the warning should repeat while the marker cannot persist: %q", err2)
		}
	})
}

// TestWaitFindingMarksNotAReviewReport (scenario 8): an implementer report
// without a header keeps the line and the stderr exactly as today.
func TestWaitFindingMarksNotAReviewReport(t *testing.T) {
	t.Run("wait: an implementer report without a header is unchanged", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("status not done: %q", out)
		}
		if strings.Contains(out, "finding_marks") {
			t.Fatalf("finding_marks invented for a non-review report: %q", out)
		}
		if strings.Contains(errText, "without an origin and a class") {
			t.Fatalf("warning for a non-review report: %q", errText)
		}
	})
}
