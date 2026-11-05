package wait

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// Review-response coverage of settled correction reports (DJA-684 slice C).
// The composed brief's Review: line names the review whose findings the
// report must answer one by one; the done line carries the review_responses
// counts and the wait warns — once per report, never changing its result.

func reviewBrief(reviewPath string) string {
	return metricsBriefBackend + "\nReview: " + reviewPath + "\n"
}

// reviewWarningFragments names every warning this check can send; a stderr
// carrying none of them is the no-warning proof.
func hasReviewWarning(text string) bool {
	for _, frag := range []string{
		"finding(s) of the review without a response",
		"the review does not have",
		"Review responses line(s) without a valid answer",
		"finding(s) without a number",
		"names a review that cannot be read",
		"has no review header",
	} {
		if strings.Contains(text, frag) {
			return true
		}
	}
	return false
}

func reviewBriefQuoted(reviewPath string) string {
	return metricsBriefBackend + "\nReview: `" + reviewPath + "`\n"
}

func writeReviewFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// markedReview is a review of n findings, each carrying a valid mark in
// report order.
func markedReview(n int) string {
	lines := []string{fmt.Sprintf("findings: %d (p0 0, p1 0, p2 0, p3 %d) | verdict: fail", n, n), "", "## Findings"}
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("- [F%d] [origin: impl] [class: c%d] finding %d", i, i, i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func responsesReport(body string) string {
	return "# Report\n\n## Review responses\n\n" + body + "\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
}

func TestWaitReviewResponsesMissing(t *testing.T) { // scenario 5
	t.Run("wait: a review of 3 findings with 2 answered publishes the key, warns the number, same settlement", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(3))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n- F2: fixed: reran the gate\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d out=%q err=%q", rc, out, errText)
		}
		if !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("status not done: %q", out)
		}
		want := `"review_responses":{"fixed":[1,2],"declined":[],"missing":[3],"unknown":[],"unmarked":0,"unnumbered":0}`
		if !strings.Contains(out, want) {
			t.Fatalf("review_responses missing or wrong in the done line: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' leaves 1 finding(s) of the review without a response (F3): read them before commit, push or release") {
			t.Fatalf("missing the without-a-response warning: %q", errText)
		}
	})
}

func TestWaitReviewResponsesAllAnsweredIsQuiet(t *testing.T) { // scenario 6
	t.Run("wait: every finding answered publishes the counts without a warning", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(3))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n- F2: declined: out of scope for this slice\n- F3: fixed: note\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		want := `"review_responses":{"fixed":[1,3],"declined":[2],"missing":[],"unknown":[],"unmarked":0,"unnumbered":0}`
		if !strings.Contains(out, want) {
			t.Fatalf("review_responses missing or wrong: %q", out)
		}
		if hasReviewWarning(errText) {
			t.Fatalf("all-answered report warned: %q", errText)
		}
	})
}

func TestWaitReviewResponsesUnknownAndUnmarked(t *testing.T) { // scenario 7
	t.Run("wait: an answer for a number the review does not have is unknown and warns", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(2))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n- F9: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"review_responses":{"fixed":[1,9],"declined":[],"missing":[2],"unknown":[9],"unmarked":0,"unnumbered":0}`) {
			t.Fatalf("unknown not in the done line: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' answers 1 finding(s) the review does not have (F9)") {
			t.Fatalf("missing the unknown warning: %q", errText)
		}
		if !strings.Contains(errText, "report of 'worker' leaves 1 finding(s) of the review without a response (F2): read them before commit, push or release") {
			t.Fatalf("missing the without-a-response warning: %q", errText)
		}
	})
	t.Run("wait: a line without the exact form is unmarked and warns", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(1))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: FIXED\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"review_responses":{"fixed":[],"declined":[],"missing":[1],"unknown":[],"unmarked":1,"unnumbered":0}`) {
			t.Fatalf("unmarked not in the done line: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' has 1 Review responses line(s) without a valid answer") {
			t.Fatalf("missing the unmarked warning: %q", errText)
		}
	})
}

func TestWaitReviewResponsesUnnumbered(t *testing.T) { // scenario 8
	t.Run("wait: a review finding without a valid number is unnumbered and warns", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, "findings: 2 (p0 0, p1 0, p2 0, p3 2) | verdict: fail\n\n## Findings\n- [F1] [origin: impl] [class: bounds] marked\n- a finding the review never numbered\n")
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"review_responses":{"fixed":[1],"declined":[],"missing":[],"unknown":[],"unmarked":0,"unnumbered":1}`) {
			t.Fatalf("unnumbered not in the done line: %q", out)
		}
		if !strings.Contains(errText, "review named by the brief of 'worker' has 1 finding(s) without a number, so their responses cannot be checked") {
			t.Fatalf("missing the unnumbered warning: %q", errText)
		}
	})
}

func TestWaitReviewResponsesWarnsOncePerReport(t *testing.T) { // scenario 9
	t.Run("wait: a second poll of the same report warns only once and keeps the key", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(2))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		_, err1, rc1 := f.run(t)
		out2, err2, rc2 := f.run(t)
		if rc1 != 0 || rc2 != 0 {
			t.Fatalf("rc1=%d rc2=%d", rc1, rc2)
		}
		if !strings.Contains(err1, "without a response (F2)") {
			t.Fatalf("first poll did not warn: %q", err1)
		}
		if strings.Contains(err2, "without a response") {
			t.Fatalf("second poll warned again: %q", err2)
		}
		if !strings.Contains(out2, `"review_responses"`) {
			t.Fatalf("second poll lost the key: %q", out2)
		}
	})
	t.Run("wait: a warning that has not gone out yet is not silenced by an earlier one", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		// The review does not exist yet: only the cannot-be-read warning.
		_, err1, _ := f.run(t)
		if !strings.Contains(err1, "names a review that cannot be read") {
			t.Fatalf("first poll: %q", err1)
		}
		// The review appears: the missing response has never been reported.
		writeReviewFile(t, reviewPath, markedReview(2))
		_, err2, _ := f.run(t)
		if !strings.Contains(err2, "without a response (F2)") || strings.Contains(err2, "cannot be read") {
			t.Fatalf("second poll: %q", err2)
		}
		// Nothing new: silence.
		_, err3, _ := f.run(t)
		if hasReviewWarning(err3) {
			t.Fatalf("third poll warned again: %q", err3)
		}
		// A new kind of problem in the same report warns once, alone.
		writeReviewFile(t, f.report, responsesReport("- F1: fixed\n- F9: fixed\n"))
		_, err4, _ := f.run(t)
		if !strings.Contains(err4, "the review does not have (F9)") || strings.Contains(err4, "without a response") {
			t.Fatalf("fourth poll: %q", err4)
		}
	})
	t.Run("wait: a marker that names another report warns again", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(2))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		writeReviewFile(t, filepath.Join(f.sd, "wait", "worker.review-responses-warned"), "/another/report.md\nmissing\n")
		_, errText, rc := f.run(t)
		if rc != 0 || !strings.Contains(errText, "without a response (F2)") {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
	})
	t.Run("wait: an unwritable marker does not abort the wait, the warning goes out and repeats", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(2))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		// The marker path is a directory: the write cannot persist and the
		// marker read fails too, so the warning is eligible again each poll.
		if err := os.Mkdir(filepath.Join(f.sd, "wait", "worker.review-responses-warned"), 0o700); err != nil {
			t.Fatal(err)
		}
		out1, err1, rc1 := f.run(t)
		if rc1 != 0 {
			t.Fatalf("rc=%d out=%q err=%q", rc1, out1, err1)
		}
		if !strings.Contains(out1, `"status":"done"`) || !strings.Contains(out1, `"review_responses"`) {
			t.Fatalf("the done line changed: %q", out1)
		}
		if !strings.Contains(err1, "without a response") {
			t.Fatalf("the warning did not go out: %q", err1)
		}
		_, err2, rc2 := f.run(t)
		if rc2 != 0 {
			t.Fatalf("rc=%d", rc2)
		}
		if !strings.Contains(err2, "without a response") {
			t.Fatalf("the warning should repeat while the marker cannot persist: %q", err2)
		}
	})
}

func TestWaitReviewResponsesUnreadableReview(t *testing.T) { // scenario 10
	t.Run("wait: an absent review warns cannot-be-read and sets no key", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "absent.md")
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for an absent review: %q", out)
		}
		if !strings.Contains(errText, "brief of 'worker' names a review that cannot be read: "+reviewPath) {
			t.Fatalf("missing the cannot-be-read warning: %q", errText)
		}
	})
	t.Run("wait: the path in the warning is filtered", func(t *testing.T) {
		reviewPath := filepath.Join(t.TempDir(), "absent\x1b[31m.md")
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		_, errText, rc := f.run(t)
		if rc != 0 || !strings.Contains(errText, "names a review that cannot be read") || strings.ContainsRune(errText, 0x1b) {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
	})
	t.Run("wait: a linked review warns cannot-be-read and sets no key", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.md")
		writeReviewFile(t, target, markedReview(1))
		link := filepath.Join(dir, "review.md")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("native symlink creation denied: %v", err)
		}
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(link), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for a linked review: %q", out)
		}
		if !strings.Contains(errText, "brief of 'worker' names a review that cannot be read: "+link) {
			t.Fatalf("missing the cannot-be-read warning: %q", errText)
		}
	})
	t.Run("wait: a directory named review.md warns cannot-be-read and sets no key", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		if err := os.Mkdir(reviewPath, 0o700); err != nil {
			t.Fatal(err)
		}
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for a directory review: %q", out)
		}
		if !strings.Contains(errText, "brief of 'worker' names a review that cannot be read: "+reviewPath) {
			t.Fatalf("missing the cannot-be-read warning: %q", errText)
		}
	})
	t.Run("wait: a review over the 4 MiB cap warns cannot-be-read and sets no key", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		if err := os.WriteFile(reviewPath, bytes.Repeat([]byte("z"), 4<<20+1), 0o600); err != nil {
			t.Fatal(err)
		}
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for an oversized review: %q", out)
		}
		if !strings.Contains(errText, "brief of 'worker' names a review that cannot be read: "+reviewPath) {
			t.Fatalf("missing the cannot-be-read warning: %q", errText)
		}
	})
	t.Run("wait: a review without a header warns and sets no key", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, "# Review\n\nno header line here\n")
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for a headerless review: %q", out)
		}
		if !strings.Contains(errText, "review named by the brief of 'worker' has no review header") {
			t.Fatalf("missing the no-header warning: %q", errText)
		}
	})
}

func TestWaitReviewResponsesWithoutReviewLine(t *testing.T) { // scenario 11
	t.Run("wait: a brief without Review: changes nothing, even with the section in the report", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key without a Review: line: %q", out)
		}
		if hasReviewWarning(errText) {
			t.Fatalf("a warning without a Review: line: %q", errText)
		}
	})
}

func TestWaitReviewResponsesNoSection(t *testing.T) { // scenario 12
	t.Run("wait: a report without the section leaves every review finding missing", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(2))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, metricsImplementerReport, reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"review_responses":{"fixed":[],"declined":[],"missing":[1,2],"unknown":[],"unmarked":0,"unnumbered":0}`) {
			t.Fatalf("all-missing not in the done line: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' leaves 2 finding(s) of the review without a response (F1, F2): read them before commit, push or release") {
			t.Fatalf("missing the without-a-response warning: %q", errText)
		}
	})
}

func TestWaitReviewResponsesEdgeCases(t *testing.T) {
	t.Run("wait: a review with zero findings keeps a zero key and no warning", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, "findings: 0 (p0 0, p1 0, p2 0, p3 0) | verdict: pass\n\n## Findings\nnone\n")
		f := newMetricsFixture(t, "off", metricsImplementerRoster, metricsImplementerReport, reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"review_responses":{"fixed":[],"declined":[],"missing":[],"unknown":[],"unmarked":0,"unnumbered":0}`) {
			t.Fatalf("zero key missing or wrong: %q", out)
		}
		if hasReviewWarning(errText) {
			t.Fatalf("a zero-finding review warned: %q", errText)
		}
	})
	t.Run("wait: a read role with Review: in the brief changes nothing", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(1))
		f := newMetricsFixture(t, "off", metricsReviewerRoster, responsesReport("- F1: fixed\n"), reviewBrief(reviewPath), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for a read role: %q", out)
		}
		if hasReviewWarning(errText) {
			t.Fatalf("a warning for a read role: %q", errText)
		}
	})
	t.Run("wait: a path with spaces between backticks works", func(t *testing.T) {
		dir := t.TempDir()
		spaced := filepath.Join(dir, "dir with space")
		if err := os.Mkdir(spaced, 0o700); err != nil {
			t.Fatal(err)
		}
		reviewPath := filepath.Join(spaced, "review with space.md")
		writeReviewFile(t, reviewPath, markedReview(1))
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBriefQuoted(reviewPath), "")
		out, _, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
		if !strings.Contains(out, `"review_responses":{"fixed":[1],"declined":[],"missing":[],"unknown":[],"unmarked":0,"unnumbered":0}`) {
			t.Fatalf("a spaced path did not reach the review: %q", out)
		}
	})
	t.Run("wait: a relative review path is no review", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBriefQuoted("review.md"), "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for a relative review path: %q", out)
		}
		if hasReviewWarning(errText) {
			t.Fatalf("a warning for a relative review path: %q", errText)
		}
	})
	t.Run("wait: an unclosed quote is no review", func(t *testing.T) {
		brief := "# Brief — correction\n\nType: backend\n\nReview: `" + "/no/such/absolute/review.md\n"
		f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), brief, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "review_responses") {
			t.Fatalf("a key for an unclosed quote: %q", out)
		}
		if hasReviewWarning(errText) {
			t.Fatalf("a warning for an unclosed quote: %q", errText)
		}
	})
	t.Run("wait: an unreadable report stays as today — no key, no warning", func(t *testing.T) {
		dir := t.TempDir()
		reviewPath := filepath.Join(dir, "review.md")
		writeReviewFile(t, reviewPath, markedReview(1))
		sd := t.TempDir()
		if err := os.MkdirAll(filepath.Join(sd, "briefs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "briefs", "worker.md"), []byte(reviewBrief(reviewPath)), 0o600); err != nil {
			t.Fatal(err)
		}
		line := jsonjs.O("status", "done")
		var stderr bytes.Buffer
		oldErr := platform.Stderr
		platform.Stderr = &stderr
		checkReviewResponses(sd, "worker", filepath.Join(sd, "reports", "worker.md"), "", line, "implementer", platform.Env{}, "")
		platform.Stderr = oldErr
		if _, ok := line.Get("review_responses"); ok {
			t.Fatalf("a key for an unreadable report: %s", jsonjs.Stringify(line))
		}
		if stderr.String() != "" {
			t.Fatalf("an unreadable report warned: %q", stderr.String())
		}
	})
}
