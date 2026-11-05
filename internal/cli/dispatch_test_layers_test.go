package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// Scenario 10: the dispatch that waits copies the wait's test_layers and
// validation keys to its own result.
func TestDispatchResultCopiesTestLayersAndValidation(t *testing.T) {
	last := jsonjs.O("status", "done", "report", "/tmp/report.md")
	last.Set("test_layers", jsonjs.O("pass", 1, "fail", 1, "not_run", 0, "blocked", 0, "na", 0, "unmarked", 0))
	last.Set("validation", "FAIL")
	wantLayers := `"test_layers":{"pass":1,"fail":1,"not_run":0,"blocked":0,"na":0,"unmarked":0}`
	wantValidation := `"validation":"FAIL"`

	t.Run("emitDispatchResult copies both keys", func(t *testing.T) {
		root := t.TempDir()
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDispatchResult("worker", "implementer", "backend", "brief.md", "/tmp/report.md", "", "done", false, last, 0, root, false, false)
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
		if !strings.Contains(out.String(), wantLayers) || !strings.Contains(out.String(), wantValidation) {
			t.Fatalf("missing keys in dispatch output: %s", out.String())
		}
	})
	t.Run("emitDuplicateDispatchResult copies both keys", func(t *testing.T) {
		report := t.TempDir() + "/report.md"
		writeRoleFixture(t, report, "<!-- report: completed -->\n")
		dup := &dispatchDuplicate{report: report, composed: "brief.md", duplicateOf: "original.md"}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDuplicateDispatchResult("worker", "implementer", "backend", dup, last, 0, t.TempDir())
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
		if !strings.Contains(out.String(), wantLayers) || !strings.Contains(out.String(), wantValidation) {
			t.Fatalf("missing keys in duplicate dispatch output: %s", out.String())
		}
	})
	t.Run("keys absent from the wait line stay out of the output", func(t *testing.T) {
		plain := jsonjs.O("status", "done", "report", "/tmp/report.md")
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDispatchResult("worker", "implementer", "backend", "brief.md", "/tmp/report.md", "", "done", false, plain, 0, t.TempDir(), false, false)
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		if strings.Contains(out.String(), "test_layers") || strings.Contains(out.String(), `"validation"`) {
			t.Fatalf("keys invented for a wait line without them: %s", out.String())
		}
	})
}
