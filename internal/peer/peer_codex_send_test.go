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

// The rc.11 case: `send local/w7:pGD --file … --now` to a working codex took
// the message and showed it in the history above the composer, but the send
// exited 15 because the screen proof counted the taken message still on the
// screen. The marker now counts only inside the codex composer region, and a
// '↳ #id' line above the composer is the follow-up queue, like pi's Steering
// and claude's queued messages.

// codexEvidenceScreen loads the real visible screen captured a few seconds
// after the send (copied into the test package).
func codexEvidenceScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "evidence-rc11-codex-history.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// codexPreScreen is the working codex screen before the send: the turn and
// the empty composer, no peer message in sight.
func codexPreScreen(t *testing.T) string {
	t.Helper()
	return "• Working (35m 51s • esc to interrupt) · Running hooks\n" +
		"  └ Tip: Use /fork to branch the current chat into a new thread.\n" +
		"\n" +
		"› Ask Codex to do anything\n" +
		"\n" +
		"  GPT-6.1-Sol medium · Context 20% use… Pursuing goal (1d 19h 23m)\n" +
		"  ← for agents · ? for shortcuts          ⚠ 3 warnings · f2 to view\n"
}

// codexID seeds the message id as the pi/claude tests do and returns the id
// and the exact prompt body the fake herdr must receive.
func codexID(t *testing.T) (id, prompt string) {
	t.Helper()
	id = "b8ba14bf"
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{0xb8, 0xba, 0x14, 0xbf})
	t.Cleanup(func() { rand.Reader = oldReader })
	return id, peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
}

func TestSendCodexHistory(t *testing.T) {
	t.Run("codex working: a taken message in the history above the composer is sent", func(t *testing.T) {
		_, prompt := codexID(t)
		screen := codexEvidenceScreen(t)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: codexPreScreen(t)},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			// The proof window: the state is working, the recent read holds
			// the message, and the visible screen is the real one — the
			// marker sits in the history, outside the composer region.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("codex", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: screen},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a taken codex message: %d enters", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the codex path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("codex working: a message still in the composer is not sent", func(t *testing.T) {
		id, prompt := codexID(t)
		// The real screen with the marker typed into the composer line: the
		// prompt was typed but its Enter never landed.
		lines := strings.Split(codexEvidenceScreen(t), "\n")
		for i := range lines {
			if strings.HasPrefix(lines[i], "›") {
				lines[i] = "› " + peer.PeerOpenTag(id) + " …"
			}
		}
		box := strings.Join(lines, "\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: codexPreScreen(t)},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			// The first proof window: the marker sits in the composer, so it
			// is never proven; the window expires.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("codex", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: box},
			// After the window: the marker is still in the composer, so one
			// Enter goes, and the second proof window fails the same way.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: box},
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 15 || out != "" {
			t.Fatalf("a message still in the composer is not sent: code=%d out=%q", code, out)
		}
		want := "soho: send: local/w0test:p0a did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane for #b8ba14bf or a reply before sending again\n"
		if stderr != want {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 1 {
			t.Fatalf("the composer message gets exactly one Enter: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the codex path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("codex working: a message in the follow-up queue after the window is queued, not lost", func(t *testing.T) {
		id, prompt := codexID(t)
		// The real screen with the message enqueued above the composer,
		// waiting for the turn to end.
		queue := strings.Replace(codexEvidenceScreen(t), "› Ask Codex to do anything",
			"↳ "+peer.PeerOpenTag(id)+" Message from another agent\n\n› Ask Codex to do anything", 1)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: codexPreScreen(t)},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("codex", "working", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			// The proof window: the marker sits in the follow-up queue, so it
			// is queued (not delivered) for the whole window; catch-alls keep
			// it unproved on slow machines too.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: queue},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a busy codex: %d enters", enters)
		}
	})
}
