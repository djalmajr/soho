package peer_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// agentJSONKind is agentJSON with the kind ("agent" field) set. The existing
// agentJSON leaves it out, so those targets resolve to an empty kind.
func agentJSONKind(kind, status, seq string) string {
	return fmt.Sprintf(`{"result":{"agent":{"pane_id":"w0test:p0a","agent":%q,"agent_status":%q,"state_change_seq":%q,"cwd":"","workspace_id":""}}}`, kind, status, seq)
}

// piSendScreen models the real pi 0.99.2 visible screen (cinzel): chat
// history above, the input box between the last two '─' (U+2500) lines (box
// may be empty), and the footer (cwd and token stats) below.
func piSendScreen(history, box string) string {
	sep := strings.Repeat("─", 66)
	foot := "~/code/herdr-soho (fix/send-pi-input-box...)\n↑5.2M ↓43k 40.3%/262k (auto)           (lbvllm) qwen3.8-27b • high\n"
	return history + "\n\n" + sep + "\n" + box + "\n" + sep + "\n" + foot
}

func TestSendPiInputBox(t *testing.T) {
	// The delivered message: header + quoted body + end line, id 01020304.
	newPromptFixture := func(t *testing.T) (prompt string) {
		id := "01020304"
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		return peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	countEnters := func(t *testing.T, f *fixture) int {
		t.Helper()
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		enters := 0
		for _, call := range calls {
			if len(call.Argv) > 3 && call.Argv[1] == "send-keys" && call.Argv[3] == "enter" {
				enters++
			}
		}
		return enters
	}

	t.Run("pi: a message already in the chat history above the input box is sent without Enter", func(t *testing.T) {
		prompt := newPromptFixture(t)
		history := "old turn output"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Delay: 5, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: piSendScreen(history+"\n"+prompt, "")},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a delivered pi message: %d enters", enters)
		}
	})
	t.Run("pi: a message in the Steering queue above the input box is queued, not sent (R-RC8C)", func(t *testing.T) {
		prompt := newPromptFixture(t)
		history := "old turn output\nSteering: " + peer.PeerOpenTag("01020304") + " Message from another agent\n↳ Option+Up to edit all queued messages"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen("old turn output", "")},
			{Argv: []string{"agent", "prompt", "w0test:p0a"}, ArgvPrefix: true},
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("pi", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: history + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piSendScreen(history, "")},
		}
		_ = prompt
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a busy pi: %d enters", enters)
		}
	})
	t.Run("pi: a message still in the input box gets Enter and today's proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		history := "old turn output"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Delay: 5, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: piSendScreen(history, prompt)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: piSendScreen(history, prompt)},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 6, Stdout: agentJSONKind("pi", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 4, Stdout: piSendScreen(history+"\n"+prompt, "")},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEnters(t, f); enters != 1 {
			t.Fatalf("the box message needs exactly one Enter: %d", enters)
		}
	})
	t.Run("pi without two separator lines: the screen proof stays whole-screen and no Enter goes", func(t *testing.T) {
		prompt := newPromptFixture(t)
		plain := prompt + "\n" // the whole visible screen, no '─' lines at all
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "Before prompt\n"},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Delay: 5, Stdout: prompt + "\n"},
			// The window's visible read and the pre-Enter read: the same
			// screen, no '─' lines — the screen proof keeps the marker held
			// (whole screen), but there is no recognized composer, so no
			// Enter goes.
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: plain},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("pi", "idle", "1")},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || out != "" || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("no Enter goes to a pi without the recognized box: %d", enters)
		}
	})
	t.Run("a claude whose box is between borders proves like pi", func(t *testing.T) {
		prompt := newPromptFixture(t)
		// Same screen as the pi history case, but 15+ lines push the marker
		// above the last 15: for a claude whose box sits between two borders
		// the taken message in the history is proof (like pi), the box holds
		// a bare ❯ (claude's empty box), and the send goes out with no Enter.
		history := prompt + "\n" + strings.Repeat("chrome\n", 11)
		screen := piSendScreen(strings.TrimSuffix(history, "\n"), "❯")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "Before prompt\n"},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("claude", "idle", "1")},
			// The claude transcript-path get: this agent has no agent_session,
			// so the path resolves to nothing and the send keeps the screen
			// proof.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Delay: 5, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: screen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: screen},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 6, Stdout: agentJSONKind("claude", "idle", "1")},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("no Enter goes to a claude whose empty box is between the borders: %d enters", enters)
		}
	})
	t.Run("pi: no Enter when the message is in the history but the transcript has no sign of it", func(t *testing.T) {
		prompt := newPromptFixture(t)
		// The message shows in the short history (inside the last 15 lines),
		// the box is empty, and the transcript never held it: the old
		// last-15-lines check would press Enter here; the box check must not.
		screen := piSendScreen("old turn output\n"+prompt, "")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "Before prompt\n"},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Delay: 5, Stdout: "old transcript without id\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: screen},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("pi", "idle", "1")},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("an Enter must not go to a pi whose box does not hold the id: %d", enters)
		}
	})
	t.Run("pi: Enter goes but the second proof fails with the new message", func(t *testing.T) {
		prompt := newPromptFixture(t)
		history := "old turn output"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Delay: 5, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: piSendScreen(history, prompt)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: piSendScreen(history, prompt)},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 6, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 2, Delay: 5, Stdout: "old transcript without id\n"},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countEnters(t, f); enters != 1 {
			t.Fatalf("the box message got its Enter before the failed second proof: %d", enters)
		}
	})
}
