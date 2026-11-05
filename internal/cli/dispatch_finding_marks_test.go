package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// Scenario 12: the dispatch that waits copies the wait's finding_marks key
// to its own result, like the other review fields.
func TestDispatchResultCopiesFindingMarks(t *testing.T) {
	last := jsonjs.O("status", "done", "report", "/tmp/report.md")
	last.Set("finding_marks", jsonjs.O("brief", 1, "impl", 1, "unknown", 0, "unmarked", 1))
	want := `"finding_marks":{"brief":1,"impl":1,"unknown":0,"unmarked":1}`

	t.Run("emitDispatchResult copies the key", func(t *testing.T) {
		root := t.TempDir()
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDispatchResult("worker", "reviewer", "review", "brief.md", "/tmp/report.md", "", "done", false, last, 0, root, false, false)
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing finding_marks in dispatch output: %s", out.String())
		}
	})
	t.Run("emitDuplicateDispatchResult copies the key", func(t *testing.T) {
		report := t.TempDir() + "/report.md"
		writeRoleFixture(t, report, "<!-- report: completed -->\n")
		dup := &dispatchDuplicate{report: report, composed: "brief.md", duplicateOf: "original.md"}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDuplicateDispatchResult("worker", "reviewer", "review", dup, last, 0, t.TempDir())
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing finding_marks in duplicate dispatch output: %s", out.String())
		}
	})
	t.Run("a wait line without the key keeps none", func(t *testing.T) {
		plain := jsonjs.O("status", "done", "report", "/tmp/report.md")
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDispatchResult("worker", "reviewer", "review", "brief.md", "/tmp/report.md", "", "done", false, plain, 0, t.TempDir(), false, false)
		platform.Stdout = old
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		if strings.Contains(out.String(), "finding_marks") {
			t.Fatalf("finding_marks invented for a wait line without it: %s", out.String())
		}
	})
}
