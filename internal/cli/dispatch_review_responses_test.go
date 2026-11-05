package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// Scenario 13: the dispatch that waits copies the wait's review_responses
// key to its own result, on both paths — the fresh dispatch and the
// duplicate-dispatch history path — like the other review fields.
func TestDispatchResultCopiesReviewResponses(t *testing.T) {
	last := jsonjs.O("status", "done", "report", "/tmp/report.md")
	last.Set("review_responses", jsonjs.O("fixed", []any{1, 2}, "declined", []any{3}, "missing", []any{}, "unknown", []any{}, "unmarked", 0, "unnumbered", 0))
	want := `"review_responses":{"fixed":[1,2],"declined":[3],"missing":[],"unknown":[],"unmarked":0,"unnumbered":0}`

	t.Run("emitDispatchResult copies the key", func(t *testing.T) {
		root := t.TempDir()
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDispatchResult("worker", "implementer", "implement", "brief.md", "/tmp/report.md", "", "done", false, last, 0, root, false, false)
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing review_responses in dispatch output: %s", out.String())
		}
	})
	t.Run("emitDuplicateDispatchResult copies the key", func(t *testing.T) {
		report := t.TempDir() + "/report.md"
		writeRoleFixture(t, report, "<!-- report: completed -->\n")
		dup := &dispatchDuplicate{report: report, composed: "brief.md", duplicateOf: "original.md"}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDuplicateDispatchResult("worker", "implementer", "implement", dup, last, 0, t.TempDir())
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing review_responses in duplicate dispatch output: %s", out.String())
		}
	})
	t.Run("a wait line without the key keeps none", func(t *testing.T) {
		plain := jsonjs.O("status", "done", "report", "/tmp/report.md")
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDispatchResult("worker", "implementer", "implement", "brief.md", "/tmp/report.md", "", "done", false, plain, 0, t.TempDir(), false, false)
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		if strings.Contains(out.String(), "review_responses") {
			t.Fatalf("review_responses invented for a wait line without it: %s", out.String())
		}
	})
}

// The strict refusal must not read the text of a warning that never refuses: a
// Review: value can hold the phrase the path check uses.
func TestDispatchStrictDoesNotRefuseAReviewWarning(t *testing.T) {
	h := newTM3bHarness(t, tm3bFullBrief()+"Review: `which the worker in review.md`\n")
	h.env["SOHO_BRIEF_LINT"] = "strict"
	code, _, stderr := h.run(t, "build", h.brief, "--no-wait")
	if code != 0 || !strings.Contains(stderr, "has a Review: line that is not an absolute path") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}
