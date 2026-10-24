package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Exercise the public command with real subprocesses and a real clock. Each
// slow answer exceeds the entire budget, so a renewed per-call timeout fails
// the elapsed assertion even when the eventual timeout exit is correct.
func TestCompactDeadlineBoundsHerdrAndPauses(t *testing.T) {
	get := []string{"agent", "get", "worker"}
	read := compactReadArgv("worker")
	text := []string{"pane", "send-text", "p1", "/compact"}
	enter := []string{"pane", "send-keys", "p1", "Enter"}
	for _, tc := range []struct {
		name  string
		rule  fakecli.Rule
		kind  string
		proof bool
	}{
		{"initial-state", fakecli.Rule{Argv: get, Call: 1, Delay: 2500, Stdout: compactStateJSON("done", 1)}, "codex", false},
		{"initial-read", fakecli.Rule{Argv: read, ArgvPrefix: true, Call: 1, Delay: 2500}, "codex", false},
		{"visible-fallback", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Delay: 2500}, "codex", false},
		{"send-text", fakecli.Rule{Argv: text, Delay: 2500}, "codex", false},
		{"first-enter", fakecli.Rule{Argv: enter, Delay: 2500}, "codex", false},
		{"proof-read", fakecli.Rule{Argv: read, ArgvPrefix: true, Call: 2, Delay: 2500}, "codex", false},
		{"no-proof-done", fakecli.Rule{}, "codex", false},
		{"no-proof-idle", fakecli.Rule{Argv: get, Stdout: compactStateJSON("idle", 1)}, "codex", false},
		{"state-after-proof", fakecli.Rule{Argv: get, Call: 2, Delay: 2500, Stdout: compactStateJSON("done", 1)}, "codex", true},
		{"state-without-proof", fakecli.Rule{Argv: get, Call: 2, Delay: 2500, Stdout: compactStateJSON("done", 1)}, "codex", false},
		{"retry-pause", fakecli.Rule{Argv: get, Code: 137}, "codex", false},
		{"composer-confirm", fakecli.Rule{Argv: read, ArgvPrefix: true, Stdout: "› /compact\n"}, "codex", false},
		{"second-enter", fakecli.Rule{Argv: enter, Call: 2, Delay: 2500}, "codex", false},
		{"composer-clear", fakecli.Rule{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Delay: 2500}, "codex", false},
		{"opencode-menu", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Delay: 2500}, "opencode", false},
		{"pi-thinking-pause", fakecli.Rule{Argv: read, ArgvPrefix: true, Stdout: "model • high\n"}, "pi", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, bound := time.Second, 2*time.Second
			if tc.name == "composer-confirm" {
				// Race instrumentation can spend the one-second budget before
				// the first Enter. Leave preparation room, still below the
				// five-second confirmation window (no second Enter).
				budget, bound = 3*time.Second, 4*time.Second
			}
			rules := []fakecli.Rule{}
			if tc.name == "second-enter" || tc.name == "composer-clear" {
				budget, bound = 10*time.Second, 12*time.Second
				tc.rule.Delay = 15000
				// Expire the two local confirmation windows without spending the
				// real phase budget before the second Enter or clear is reached.
				fakeFastClock(t, 6*time.Second)
				rules = append(rules, fakecli.Rule{Argv: read, ArgvPrefix: true, Stdout: "› /compact\n"})
			}
			if len(tc.rule.Argv) != 0 {
				rules = append(rules, tc.rule)
			}
			if tc.name == "visible-fallback" {
				rules = append(rules, fakecli.Rule{Argv: read, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"agent_not_idle"}}`})
			}
			if tc.proof {
				rules = append(rules, fakecli.Rule{Argv: read, ArgvPrefix: true, Call: 2, Stdout: "Context compacted\n›\n"})
			}
			rules = append(rules,
				fakecli.Rule{Argv: get, Stdout: compactStateJSON("done", 1)},
				fakecli.Rule{Argv: read, ArgvPrefix: true, Stdout: "> /compact\n›\n"},
				fakecli.Rule{Argv: text}, fakecli.Rule{Argv: enter},
				fakecli.Rule{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}},
			)
			f := newCompactFixture(t, tc.kind, rules)
			start := time.Now()
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", budget.String())
			elapsed := time.Since(start)
			if code != 9 || compactJSON(t, out)["status"] != "timeout" || elapsed < budget-100*time.Millisecond || elapsed > bound {
				t.Fatalf("code=%d elapsed=%s (budget=%s bound=%s slow=%dms) stdout=%q stderr=%q calls=%#v", code, elapsed, budget, bound, tc.rule.Delay, out, stderr, f.calls(t))
			}
			calls := f.calls(t)
			if tc.name == "second-enter" || tc.name == "composer-clear" {
				t.Logf("target=%s budget=%s bound=%s injected-delay=%dms elapsed=%s remaining-upper-slack=%s", tc.name, budget, bound, tc.rule.Delay, elapsed, bound-elapsed)
			}
			if tc.name == "second-enter" && countArgv(calls, enter) != 2 {
				t.Fatalf("second Enter was not reached: %#v", calls)
			}
			if tc.name == "composer-clear" && countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}) != 1 {
				t.Fatalf("clear was not reached: %#v", calls)
			}
			if tc.proof && countArgv(calls, get) != 2 {
				t.Fatalf("idle wait was not exercised: %#v", calls)
			}
			if tc.name == "retry-pause" && countArgv(calls, get) != 1 {
				t.Fatalf("retry escaped deadline: %#v", calls)
			}
			if tc.name == "composer-confirm" && countArgv(calls, enter) != 1 {
				t.Fatalf("extra Enter after deadline: %#v", calls)
			}
			if tc.name == "pi-thinking-pause" && compactJSON(t, out)["thinking_restored"] != false {
				t.Fatalf("restoration must be reported: %s", out)
			}
			if n := countArgv(calls, text); n > 1 {
				t.Fatalf("resent /compact: %#v", calls)
			}
		})
	}
}

type review504CleanupPanicWriter struct {
	buf    *bytes.Buffer
	marker any
	fired  bool
}

func (w *review504CleanupPanicWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "deadline exhausted before") {
		w.fired = true
		panic(w.marker)
	}
	return w.buf.Write(p)
}

func TestRereview504CleanupBugNotMasked(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "model • high\n"},
		{Argv: compactReadArgv("worker"), Stdout: "model • off\n"},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Code: 1, Stderr: "transport refused"},
	})
	oldOut, oldErr := platform.Stdout, platform.Stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	var out, stderr bytes.Buffer
	marker := &struct{ Reason string }{"unexpected cleanup writer panic"}
	w := &review504CleanupPanicWriter{buf: &stderr, marker: marker}
	platform.Stdout, platform.Stderr = &out, w
	var got any
	code := -1
	func() {
		defer func() { got = recover() }()
		code = Run([]string{"compact", "worker", "--timeout", "3s"}, f.env)
	}()
	platform.Stdout, platform.Stderr = oldOut, oldErr
	t.Logf("cleanup-panic-raised=%t same-panic-escaped=%t recovered=%#v code=%d taps=%d stdout=%q stderr=%q", w.fired, got == marker, got, code, countArgv(f.calls(t), []string{"pane", "send-keys", "p1", "shift+tab"}), out.String(), stderr.String())
	if countArgv(f.calls(t), []string{"pane", "send-text", "p1", "/compact"}) != 1 {
		t.Fatal("transport failure was not reached")
	}
	if !w.fired {
		t.Fatal("cleanup panic was not injected")
	}
	if got != marker {
		t.Fatal("unexpected cleanup panic was replaced by original operational exit")
	}
}

func TestReview504PreserveTransportFailure(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "model • high\n"},
		{Argv: compactReadArgv("worker"), Stdout: "model • off\n"},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Code: 1, Stderr: "transport refused"},
	})
	start := time.Now()
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "3s")
	calls := f.calls(t)
	t.Logf("code=%d elapsed=%s stdout=%q stderr=%q calls=%#v", code, time.Since(start), out, stderr, calls)
	if countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}) != 2 {
		t.Fatal("transport failure and deferred restoration were not reached")
	}
	if code != 4 || !strings.Contains(stderr, "could not send /compact") {
		t.Fatal("observed transport failure lost during restoration")
	}
	if out != "" || !strings.Contains(stderr, "set it by hand before the next brief") {
		t.Fatal("cleanup expiry must warn without replacing the original error with timeout JSON")
	}
}

func TestReview504PreserveRestorationObservation(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "model • high\n"},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: "model • off\n"},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: "Compacted from 12345 tokens\nmodel • off\n"},
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: "Compacted from 12345 tokens\nmodel • high\n"},
		// Two fixed thinking pauses consume four seconds before this read;
		// leave race preparation slack, then stall beyond the whole budget.
		{Argv: compactReadArgv("worker"), Call: 5, Delay: 12000, Stdout: "model • high\n"},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}},
	})
	start := time.Now()
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "8s")
	t.Logf("code=%d elapsed=%s stdout=%q stderr=%q calls=%#v", code, time.Since(start), out, stderr, f.calls(t))
	if countArgv(f.calls(t), compactReadArgv("worker")) != 5 {
		t.Fatal("post-restoration diagnostic not reached")
	}
	if compactJSON(t, out)["thinking_restored"] != true {
		t.Fatal("successful observed restoration reported false")
	}
	if code != 9 || compactJSON(t, out)["status"] != "timeout" || strings.Contains(stderr, "set it by hand") {
		t.Fatal("diagnostic expiry must preserve the confirmed restore without a manual-restoration warning")
	}
}

func TestCompactDeadlineFreshProofControl(t *testing.T) {
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("done", 1)},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Call: 1, Stdout: "›\n"},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "Context compacted\n›\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}},
	})
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "5s")
	if code != 0 || compactJSON(t, out)["status"] != "compacted" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
}
