package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// freshBase builds the pre-send baseline the fresh-proof rule compares a
// screen against (the D22 fix: claude, codex and pi accept a proof only when
// it is new since the baseline, never when the screen just redrew or
// scrolled with the same proof text and counts).
func freshBase(before, proof string) compactProofBaseline {
	return compactProofBaseline{screen: before, proof: proof, seen: compactProofLines(before, proof)}
}

// TestCompactProofNewRule exercises the fresh-proof rule directly (claude,
// codex, pi): a proof line unseen since the baseline, a grown proof-line
// count, or a grown echo count with the proof below the last echo — and the
// negative class: a screen that only changed clock, context, working/idle or
// scrolled with the identical proof and the same counts is not proof, and a
// new echo without a proof is not proof.
func TestCompactProofNewRule(t *testing.T) {
	proof := "Context compacted"
	cases := []struct {
		name   string
		base   compactProofBaseline
		screen string
		want   bool
	}{
		{
			name:   "an unseen proof line since the baseline counts",
			base:   freshBase("old\n› /compact\nContext compacted\nprompt\n", proof),
			screen: "prompt\n› /compact\nContext compacted (42 tokens kept)\n",
			want:   true,
		},
		{
			name:   "a grown proof-line count with the identical text counts",
			base:   freshBase("old\n› /compact\nContext compacted\nprompt\n", proof),
			screen: "old\n› /compact\nContext compacted\nContext compacted\nprompt\n",
			want:   true,
		},
		{
			name:   "a grown echo count with the proof below the last echo counts",
			base:   freshBase("old\nContext compacted\nprompt\n", proof),
			screen: "prompt\n› /compact\n› /compact\nContext compacted\n",
			want:   true,
		},
		{
			name:   "a clock tick on a screen with a stale proof below the old echo is not proof",
			base:   freshBase("earlier reply\n› /compact\nContext compacted\nclock: 17:45\nprompt\n", proof),
			screen: "earlier reply\n› /compact\nContext compacted\nclock: 17:46\nprompt\n",
			want:   false,
		},
		{
			name:   "a context redraw on a screen with a stale proof below the old echo is not proof",
			base:   freshBase("old\n› /compact\nContext compacted\ncontext 51%\nprompt\n", proof),
			screen: "old\n› /compact\nContext compacted\ncontext 52%\nprompt\n",
			want:   false,
		},
		{
			name:   "a working/idle line change on a screen with a stale proof is not proof",
			base:   freshBase("earlier reply\n› /compact\nContext compacted\n• Idle\nprompt\n", proof),
			screen: "earlier reply\n› /compact\nContext compacted\n• Working (1s • esc to interrupt)\n",
			want:   false,
		},
		{
			name:   "a realistic scroll (lines shifted, old echo gone) with the identical proof and the same counts is not proof",
			base:   freshBase("› /compact\nContext compacted\nreply A\nreply B\nprompt\n", proof),
			screen: "reply A\nreply B\n› /compact\nContext compacted\nprompt\n",
			want:   false,
		},
		{
			name:   "a new echo without a proof is not proof",
			base:   freshBase("old conversation\n› Ask Codex to do anything\n", proof),
			screen: "old conversation\n› /compact\n› Ask Codex to do anything\n",
			want:   false,
		},
		{
			name:   "the stale proof above the new echo is not the proof of the new one",
			base:   freshBase("old\nContext compacted\nprompt\n", proof),
			screen: "old\nContext compacted\n› /compact\nprompt\n",
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compactProofNew(c.screen, c.base); got != c.want {
				t.Fatalf("compactProofNew = %v want %v:\nbase=%q\nscreen=%q", got, c.want, c.base.screen, c.screen)
			}
		})
	}
}

// TestCompactD22Screens runs the D22 probe cases end-to-end through
// compactRun with the fake CLI (review probe A, C, D, E, F plus the
// new-echo-without-proof and the fresh-line-after-scroll variants). Every
// /compact is sent exactly once (the brief is never resent); the ambiguous
// screens end at the deadline timeout with one Enter and no clear, never the
// stuck-composer protocol ("nothing was compacted"); a fresh proof or a
// grown proof count ends as compacted.
func TestCompactD22Screens(t *testing.T) {
	cases := []struct {
		name       string
		before     string
		after      string
		wantCode   int
		wantStatus string
	}{
		{
			name:     "A stale proof below the old echo plus a clock tick",
			before:   "earlier reply\n› /compact\nContext compacted\nclock: 17:45\n› Ask Codex to do anything\n",
			after:    "earlier reply\n› /compact\nContext compacted\nclock: 17:46\n› Ask Codex to do anything\n",
			wantCode: 9, wantStatus: "timeout",
		},
		{
			name:     "C scrolled plus busy composer (the echo is the last › line)",
			before:   "old conversation\n› /compact\nContext compacted\n› Ask Codex to do anything\n",
			after:    "new conversation\n› /compact\nContext compacted\n• Working (1s • esc to interrupt)\n",
			wantCode: 9, wantStatus: "timeout",
		},
		{
			name:     "D realistic scroll: lines shifted up, old echo gone",
			before:   "› /compact\nContext compacted\nreply A\nreply B\n› Ask Codex to do anything\n",
			after:    "reply A\nreply B\n› /compact\nContext compacted\n› Ask Codex to do anything\n",
			wantCode: 9, wantStatus: "timeout",
		},
		{
			name:     "E stale proof plus busy composer",
			before:   "earlier reply\n› /compact\nContext compacted\n› Ask Codex to do anything\n",
			after:    "earlier reply\n› /compact\nContext compacted\n• Working (1s • esc to interrupt)\n",
			wantCode: 9, wantStatus: "timeout",
		},
		{
			name:     "F new echo plus the identical proof (the proof count grows)",
			before:   "earlier reply\n› /compact\nContext compacted\n› Ask Codex to do anything\n",
			after:    "earlier reply\n› /compact\nContext compacted\n› /compact\nContext compacted\n› Ask Codex to do anything\n",
			wantCode: 0, wantStatus: "compacted",
		},
		{
			name:     "G a new echo without a proof",
			before:   "old conversation\n› Ask Codex to do anything\n",
			after:    "old conversation\n› /compact\n› Ask Codex to do anything\n",
			wantCode: 9, wantStatus: "timeout",
		},
		{
			name:     "H a fresh proof line after the scroll",
			before:   "old conversation\n› /compact\nContext compacted\n› Ask Codex to do anything\n",
			after:    "new conversation\n› /compact\nContext compacted (42 tokens kept)\n› Ask Codex to do anything\n",
			wantCode: 0, wantStatus: "compacted",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			f := newCompactFixture(t, "codex", []fakecli.Rule{
				// All agent get calls idle: the pre-send check, the loop's
				// dead check and the idle wait after a fresh proof.
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: c.before},
				{Argv: compactReadArgv("worker"), Stdout: c.after},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
			})
			code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
			if code != c.wantCode {
				t.Fatalf("code=%d want %d out=%s stderr=%s", code, c.wantCode, out, errText)
			}
			if status := compactJSON(t, out)["status"]; status != c.wantStatus {
				t.Fatalf("status=%v want %q: %s", status, c.wantStatus, out)
			}
			if errText != "" {
				t.Fatalf("stderr on a fake-clock run must be empty (no stuck-composer exit, no short-timeout warning): %q", errText)
			}
			calls := f.calls(t)
			// The brief is never resent and the ambiguous screens take no
			// second Enter and no clear.
			if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
				t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
			}
			if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
				t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
			}
			if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
				t.Fatalf("ctrl+u calls=%d want 0: %#v", n, calls)
			}
		})
	}
}

// TestCompactProofObservedBeforeScrollKeepsCompacted covers the proofObserved
// latch: the first positive proof is remembered for the rest of the attempt,
// including the last read after the deadline. The screen scrolls while the
// idle wait is still running (the proof leaves the 40-line window), so a
// re-evaluation of the last read alone would time out; the latch still gets
// the capped idle wait, and the worker back at idle ends the attempt as
// compacted. The latch never skips the return to idle.
func TestCompactProofObservedBeforeScrollKeepsCompacted(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	const before = "old conversation\n› Ask Codex to do anything\n"
	const proof = "old conversation\n› /compact\nContext compacted\n› Ask Codex to do anything\n"
	const scrolled = "reply A\nreply B\n› Ask Codex to do anything\n"
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		// 1: pre-send (idle). 2: the idle wait after the proof, the worker
		// still working when the deadline runs out. 3: the capped idle wait
		// after the deadline, the worker back at idle.
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		// 1: pre-send, no proof and no echo. 2: the first proof poll, the
		// fresh proof on screen. 3 on: the screen after the scroll, the
		// proof out of the window.
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: proof},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: scrolled},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["status"] != "compacted" {
		t.Fatalf("json=%v want compacted (the proof seen before the scroll is latched): %s", value, out)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 3 {
		t.Fatalf("agent get calls=%d want 3 (pre-send, the working idle wait, the idle one after the deadline): %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1 (the latch never resends /compact): %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
		t.Fatalf("ctrl+u calls=%d want 0: %#v", n, calls)
	}
}

// TestCompactProofObservedLatchStillTimesOutWhenNeverIdle verifies the other
// side of the latch: a proof seen before the deadline does not end the
// attempt without the worker back at idle. The worker stays working through
// the capped idle wait after the deadline, so the result is the timeout (9),
// with no resend.
func TestCompactProofObservedLatchStillTimesOutWhenNeverIdle(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	const before = "old conversation\n› Ask Codex to do anything\n"
	const proof = "old conversation\n› /compact\nContext compacted\n› Ask Codex to do anything\n"
	const scrolled = "reply A\nreply B\n› Ask Codex to do anything\n"
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		// All agent get calls working after the pre-send check: the idle wait
		// runs out the deadline and the capped wait after the deadline finds
		// the worker still working.
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: compactStateJSON("working", 2)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: proof},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: scrolled},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "1000")
	if code != 9 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["status"] != "timeout" {
		t.Fatalf("json=%v want timeout (the latched proof still needs the worker back at idle)", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1 (the brief is never resent on the timeout): %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
		t.Fatalf("ctrl+u calls=%d want 0: %#v", n, calls)
	}
}

// TestCompactCodexStuckTransitionsToHistoryEcho covers the transition from a
// really stuck composer to a delivered history echo with a stale proof: no
// extra key may be sent. The pre-send screen already holds a stale
// compaction (echo plus proof), so the echo the /compact leaves when it runs
// late is the identical line with the identical proof — not fresh proof.
// Whether the transition shows up on the confirm read or on the re-read
// right before the second Enter, the screen goes back to the proof check
// with only the first Enter sent, and the attempt ends at the deadline
// timeout (9), never the stuck-composer exit (4). The delivered screen is
// covered with the composer back to its placeholder line and with the
// composer busy (a Working line): in the busy case the last › line on screen
// is the /compact echo itself, so only the proof-below guard in the confirm
// and re-read consumers keeps that echo from being read as a stuck box.
func TestCompactCodexStuckTransitionsToHistoryEcho(t *testing.T) {
	const staleBefore = "old\n› /compact\nContext compacted\n› Ask Codex to do anything\n"
	const boxHolding = "old\n› /compact\nContext compacted\n› /compact\n"
	const deliveredLate = "new\n› /compact\nContext compacted\n› Ask Codex to do anything\n"
	const deliveredBusy = "new\n› /compact\nContext compacted\n• Working (1s • esc to interrupt)\n"
	t.Run("the transition shows up on the confirm read", func(t *testing.T) {
		fakeFastClock(t, 6*time.Second)
		f := newCompactFixture(t, "codex", []fakecli.Rule{
			// 1: pre-send (idle). 2 on: the loop's dead check (the confirm
			// exits before the re-read state check).
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			// 1: pre-send, the stale compaction. 2: the first proof poll, the
			// box still holding /compact (really stuck: no proof below the
			// last /compact). 3: the confirm, the command delivered late — the
			// history echo with the stale proof, the box empty. 4: the last
			// read after the deadline.
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: staleBefore},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: boxHolding},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: deliveredLate},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s (the delivered echo with the stale proof is not fresh proof)", code, out, errText)
		}
		if errText != "" {
			t.Fatalf("stderr must be empty (no stuck-composer exit): %q", errText)
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
			t.Fatalf("Enter calls=%d want 1 (the confirm saw the delivered echo, no second Enter): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
			t.Fatalf("ctrl+u calls=%d want 0 (the box was never cleared): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
			t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
		}
	})
	t.Run("the transition shows up on the re-read before the second Enter", func(t *testing.T) {
		fakeFastClock(t, 6*time.Second)
		f := newCompactFixture(t, "codex", []fakecli.Rule{
			// 1: pre-send (idle). 2: the confirm's dead check. 3: the re-read
			// state check before the second Enter. 4 on: the loop's dead
			// check.
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			// 1: pre-send, the stale compaction. 2: the first proof poll, the
			// box still holding /compact. 3: the confirm, still stuck. 4: the
			// re-read before the second Enter, the delivered history echo with
			// the stale proof. 5: the last read after the deadline.
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: staleBefore},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: boxHolding},
			{Argv: compactReadArgv("worker"), Call: 3, Stdout: boxHolding},
			{Argv: compactReadArgv("worker"), Call: 4, Stdout: deliveredLate},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: deliveredLate},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s (the delivered echo with the stale proof is not fresh proof)", code, out, errText)
		}
		if errText != "" {
			t.Fatalf("stderr must be empty (no stuck-composer exit): %q", errText)
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
			t.Fatalf("Enter calls=%d want 1 (the re-read saw the delivered echo, no second Enter): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
			t.Fatalf("ctrl+u calls=%d want 0 (the box was never cleared): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
			t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
		}
	})
	t.Run("the transition shows up on the confirm read with a busy composer", func(t *testing.T) {
		fakeFastClock(t, 6*time.Second)
		f := newCompactFixture(t, "codex", []fakecli.Rule{
			// 1: pre-send (idle). 2 on: the loop's dead check (the confirm
			// exits before the re-read state check).
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			// 1: pre-send, the stale compaction. 2: the first proof poll, the
			// box still holding /compact (really stuck: no proof below the
			// last /compact). 3: the confirm, the command delivered late — the
			// history echo with the stale proof, the composer busy (the last ›
			// line is the echo itself, the guard must read it as not stuck).
			// 4: the last read after the deadline.
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: staleBefore},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: boxHolding},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: deliveredBusy},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s (the busy-composer echo with the stale proof is not fresh proof)", code, out, errText)
		}
		if strings.Contains(errText, "nothing was compacted") {
			t.Fatalf("the delivered echo with the stale proof must not run the stuck-composer clear: %q", errText)
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
			t.Fatalf("Enter calls=%d want 1 (the confirm saw the delivered echo, no second Enter): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
			t.Fatalf("ctrl+u calls=%d want 0 (the box was never cleared): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
			t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
		}
	})
	t.Run("the transition shows up on the re-read before the second Enter with a busy composer", func(t *testing.T) {
		fakeFastClock(t, 6*time.Second)
		f := newCompactFixture(t, "codex", []fakecli.Rule{
			// 1: pre-send (idle). 2: the confirm's dead check. 3: the re-read
			// state check before the second Enter (idle: the guard must stop
			// the protocol here). 4 on: the loop's dead check.
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			// 1: pre-send, the stale compaction. 2: the first proof poll, the
			// box still holding /compact. 3: the confirm, still stuck. 4: the
			// re-read before the second Enter, the delivered history echo with
			// the stale proof and the composer busy. 5: the last read after
			// the deadline.
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: staleBefore},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: boxHolding},
			{Argv: compactReadArgv("worker"), Call: 3, Stdout: boxHolding},
			{Argv: compactReadArgv("worker"), Call: 4, Stdout: deliveredBusy},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: deliveredBusy},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s (the busy-composer echo with the stale proof is not fresh proof)", code, out, errText)
		}
		if strings.Contains(errText, "nothing was compacted") {
			t.Fatalf("the delivered echo with the stale proof must not run the stuck-composer clear: %q", errText)
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
			t.Fatalf("Enter calls=%d want 1 (the re-read saw the delivered echo, no second Enter): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
			t.Fatalf("ctrl+u calls=%d want 0 (the box was never cleared): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
			t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
		}
	})
}

// TestCompactNewEchoWithoutProofTimesOut verifies the acceptance case at the
// compactRun level: a screen that gained the /compact echo but no proof ends
// at the deadline timeout (9), not a silent "compacted" — the echo alone
// does not prove the compaction ran.
func TestCompactNewEchoWithoutProofTimesOut(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	const before = "old conversation\n› Ask Codex to do anything\n"
	const after = "old conversation\n› /compact\n› Ask Codex to do anything\n"
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: after},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "5000")
	if code != 9 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["status"] != "timeout" {
		t.Fatalf("json=%v want timeout (a new echo without a proof is not proof)", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
		t.Fatalf("ctrl+u calls=%d want 0: %#v", n, calls)
	}
}

// TestCompactStaleProofNeverRereadAsSuccessAfterRetry verifies that when the
// stuck-composer protocol runs on a really stuck box and the box is cleared
// (exit 4), the stale proof on screen is never reported as a success: there
// is no result JSON at all on that exit.
func TestCompactStaleProofNeverRereadAsSuccessAfterRetry(t *testing.T) {
	stale := "earlier reply\n› /compact\nContext compacted\n› /compact\n"
	t.Run("a stale proof on screen does not turn the stuck exit into a success", func(t *testing.T) {
		fakeFastClock(t, 6*time.Second)
		f := newCompactFixture(t, "codex", []fakecli.Rule{
			// 1: pre-send (idle). 2: the confirm's dead check. 3: the re-read
			// state check before the second Enter (idle: the protocol runs to
			// the clear).
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			// 1: pre-send, the stale compaction on screen. 2 on: the box keeps
			// holding /compact with the stale proof above it — really stuck
			// (no proof below the last /compact).
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: stale},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: stale},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 4 || out != "" {
			t.Fatalf("code=%d out=%s stderr=%s (the stale proof must not read as a success)", code, out, errText)
		}
		if !strings.Contains(errText, "cleared it, nothing was compacted") {
			t.Fatalf("stderr=%q want the stuck-composer clear message", errText)
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 2 {
			t.Fatalf("Enter calls=%d want 2 (the current protocol on a really stuck box): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 1 {
			t.Fatalf("ctrl+u calls=%d want 1: %#v", n, calls)
		}
	})
}
