package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The codex screens of DJA-408: a TUI scrolled up, with the `Back to bottom`
// indicator above the composer, hides the new `Context compacted` line; the
// footer gauge is what shows the compaction. The footers are the lines seen
// on the real TUI.
const (
	codexFooterBefore49    = "  GPT-6.1-Sol medium · Context 49% used · 475K window · weekly 76% left · main\n"
	codexFooterAfter3      = "  GPT-6.1-Sol medium · Context 3% used · 475K window · weekly 76% left · main\n"
	codexFooterCompacting  = "  GPT-6.1-Sol medium · Context 0% used · weekly 76% left · main\n"
	codexScrolledIndicator = "  New activity · ↓ Back to bottom · esc\n"
	codexScrolledLast      = "  New activity · enter/esc latest · ? shortcuts      ⚠ 4 warnings · f2 to view\n"
)

// codexScrolled is a scrolled codex screen: a piece of the history, the
// indicator, the empty composer and the footer with the given gauge.
func codexScrolled(footer string) string {
	return "• Earlier reply from the worker\n" + codexScrolledIndicator + "\n› Ask Codex to do anything\n\n" + footer
}

// TestCompactCodexFooterDropCompacted is the DJA-408 screen end to end: the
// TUI is scrolled, so the compaction never prints `Context compacted` in the
// visible area, and the proof is the footer gauge falling from 49% to 3%. A
// read in the middle of the compaction (gauge 0%, no window) is not proof.
func TestCompactCodexFooterDropCompacted(t *testing.T) {
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		// The liveness check after the mid-compaction read, then the idle wait.
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 3)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: "• Compacting context (17s • esc to interrupt)\n" + codexScrolledIndicator + "\n› Ask Codex to do anything\n\n" + codexFooterCompacting},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: codexScrolled(codexFooterAfter3) + codexScrolledLast},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["agent"] != "worker" || value["kind"] != "codex" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	// No key to bring the TUI back to the bottom, and no retry of the command.
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
	}
	for _, key := range []string{"esc", "Escape", "End", "ctrl+u"} {
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", key}); n != 0 {
			t.Fatalf("%s calls=%d want 0: %#v", key, n, calls)
		}
	}
	// Pre-send, the mid-compaction poll (no proof) and the polls that read the
	// proof: the footer drop waits for idle like every other proof.
	if n := countArgv(calls, compactReadArgv("worker")); n != 3 {
		t.Fatalf("recent reads=%d want 3: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 3 {
		t.Fatalf("agent get calls=%d want 3 (pre-send, liveness, idle wait): %#v", n, calls)
	}
}

// TestDispatchCompactCodexFooterDropSendsBrief is the same scrolled screen
// inside a dispatch: the compact step ends as compacted and the brief goes
// out.
func TestDispatchCompactCodexFooterDropSendsBrief(t *testing.T) {
	extra := []fakecli.Rule{
		// 1: compact pre-send check, 2: the idle wait after the proof, 3: the
		// dispatch's before-state.
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: codexScrolled(codexFooterAfter3) + codexScrolledLast},
		{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 {
		t.Fatalf("code=%d want 0 out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchLastLineStatus(t, out); status != "submitted" {
		t.Fatalf("wait_status=%s want submitted out=%s", status, out)
	}
	if !strings.Contains(errText, "dispatch: compacted 'worker' in ") {
		t.Fatalf("missing the compacted note on stderr: %s", errText)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 1 {
		t.Fatalf("prompt calls=%d want 1 (the brief is sent after the compaction): %#v", n, calls)
	}
}

// TestCompactCodexFooterNoProofTimesOut runs the screens where the footer is
// not proof: the gauge does not fall, the mid-compaction footer alone, a
// gauge before the send under 10%, a gauge-shaped line in the history above
// the last four non-empty lines, and no gauge before the send. Each one ends
// at the timeout (9) with /compact sent once, like before the footer proof.
func TestCompactCodexFooterNoProofTimesOut(t *testing.T) {
	history := "Context 3% used · 475K window\n"
	cases := []struct {
		name   string
		before string
		after  string
	}{
		{
			name:   "the gauge does not fall",
			before: codexScrolled(codexFooterBefore49),
			after:  codexScrolled(codexFooterBefore49) + codexScrolledLast,
		},
		{
			name:   "the footer in the middle of the compaction alone",
			before: codexScrolled(codexFooterBefore49),
			after:  "• Compacting context (17s • esc to interrupt)\n" + codexScrolledIndicator + "\n› Ask Codex to do anything\n\n" + codexFooterCompacting,
		},
		{
			name:   "a gauge before the send under 10%",
			before: codexScrolled("  GPT-6.1-Sol medium · Context 8% used · 475K window · weekly 76% left · main\n"),
			after:  codexScrolled("  GPT-6.1-Sol medium · Context 2% used · 475K window · weekly 76% left · main\n"),
		},
		{
			name:   "the gauge only in the history above the last four lines",
			before: codexScrolled(codexFooterBefore49),
			after:  history + "• Earlier reply from the worker\n" + codexScrolledIndicator + "› Ask Codex to do anything\n  GPT-6.1-Sol medium · weekly 76% left · main\n" + codexScrolledLast,
		},
		{
			name:   "no gauge before the send",
			before: "• Earlier reply from the worker\n› Ask Codex to do anything\n",
			after:  codexScrolled(codexFooterAfter3),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			f := newCompactFixture(t, "codex", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: c.before},
				{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: c.after},
			})
			code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
			if code != 9 {
				t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
			}
			if value := compactJSON(t, out); value["kind"] != "codex" || value["status"] != "timeout" {
				t.Fatalf("json=%v", value)
			}
			calls := f.calls(t)
			if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
				t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
			}
		})
	}
}

// TestCompactCodexFooterDropAfterDeadlineCompacted: the gauge falls only in
// the read after the deadline; the last read runs the footer proof too, the
// compact ends as compacted, and /compact is never sent again.
func TestCompactCodexFooterDropAfterDeadlineCompacted(t *testing.T) {
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		// The pre-send read and the polls inside the deadline: the gauge stays.
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: codexScrolled(codexFooterBefore49)},
		// Only the read after the deadline shows the drop.
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: codexScrolled(codexFooterAfter3)},
	})
	compactDeadlineAfterReads(t, f, 3)
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 0 || withoutShortTimeoutWarning(errText) != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["kind"] != "codex" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 500 {
		t.Fatalf("elapsed_ms=%v want >= 500 (the drop came after the deadline): %v", value["elapsed_ms"], value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, compactReadArgv("worker")); n != 4 {
		t.Fatalf("recent reads=%d want 4: %#v", n, calls)
	}
}

// TestCompactCodexContextRead exercises the footer reading directly: the
// four-line window, the first match from the bottom, the window text and the
// 0-100 range.
func TestCompactCodexContextRead(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   int
		ok     bool
	}{
		{"the footer", codexScrolled(codexFooterBefore49), 49, true},
		{"megabyte window", "› Ask Codex\n  Sol · Context 7% used · 1M window · main\n", 7, true},
		{"a decimal window", "› Ask Codex\n  Sol · Context 7% used · 1.5M window · main\n", 7, true},
		{"0 and 100 are valid", "› Ask Codex\n  Sol · Context 100% used · 475K window\n", 100, true},
		{"over 100 is not a gauge", "› Ask Codex\n  Sol · Context 101% used · 475K window\n", 0, false},
		{"no window", "› Ask Codex\n  Sol · Context 0% used · weekly 76% left\n", 0, false},
		{"no dot before the window", "› Ask Codex\n  Sol Context 3% used 475K window\n", 0, false},
		{"the bottom match wins", "  Context 40% used · 475K window\n› Ask Codex\n  Context 5% used · 475K window\n", 5, true},
		{"the fourth non-empty line from the bottom counts", "  Context 5% used · 475K window\n\na\n\nb\nc\n", 5, true},
		{"the fifth non-empty line from the bottom does not", "  Context 5% used · 475K window\na\nb\nc\nd\n", 0, false},
		{"blank lines do not use the window", "  Context 5% used · 475K window\n\n\na\n\n\nb\nc\n\n", 5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := compactCodexContext(c.screen)
			if got != c.want || ok != c.ok {
				t.Fatalf("compactCodexContext = (%d, %v) want (%d, %v): %q", got, ok, c.want, c.ok, c.screen)
			}
		})
	}
}

// TestCompactCodexContextDroppedRule checks the drop rule: a base of at least
// 10 and a gauge at most half of it (integer division).
func TestCompactCodexContextDroppedRule(t *testing.T) {
	footer := func(n string) string { return "› Ask Codex\n  Sol · Context " + n + "% used · 475K window\n" }
	cases := []struct {
		base   int
		after  string
		want   bool
		reason string
	}{
		{49, "3", true, "49 to 3"},
		{49, "24", true, "49 / 2 is 24: the limit counts"},
		{49, "25", false, "25 is over the half"},
		{10, "5", true, "the lowest base, half"},
		{10, "6", false, "the lowest base, over the half"},
		{9, "0", false, "a base under 10 has no drop proof"},
		{8, "2", false, "a base under 10 has no drop proof"},
		{49, "49", false, "no drop"},
		{49, "60", false, "the gauge went up"},
	}
	for _, c := range cases {
		if got := compactCodexContextDropped(c.base, footer(c.after)); got != c.want {
			t.Fatalf("base %d after %s: dropped = %v want %v (%s)", c.base, c.after, got, c.want, c.reason)
		}
	}
	if compactCodexContextDropped(49, "› Ask Codex\n  Sol · weekly 76% left\n") {
		t.Fatal("a screen without a gauge is not a drop")
	}
}
