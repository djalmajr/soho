// supervise_wake_test.go covers the supervisor's wake wiring (slice 8c):
// the persisted cursor and the background worker run the machine's wake
// hook for every queued event, the watch loop only enqueues (never blocks
// on the hook), a restarted supervisor re-runs only the events after the
// cursor, a failing hook is retried and still advances the cursor, and the
// terminal wait is bounded. The fake hook is the re-executed test binary
// (fakecli), and the supervisor pauses come from the shared fake clock.
package job

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// wakeEnvKeys are the four SOHO_JOB_* vars the hook must see.
var wakeEnvKeys = []string{
	"SOHO_JOB_ID", "SOHO_JOB_SEQ", "SOHO_JOB_EVENT",
	"SOHO_JOB_IDEMPOTENCY_KEY",
}

// wakeSupFixture is supFixture plus the fake wake hook installed in its
// own directory and the hermetic env for it (the pattern of wake_test.go).
func wakeSupFixture(t *testing.T, rules []fakecli.Rule, opts fakecli.InstallOptions) (*Store, string, *fakeClock, *fakeTeam, string, platform.Env) {
	t.Helper()
	store, jobDir, clock, team := supFixture(t)
	fakeDir := t.TempDir()
	if _, err := fakecli.InstallWithOptions(t, fakeDir, "wakehook", rules, opts); err != nil {
		t.Fatalf("install the fake wake hook: %v", err)
	}
	env := platform.Env{}
	for _, item := range fakecli.Env(nil, fakeDir) {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			env[key] = value
		}
	}
	return store, jobDir, clock, team, fakeDir, env
}

// wakeSeqValue reads the persisted cursor file: its content and whether it
// exists.
func wakeSeqValue(t *testing.T, jobDir string) (string, bool) {
	t.Helper()
	// The background worker may be replacing the cursor while the test
	// reads it: read it the lock-free way, as the package's readers do, so
	// the read neither fails on the replace nor makes it fail on Windows.
	raw, err := readReplacedFile(filepath.Join(jobDir, "wake.seq"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		t.Fatalf("read wake.seq: %v", err)
	}
	return string(raw), true
}

// hookSeqs returns the SOHO_JOB_SEQ of every recorded hook call.
func hookSeqs(t *testing.T, fakeDir string) []int {
	t.Helper()
	calls := wakeHookCalls(t, fakeDir)
	out := make([]int, 0, len(calls))
	for _, call := range calls {
		seq, err := strconv.Atoi(call.Env["SOHO_JOB_SEQ"])
		if err != nil {
			t.Fatalf("hook call %d has no seq env: %v", len(out), call.Env)
		}
		out = append(out, seq)
	}
	return out
}

// TestSuperviseWakeNoteRunsHookOnce: a question written by Store.Note (as
// job note does) runs the hook exactly once, with the exact event line on
// stdin and the four env vars; the non-waking events never run it; and
// wake.seq advances to the last processed seq.
func TestSuperviseWakeNoteRunsHookOnce(t *testing.T) {
	store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
		[]fakecli.Rule{{AnyArgs: true}},
		fakecli.InstallOptions{CaptureStdin: true, CaptureEnv: wakeEnvKeys})
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision", Refs: map[string]string{"agente": "orch-1"}}); err != nil {
		t.Fatal(err)
	}
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env}

	exit, err := sup.Run()
	if err != nil || exit != 0 {
		t.Fatalf("Run: exit=%d err=%v, want 0", exit, err)
	}
	// The question and the terminal event ran the hook, once each; the
	// accepted, worker_spawned and worker_done events never did. The
	// terminal event is appended in the terminal path, after the last tick,
	// and still wakes the dispatcher.
	calls := wakeHookCalls(t, fakeDir)
	if got := hookSeqs(t, fakeDir); !reflect.DeepEqual(got, []int{2, 5}) {
		t.Fatalf("hook seqs = %v, want [2 5] (question, terminal)", got)
	}
	eventsRaw, err := os.ReadFile(filepath.Join(jobDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(eventsRaw), "\n")
	if len(lines) != 7 || !strings.Contains(lines[1], `"tipo":"question"`) {
		t.Fatalf("events.jsonl = %q, want the question on its second line", lines)
	}
	// stdin is the exact events.jsonl line, not a re-rendering.
	if calls[0].Stdin != lines[1]+"\n" {
		t.Fatalf("stdin = %q, want the exact event line %q", calls[0].Stdin, lines[1])
	}
	// The four env vars carry the exact values.
	want := map[string]string{
		"SOHO_JOB_ID":              "job-1",
		"SOHO_JOB_SEQ":             "2",
		"SOHO_JOB_EVENT":           "question",
		"SOHO_JOB_IDEMPOTENCY_KEY": "job-1:2",
	}
	if len(calls[0].Env) != len(want) {
		t.Fatalf("captured env = %v, want %v", calls[0].Env, want)
	}
	for key, value := range want {
		if calls[0].Env[key] != value {
			t.Fatalf("env %s = %q, want %q", key, calls[0].Env[key], value)
		}
	}
	// The cursor advanced to the last processed seq: 5 (terminal).
	if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "5" {
		t.Fatalf("wake.seq = %q ok=%v, want 5", seq, ok)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
	if st := supState(t, store); st.Status != StatusDone {
		t.Fatalf("state = %+v, want done", st)
	}
}

// TestSuperviseWakeBlockingHookNeverDelaysLoop: a hook that blocks (a 5 s
// fake delay bounded by the 2 s per-attempt timeout) never delays the
// watch loop: the ticks keep running while the hook is in flight, and the
// drain happens on the terminal wait, not in the loop.
func TestSuperviseWakeBlockingHookNeverDelaysLoop(t *testing.T) {
	store, jobDir, clock, team, _, env := wakeSupFixture(t,
		[]fakecli.Rule{{AnyArgs: true, Delay: 5000}},
		fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision"}); err != nil {
		t.Fatal(err)
	}
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "working"}}
	clock.stopAfter = 3
	clock.stop = errors.New("stop the watch loop")
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env, Timeout: 2 * time.Second, Delays: []time.Duration{}}
	sup.Wake.Friction = func(msg string) { friction = append(friction, msg) }

	// Pay the fixture's re-exec cost in setup, outside the timed window:
	// the 1 s bound below measures the watch loop while the hook is in
	// flight, not the hook child's first execution (the warmPrepared
	// principle; the warm-up runs its own fake in its own directory, so
	// the test's call log and rules are untouched).
	warmWakeImage(t, env)
	start := time.Now()
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Error("the watch loop ended before the stop: it stopped looping")
				return
			}
			if !errors.Is(r.(error), clock.stop) {
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	elapsed := time.Since(start)
	// Three ticks while the 5 s hook is still in flight: the loop ran them
	// in under a second, so it never waited for the hook.
	if team.statusCalls != 3 {
		t.Fatalf("status calls = %d, want 3 (the ticks kept running)", team.statusCalls)
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("the loop took %s, want under 1s while the 5s hook is in flight", elapsed)
	}
	// The worker is still in the first hook attempt: the cursor has not
	// advanced past the question.
	if seq, ok := wakeSeqValue(t, jobDir); ok && seq == "2" {
		t.Fatal("the cursor advanced past the question while the hook is still in flight")
	}
	// The terminal wait drains the worker: the attempt times out at 2 s,
	// the cursor advances past the question (the last processed seq is 3,
	// worker_spawned), and the failure is friction, not a failed job.
	sup.stopWake(10 * time.Second)
	if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "3" {
		t.Fatalf("wake.seq = %q ok=%v, want 3 after the drain", seq, ok)
	}
	wantFriction(t, friction, "job: wake hook failed for job-1:2 after 1 attempts (timeout)")
	if st := supState(t, store); st.Status != StatusRunning {
		t.Fatalf("state = %+v, want still running (a failing hook never fails the job)", st)
	}
}

// TestSuperviseWakeCrashRerunsAfterCursorOnly: a supervisor restarted with
// wake.seq behind the log re-runs the hooks for the events after it
// (at-least-once; the idempotency key lets the receiver deduplicate) and
// never re-runs those at or before it. The crash is simulated by the
// cursor losing its last write.
func TestSuperviseWakeCrashRerunsAfterCursorOnly(t *testing.T) {
	store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
		[]fakecli.Rule{{AnyArgs: true}},
		fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
	// The first run's events: accepted (1), question (2), decision (3).
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Note("job-1", EventIn{Tipo: "decision", Escopo: "global", Resumo: "second"}); err != nil {
		t.Fatal(err)
	}
	var friction []string
	sup1 := supSupervisor(store, clock, team, &friction)
	sup1.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env}
	sup1.startWake()
	if n, done, err := sup1.dispatchWakes(); err != nil || done || n != 3 {
		t.Fatalf("first dispatch: n=%d done=%v err=%v, want 3 events queued", n, done, err)
	}
	sup1.stopWake(10 * time.Second)
	if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "3" {
		t.Fatalf("after the drain wake.seq = %q ok=%v, want 3", seq, ok)
	}
	// The crash lost the cursor write of the last event: the cursor (2)
	// is behind the log (3).
	if err := os.WriteFile(filepath.Join(jobDir, "wake.seq"), []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	// After the crash the orchestrator writes more events: note (4),
	// question (5).
	if _, err := store.Note("job-1", EventIn{Tipo: "note", Resumo: "after the crash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "third"}); err != nil {
		t.Fatal(err)
	}

	// The restarted supervisor queues only the events after the cursor:
	// the decision (3) is re-run, the note (4) and worker_spawned (6) are
	// queued without running the hook, and the new question (5) runs it.
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "done", Report: report}}
	var friction2 []string
	sup2 := supSupervisor(store, clock, team, &friction2)
	sup2.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env}
	exit, err := sup2.Run()
	if err != nil || exit != 0 {
		t.Fatalf("second Run: exit=%d err=%v, want 0", exit, err)
	}
	// The question (2) and accepted (1) at or before the cursor are never
	// re-run; the decision (3) after it is (at-least-once); the second run's
	// terminal event (8, after worker_spawned 6 and worker_done 7) wakes too.
	if got := hookSeqs(t, fakeDir); !reflect.DeepEqual(got, []int{2, 3, 3, 5, 8}) {
		t.Fatalf("hook seqs = %v, want [2 3 3 5 8]", got)
	}
	if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "8" {
		t.Fatalf("final wake.seq = %q ok=%v, want 8", seq, ok)
	}
	if len(friction) != 0 || len(friction2) != 0 {
		t.Fatalf("friction = %v %v, want none", friction, friction2)
	}
}

// TestSuperviseWakeFailingHookRetriesThenAdvances: a hook that fails every
// attempt is retried by WakeHook.Run (injected zero delays), then the
// cursor still advances and the job state is unchanged: the outcome
// follows the report, and the hook failure adds no event.
func TestSuperviseWakeFailingHookRetriesThenAdvances(t *testing.T) {
	store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
		[]fakecli.Rule{{AnyArgs: true, Code: 1}},
		fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision"}); err != nil {
		t.Fatal(err)
	}
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env,
		Delays: []time.Duration{0, 0, 0}, Sleep: func(time.Duration) {}}
	sup.Wake.Friction = func(msg string) { friction = append(friction, msg) }

	exit, err := sup.Run()
	if err != nil || exit != 0 {
		t.Fatalf("Run: exit=%d err=%v, want 0 (a failing hook never fails the job)", exit, err)
	}
	// The question (2) and the terminal event (5) each get one attempt and
	// three retries.
	if got := hookSeqs(t, fakeDir); !reflect.DeepEqual(got, []int{2, 2, 2, 2, 5, 5, 5, 5}) {
		t.Fatalf("hook seqs = %v, want [2 2 2 2 5 5 5 5]", got)
	}
	wantMsgs := []string{
		"job: wake hook failed for job-1:2 after 4 attempts (exit 1)",
		"job: wake hook failed for job-1:5 after 4 attempts (exit 1)",
	}
	if !reflect.DeepEqual(friction, wantMsgs) {
		t.Fatalf("friction = %v, want %v", friction, wantMsgs)
	}
	// The cursor advanced past the failed events: the last processed seq
	// is 5 (terminal).
	if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "5" {
		t.Fatalf("wake.seq = %q ok=%v, want 5 (the cursor still advances)", seq, ok)
	}
	// The job state is unchanged: the outcome follows the report, and the
	// hook failure added no event.
	if st := supState(t, store); st.Status != StatusDone {
		t.Fatalf("state = %+v, want done", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "question", "worker_spawned", "worker_done", "terminal", "cleanup"}) {
		t.Fatalf("event types = %v, want no failure event", got)
	}
}

// TestSuperviseWakeStopWakeBounded: stopWake uses a bounded timer, not a
// wall-clock deadline: it returns when the worker drains, or at the
// timeout while the worker is still in flight — it never hangs.
func TestSuperviseWakeStopWakeBounded(t *testing.T) {
	t.Run("drains when the worker finishes in time", func(t *testing.T) {
		store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
			[]fakecli.Rule{{AnyArgs: true}},
			fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
		if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision"}); err != nil {
			t.Fatal(err)
		}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env}
		sup.startWake()
		// Two events so far (accepted + the question): both queue.
		if n, done, err := sup.dispatchWakes(); err != nil || done || n != 2 {
			t.Fatalf("dispatch: n=%d done=%v err=%v, want 2", n, done, err)
		}
		start := time.Now()
		sup.stopWake(2 * time.Second)
		if elapsed := time.Since(start); elapsed >= 2*time.Second {
			t.Fatalf("stopWake took %s, want the drain to finish long before the timeout", elapsed)
		}
		if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "2" {
			t.Fatalf("wake.seq = %q ok=%v, want 2 after the drain", seq, ok)
		}
		if len(wakeHookCalls(t, fakeDir)) != 1 {
			t.Fatalf("hook calls = %d, want 1 (the question)", len(wakeHookCalls(t, fakeDir)))
		}
	})
	t.Run("returns at the timeout while the worker is still in flight", func(t *testing.T) {
		store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
			[]fakecli.Rule{{AnyArgs: true, Delay: 3000}},
			fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
		if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision"}); err != nil {
			t.Fatal(err)
		}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		// One attempt (no retries): the 3 s hook outlasts the 300 ms wait.
		sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env, Delays: []time.Duration{}}
		sup.startWake()
		if n, done, err := sup.dispatchWakes(); err != nil || done || n != 2 {
			t.Fatalf("dispatch: n=%d done=%v err=%v, want 2", n, done, err)
		}
		start := time.Now()
		sup.stopWake(300 * time.Millisecond)
		if elapsed := time.Since(start); elapsed < 250*time.Millisecond || elapsed >= 2*time.Second {
			t.Fatalf("stopWake took %s, want the ~300ms timer bound (the worker needs 3s)", elapsed)
		}
		// The worker is still draining: wait on its completion signal
		// (wakeDone, closed when the worker leaves the queue) with the same
		// 6 s bound the old file poll used — the cursor file is read only
		// after the worker is gone, so the read never races its atomic
		// replace, and the test ends with no surviving goroutine.
		select {
		case <-sup.wakeDone:
		case <-time.After(6 * time.Second):
			t.Fatal("the worker did not drain within 6s")
		}
		if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "2" {
			t.Fatalf("wake.seq = %q ok=%v, want 2 after the drain", seq, ok)
		}
		if len(wakeHookCalls(t, fakeDir)) != 1 {
			t.Fatalf("hook calls = %d, want 1", len(wakeHookCalls(t, fakeDir)))
		}
	})
}

// TestSuperviseWakeDisabled: a nil Wake and an empty Cmd start no worker,
// write no cursor, and leave the loop and the terminal path alone.
func TestSuperviseWakeDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		wake *WakeHook
	}{
		{"nil Wake", nil},
		{"empty Cmd", &WakeHook{Cmd: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, jobDir, clock, team, fakeDir, _ := wakeSupFixture(t,
				[]fakecli.Rule{{AnyArgs: true}}, fakecli.InstallOptions{})
			if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision"}); err != nil {
				t.Fatal(err)
			}
			report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
			team.statusScript = []fakeStatus{{State: "done", Report: report}}
			var friction []string
			sup := supSupervisor(store, clock, team, &friction)
			sup.Wake = tc.wake

			exit, err := sup.Run()
			if err != nil || exit != 0 {
				t.Fatalf("Run: exit=%d err=%v, want 0", exit, err)
			}
			if _, ok := wakeSeqValue(t, jobDir); ok {
				t.Fatal("a disabled hook writes no cursor")
			}
			if len(wakeHookCalls(t, fakeDir)) != 0 {
				t.Fatalf("a disabled hook ran %d times", len(wakeHookCalls(t, fakeDir)))
			}
			if len(friction) != 0 {
				t.Fatalf("friction = %v, want none", friction)
			}
		})
	}
}

// requireNoLeakedGoroutines registers a cleanup, run after every other
// cleanup of the test, that fails the test when the goroutine count has
// not come back to its level at the call within a bound: a dispatch, a
// worker or a hook wait left behind by a failure is reported, never
// masked by the package watchdog.
func requireNoLeakedGoroutines(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				t.Errorf("goroutines = %d after the test, want at most %d (one was left behind)", runtime.NumGoroutine(), before)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// dispatchWakesWithin runs one dispatch on a goroutine and fails the test
// when it has not returned after the bound: a dispatch that blocked on a
// full queue would hold the watch loop. Before failing it releases the
// blocked dispatch with release (which must free queue slots: drain a
// test-owned queue or let a held worker go), repeating it until the
// goroutine returns, so a failure never leaves a goroutine behind; a
// goroutine that still does not return is reported as its own failure and
// stuck is set, so the caller never closes the queue under it.
func dispatchWakesWithin(t *testing.T, sup *Supervisor, release func(), stuck *bool) (int, bool, error) {
	t.Helper()
	type result struct {
		n    int
		done bool
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		n, done, err := sup.dispatchWakes()
		ch <- result{n, done, err}
	}()
	select {
	case r := <-ch:
		return r.n, r.done, r.err
	case <-time.After(20 * time.Second):
	}
	deadline := time.After(20 * time.Second)
	for {
		release()
		select {
		case <-ch:
			t.Fatal("dispatchWakes blocked on a full queue (released after the bound)")
			return 0, false, nil
		case <-deadline:
			*stuck = true
			t.Fatal("dispatchWakes blocked on a full queue and did not return after its release")
			return 0, false, nil
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestSuperviseWakeQueueFullDefersTheRest: a full queue (256 slots) leaves
// the rest of the events for the next tick and never blocks the loop; the
// next tick queues the deferred events in order once slots are free. The
// queue is the supervisor's own field with the production capacity, but
// the test is its only receiver, so the counts are exact: no consumer can
// free a slot while the queue is filled.
func TestSuperviseWakeQueueFullDefersTheRest(t *testing.T) {
	// Catches the mutations "a full queue blocks the send" (the select
	// default removed: the bounded dispatch fails), "a full queue drops the
	// rest" (the high-water mark advanced past an unqueued event: the
	// deferred tick queues nothing) and "the capacity changed".
	if wakeQueueCap != 256 {
		t.Fatalf("wakeQueueCap = %d, want 256", wakeQueueCap)
	}
	requireNoLeakedGoroutines(t)
	store, _, clock, team := supFixture(t)
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "first question"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 255; i++ {
		if _, err := store.Note("job-1", EventIn{Tipo: "note", Resumo: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "last question"}); err != nil {
		t.Fatal(err)
	}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	sup.wakeQueue = make(chan Event, wakeQueueCap)
	stuck := false
	drain := func() {
		for len(sup.wakeQueue) > 0 {
			<-sup.wakeQueue
		}
	}
	// 258 events (accepted + question + 255 notes + question) against the
	// 256 slots: one tick queues exactly the capacity and leaves two.
	if n, done, err := dispatchWakesWithin(t, sup, drain, &stuck); err != nil || done || n != 256 {
		t.Fatalf("first dispatch: n=%d done=%v err=%v, want 256 (the rest waits for the next tick)", n, done, err)
	}
	if n, done, err := dispatchWakesWithin(t, sup, drain, &stuck); err != nil || done || n != 0 {
		t.Fatalf("dispatch on a full queue: n=%d done=%v err=%v, want 0", n, done, err)
	}
	for want := 1; want <= 256; want++ {
		if ev := <-sup.wakeQueue; ev.Seq != want {
			t.Fatalf("queued seq = %d, want %d (in order)", ev.Seq, want)
		}
	}
	if n, done, err := dispatchWakesWithin(t, sup, drain, &stuck); err != nil || done || n != 2 {
		t.Fatalf("deferred dispatch: n=%d done=%v err=%v, want 2", n, done, err)
	}
	for want := 257; want <= 258; want++ {
		if ev := <-sup.wakeQueue; ev.Seq != want {
			t.Fatalf("deferred seq = %d, want %d (in order)", ev.Seq, want)
		}
	}
	if n, done, err := dispatchWakesWithin(t, sup, drain, &stuck); err != nil || done || n != 0 {
		t.Fatalf("dispatch with nothing left: n=%d done=%v err=%v, want 0", n, done, err)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// waitHookEntered waits (bounded) until the fake hook process has logged
// its call for seq: the fake logs the call before it waits on its gate
// file, so the entry proves the worker is inside that hook. A call log
// caught mid-append reads as "not yet".
func waitHookEntered(t *testing.T, fakeDir string, seq int) {
	t.Helper()
	want := strconv.Itoa(seq)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if calls, err := fakecli.ReadCalls(filepath.Join(fakeDir, "wakehook.calls.jsonl")); err == nil {
			for _, call := range calls {
				if call.Env["SOHO_JOB_SEQ"] == want {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wake hook for seq %d was never entered", seq)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSuperviseWakeFullQueueDrainsThroughTheWorker: with the real wake
// worker held inside the first question's hook, a tick fills the queue to
// its 256 slots and leaves the last question for a later tick; once the
// hook is released the worker drains, the next tick queues the deferred
// question, and the hook runs for exactly the two questions, in order,
// with the cursor at the last event. The fake hook waits on a gate file
// and logs its call before waiting, so the test knows the worker is inside
// the hook (and holds no queue slot) without relying on timing.
func TestSuperviseWakeFullQueueDrainsThroughTheWorker(t *testing.T) {
	// Catches the mutations "a full queue blocks the send" (the fill
	// dispatch blocks while the worker is held: the bounded dispatch
	// fails), "a full queue drops the rest" (the deferred question is never
	// queued: the deferred dispatch queues nothing and its hook never runs)
	// and "the capacity changed".
	requireNoLeakedGoroutines(t)
	gate := filepath.Join(t.TempDir(), "release")
	store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
		[]fakecli.Rule{{AnyArgs: true, WaitFile: gate}},
		fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "first question"}); err != nil {
		t.Fatal(err)
	}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	// One attempt longer than the fake's own gate wait, so the held hook
	// is never retried by the hook's timeout.
	sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env, Timeout: 2 * time.Minute}
	sup.startWake()
	stuck, stopped := false, false
	open := func() { _ = os.WriteFile(gate, nil, 0o600) }
	// On any failure, let the held hook go and stop the worker, so the
	// worker goroutine and its hook process never outlive the test.
	t.Cleanup(func() {
		open()
		if !stopped && !stuck {
			sup.stopWake(30 * time.Second)
		}
	})
	// The accepted event and the first question.
	if n, done, err := dispatchWakesWithin(t, sup, open, &stuck); err != nil || done || n != 2 {
		t.Fatalf("first dispatch: n=%d done=%v err=%v, want 2", n, done, err)
	}
	// The worker took both and is inside the first question's hook.
	waitHookEntered(t, fakeDir, 2)
	if got := len(sup.wakeQueue); got != 0 {
		t.Fatalf("queue length = %d with the worker inside the first hook, want 0", got)
	}
	// 256 non-waking notes and the last question: 257 events against the
	// 256 free slots, 259 in all.
	for i := 0; i < 256; i++ {
		if _, err := store.Note("job-1", EventIn{Tipo: "note", Resumo: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "last question"}); err != nil {
		t.Fatal(err)
	}
	if n, done, err := dispatchWakesWithin(t, sup, open, &stuck); err != nil || done || n != 256 {
		t.Fatalf("fill dispatch: n=%d done=%v err=%v, want 256 (the last question waits for the next tick)", n, done, err)
	}
	// Still held and full: the next tick queues nothing and returns.
	if n, done, err := dispatchWakesWithin(t, sup, open, &stuck); err != nil || done || n != 0 {
		t.Fatalf("dispatch on a full queue: n=%d done=%v err=%v, want 0", n, done, err)
	}
	if got := hookSeqs(t, fakeDir); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("hook seqs while held = %v, want [2]", got)
	}
	open()
	// The next tick queues the deferred question once the worker frees a
	// slot. The queue length is safe to read concurrently; the cursor file
	// is replaced at every drained event and a concurrent read would race
	// the replacement on Windows.
	deadline := time.Now().Add(60 * time.Second)
	for len(sup.wakeQueue) > wakeQueueCap-1 {
		if time.Now().After(deadline) {
			t.Fatalf("the worker freed no slot within 60s; queue length = %d", len(sup.wakeQueue))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n, done, err := dispatchWakesWithin(t, sup, open, &stuck); err != nil || done || n != 1 {
		t.Fatalf("deferred dispatch: n=%d done=%v err=%v, want 1", n, done, err)
	}
	stopped = true
	sup.stopWake(30 * time.Second)
	// stopWake's 30 s budget can expire with the worker still draining;
	// assert it finished (wakeDone closed) before reading anything it
	// writes.
	select {
	case <-sup.wakeDone:
	default:
		t.Fatalf("the wake worker did not drain within 30s")
	}
	if seq, ok := wakeSeqValue(t, jobDir); !ok || seq != "259" {
		t.Fatalf("wake.seq = %q ok=%v, want 259 (every event, including the deferred question)", seq, ok)
	}
	if got := hookSeqs(t, fakeDir); !reflect.DeepEqual(got, []int{2, 259}) {
		t.Fatalf("hook seqs = %v, want [2 259] (each question once, in order)", got)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// TestSuperviseWakeAlreadyTerminalDrainsBehindCursor: a supervisor that
// returns before the watch loop (here a restart on a job that already
// ended) still runs the waking events after the cursor, so a crash between
// the terminal transition and the cursor write never drops them.
func TestSuperviseWakeAlreadyTerminalDrainsBehindCursor(t *testing.T) {
	store, jobDir, clock, team, fakeDir, env := wakeSupFixture(t,
		[]fakecli.Rule{{AnyArgs: true}},
		fakecli.InstallOptions{CaptureEnv: wakeEnvKeys})
	if _, err := store.Note("job-1", EventIn{Tipo: "question", Resumo: "needs a decision"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{StatusFinishing, StatusDone} {
		if _, err := store.Transition("job-1", to); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(jobDir, "wake.seq"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	sup.Wake = &WakeHook{Cmd: "wakehook", JobID: "job-1", Env: env}
	exit, err := sup.Run()
	if err != nil || exit != 0 {
		t.Fatalf("Run: exit=%d err=%v, want 0", exit, err)
	}
	if got := hookSeqs(t, fakeDir); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("hook seqs = %v, want [2] (the question after the cursor)", got)
	}
	if team.spawnCalls != 0 || len(team.dispatches) != 0 {
		t.Fatalf("a terminal job spawned or dispatched: %d %v", team.spawnCalls, team.dispatches)
	}
}
