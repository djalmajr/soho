package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
)

// newDispatchMetricsForFixture is the tm3b harness with a claude/anthropic
// reviewer for the sidecar schema check:
// author (codex/openai, full columns) and ghost (pi, empty family and
// effort columns).
func newDispatchMetricsForFixture(t *testing.T) tm3bHarness {
	t.Helper()
	h := newTM3bHarness(t, strings.Replace(tm3bFullBrief(), "# Owned files\ninternal/a.go\n\n", "", 1))
	rolesDir := filepath.Dir(h.role)
	if err := os.WriteFile(filepath.Join(rolesDir, "reviewer.md"), []byte("---\nname: Reviewer\nmode: review\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"rev\tw0test:p0r\tclaude\treviewer\tanthropic\t0\t" + h.root + "\tnow\tclaude-sonnet-4\ttask\treviewer\t\t\t\thigh\n" +
		"author\tw0test:p0o\tcodex\timplementer\topenai\t0\t" + h.root + "\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n" +
		"ghost\tw0test:p0g\tpi\timplementer\t\t0\t\t\tm/1\ttask\timplementer\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

// dispatchForSidecar runs the reviewer dispatch and returns the final sidecar
// next to the composed prompt.
func dispatchForSidecar(t *testing.T, h tm3bHarness, args ...string) (string, *jsonjs.Object) {
	t.Helper()
	dispatchArgs := append([]string{"rev", h.brief}, args...)
	code, out, stderr := h.run(t, append(dispatchArgs, "--no-wait")...)
	if code != 0 {
		t.Fatalf("dispatch code=%d stderr=%s", code, stderr)
	}
	composed, ok := dispatchOutputField(t, out, "composed_prompt")
	if !ok {
		t.Fatalf("success JSON omitted the composed prompt: %s", out)
	}
	sidecar := dispatch.DispatchSidecar(composed)
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar %s: %v", sidecar, err)
	}
	value, err := jsonjs.Parse(raw)
	if err != nil {
		t.Fatalf("sidecar is not JSON: %v: %q", err, raw)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("sidecar is not an object: %q", raw)
	}
	return string(raw), obj
}

func TestDispatchMetricsForSidecar(t *testing.T) {
	h := newDispatchMetricsForFixture(t)
	raw, obj := dispatchForSidecar(t, h)
	if _, ok := obj.Get("for"); ok {
		t.Fatalf("new dispatch recorded retired author selection: %s", raw)
	}
}
