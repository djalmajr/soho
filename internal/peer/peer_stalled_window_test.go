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
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The D15 defect, real on Windows: a send to a codex in done exited 15
// "stalled" while the message had already arrived — the codex took more than
// the prompt wait to start working and to redraw the screen, and the stalled
// path checked the screen exactly once, at the moment the prompt call gave
// up. The check now repeats on every poll until the end of the proof window:
// the first taken read is sent, and the window's end without proof keeps the
// 15, which also saves the last visible screen read to
// <state>/wait/send-<id>.screen so the next occurrence is diagnosable. No
// key is pressed and the text is never resent.

const (
	d15WindowID      = "01020304"
	d15WindowStale   = "› Ask Codex to do anything\n\n  GPT-6.1-Sol high · context 20% · Run herdr agents\n  New activity · Earlier messages available.  enter/esc latest · ? shortcuts\n"
	d15WindowOldHist = "old turn output\n"
)

// d15WindowTakenScreen models the codex screen after the late redraw: the
// taken message drawn in the history above the empty composer, a bare "›".
func d15WindowTakenScreen() string {
	return "old turn output\n" +
		"› " + peer.PeerOpenTag(d15WindowID) + "\n\n" +
		"> hello\n" +
		peer.PeerEndLine(d15WindowID) + "\n" +
		"›\n"
}

// d15WindowPrompt builds the exact prompt body the fake herdr must receive:
// the remote header for the fixture (the hostname is pinned) and the body.
func d15WindowPrompt(t *testing.T) string {
	t.Helper()
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	t.Cleanup(func() { rand.Reader = oldReader })
	return remotePeerHeader(t, d15WindowID) + "\n\n> hello\n" + peer.PeerEndLine(d15WindowID)
}

// d15WindowPromptArgv is the full `agent prompt` argv on the remote machine.
func d15WindowPromptArgv(prompt string) []string {
	return append([]string{"--machine", "windows", "agent", "prompt", "w0test:p0a", prompt},
		"--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000")
}

// d15WindowRecent is a `agent read ... --source recent-unwrapped` rule on the
// remote machine, matched on the prefix (the --lines value is not pinned).
func d15WindowRecent(call int, out string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: call, Stdout: out}
}

// d15WindowGet and d15WindowVisible are the remote `agent get` and `agent
// read ... --source visible` rules the window tests script (the r2 and d15
// files keep their own function-local variants).
func d15WindowGet(call int, status, seq string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: call, Stdout: agentJSONKind("codex", status, seq)}
}

func d15WindowVisible(call int, screen string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: call, Stdout: screen}
}

// d15WindowVisibleAfterPrompt counts the `agent read ... --source visible`
// calls after the first `agent prompt` call: the box detection plus the
// repeated stalled checks of the window.
func d15WindowVisibleAfterPrompt(t *testing.T, f *fixture) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := -1
	for i, c := range calls {
		for j := 0; j+1 < len(c.Argv); j++ {
			if c.Argv[j] == "agent" && c.Argv[j+1] == "prompt" {
				prompt = i
				break
			}
		}
		if prompt >= 0 {
			break
		}
	}
	if prompt < 0 {
		t.Fatal("no agent prompt call")
	}
	reads := 0
	for _, c := range calls[prompt+1:] {
		for j := 0; j+1 < len(c.Argv); j++ {
			if c.Argv[j] == "--source" && c.Argv[j+1] == "visible" {
				reads++
				break
			}
		}
	}
	return reads
}

func TestSendStalledWindowD15(t *testing.T) {
	// The D15 case: agent prompt reports agent_prompt_stalled and the first
	// screen reads still show the stale state, without the #id. After two
	// stale checks the codex has started working (state_change_seq moved from
	// the done baseline) and the screen shows the message in the history
	// above the empty composer: the third check proves the arrival — sent,
	// exit 0, no key.
	prompt := d15WindowPrompt(t)
	taken := d15WindowTakenScreen()
	rules := []fakecli.Rule{
		d15WindowGet(1, "done", "1"),
		d15WindowVisible(1, d15WindowStale),
		d15WindowGet(2, "done", "1"),
		d15WindowGet(3, "done", "1"),
		{Argv: d15WindowPromptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
		// The stalled read of the visible screen: the stale state, no marker
		// in the composer — the Enter branch is not taken.
		d15WindowVisible(2, d15WindowStale),
		// First window check: the screen is still stale and the state did not
		// move, so the recent history (without the marker) is consulted.
		d15WindowVisible(3, d15WindowStale),
		d15WindowGet(4, "done", "1"),
		d15WindowRecent(1, d15WindowOldHist),
		// Second window check: still stale, still no movement.
		d15WindowVisible(4, d15WindowStale),
		d15WindowGet(5, "done", "1"),
		d15WindowRecent(2, d15WindowOldHist),
		// Third window check: the late redraw — the message in the history
		// above the empty composer and the sequence moved from the done
		// baseline prove the arrival.
		d15WindowVisible(5, taken),
		d15WindowGet(6, "working", "2"),
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "5000", "100"
	code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
	if code != 0 || out != "sent to "+d15Pane+"\n" || stderr != "" {
		t.Fatalf("the late redraw must prove the arrival: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if enters := remoteEnters(t, f); enters != 0 {
		t.Fatalf("the stalled window presses no key: %d enters", enters)
	}
	if prompts := remotePrompts(t, f); prompts != 1 {
		t.Fatalf("the stalled window never resends the text: %d prompts", prompts)
	}
	// The box detection plus the three window checks: the proof only came
	// after the screen redrew, which the single check of the old code missed.
	if reads := d15WindowVisibleAfterPrompt(t, f); reads != 4 {
		t.Fatalf("the check repeats on every poll until the redraw: %d visible reads after the prompt", reads)
	}
}

func TestSendStalledWindowUnproved(t *testing.T) {
	// The window ends without proof: the screen stays stale and the state
	// never moves. The 15 keeps today's cause and now also saves the last
	// visible screen read, cited in the message — and the screen content
	// never reaches the peer log.
	prompt := d15WindowPrompt(t)
	rules := []fakecli.Rule{
		d15WindowGet(1, "done", "1"),
		d15WindowVisible(1, d15WindowStale),
		d15WindowGet(2, "done", "1"),
		d15WindowGet(3, "done", "1"),
		{Argv: d15WindowPromptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
		d15WindowVisible(2, d15WindowStale), // box detection: the stale state
		// First window check: stale screen, unmoved state, no marker.
		d15WindowVisible(3, d15WindowStale),
		d15WindowGet(4, "done", "1"),
		d15WindowRecent(1, d15WindowOldHist),
		// Catch-alls: a second check on a fast machine and any later read.
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: d15WindowStale},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "done", "1")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: d15WindowOldHist},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	screenPath := filepath.Join(f.dir, "state", "ws-test", "wait", "send-"+d15WindowID+".screen")
	code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
	want := "soho: send: " + d15Pane + " did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 0.001s window; read its pane (herdr --machine windows agent read w0test:p0a --source visible) for #01020304 or a reply before sending again; screen saved to " + screenPath + "\n"
	if code != 15 || out != "" || stderr != want {
		t.Fatalf("the unproved window keeps the 15 and cites the saved screen: code=%d out=%q stderr=%q", code, out, stderr)
	}
	b, err := os.ReadFile(screenPath)
	if err != nil {
		t.Fatalf("the unproved stalled 15 saves the last visible screen: %v", err)
	}
	if string(b) != d15WindowStale {
		t.Fatalf("the saved screen is the last visible read:\n%q", string(b))
	}
	if enters := remoteEnters(t, f); enters != 0 {
		t.Fatalf("the unproved window presses no key: %d enters", enters)
	}
	if prompts := remotePrompts(t, f); prompts != 1 {
		t.Fatalf("the unproved window never resends the text: %d prompts", prompts)
	}
	// Privacy: the screen goes only to the local file, never to the peer log.
	logData, err := os.ReadFile(filepath.Join(f.dir, "state", "ws-test", "peer-messages.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), "GPT-6.1-Sol") {
		t.Fatalf("the screen content must not reach the peer log:\n%s", string(logData))
	}
}

func TestSendStalledWindowBoxBranchSavesScreen(t *testing.T) {
	// The id in the box is untouched by the change: one Enter, then the
	// proof window. When the window ends unproved, the 15 of today now also
	// saves the last visible screen read (the box screen) and cites it.
	_, prompt := stalledID(t)
	history := "old turn output"
	box := piSendScreen(history, prompt)
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		// The stalled read: the message sits in the box, so the Enter goes.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: box},
		{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
		// The proof window: the state stays idle with the same sequence and
		// the message still sits in the box, so the window fails.
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Stdout: prompt + "\n"},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: box},
		// Catch-alls for a second window iteration on a fast machine.
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: box},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	screenPath := filepath.Join(f.dir, "state", "ws-test", "wait", "send-01020304.screen")
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	want := "soho: send: local/w0test:p0a did not confirm taking the message: the last read showed it in its input box after one Enter; delivery is uncertain; read its pane for #01020304 or a reply before sending again; screen saved to " + screenPath + "\n"
	if code != 15 || out != "" || stderr != want {
		t.Fatalf("the box branch keeps today's 15 and cites the saved screen: code=%d out=%q stderr=%q", code, out, stderr)
	}
	b, err := os.ReadFile(screenPath)
	if err != nil {
		t.Fatalf("the box branch 15 saves the last visible screen: %v", err)
	}
	if string(b) != box {
		t.Fatalf("the saved screen is the box screen:\n%q", string(b))
	}
	if enters := countSendKeyEnters(t, f); enters != 1 {
		t.Fatalf("the box branch presses exactly one Enter: %d", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the box branch never resends the text: %d prompts", prompts)
	}
}

func TestSendStalledWindowSaveImpossible(t *testing.T) {
	// The save is best-effort: when <state>/wait is not a directory it cannot
	// be created and the temp file cannot be written, so the 15 keeps
	// today's message exactly, without the screen note.
	_, prompt := stalledID(t)
	history := "old turn output"
	empty := piSendScreen(history, "")
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: empty},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		// The stalled read: the box is empty, so no Enter and the window.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: empty},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: empty},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Stdout: history + "\n"},
		// Catch-alls for a second window check on a fast machine.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: empty},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: history + "\n"},
	}
	f := newFixture(t, rules)
	// Make the save impossible: the wait dir is a regular file.
	waitPath := filepath.Join(f.dir, "state", "ws-test", "wait")
	if err := os.MkdirAll(filepath.Dir(waitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(waitPath, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	want := "soho: send: local/w0test:p0a did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 0.001s window; read its pane for #01020304 or a reply before sending again\n"
	if code != 15 || stderr != want {
		t.Fatalf("an impossible save keeps today's message, nothing else: code=%d stderr=%q", code, stderr)
	}
	// No screen file and no half-written temp: the state dir only holds the
	// peer log and the (still a file) wait marker.
	entries, err := os.ReadDir(filepath.Dir(waitPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "peer-messages.tsv" && e.Name() != "wait" {
			t.Fatalf("the failed save leaves nothing behind: %s", e.Name())
		}
	}
	if info, err := os.Stat(waitPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the wait marker is untouched (code=%d): %v", code, err)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no Enter without the marker in the box: %d", enters)
	}
}

// stalledWindowSetupRules is the stalled setup: a codex in done at sequence
// 1, the pre-send screen still stale, and the prompt that stalls. Call 2 of
// the visible read is the box detection (stale: no marker).
func stalledWindowSetupRules(t *testing.T, tail []fakecli.Rule) []fakecli.Rule {
	prompt := d15WindowPrompt(t)
	rules := []fakecli.Rule{
		d15WindowGet(1, "done", "1"),
		d15WindowVisible(1, d15WindowStale),
		d15WindowGet(2, "done", "1"),
		d15WindowGet(3, "done", "1"),
		{Argv: d15WindowPromptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
		d15WindowVisible(2, d15WindowStale),
	}
	return append(rules, tail...)
}

// stalledWindowCatchAll is the catch-all for every read after the pinned
// ones: the given screen, the unmoved state, and a recent history without
// the marker.
func stalledWindowCatchAll(screen string) []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: screen},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "done", "1")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "old history\n"},
	}
}

func TestReviewS74Deadline(t *testing.T) {
	// A slow read near the end of the window must not push it far past its
	// deadline: the second check's 16s visible read runs against the default
	// 15s window, and the send must still finish within one window plus the
	// one-second floor a read gets to finish (the probe's 16s bound).
	//
	// The bound sits at the window phase: the send's preparation calls
	// (the state and screen reads before the prompt, and the prompt) are not
	// part of the window, so the clock starts at the box detection (the
	// second visible read), as the fake recorded that call's own start. The
	// window opens only when that read returns, so the phase timed here
	// holds the whole window plus that read, and an uncapped 16s read,
	// which starts after it, ends past the 16s bound.
	slow := d15WindowVisible(4, d15WindowStale)
	slow.Delay = 16000
	tail := []fakecli.Rule{d15WindowVisible(3, d15WindowStale), slow}
	tail = append(tail, stalledWindowCatchAll(d15WindowStale)...)
	f := newFixture(t, stalledWindowSetupRules(t, tail))
	start := time.Now()
	code, _, _ := f.run([]string{"send", d15Pane, "hello"})
	end := time.Now()
	box, ok := nthCallStart(f, d15WindowVisibleArgv, 2, start)
	if !ok {
		t.Fatal("the call log holds no box detection read with its start")
	}
	elapsed := end.Sub(box)
	t.Logf("default_window=15s elapsed=%s send=%s code=%d prompts=%d enters=%d", elapsed, end.Sub(start), code, remotePrompts(t, f), remoteEnters(t, f))
	if code != 15 || elapsed > 16*time.Second {
		t.Fatal("stalled proof calls exceeded the one-window time budget")
	}
}

func TestReviewS74LastFallbackScreen(t *testing.T) {
	// When the recent read gets agent_not_idle, readScreen falls back to a
	// visible read: that fallback is a visible screen read too, so the saved
	// screen must be the fallback's, not an earlier read.
	latest := "LATEST_VISIBLE_SENTINEL\n›\n"
	tail := []fakecli.Rule{
		d15WindowVisible(3, d15WindowStale),
		d15WindowGet(4, "done", "1"),
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
		d15WindowVisible(4, latest),
	}
	f := newFixture(t, stalledWindowSetupRules(t, tail))
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	code, _, stderr := f.run([]string{"send", d15Pane, "hello"})
	p := filepath.Join(f.dir, "state", "ws-test", "wait", "send-01020304.screen")
	raw, err := os.ReadFile(p)
	t.Logf("code=%d saved_latest=%t saved=%q stderr=%q", code, string(raw) == latest, string(raw), stderr)
	if code != 15 || err != nil || string(raw) != latest {
		t.Fatal("saved screen omitted the last successful visible fallback read")
	}
}

func TestSaveStalledScreenNowrite(t *testing.T) {
	// The CLI already refuses the send with SOHO_NOWRITE=1 (exit 2)
	// before the stalled 15, but the helper carries its own guard: under
	// NOWRITE it must write nothing, so no screen can ever leak from the
	// send flow. The control without the flag proves the helper writes.
	screen := "NOWRITE_HELPER_SCREEN\n›\n"
	dir := t.TempDir()
	waitDir := filepath.Join(dir, "wait")
	env := platform.Env{}
	path := peer.SaveStalledScreen(dir, "01020304", screen, env)
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != screen {
		t.Fatalf("helper without NOWRITE did not write the screen: %v %q", err, raw)
	}
	env["SOHO_NOWRITE"] = "1"
	if got := peer.SaveStalledScreen(dir, "01020304", screen, env); got != "" {
		t.Fatalf("helper under NOWRITE reported a path: %s", got)
	}
	if entries, err := os.ReadDir(waitDir); err != nil || len(entries) != 1 {
		var names []string
		if err == nil {
			for _, e := range entries {
				names = append(names, e.Name())
			}
		}
		t.Fatalf("helper under NOWRITE left files in %s: %v %v", waitDir, names, err)
	}
	if got := peer.SaveStalledScreen(dir, "01020304", "", env); got != "" {
		t.Fatalf("helper with an empty screen reported a path: %s", got)
	}
}

func TestSendStalledWindowUnreadableScreen(t *testing.T) {
	// A check whose screen cannot be read proves nothing and does not
	// interrupt the wait: the window keeps polling (many failed reads) until
	// its end, and the 15 saves the last screen that was actually read — the
	// pre-send one.
	_, prompt := stalledID(t)
	history := "old turn output"
	pre := piSendScreen(history, "")
	readFail := `{"error":{"code":"screen_unavailable","message":"no screen"}}`
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		// Every visible read after the prompt fails: the box detection and
		// every check of the window read an unreadable screen.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stderr: readFail, Code: 1},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "2000", "50"
	screenPath := filepath.Join(f.dir, "state", "ws-test", "wait", "send-01020304.screen")
	code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	want := "soho: send: local/w0test:p0a did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 2s window; read its pane for #01020304 or a reply before sending again; screen saved to " + screenPath + "\n"
	if code != 15 || stderr != want {
		t.Fatalf("the unreadable checks run the window to its end and the 15 cites the saved screen: code=%d stderr=%q", code, stderr)
	}
	if reads := d15WindowVisibleAfterPrompt(t, f); reads < 2 {
		t.Fatalf("an unreadable screen does not interrupt the wait: %d visible reads after the prompt", reads)
	}
	b, err := os.ReadFile(screenPath)
	if err != nil {
		t.Fatalf("the 15 saves the last screen that was actually read: %v", err)
	}
	if string(b) != pre {
		t.Fatalf("the saved screen is the pre-send read:\n%q", string(b))
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no key is pressed while the screen is unreadable: %d", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the unreadable window never resends the text: %d prompts", prompts)
	}
}

// TestSendStalledCodexHistoryDelivered is the D15c positive: the prompt
// stalls while codex works, and the box detection reads a screen where the
// delivered message's history block is the last `›` line (the real composer
// is not on screen). The peerMessageInHistory guard keeps the id out of the
// input box, so no Enter goes and the window proves the arrival through the
// recent marker on the fallback screen.
func TestSendStalledCodexHistoryDelivered(t *testing.T) {
	history := "› " + peer.PeerOpenTag("01020304") + "\n" +
		"  Sender: orchestrator-2 (w14:p1 on Run2Biz.local); kind claude.\n" +
		"  Sent: 2026-10-09T14:22:05Z\n" +
		"  Reply with: soho send local/w14:p1 \"<your reply>\"\n" +
		"  A message from another agent follows, each line quoted with \"> \".\n" +
		"\n" +
		"  > primeira linha do corpo\n" +
		"  > segunda linha do corpo\n" +
		"  " + peer.PeerEndLine("01020304") + "\n" +
		"• Working (3s • esc to interrupt)"
	f := newFixture(t, []fakecli.Rule{
		d15WindowGet(1, "done", "1"),
		d15WindowVisible(1, d15WindowStale),
		d15WindowGet(2, "done", "1"),
		d15WindowGet(3, "done", "1"),
		{Argv: d15WindowPromptArgv(d15WindowPrompt(t)), Stderr: stalledPromptErr, Code: 1},
		d15WindowVisible(2, history), // box detection: the delivered history block
		d15WindowVisible(3, history), // first stalled check (post-fix path)
		d15WindowGet(4, "done", "1"), // no seq move: the recent path proves
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
		d15WindowVisible(4, history), // the recent → visible fallback
		// Catch-alls so a pre-fix run takes the box branch, laps its window
		// without proof, and exits 15.
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: history},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "done", "1")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
	})
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
	code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
	t.Logf("code=%d out=%q stderr=%q enters=%d", code, out, stderr, remoteEnters(t, f))
	if code != 0 || !strings.HasPrefix(out, "sent to ") {
		t.Fatalf("the delivered history message must prove the send: %s", stderr)
	}
	if enters := remoteEnters(t, f); enters != 0 {
		t.Fatalf("the history block is not the input box, so no Enter goes: %d", enters)
	}
}

// readSource returns the --source value of a `herdr agent read` argv, or
// "" when the argv holds none.
func readSource(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--source" {
			return argv[i+1]
		}
	}
	return ""
}

// argvPair reports whether the argv holds the flag with the given value.
func argvPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// hasFlag reports whether the argv holds the flag, whatever its value.
func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// nthCallStart returns when the n-th call whose argv is exactly argv began
// running, as the fake recorded it (fakecli.ReadCallStarts), and false while
// the call log does not hold that call and its start yet. A start before
// notBefore is a line read while the fake was still writing it, and counts
// as not there yet.
func nthCallStart(f *fixture, argv []string, n int, notBefore time.Time) (time.Time, bool) {
	log := filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl")
	calls, err := fakecli.ReadCalls(log)
	if err != nil {
		return time.Time{}, false
	}
	starts, err := fakecli.ReadCallStarts(log)
	if err != nil {
		return time.Time{}, false
	}
	seen := 0
	for i, c := range calls {
		if strings.Join(c.Argv, "\x00") != strings.Join(argv, "\x00") {
			continue
		}
		if seen++; seen == n {
			if i >= len(starts) || starts[i].Before(notBefore) {
				return time.Time{}, false
			}
			return starts[i], true
		}
	}
	return time.Time{}, false
}

// d15WindowVisibleArgv is the remote visible read d15WindowVisible scripts.
var d15WindowVisibleArgv = []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}

func TestIndependentFallbackBudget(t *testing.T) {
	// The recent read's agent_not_idle fallback must not get a fresh copy of
	// the budget the recent read already consumed: the recent read fails
	// 2.1s into the 3s window and the fallback runs on the time then left
	// (floored at 1s). The bound sits at the budget phase — the send's
	// preparation calls are not part of the 3s budget — so neither the
	// whole-send wall clock nor the cut of a slow fallback is bounded here:
	// the phase test TestReadScreenFallbackGetsTheRemainingBudget measures
	// them.
	//
	// The fallback shows its own screen (a sentinel) and every visible read
	// after it fails, so the screen the 15 saves — the last visible screen
	// read — is the sentinel. The sentinel alone does not tell the fallback
	// from the next check's first read: both are the fourth visible read.
	// What tells them apart is the call after it: the fallback ends its
	// check (its screen holds no receipt marker), so the next call is the
	// next check's visible read, while a check's own readable screen is
	// followed by the state get the check makes next.
	//
	// The window opens when the box detection (the second visible read)
	// returns, so its deadline is at least 3s after that read's own start
	// as the fake recorded it. The recent read waits for a file the test
	// creates 2.1s after that start: it fails at a fixed point of the
	// window's clock, and the time the window's earlier checks and its own
	// opening take is absorbed in that wait instead of added to it, so it
	// cannot run into the deadline and time out before its fallback.
	const recentDelay = 2100 * time.Millisecond
	release := filepath.Join(t.TempDir(), "recent-fails")
	recent := d15WindowRecent(1, "")
	recent.WaitFile = release
	recent.Stderr = notIdleErr
	recent.Code = 1
	const fallbackScreen = "FALLBACK_VISIBLE_SENTINEL\n›\n"
	fallback := d15WindowVisible(4, fallbackScreen)
	unreadable := fakecli.Rule{Argv: d15WindowVisibleArgv, Stderr: `{"error":{"code":"screen_unavailable","message":"no screen"}}`, Code: 1}
	tail := []fakecli.Rule{d15WindowVisible(3, d15WindowStale), d15WindowGet(4, "done", "1"), recent, fallback, unreadable}
	f := newFixture(t, stalledWindowSetupRules(t, tail))
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "3000", "1"
	start := time.Now()
	stop, released := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(released)
		for {
			if box, ok := nthCallStart(f, d15WindowVisibleArgv, 2, start); ok {
				select {
				case <-stop:
				case <-time.After(time.Until(box.Add(recentDelay))):
					_ = os.WriteFile(release, nil, 0o600)
				}
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-released })
	code, _, _ := f.run([]string{"send", d15Pane, "hello"})
	elapsed := time.Since(start)
	t.Logf("window=3s floor=1s elapsed=%s code=%d prompts=%d enters=%d", elapsed, code, remotePrompts(t, f), remoteEnters(t, f))
	if code != 15 {
		t.Fatalf("the stalled send without proof keeps 15: code=%d", code)
	}
	if prompts := remotePrompts(t, f); prompts != 1 {
		t.Fatalf("the window never resends the text: %d prompts", prompts)
	}
	if enters := remoteEnters(t, f); enters != 0 {
		t.Fatalf("no key is pressed while the screen is stale: %d enters", enters)
	}
	saved, err := os.ReadFile(filepath.Join(f.dir, "state", "ws-test", "wait", "send-"+d15WindowID+".screen"))
	if err != nil || string(saved) != fallbackScreen {
		t.Fatalf("the saved screen is the recent read's visible fallback: %v %q", err, saved)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The expected recent → visible call sequence in the send: a --source
	// recent-unwrapped read was made and the call right after the first one
	// is a --source visible read without --lines. On its own it does not
	// prove the visible read is readScreen's fallback — a later diagnostic
	// read matches the same shape; the saved sentinel screen above proves
	// the fallback ran, and the rest of the fallback proof (the 1s floor,
	// exactly two calls, recent + floor to window + floor + slack) is the
	// direct TestReadScreenFallbackGetsTheRemainingBudget.
	recentIdx := -1
	for i, c := range calls {
		if readSource(c.Argv) == "recent-unwrapped" {
			recentIdx = i
			break
		}
	}
	if recentIdx < 0 {
		t.Fatalf("no recent-unwrapped read in the call log: %d calls", len(calls))
	}
	if recentIdx+1 >= len(calls) {
		t.Fatalf("no call right after the first recent read: %d calls", len(calls))
	}
	next := calls[recentIdx+1]
	if readSource(next.Argv) != "visible" {
		t.Fatalf("the call right after the first recent read is not a visible read: %v", next.Argv)
	}
	if hasFlag(next.Argv, "--lines") {
		t.Fatalf("the visible read right after the first recent read carries --lines: %v", next.Argv)
	}
	if recentIdx+2 < len(calls) && hasArgvSuffix(calls[recentIdx+2].Argv, "agent", "get", "w0test:p0a") {
		t.Fatalf("the visible read after the recent read is a check's own read, not its fallback: a state get follows it: %v", calls[recentIdx+2].Argv)
	}
}

// hasArgvSuffix reports whether the argv ends with tail.
func hasArgvSuffix(argv []string, tail ...string) bool {
	return len(argv) >= len(tail) && strings.Join(argv[len(argv)-len(tail):], "\x00") == strings.Join(tail, "\x00")
}

func TestReadScreenFallbackGetsTheRemainingBudget(t *testing.T) {
	// The bound sits at the budget phase: the send's preparation calls are
	// not part of the 3s budget, so only this readScreen call is timed —
	// the recent read fails inside the budget with the time left under the
	// 1s floor, and the 10s fallback is cut by that floor, so the call ends
	// inside the recent read + floor + the bound; a fresh budget would run
	// near recent + 3s and time out after 3s.
	//
	// The recent read fails at a fixed point of the budget's clock, not
	// after a delay of its own: it waits for a file the test creates
	// recentDelay after the budget starts, so the time the process takes to
	// open, which counts against the same 3s budget, is absorbed in that
	// wait instead of added to it. The read then ends recentDelay into the
	// budget for any opening shorter than that (plus the fake's 20ms file
	// poll and its exit), leaving under the 1s floor (100ms margin), and
	// a slower opening only moves its end towards the 3s budget, where the
	// floor still engages.
	const (
		window      = 3000 * time.Millisecond
		floor       = 1000 * time.Millisecond
		recentDelay = 2100 * time.Millisecond
		// slack over window + floor for the kill of the cut fallback and the
		// run around it; a fresh budget ends at recent + window (5.1s), above
		// the 4.8s ceiling it leaves.
		slack = 800 * time.Millisecond
	)
	release := filepath.Join(t.TempDir(), "recent-fails")
	recent := d15WindowRecent(1, "")
	recent.WaitFile = release
	recent.Stderr = notIdleErr
	recent.Code = 1
	fallback := d15WindowVisible(1, d15WindowStale)
	fallback.Delay = 10000
	f := newFixture(t, []fakecli.Rule{recent, fallback})
	start := time.Now()
	timer := time.AfterFunc(recentDelay, func() { _ = os.WriteFile(release, nil, 0o600) })
	t.Cleanup(func() { timer.Stop() })
	screen, cause := peer.ReadScreen("windows", "w0test:p0a", "recent-unwrapped", 67, f.env, int(window/time.Millisecond), nil)
	elapsed := time.Since(start)
	t.Logf("window=3s floor=1s elapsed=%s cause=%q", elapsed, cause)
	if screen != "" {
		t.Fatalf("the cut fallback read returns no screen: %q", screen)
	}
	if cause != "herdr agent read timed out after 1s" {
		t.Fatalf("the fallback got the floored remaining budget, not a fresh one: %q", cause)
	}
	if elapsed < recentDelay+floor || elapsed > window+floor+slack {
		t.Fatalf("the budget phase runs the recent read + floor: elapsed=%s", elapsed)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("exactly two reads, the recent and its visible fallback: %d calls", len(calls))
	}
	if readSource(calls[0].Argv) != "recent-unwrapped" || !argvPair(calls[0].Argv, "--lines", "67") {
		t.Fatalf("the first call is the recent read with --lines 67: %v", calls[0].Argv)
	}
	if readSource(calls[1].Argv) != "visible" {
		t.Fatalf("the second call is the visible fallback: %v", calls[1].Argv)
	}
	if hasFlag(calls[1].Argv, "--lines") {
		t.Fatalf("the fallback visible read carries --lines: %v", calls[1].Argv)
	}
}
