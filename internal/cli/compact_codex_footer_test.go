package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
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
	codexTruncatedAfter3   = "› Ask Codex to do anything\n\n  GPT-6.1-Sol high · Context 3% used · …\n  New activity · enter/esc latest   ⚠ 1\n"
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
// gauge-shaped line in the history above the last four non-empty lines, and
// no gauge before the send. Each one ends at the timeout (9) with /compact
// sent once, like before the footer proof. A gauge under 10% before the send
// skips the compaction instead (compact_codex_low_context_test.go).
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
			code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
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

// codexHistoryPhrase is a sentence of the history that looks like the footer
// gauge; codexHistoryAboveComposer puts it right above the composer, with the
// real footer of a compaction in progress (no window) below.
const codexHistoryPhrase = "• Earlier report: Context 3% used · 475K window was recorded yesterday.\n"

func codexHistoryAboveComposer() string {
	return "• Earlier reply from the worker\n" + codexHistoryPhrase + "› Ask Codex to do anything\n\n" + codexFooterCompacting
}

// TestCompactCodexFooterHistoryAboveComposerTimesOut is the review finding:
// with the real footer momentarily without the window, a gauge-shaped
// sentence of the history just above the composer must not be read as the
// footer. Nothing was compacted: the compact ends at the timeout (9). The
// pane stays working after the send, so the windowless zero footer is not
// the idle proof of codex 0.164 (DJA-552) and only the history phrase could
// end the wait.
func TestCompactCodexFooterHistoryAboveComposerTimesOut(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: codexHistoryAboveComposer()},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["kind"] != "codex" || value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
}

// TestDispatchCompactCodexFooterHistoryAboveComposerKeepsBriefUnsent is the
// same screen inside a dispatch: the compact step times out (9) and no brief
// is sent.
func TestDispatchCompactCodexFooterHistoryAboveComposerKeepsBriefUnsent(t *testing.T) {
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	fixedNow := platform.Now
	platform.Now = func() time.Time {
		value := base
		base = base.Add(310 * time.Second)
		return value
	}
	t.Cleanup(func() { platform.Now = fixedNow })
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: codexHistoryAboveComposer()},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "the compact step of 'worker' did not finish (exit 9); the brief was not sent") {
		t.Fatalf("stderr without the phase and brief_sent note: %q", errText)
	}
	if !strings.Contains(out, `"status":"timeout"`) {
		t.Fatalf("missing the timeout JSON: %s", out)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 0 {
		t.Fatalf("the brief was sent after the compact timeout: %#v", calls)
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
	// The synthetic deadline controls the final-read protocol; keep the real
	// phase budget available until that protocol has been exercised.
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30s")
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
		{"the narrow pane screen", codexTruncatedAfter3, 3, true},
		{"the narrow pane at zero is still compacting", strings.Replace(codexTruncatedAfter3, "3%", "0%", 1), 0, false},
		{"the footer cut immediately after used", "› Ask Codex\n  Sol · Context 3% used\n", 3, true},
		{"zero cut immediately after used is still compacting", "› Ask Codex\n  Sol · Context 0% used\n", 0, false},
		{"a positive truncated upper bound", "› Ask Codex\n  Sol · Context 100% used · …\n", 100, true},
		{"a truncated out-of-range value", "› Ask Codex\n  Sol · Context 101% used · …\n", 0, false},
		{"an incomplete truncated separator", "› Ask Codex\n  Sol · Context 3% used ·\n", 0, false},
		{"ASCII dots are not the clipped footer", "› Ask Codex\n  Sol · Context 3% used · ...\n", 0, false},
		{"a windowless nonzero footer with other text", "› Ask Codex\n  Sol · Context 3% used · weekly 76% left\n", 0, false},
		{"another line below new activity", codexTruncatedAfter3 + "  ? shortcuts\n", 3, true},
		{"the truncated gauge above the composer is not read", "  Sol · Context 3% used · …\n› Ask Codex\n  New activity · enter/esc latest\n", 0, false},
		{"zero with the complete window remains valid", "› Ask Codex\n  Sol · Context 0% used · 475K window\n", 0, true},
		{"the footer", codexScrolled(codexFooterBefore49), 49, true},
		{"megabyte window", "› Ask Codex\n  Sol · Context 7% used · 1M window · main\n", 7, true},
		{"a decimal window", "› Ask Codex\n  Sol · Context 7% used · 1.5M window · main\n", 7, true},
		{"0 and 100 are valid", "› Ask Codex\n  Sol · Context 100% used · 475K window\n", 100, true},
		{"over 100 is not a gauge", "› Ask Codex\n  Sol · Context 101% used · 475K window\n", 0, false},
		{"no window", "› Ask Codex\n  Sol · Context 0% used · weekly 76% left\n", 0, false},
		{"no dot before the window", "› Ask Codex\n  Sol Context 3% used 475K window\n", 0, false},
		{"the bottom match wins", "› Ask Codex\n  Context 40% used · 475K window\n  Context 5% used · 475K window\n", 5, true},
		{"the composer is the fourth non-empty line from the bottom", "› Ask Codex\n\na\n\nb\n  Context 5% used · 475K window\n", 5, true},
		{"the composer is the fifth non-empty line from the bottom", "› Ask Codex\na\nb\nc\n  Context 5% used · 475K window\n", 0, false},
		{"blank lines do not use the window", "› Ask Codex\n\n\na\n\n\nb\n  Context 5% used · 475K window\n\n", 5, true},
		{"no composer in the last four lines", "  Context 5% used · 475K window\na\nb\n", 0, false},
		{"a gauge above the composer is not read", "  Context 5% used · 475K window\n› Ask Codex\n  Sol · weekly 76% left\n", 0, false},
		{"a gauge above the composer is not read when the footer has no window", codexHistoryAboveComposer(), 0, false},
		{"the first gauge line decides: no window below a line with it", "› Ask Codex\n  Context 5% used · 475K window\n  Context 0% used\n", 0, false},
		{"the first gauge line decides: an out-of-range N", "› Ask Codex\n  Context 5% used · 475K window\n  Context 101% used · 475K window\n", 0, false},
		{"the first gauge line decides: with the window it counts", "› Ask Codex\n  Context 0% used\n  Context 5% used · 475K window\n", 5, true},
		{"the bottom-most composer line anchors, a › line above it does not", "› /compact\n  Context 3% used · 475K window was noted\n› Ask Codex\n  Sol · weekly 76% left\n", 0, false},
		{"a long indented composer line still anchors", "    › Ask Codex\n  Context 5% used · 475K window\n", 5, true},
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

func TestCompactCodexTruncatedContextDropped(t *testing.T) {
	before := strings.Replace(codexTruncatedAfter3, "3%", "45%", 1)
	base, ok := compactCodexContext(before)
	if !ok || base != 45 {
		t.Fatalf("base=(%d,%t), want (45,true); screen=%q", base, ok, before)
	}
	if !compactCodexContextDropped(base, codexTruncatedAfter3) {
		t.Fatalf("45 to 3 must prove a drop: %q", codexTruncatedAfter3)
	}
	if compactCodexContextDropped(base, strings.Replace(codexTruncatedAfter3, "3%", "0%", 1)) || compactCodexContextDropped(base, before) {
		t.Fatal("a compacting footer or an unchanged gauge must not prove a drop")
	}
}

func TestCompactCodexTruncatedFooterDropCompacted(t *testing.T) {
	fakeFastClock(t, time.Second)
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 3)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: strings.Replace(codexTruncatedAfter3, "3%", "45%", 1)},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: strings.Replace(codexTruncatedAfter3, "3%", "0%", 1)},
		{Argv: compactReadArgv("worker"), Stdout: codexTruncatedAfter3},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "600000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "codex" || value["status"] != "compacted" {
		t.Fatalf("compact result=%v", value)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed >= 600000 {
		t.Fatalf("elapsed_ms=%v must be below 600000: %v", value["elapsed_ms"], value)
	}
	calls := f.calls(t)
	if reads := countArgv(calls, compactReadArgv("worker")); reads != 3 {
		t.Fatalf("visible reads=%d want 3 (base, compacting, proof); calls=%#v", reads, calls)
	}
	if gets := countArgv(calls, []string{"agent", "get", "worker"}); gets != 3 {
		t.Fatalf("agent gets=%d want 3 (pre-send, liveness, idle); calls=%#v", gets, calls)
	}
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" {
			switch call.Argv[1] {
			case "send-text":
				if strings.Join(call.Argv, "\x00") != "pane\x00send-text\x00p1\x00/compact" {
					t.Fatalf("unexpected pane text: %q", call.Argv)
				}
			case "send-keys":
				if strings.Join(call.Argv, "\x00") != "pane\x00send-keys\x00p1\x00Enter" {
					t.Fatalf("unexpected navigation key: %q", call.Argv)
				}
			}
		}
	}
	if countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}) != 1 {
		t.Fatalf("the compact command must be sent exactly once: %#v", calls)
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
