package peer_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// notReadyErr is the structured error herdr agent get reports while the
// pane's `agent start` is still finishing: the pane goes idle about 2 s
// before the start returns, and a get inside that window fails with
// agent_not_ready (the transient code spawn already treats as in-progress).
const notReadyErr = `{"error":{"code":"agent_not_ready","message":"agent w0test:p0a is not an active named agent"}}`

// peerLogResult reads the last peer-messages.tsv row's result field of the
// fixture's workspace state dir.
func peerLogResult(t *testing.T, f *fixture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "state", "ws-test", peer.PeerLogFile))
	if err != nil {
		t.Fatalf("peer log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	fields := strings.Split(lines[len(lines)-1], "\t")
	if len(fields) < 4 {
		t.Fatalf("peer log line=%q", lines[len(lines)-1])
	}
	return fields[3]
}

// countCalls counts the logged herdr calls whose argv starts with sub.
func countCalls(calls []fakecli.Call, sub ...string) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) >= len(sub) && strings.Join(c.Argv[:len(sub)], " ") == strings.Join(sub, " ") {
			n++
		}
	}
	return n
}

// callStartsOf returns when each logged call whose argv is exactly argv
// began running, as the fake recorded it (fakecli.ReadCallStarts), in the
// log's order. It runs after the send returned, so every call's start is
// already written.
func callStartsOf(t *testing.T, f *fixture, argv []string) []time.Time {
	t.Helper()
	log := filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl")
	calls, err := fakecli.ReadCalls(log)
	if err != nil {
		t.Fatal(err)
	}
	starts, err := fakecli.ReadCallStarts(log)
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != len(calls) {
		t.Fatalf("the fake recorded %d starts for %d calls", len(starts), len(calls))
	}
	var out []time.Time
	for i, c := range calls {
		if strings.Join(c.Argv, "\x00") == strings.Join(argv, "\x00") {
			out = append(out, starts[i])
		}
	}
	return out
}

func TestSendAgentNotReady(t *testing.T) {
	t.Run("--now --timeout 5000 delivers once the starting agent idles (two agent_not_ready polls)", func(t *testing.T) { // DJA-377: "agent get returns agent_not_ready twice, then the agent idles: send --now --timeout 5000 delivers, exits 0 and the log records the delivery"
		id := "01020304"
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		prompt := peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stderr: notReadyErr, Code: 1},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stderr: notReadyErr, Code: 1},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 6, Stdout: agentJSON("idle", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "before prompt\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: "after prompt\n"},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		}
		f := newFixture(t, rules)
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "5000", "hello"})
		end := time.Now()
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		// The send waited the two not-ready polls (the default 1 s
		// SOHO_SEND_POLL_MS each) and delivered inside the 5 s --timeout:
		// the deadline plus one poll is the outer bound the send may not pass.
		// The deadline starts when the first get reports agent_not_ready, so
		// the clock starts at that get's own start as the fake recorded it,
		// as in the never-ready case below.
		gets := callStartsOf(t, f, []string{"agent", "get", "w0test:p0a"})
		if len(gets) == 0 {
			t.Fatal("no agent get in the call log")
		}
		elapsed := end.Sub(gets[0])
		t.Logf("gets=%d elapsed=%s", len(gets), elapsed)
		if elapsed > time.Duration(5000+1100)*time.Millisecond {
			t.Fatalf("elapsed=%s", elapsed)
		}
		if got := peerLogResult(t, f); got != "sent" {
			t.Fatalf("log result=%q", got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		// Gets: the initial not-ready, the two polled not-readies, the
		// resolved target, the screen get, the pre-send get and the
		// poll-window proof get; the prompt exactly once.
		if got := countCalls(calls, "agent", "get", "w0test:p0a"); got != 6 {
			t.Fatalf("get calls=%d calls=%+v", got, calls)
		}
		if got := countCalls(calls, "agent", "prompt"); got != 1 {
			t.Fatalf("prompt calls=%d calls=%+v", got, calls)
		}
	})
	t.Run("--now --timeout 1500 with the agent never ready exits 4 with today's message plus the waited time, no prompt", func(t *testing.T) { // DJA-377: "agent get always agent_not_ready: send --now --timeout 1500 exits 4 with the today message plus (still not ready after 1.5s) after at most about 1.5 s plus one poll, no prompt sent"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Stderr: notReadyErr, Code: 1},
		}
		f := newFixture(t, rules)
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1500", "hello"})
		end := time.Now()
		if code != 4 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		// The message keeps today's cause and appends the waited time as the
		// send's --timeout in numberSeconds form: numberSeconds(1500) is
		// "1.5", so the documented rounding here is the exact millisecond
		// value of the timeout, not a second floor or ceiling.
		want := "soho: send: local/w0test:p0a unavailable: agent_not_ready: agent w0test:p0a is not an active named agent (still not ready after 1.5s)\n"
		if stderr != want {
			t.Fatalf("stderr=%q want=%q", stderr, want)
		}
		// The deadline is the --timeout (1.5 s); one poll (the default 1 s
		// SOHO_SEND_POLL_MS) plus spawn overhead is the most the run can
		// overshoot it. The deadline starts when the first get reports
		// agent_not_ready, so the clock starts at that get's own start as
		// the fake recorded it: the send's setup and the opening of that
		// first process are not part of the wait. The last get starts at
		// most the --timeout plus one poll after the first, and the send
		// ends inside the same outer bound as before.
		gets := callStartsOf(t, f, []string{"agent", "get", "w0test:p0a"})
		if len(gets) == 0 {
			t.Fatal("no agent get in the call log")
		}
		retried, elapsed := gets[len(gets)-1].Sub(gets[0]), end.Sub(gets[0])
		t.Logf("gets=%d last_get=%s elapsed=%s", len(gets), retried, elapsed)
		if retried > time.Duration(1500+1000)*time.Millisecond {
			t.Fatalf("the last get started after the --timeout plus one poll: %s", retried)
		}
		if elapsed > time.Duration(1500+1000+1000)*time.Millisecond {
			t.Fatalf("elapsed=%s", elapsed)
		}
		if got := peerLogResult(t, f); got != "error" {
			t.Fatalf("log result=%q", got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		// The initial get plus at most two polls inside the 1.5 s deadline,
		// and never a prompt.
		if got := countCalls(calls, "agent", "get", "w0test:p0a"); got < 1 || got > 3 {
			t.Fatalf("get calls=%d calls=%+v", got, calls)
		}
		if got := countCalls(calls, "agent", "prompt"); got != 0 {
			t.Fatalf("prompt calls=%d calls=%+v", got, calls)
		}
	})
	t.Run("agent_not_found still exits 4 at once, with no extra poll", func(t *testing.T) { // DJA-377: "agent_not_found keeps exiting at once with exit 4, no extra poll"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "soho-s2"}, Stderr: `{"error":{"code":"agent_not_found","message":"no such agent"}}`, Code: 1},
		}
		f := newFixture(t, rules)
		code, _, stderr := f.run([]string{"send", "soho-s2", "--timeout", "1500", "hello"})
		if code != 4 || !strings.Contains(stderr, "send: no agent in soho-s2") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		// One herdr call at all: the initial get. No poll, and no hint
		// listing (the target is a name, not a <workspace>:<pane> reference,
		// so noAgentHint lists nothing).
		if len(calls) != 1 {
			t.Fatalf("calls=%+v", calls)
		}
	})
	t.Run("remote target: the same agent_not_ready wait applies, every get keeps --machine remote", func(t *testing.T) { // DJA-377: "destino remoto <máquina>/<pane>: a mesma regra vale, porque o agentGet já recebe a máquina"
		rules := []fakecli.Rule{
			{Argv: []string{"--machine", "remote", "agent", "get", "w0test:p0a"}, Stderr: notReadyErr, Code: 1},
		}
		f := newFixture(t, rules)
		code, _, stderr := f.run([]string{"send", "remote/w0test:p0a", "--now", "--timeout", "1500", "hello"})
		end := time.Now()
		if code != 4 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		want := "soho: send: remote/w0test:p0a unavailable: agent_not_ready: agent w0test:p0a is not an active named agent (still not ready after 1.5s)\n"
		if stderr != want {
			t.Fatalf("stderr=%q want=%q", stderr, want)
		}
		// The same bound as the local case, from the first remote get's own
		// start as the fake recorded it: the deadline starts when that get
		// reports agent_not_ready.
		gets := callStartsOf(t, f, []string{"--machine", "remote", "agent", "get", "w0test:p0a"})
		if len(gets) == 0 {
			t.Fatal("no agent get in the call log")
		}
		elapsed := end.Sub(gets[0])
		t.Logf("gets=%d elapsed=%s", len(gets), elapsed)
		if elapsed > time.Duration(1500+1000+1000)*time.Millisecond {
			t.Fatalf("elapsed=%s", elapsed)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		// Every call goes to the remote machine, --machine first; the initial
		// get plus at most two polls; never a prompt.
		for _, c := range calls {
			if len(c.Argv) < 2 || c.Argv[0] != "--machine" || c.Argv[1] != "remote" {
				t.Fatalf("a call without --machine remote first: %+v", calls)
			}
		}
		if got := countCalls(calls, "--machine", "remote", "agent", "get", "w0test:p0a"); got < 1 || got > 3 {
			t.Fatalf("get calls=%d calls=%+v", got, calls)
		}
		if got := countCalls(calls, "agent", "prompt"); got != 0 {
			t.Fatalf("prompt calls=%d calls=%+v", got, calls)
		}
	})
	t.Run("without --now the agent_not_ready wait is charged to the busy wait's budget", func(t *testing.T) { // DJA-377: "without --now a target that is agent_not_ready for a while and then working falls into today's busy wait and the total time does not pass --timeout"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stderr: notReadyErr, Code: 1},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("working", "1")},
			{Argv: []string{"agent", "wait", "w0test:p0a", "--until", "idle", "--until", "done", "--timeout"}, ArgvPrefix: true,
				Stderr: `{"error":{"code":"timeout","message":"timed out"}}`, Code: 1},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("working", "1")},
		}
		f := newFixture(t, rules)
		start := time.Now()
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--timeout", "5000", "hello"})
		elapsed := time.Since(start)
		if code != 17 || !strings.Contains(stderr, "is still working after 5s") || !strings.Contains(stderr, "nothing was sent") {
			t.Fatalf("code=%d stderr=%q elapsed=%s", code, stderr, elapsed)
		}
		// The not-ready poll (the default 1 s SOHO_SEND_POLL_MS) plus the
		// busy wait must fit the --timeout: the wait gets what the poll left,
		// the two waits never add up.
		if elapsed > time.Duration(5000+1000+500)*time.Millisecond {
			t.Fatalf("elapsed=%s", elapsed)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		budget, foundWait := 0, false
		for _, c := range calls {
			if len(c.Argv) == 9 && strings.Join(c.Argv[:8], " ") == "agent wait w0test:p0a --until idle --until done --timeout" {
				foundWait = true
				budget, _ = strconv.Atoi(c.Argv[8])
			}
		}
		if !foundWait {
			t.Fatalf("no wait call: %+v", calls)
		}
		// The wait's --timeout is the 5 s send timeout minus the time the
		// not-ready poll spent: strictly below the full timeout, and
		// positive so the herdr call still gets a timeout.
		if budget <= 0 || budget >= 5000 {
			t.Fatalf("wait budget=%d calls=%+v", budget, calls)
		}
		if got := countCalls(calls, "agent", "prompt"); got != 0 {
			t.Fatalf("prompt calls=%d calls=%+v", got, calls)
		}
		if got := peerLogResult(t, f); got != "busy" {
			t.Fatalf("log result=%q", got)
		}
	})
}

// DJA-377 review: every agent_not_ready retry gets only what is left of the
// send's --timeout. A retry whose agent get hangs is cut at the deadline and
// the send exits 4 as still not ready, instead of waiting out the get's own
// 30 s timeout.
func TestSendAgentNotReadySlowRetryKeepsTheDeadline(t *testing.T) {
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stderr: notReadyErr, Code: 1},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Delay: 3000, Stderr: notReadyErr, Code: 1},
	})
	f.env["SOHO_SEND_POLL_MS"] = "10"
	start := time.Now()
	code, _, stderr := f.run([]string{"send", "worker", "--now", "--timeout", "200ms", "hello"})
	elapsed := time.Since(start)
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if code != 4 || !strings.Contains(stderr, "(still not ready after 0.2s)") || countCalls(calls, "agent", "prompt") != 0 {
		t.Fatalf("code=%d stderr=%q calls=%v; want exit 4 still not ready, no prompt", code, stderr, calls)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("the slow retry held the send for %v, far past its 200ms --timeout", elapsed)
	}
}

// DJA-377 review: the busy wait after an agent_not_ready wait gets only the
// real time left before the shared deadline, never a floor that revives an
// exhausted budget; nothing left means no new wait.
func TestSendBusyBudgetAfterNotReady(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name     string
		notReady bool
		left     time.Duration
		wantMS   int
		wantWait bool
	}{
		{"no not-ready wait: the full --timeout", false, 0, 5000, true},
		{"30 ms left stays 30 ms", true, 30 * time.Millisecond, 30, true},
		{"1 ms left stays 1 ms", true, time.Millisecond, 1, true},
		{"nothing left: no new wait", true, 0, 0, false},
		{"past the deadline: no new wait", true, -50 * time.Millisecond, -50, false},
	}
	for _, tc := range cases {
		gotMS, gotWait := peer.SendBusyBudgetMS(5000, tc.notReady, now.Add(tc.left), now)
		if gotWait != tc.wantWait || (tc.wantWait && gotMS != tc.wantMS) {
			t.Fatalf("%s: got (%d, %v), want (%d, %v)", tc.name, gotMS, gotWait, tc.wantMS, tc.wantWait)
		}
	}
}

// DJA-377 follow-up: the retry's get is bounded by what is left of the
// send's --timeout, cut to whole milliseconds, so it can time out a fraction
// of a millisecond before the deadline. A timed-out retry still counts as
// out of budget; any other failure counts only once the deadline passed.
func TestNotReadyRetryOutOfBudget(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name     string
		ok       bool
		code     string
		timedOut bool
		left     time.Duration
		want     bool
	}{
		{"timed-out get just before the deadline", false, "", true, 500 * time.Microsecond, true},
		{"timed-out get past the deadline", false, "", true, -time.Millisecond, true},
		{"other failure before the deadline", false, "", false, 500 * time.Microsecond, false},
		{"other failure past the deadline", false, "", false, 0, true},
		{"still not ready", false, "agent_not_ready", false, 0, false},
		{"resolved", true, "", false, 0, false},
	}
	for _, tc := range cases {
		if got := peer.NotReadyRetryOutOfBudget(tc.ok, tc.code, tc.timedOut, now.Add(tc.left), now); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The timed-out flag reaches the resolved target: a get cut by its own
// timeout reports TimedOut, a get that answers does not. The answering
// get has no delay; its 20 s budget only covers starting the fake on a
// loaded Windows runner, where 2 s was not always enough.
func TestResolveTargetWithinReportsTimedOut(t *testing.T) {
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "slow"}, Delay: 3000, Stderr: notReadyErr, Code: 1},
		{Argv: []string{"agent", "get", "fast"}, Stderr: notReadyErr, Code: 1},
	})
	if !peer.ResolveTargetWithinTimedOut("slow", f.env, 100) {
		t.Fatal("a get past its 100 ms timeout did not report TimedOut")
	}
	if peer.ResolveTargetWithinTimedOut("fast", f.env, 20000) {
		t.Fatal("a get that answered reported TimedOut")
	}
}
