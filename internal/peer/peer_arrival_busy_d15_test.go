package peer_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The rc.12 codex case: send to an idle remote codex, `agent prompt` returns
// agent_prompt_stalled, and the marker is not in the input box — the real
// screen captured after shows the message already taken: the header line in
// the history drawn with the composer's "› " prefix (line 29), the end line
// glued to the last quoted line (line 45), and the empty composer (line 49).
// Before the fix the send died 15 "stalled" without ever checking that the
// message was taken; now the stalled path proves the arrival once (the marker
// in the recent history and a visible screen that shows it taken, or a state
// sequence that moved from an idle baseline) and exits 0. A message still in
// the composer is still not sent: the box branch and the screen rules are
// untouched.

const d15Pane = "windows/w0test:p0a"

// d15Fixture loads the real visible screen captured after the rc.12 send.
func d15Fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "evidence-rc12-pinar-codex-after.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// remotePrompts counts the `agent prompt` calls on the remote machine: the
// --machine prefix shifts the subcommand out of argv[1], so the package
// helper does not see it.
func remotePrompts(t *testing.T, f *fixture) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	prompts := 0
	for _, call := range calls {
		for i := 0; i+1 < len(call.Argv); i++ {
			if call.Argv[i] == "agent" && call.Argv[i+1] == "prompt" {
				prompts++
				break
			}
		}
	}
	return prompts
}

// remoteEnters counts the `agent send-keys … enter` calls on the remote
// machine.
func remoteEnters(t *testing.T, f *fixture) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	enters := 0
	for _, call := range calls {
		hasSendKeys, hasEnter := false, false
		for _, a := range call.Argv {
			if a == "send-keys" {
				hasSendKeys = true
			}
			if a == "enter" {
				hasEnter = true
			}
		}
		if hasSendKeys && hasEnter {
			enters++
		}
	}
	return enters
}

func TestSendArrivalBusyCodexStalled(t *testing.T) {
	// The id is the real rc.12 message id, so the marker matches the fixture
	// screen line by line.
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
	// The idle codex screen before the send: the empty composer, no peer
	// message in sight.
	preScreen := "› Ask Codex to do anything\n\n  GPT-6.1-Sol high · context 20% · Run herdr agents\n  New activity · Earlier messages available.  enter/esc latest · ? shortcuts\n"
	remoteGets := func(call int, status, seq string) fakecli.Rule {
		return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: call, Stdout: agentJSONKind("codex", status, seq)}
	}
	remoteVisible := func(call int, screen string) fakecli.Rule {
		return fakecli.Rule{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: call, Stdout: screen}
	}

	t.Run("stalled, the marker out of the box: the taken message in the history is sent, exit 0", func(t *testing.T) {
		prompt := newPrompt(t)
		screen := d15Fixture(t)
		rules := []fakecli.Rule{
			remoteGets(1, "idle", "1"),
			remoteVisible(1, preScreen),
			remoteGets(2, "idle", "1"),
			remoteGets(3, "idle", "1"),
			{Argv: promptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
			// The stalled read of the visible screen: the message is already
			// in the history, the composer is empty — no marker in the box.
			remoteVisible(2, screen),
			// The one-shot proof: the state did not move (the idle codex's
			// seq is the same), so the marker in the recent history against
			// the taken screen is the proof.
			remoteGets(4, "idle", "1"),
			{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
			{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
		if code != 0 || out != "sent to windows/w0test:p0a\n" || stderr != "" {
			t.Fatalf("the taken message must be sent: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := remoteEnters(t, f); enters != 0 {
			t.Fatalf("the marker out of the box presses nothing: %d enters", enters)
		}
		if prompts := remotePrompts(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("stalled, the marker in the composer: one Enter and the taken screen proves", func(t *testing.T) {
		prompt := newPrompt(t)
		screen := d15Fixture(t)
		// The screen at the stalled read: the marker sits in the composer
		// (the typed Enter never landed); after the Enter the screen shows
		// the message taken — the fixture.
		boxScreen := strings.Replace(screen, "› Ask Codex to do anything",
			"› "+peer.PeerOpenTag(id), 1)
		rules := []fakecli.Rule{
			remoteGets(1, "idle", "1"),
			remoteVisible(1, preScreen),
			remoteGets(2, "idle", "1"),
			remoteGets(3, "idle", "1"),
			{Argv: promptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
			remoteVisible(2, boxScreen),
			{Argv: []string{"--machine", "windows", "agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
			// The second proof window: the state did not move, the recent
			// holds the marker, and the visible screen is the taken one.
			remoteGets(4, "idle", "1"),
			{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
			remoteVisible(3, screen),
			// Catch-alls for a second window iteration on a slow machine.
			{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("codex", "idle", "1")},
			{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
		if code != 0 || out != "sent to windows/w0test:p0a\n" || stderr != "" {
			t.Fatalf("the Enter of the stalled path reaches the taken screen and is sent: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := remoteEnters(t, f); enters != 1 {
			t.Fatalf("the stalled message in the box needs exactly one Enter: %d", enters)
		}
		if prompts := remotePrompts(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("stalled, the marker in the composer and the screen never moves: today's 15 after one Enter", func(t *testing.T) {
		prompt := newPrompt(t)
		// The screen at the stalled read: the marker sits in the composer,
		// and after the Enter it is still there — the message is not taken.
		boxScreen := strings.Replace(d15Fixture(t), "› Ask Codex to do anything",
			"› "+peer.PeerOpenTag(id), 1)
		rules := []fakecli.Rule{
			remoteGets(1, "idle", "1"),
			remoteVisible(1, preScreen),
			remoteGets(2, "idle", "1"),
			remoteGets(3, "idle", "1"),
			{Argv: promptArgv(prompt), Stderr: stalledPromptErr, Code: 1},
			remoteVisible(2, boxScreen),
			{Argv: []string{"--machine", "windows", "agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
			// The second proof window: the state did not move and the marker
			// still sits in the composer, so the window fails.
			remoteGets(4, "idle", "1"),
			{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
			{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: boxScreen},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", d15Pane, "hello"})
		if code != 15 || out != "" {
			t.Fatalf("a message still in the composer is not sent: code=%d out=%q", code, out)
		}
		want := "soho: send: windows/w0test:p0a did not confirm taking the message: the last read showed it in its input box after one Enter; delivery is uncertain; read its pane (herdr --machine windows agent read w0test:p0a --source visible) for #29a25864 or a reply before sending again; screen saved to " + filepath.Join(f.dir, "state", "ws-test", "wait", "send-29a25864.screen") + "\n"
		if stderr != want {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := remoteEnters(t, f); enters != 1 {
			t.Fatalf("the failed second proof still gets one Enter only: %d", enters)
		}
	})
}
