package peer_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Round 2 of the send arrival proof: the three review findings of R-R12C
// become tests. The stalled path's moved-sequence shortcut must not report
// sent when the read screen still holds the marker in the composer; the
// busy transcript wait must consult the visible queue before entering the
// wait and on every lap of it, independent of the transcript count; and the
// wait applies to any window, a sub-second one included, whenever the
// transcript proof is armed and the target keeps working.

// The idle codex screen before the send: the empty composer, no peer
// message in sight.
const r2PreScreen = "› Ask Codex to do anything\n\n  GPT-6.1-Sol high · context 20% · Run herdr agents\n  New activity · Earlier messages available.  enter/esc latest · ? shortcuts\n"

const r2WorkingScreen = "✻ Cranking out changes…\n"

func TestSendArrivalBusyR2ComposerSequence(t *testing.T) {
	// P1: inside stalledTaken the state_change_seq shortcut returned sent
	// checking only the dialog. A read that still holds the marker in the
	// composer, with the sequence moved since the idle baseline, must not be
	// sent: the message is still in the box, and the stalled path presses no
	// key and never resends the text.
	const id = "29a25864"
	newPrompt := func(t *testing.T) string {
		t.Helper()
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{0x29, 0xa2, 0x58, 0x64})
		t.Cleanup(func() { rand.Reader = oldReader })
		return remotePeerHeader(t, id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	promptArgv := func(prompt string) []string {
		return append([]string{"--machine", "windows", "agent", "prompt", "w0test:p0a", prompt},
			"--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000")
	}
	remoteGets := func(call int, status, seq string) fakecli.Rule {
		return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: call, Stdout: agentJSONKind("codex", status, seq)}
	}
	remoteVisible := func(call int, screen string) fakecli.Rule {
		return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: call, Stdout: screen}
	}

	prompt := newPrompt(t)
	// The one-shot proof read: the marker sits in the composer while the
	// sequence has moved from the idle baseline.
	boxScreen := strings.Replace(r2PreScreen, "› Ask Codex to do anything", "› [soho:peer] #"+id+" Message from another agent", 1)
	rules := []fakecli.Rule{
		remoteGets(1, "idle", "1"),
		remoteVisible(1, r2PreScreen),
		remoteGets(2, "idle", "1"),
		remoteGets(3, "idle", "1"),
		{Argv: promptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
		// The stalled read of the visible screen: the empty composer, no
		// marker in the box — the Enter branch is not taken.
		remoteVisible(2, r2PreScreen),
		// The one-shot proof: the sequence moved from the idle baseline.
		remoteGets(4, "working", "2"),
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: boxScreen},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
	if code != 15 || out != "" {
		t.Fatalf("must refuse sent with the marker still in the composer: code=%d out=%q stderr=%q", code, out, stderr)
	}
	want := "soho: send: windows/w0test:p0a did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 0.001s window; read its pane (herdr --machine windows agent read w0test:p0a --source visible) for #29a25864 or a reply before sending again; screen saved to " + filepath.Join(f.dir, "state", "ws-test", "wait", "send-29a25864.screen") + "\n"
	if stderr != want {
		t.Fatalf("the refusal keeps today's stalled 15: %q", stderr)
	}
	if enters := remoteEnters(t, f); enters != 0 {
		t.Fatalf("the marker out of the box presses nothing: %d enters", enters)
	}
	if prompts := remotePrompts(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

func TestSendArrivalBusyR2VisibleQueueNoGrowth(t *testing.T) {
	// P2: the visible queue was consulted only after a transcript count
	// growth. With the queue line on screen and the transcript untouched,
	// the send must report queued right after the window — well before the
	// command --timeout — with no key.
	// D23: the settle bound measures the elapsed time against the --timeout,
	// widened far past the 1s window (30 s): the settled send takes the
	// window plus a few fake calls, while a wait that ran to the --timeout
	// would take the full 30 s — the 2 s bound flaked on wall time when the
	// whole Go suite ran in parallel (2.7 s on a loaded host), and the read
	// sequence alone cannot separate the window laps from the wait's laps,
	// so the margin is the deadline distance, measured against the --timeout.
	const id = "01020304"
	const cwd = "/tmp/claude-work"
	newPrompt := func(t *testing.T) string {
		t.Helper()
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		return peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	promptArgv := func(prompt string) []string {
		return []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}
	}

	prompt := newPrompt(t)
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	// The transcript holds only the earlier conversation: no new user line
	// lands during the busy turn.
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	// After the prompt the message sits in claude's open queue: the marker
	// and the queue line are on screen.
	queuedScreen := r2WorkingScreen + prompt + "\nPress up to edit queued messages\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
		// The pre-send screen is the working turn without the queue.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: r2WorkingScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: queuedScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
		{Argv: promptArgv(prompt)},
	}
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
	started := time.Now()
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "30000", "hello"})
	if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") {
		t.Fatalf("a visible queue line is queued, not sent: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if want := "send: local/w0test:p0a is busy; waiting for its transcript to show the message (up to 30s)\n"; stderr != want {
		t.Fatalf("the busy warning goes to stderr exactly once while the transcript still has no line: %q", stderr)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("the visible queue settles after the 1s window, not after the 30s --timeout, took %s", elapsed)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no key may go to a busy claude with an open queue: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the busy path never resends the text: %d prompts", prompts)
	}
}

func TestSendArrivalBusyR2QueueDuringWait(t *testing.T) {
	// P2, every lap: the queue line that appears while the transcript wait is
	// running — the wait entry saw no queue — is due on the lap that shows
	// it, independent of the transcript count, with no key and well before
	// the command --timeout.
	const id = "01020304"
	const cwd = "/tmp/claude-work"
	newPrompt := func(t *testing.T) string {
		t.Helper()
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		return peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	promptArgv := func(prompt string) []string {
		return []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}
	}

	prompt := newPrompt(t)
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	// The transcript holds only the earlier conversation: no new user line
	// lands during the busy turn.
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	// The queue line appears after the wait entry read: the pre-screen, the
	// window lap and the entry read all show the plain working turn, and the
	// reads from the wait's first lap on show the queue.
	queuedScreen := r2WorkingScreen + prompt + "\nPress up to edit queued messages\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: r2WorkingScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: r2WorkingScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: r2WorkingScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: queuedScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
		{Argv: promptArgv(prompt)},
	}
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	started := time.Now()
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "3000", "hello"})
	if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") {
		t.Fatalf("a queue line that appears during the wait is queued: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if want := "send: local/w0test:p0a is busy; waiting for its transcript to show the message (up to 3s)\n"; stderr != want {
		t.Fatalf("the busy warning goes to stderr exactly once: %q", stderr)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the queue line shown on a lap is due on that lap, not after the 3s --timeout, took %s", elapsed)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no key may go to a busy claude with an open queue: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the busy path never resends the text: %d prompts", prompts)
	}
}

// appendTranscriptLineOnVisibleRead appends the transcript line on a fake
// event, not a timer: the goroutine polls the fake herdr's call log and
// appends once the nth counted `agent read ... --source visible` call after
// the prompt call is logged, where counted excludes the readScreen
// agent_not_idle fallback (internal/peer/peer.go): a visible read the log
// shows right after a `recent` read is that fallback, which the proof
// window's laps do, and it does not count. The window's laps otherwise log
// no visible read (the target is working, so the moved-sequence shortcut
// never fires; the count cannot grow before the line lands), so the first
// counted read after the prompt is the busy wait entry's queue check and
// the second is the wait's first lap. The line therefore lands inside the
// busy wait — after the entry's count check, which warns — and is seen by
// the first or the second lap, independent of machine load. The old 800 ms
// timer was anchored on the prompt call's log write while the 500 ms window
// is anchored on the fake prompt process's exit, so on a loaded host the
// process's own lifetime could push the line inside the window and the
// send proved in-window without the busy warning.
func appendTranscriptLineOnVisibleRead(t *testing.T, callsLog, transcriptPath, line string, n int) <-chan bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		ok := false
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if calls, err := fakecli.ReadCalls(callsLog); err == nil {
				if nVisibleReadsAfterPrompt(calls, n) {
					f, werr := os.OpenFile(transcriptPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if werr == nil {
						_, werr = f.WriteString(line + "\n")
						_ = f.Close()
						ok = werr == nil
					}
				}
			}
			if ok {
				break
			}
			time.Sleep(time.Millisecond)
		}
		done <- ok
	}()
	return done
}

// nVisibleReadsAfterPrompt reports whether at least n counted `agent read
// … --source visible` calls are logged after the first `agent prompt`
// call: a visible read whose previous logged call is a `recent` read is
// readScreen's agent_not_idle fallback and does not count.
func nVisibleReadsAfterPrompt(calls []fakecli.Call, n int) bool {
	prompt := -1
	for i, c := range calls {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "prompt" {
			prompt = i
			break
		}
	}
	if prompt < 0 {
		return false
	}
	reads := 0
	for i := prompt + 1; i < len(calls); i++ {
		if !isVisibleRead(calls[i].Argv) {
			continue
		}
		if i > 0 && isRecentRead(calls[i-1].Argv) {
			continue
		}
		reads++
	}
	return reads >= n
}

func isRecentRead(argv []string) bool {
	if len(argv) < 2 || argv[0] != "agent" || argv[1] != "read" {
		return false
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--source" && strings.HasPrefix(argv[i+1], "recent") {
			return true
		}
	}
	return false
}

func isVisibleRead(argv []string) bool {
	if len(argv) < 2 || argv[0] != "agent" || argv[1] != "read" {
		return false
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--source" && argv[i+1] == "visible" {
			return true
		}
	}
	return false
}

func TestSendArrivalBusyR2SubsecondWindow(t *testing.T) {
	// P2: the transcript wait applied only to windows of at least a second.
	// A sub-second window is a valid configuration: with the transcript
	// proof armed and the target still working, the user line that lands
	// after the 500 ms window must prove the arrival before the deadline.
	// The line is written on the fake's second visible read after the
	// prompt (the wait's first lap), not on a timer: the old 800 ms timer
	// raced the fake prompt process's lifetime on a loaded host and could
	// land inside the window, where the send proves without the busy
	// warning (the review's "the busy warning goes to stderr exactly once:
	// \"\"" failure).
	const id = "01020304"
	const cwd = "/tmp/claude-work"
	// The transcript line holds the real peer end line (with the # marker),
	// the way Claude Code records the taken message.
	userLine := `{"type":"user","message":{"content":"` + peer.PeerEndLine(id) + `"}}`
	newPrompt := func(t *testing.T) string {
		t.Helper()
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		return peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	promptArgv := func(prompt string) []string {
		return []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}
	}

	prompt := newPrompt(t)
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: r2WorkingScreen},
		// A working claude's alternate-screen history cannot be captured:
		// the recent read fails and falls back to the visible screen.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
		{Argv: promptArgv(prompt)},
	}
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "500", "50"
	done := appendTranscriptLineOnVisibleRead(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, userLine, 2)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "4000", "hello"})
	if code != 0 || out != "sent to local/w0test:p0a\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if want := "send: local/w0test:p0a is busy; waiting for its transcript to show the message (up to 4s)\n"; stderr != want {
		t.Fatalf("the busy warning goes to stderr exactly once: %q", stderr)
	}
	if !<-done {
		t.Fatal("the transcript line was not appended after the wait's first lap")
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no key is sent in the extended wait: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the busy path never resends the text: %d prompts", prompts)
	}
}
