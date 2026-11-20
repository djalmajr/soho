package cli

import (
	"bytes"
	"os"
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

// SOH-442: the real dispatch command puts the declared test-layers line in
// the composed prompt the worker reads; a brief without the line does not.
func TestDispatchDeclaredTestLayersPrompt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		brief string
		want  bool
	}{
		{
			name:  "declared",
			brief: "# Goal\nrun\n\nTest layers: unit, mutation\n# Expected result\nDone.\n# Owned files\nnone\n# Forbidden\nNo commit.\n# Report\ndone\n",
			want:  true,
		},
		{
			name:  "undeclared",
			brief: "# Goal\nrun\n# Expected result\nDone.\n# Owned files\nnone\n# Forbidden\nNo commit.\n# Report\ndone\n",
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The git-policy fixture also pins the clock that matches the fake
			// screen's composed-prompt evidence, so the arrival is proved.
			f := gitPolicyFixture(t, "implementer")
			if err := os.WriteFile(f.brief, []byte(tc.brief), 0o600); err != nil {
				t.Fatal(err)
			}
			if code, out, errText := f.run(t, "worker", f.brief, "--no-wait"); code != 0 {
				t.Fatalf("dispatch code=%d out=%s stderr=%s", code, out, errText)
			}
			prompts := composedFor(t, f, "worker")
			if len(prompts) != 1 {
				t.Fatalf("composed prompts=%d want 1", len(prompts))
			}
			const line = "- Test layers that apply: unit, mutation; mark the others n/a: <why>.\n"
			if got := strings.Count(prompts[0], line); (got == 1) != tc.want {
				t.Fatalf("composed prompt line count=%d want=%v: %q", got, tc.want, prompts[0])
			}
		})
	}
}
