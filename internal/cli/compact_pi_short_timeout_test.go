package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestCompactPiShortTimeoutKeepsProofBudget: a timeout at or below the 15 s
// restore reserve still leaves the proof wait a positive budget, so a ready
// legacy proof is consumed and the thinking restored.
func TestCompactPiShortTimeoutKeepsProofBudget(t *testing.T) {
	for _, timeout := range []string{"10s", "15s"} {
		t.Run(timeout, func(t *testing.T) {
			f := newCompactFixture(t, "pi", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: "model • high\n"},
				{Argv: compactReadArgv("worker"), Call: 2, Stdout: "model • off\n"},
				{Argv: compactReadArgv("worker"), Stdout: "Compacted from 12345 tokens\nmodel • high\n"},
				{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}},
			})
			start := time.Now()
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", timeout)
			calls := f.calls(t)
			t.Logf("timeout=%s code=%d elapsed=%s reads=%d taps=%d out=%s stderr=%s", timeout, code, time.Since(start), countArgv(calls, compactReadArgv("worker")), countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}), strings.TrimSpace(out), stderr)
			if code != 0 || compactJSON(t, out)["status"] != "compacted" || compactJSON(t, out)["thinking_restored"] != true {
				t.Fatal("a ready legacy proof was not consumed within a short timeout")
			}
		})
	}
}

func TestCompactPiRestoreReserve(t *testing.T) {
	for _, tc := range []struct{ timeout, remaining, want time.Duration }{
		{300 * time.Second, 290 * time.Second, 30 * time.Second},
		{60 * time.Second, 55 * time.Second, 15 * time.Second},
		{18 * time.Second, 14 * time.Second, 7 * time.Second},
		{10 * time.Second, 6 * time.Second, 3 * time.Second},
		{10 * time.Second, 0, 0},
		{10 * time.Second, -time.Second, 0},
	} {
		if got := compactPiRestoreReserve(tc.timeout, tc.remaining); got != tc.want {
			t.Errorf("reserve(%s, %s) = %s, want %s", tc.timeout, tc.remaining, got, tc.want)
		}
	}
}
