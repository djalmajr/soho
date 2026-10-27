package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func piScrolledScreens(t *testing.T) (string, string) {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(packageDir, "testdata", "pi-compact-scrolled-"+name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read("before"), read("after")
}

func piScrolledRules(before, after, pane string) []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: strings.ReplaceAll(before, "• high", "• thinking off")},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: after},
		{Argv: compactReadArgv("worker"), Stdout: strings.ReplaceAll(after, "• thinking off", "• high")},
		{Argv: []string{"pane", "send-text", pane, "/compact"}},
		{Argv: []string{"pane", "send-keys", pane, "Enter"}},
		{Argv: []string{"pane", "send-keys", pane, "shift+tab"}},
	}
}

func TestCompactPiScrolledIdleRestoresThinking(t *testing.T) {
	before, after := piScrolledScreens(t)
	for _, state := range []string{"idle", "done"} {
		t.Run(state, func(t *testing.T) {
			rules := append([]fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON(state, 2)}}, piScrolledRules(before, after, "p1")...)
			f := newCompactFixture(t, "pi", rules)
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "30s")
			if code != 0 || stderr != "" || compactJSON(t, out)["status"] != "compacted" || compactJSON(t, out)["thinking_restored"] != true {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
			}
			if n := countArgv(f.calls(t), []string{"pane", "send-keys", "p1", "shift+tab"}); n != 2 {
				t.Fatalf("thinking taps=%d want 2", n)
			}
		})
	}
}

func TestDispatchCompactPiScrolledSendsBriefOnce(t *testing.T) {
	before, after := piScrolledScreens(t)
	// Specific rules precede the common fallback; get 1..3 cover compact,
	// get 4 is dispatch's baseline, and get 5 proves the prompt's arrival.
	rules := []fakecli.Rule{}
	for i := 1; i <= 4; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: compactStateJSON("idle", 1)})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 5, Stdout: compactStateJSON("working", 2)})
	rules = append(rules, piScrolledRules(before, after, "w0test:p0a")...)
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", rules...)
	// The point is the compact proof and a single send; the arrival check's
	// read count varies with the host's timing, so it is off here.
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	arrivalRosterKind(t, f, "pi")
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 || dispatchLastLineStatus(t, out) != "submitted" || !strings.Contains(stderr, "dispatch: compacted 'worker'") || strings.Contains(stderr, "set it by hand") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 1 {
		t.Fatalf("brief sends=%d want 1", n)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "w0test:p0a", "shift+tab"}); n != 2 {
		t.Fatalf("thinking taps=%d want 2", n)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}); n != 1 {
		t.Fatalf("compact sends=%d want 1", n)
	}
}

func TestCompactPiScrolledFooterCases(t *testing.T) {
	before, after := piScrolledScreens(t)
	// These proof-only fixtures have no readable thinking level.
	before = strings.ReplaceAll(before, "• high", "• unavailable")
	after = strings.ReplaceAll(after, "• thinking off", "• unavailable")
	for _, tc := range []struct {
		name, base, screen, state string
		want                      int
	}{
		{"unknown-idle", "57.1", after, "idle", 0},
		{"unknown-working", "57.1", after, "working", 9},
		{"half-or-less", "57.1", strings.ReplaceAll(after, "?/262k", "28.5%/262k"), "idle", 0},
		// 30 is above half of 57.1 (28.55); the brief's example conflicts with its rule.
		{"thirty-above-half", "57.1", strings.ReplaceAll(after, "?/262k", "30.0%/262k"), "idle", 9},
		{"forty", "57.1", strings.ReplaceAll(after, "?/262k", "40.0%/262k"), "idle", 9},
		{"base-seven", "7", after, "idle", 9},
		{"running-marker", "57.1", "Compacting context...\n" + after, "idle", 9},
		{"history-only", "57.1", "?/262k\nold output\n────────────────────\n\n────────────────────\n~/project\nno gauge\n", "idle", 9},
		{"no-input-box", "57.1", "history ?/262k\n", "idle", 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeFastClock(t, 60*time.Second)
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON(tc.state, 2)},
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 3)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: strings.ReplaceAll(before, "57.1%/262k", tc.base+"%/262k")},
				{Argv: compactReadArgv("worker"), Call: 2, Stdout: tc.screen},
				{Argv: compactReadArgv("worker"), Stdout: "no footer\n"},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}},
			}
			f := newCompactFixture(t, "pi", rules)
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "30s")
			if code != tc.want {
				t.Fatalf("code=%d want=%d out=%s stderr=%s calls=%#v", code, tc.want, out, stderr, f.calls(t))
			}
			if tc.want == 0 && compactJSON(t, out)["status"] != "compacted" {
				t.Fatalf("out=%s", out)
			}
			if tc.want == 9 && compactJSON(t, out)["status"] != "timeout" {
				t.Fatalf("out=%s", out)
			}
			t.Logf("base=%s state=%s code=%d output=%s", tc.base, tc.state, code, strings.TrimSpace(out))
		})
	}
}

func TestCompactPiScrolledTimeoutRestoreReserve(t *testing.T) {
	before, after := piScrolledScreens(t)
	for _, fail := range []bool{false, true} {
		name := "restored"
		if fail {
			name = "restore-failed"
		}
		t.Run(name, func(t *testing.T) {
			// The proof read overruns the proof deadline (the 18s budget minus a
			// reserve capped at half of what remains after the Enter), so the real
			// context cancellation, not the synthetic poll clock, triggers cleanup,
			// and the restore runs before the budget ends.
			rules := []fakecli.Rule{{Argv: compactReadArgv("worker"), Call: 3, Delay: 20000, Stdout: after}}
			if fail {
				rules = append(rules, fakecli.Rule{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}, Call: 2, Code: 1, Stderr: "restore refused"})
			}
			rules = append(rules, piScrolledRules(before, after, "p1")...)
			f := newCompactFixture(t, "pi", rules)
			began := time.Now()
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "18s")
			elapsed := time.Since(began)
			if code != 9 || compactJSON(t, out)["thinking_restored"] != !fail || elapsed < 3*time.Second || elapsed >= 18*time.Second {
				t.Fatalf("code=%d elapsed=%s out=%s stderr=%s", code, elapsed, out, stderr)
			}
			if n := countArgv(f.calls(t), []string{"pane", "send-keys", "p1", "shift+tab"}); n != 2 {
				t.Fatalf("restore not attempted: taps=%d", n)
			}
			if strings.Contains(stderr, "set it by hand") != fail {
				t.Fatalf("warning fail=%t stderr=%s", fail, stderr)
			}
			t.Logf("restore-fails=%t elapsed=%s out=%s stderr=%s", fail, elapsed, strings.TrimSpace(out), strings.TrimSpace(stderr))
		})
	}
}
