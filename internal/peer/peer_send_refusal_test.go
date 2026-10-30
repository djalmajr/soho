package peer_test

import (
	"bytes"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The sender reads the receiver's refusal of its message (exit 18, no
// extra Enter) on the visible screen, in the recent history and in the
// transcript, for Claude, Codex and pi. The refusal is inbound=off, the one
// receiver refusal that stays after DJA-610.
func refusalFixtureID(t *testing.T) {
	t.Helper()
	old := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	t.Cleanup(func() { rand.Reader = old })
}
func refusalMessage() string {
	return peer.PeerHeader("local/-", "-", "-", "-", signID) + "\n\n> hello\n" + peer.PeerEndLine(signID)
}

func refusalRules(prompt, history string) []fakecli.Rule {
	clean := "old turn output"
	return []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(clean, "")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: piSendScreen(history, prompt)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: piSendScreen(history, prompt)},
		{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
	}
}

func TestSendReadsTheReceiversRefusal(t *testing.T) {
	refusal := func(id, result string) string {
		return "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + id + " " + result + "\n  The refused text stays in the input; clear it (Ctrl+C) before the next message."
	}
	t.Run("the refusal of this message exits 18 and presses nothing", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+refusal(signID, "refused-inbound-off")))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		want := "soho: send: local/w0test:p0a refused the message (refused-inbound-off); it did not reach the model\n"
		if code != 18 || out != "" || stderr != want {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("a refused message got %d Enter", enters)
		}
		fields := strings.Split(strings.TrimSpace(peerMessagesLog(t, f)), "\t")
		if len(fields) < 6 || fields[3] != "refused" || fields[5] != signID {
			t.Fatalf("peer-messages.tsv = %q", fields)
		}
	})
	t.Run("the control: without the refusal the stalled message in the box gets its Enter", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output"))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, _ := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 {
			t.Fatal("no refusal on screen, yet exit 18")
		}
		if enters := countSendKeyEnters(t, f); enters != 1 {
			t.Fatalf("the control needs its Enter to be meaningful: %d", enters)
		}
	})
	t.Run("a refusal of another message does not decide this send", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+refusal("deadbeef", "refused-inbound-off")))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 || strings.Contains(stderr, "refused the message") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if strings.Contains(peerMessagesLog(t, f), "\trefused\t") {
			t.Fatalf("peer-messages.tsv records a refusal: %q", peerMessagesLog(t, f))
		}
	})
	t.Run("a longer id that begins with this one is another message", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+refusal(signID+"ab", "refused-inbound-off")))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, _ := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 {
			t.Fatal("a longer id was read as this message's refusal")
		}
	})
	t.Run("a quoted refusal line is body, not a refusal", func(t *testing.T) {
		_, prompt := stalledID(t)
		quoted := "> [soho:peer-refused] #" + signID + " refused-inbound-off"
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+quoted))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, _ := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 {
			t.Fatal("a quoted refusal line exited 18")
		}
	})
	t.Run("the refusal and the header on the same screen exit 18", func(t *testing.T) {
		_, prompt := stalledID(t)
		history := "old turn output\n" + refusal(signID, "refused-inbound-off")
		rules := refusalRules(prompt, history)
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 18 || !strings.Contains(stderr, "(refused-inbound-off)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("%d Enter after the refusal", enters)
		}
	})
}

// The refusal wins over the arrival proof: a state sequence that moved and a
// message that looks taken do not make the send report sent.
func TestSendRefusalBeatsTheArrivalProof(t *testing.T) {
	refusalFixtureID(t)
	prompt := refusalMessage()
	refusal := "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + signID + " refused-inbound-off\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "before prompt\n"},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		// The sequence moves (the usual proof of a taken message)...
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSON("idle", "2")},
		// ...but the screen shows the refusal.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: refusal},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: refusal},
	}
	f := newFixture(t, rules)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-inbound-off)") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if strings.Contains(peerMessagesLog(t, f), "\tsent\t") {
		t.Fatalf("a refused message was logged as sent: %q", peerMessagesLog(t, f))
	}
}

// The sequence moved and the visible screen shows no refusal (it scrolled
// away), but the recent history still holds it: the usual proof of a taken
// message must not hide it.
func TestSendRefusalInTheRecentHistoryBeatsTheSequenceProof(t *testing.T) {
	refusalFixtureID(t)
	prompt := refusalMessage()
	refusal := "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + signID + " refused-inbound-off\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "before prompt\n"},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		// The sequence moves (the usual proof of a taken message)...
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSON("idle", "2")},
		// ...the visible screen shows nothing of the refusal...
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: "done, the refusal scrolled out\n"},
		// ...and the recent history still holds it.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "earlier output\n" + refusal},
	}
	f := newFixture(t, rules)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-inbound-off)") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("%d Enter after the refusal", enters)
	}
	log := peerMessagesLog(t, f)
	if strings.Contains(log, "\tsent\t") || !strings.Contains(log, "\trefused\t") {
		t.Fatalf("peer-messages.tsv = %q", log)
	}
}

// The refusal is read from the recent history alone (the visible screen has
// scrolled it away), and from a local claude's transcript alone.
func TestSendRefusalFromRecentHistoryAndTranscript(t *testing.T) {
	refusalText := "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + signID + " refused-inbound-off\n"
	t.Run("only the recent history shows it", func(t *testing.T) {
		_, prompt := stalledID(t)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "❯ \n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "earlier output\n" + refusalText},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-inbound-off)") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("%d Enter after the refusal", enters)
		}
	})

	cwd := "/tmp/claude-work"
	systemLine := `{"type":"system","subtype":"informational","content":"UserPromptSubmit operation blocked by hook:\n[soho:peer-refused] #` + signID + ` refused-inbound-off\nThe refused text stays in the input; clear it (Ctrl+C) before the next message."}`
	userLine := `{"type":"user","message":{"content":"` + peer.PeerEndLine(signID) + `"}}`
	claudeRules := func(prompt string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
		}
	}
	run := func(t *testing.T, appended string, extra ...string) (int, string, string, *fixture) {
		_, prompt := stalledID(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
		f := newFixture(t, claudeRules(prompt))
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, signID, appended)
		code, out, stderr := f.run(append([]string{"send", "w0test:p0a", "--now", "--timeout", "1000"}, append(extra, "hello")...))
		if !<-done {
			t.Fatal("the transcript line was not appended after the prompt call")
		}
		return code, out, stderr, f
	}
	t.Run("a local claude's transcript gains the system entry", func(t *testing.T) {
		code, out, stderr, f := run(t, systemLine)
		if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-inbound-off)") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("%d Enter after the refusal", enters)
		}
	})
	t.Run("the refusal beats the transcript's user line", func(t *testing.T) {
		code, out, stderr, _ := run(t, userLine+"\n"+systemLine)
		if code != 18 || out != "" || !strings.Contains(stderr, "refused the message") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("the control: the user line alone is the usual proof", func(t *testing.T) {
		code, out, _, _ := run(t, userLine)
		if code != 0 || out != "sent to local/w0test:p0a\n" {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("an assistant or user line that merely cites the refusal is not one", func(t *testing.T) {
		cite := `{"type":"assistant","message":{"content":"[soho:peer-refused] #` + signID + ` refused-inbound-off"}}`
		code, _, _, _ := run(t, cite)
		if code == 18 {
			t.Fatal("an assistant line was read as the receiver's refusal")
		}
	})
}

func TestSendCodexHookRefusal(t *testing.T) {
	for _, prefix := range []string{"  └ ", "  └ > "} {
		t.Run(prefix, func(t *testing.T) {
			_, prompt := stalledID(t)
			screen := "• Blocked by hook\n" + prefix + "[soho:peer-refused] #" + signID + " refused-inbound-off\n"
			rules := refusalRules(prompt, screen)
			for i := range rules {
				if len(rules[i].Argv) > 1 && rules[i].Argv[0] == "agent" && rules[i].Argv[1] == "get" {
					rules[i].Stdout = agentJSONKind("codex", "idle", "1")
				}
			}
			f := newFixture(t, rules)
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
			if prefix == "  └ " {
				want := "soho: send: local/w0test:p0a refused the message (refused-inbound-off); it did not reach the model\n"
				if code != 18 || out != "" || stderr != want {
					t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
				}
				if n := countSendKeyEnters(t, f); n != 0 {
					t.Fatalf("refusal sent %d Enter", n)
				}
			} else if code == 18 {
				t.Fatalf("quoted refusal rejected send: %s", stderr)
			}
		})
	}
}

func TestSendPiHookRefusal(t *testing.T) {
	for _, prefix := range []string{" Error: ", " Error: > ", " > Error: ", ""} {
		t.Run(prefix, func(t *testing.T) {
			_, prompt := stalledID(t)
			rules := refusalRules(prompt, prefix+"[soho:peer-refused] #"+signID+" refused-inbound-off\n")
			for i := range rules {
				if len(rules[i].Argv) > 1 && rules[i].Argv[0] == "agent" && rules[i].Argv[1] == "get" {
					rules[i].Stdout = agentJSONKind("pi", "idle", "1")
				}
			}
			f := newFixture(t, rules)
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
			if prefix == " Error: " || prefix == "" {
				want := "soho: send: local/w0test:p0a refused the message (refused-inbound-off); it did not reach the model\n"
				if code != 18 || out != "" || stderr != want {
					t.Fatalf("code=%d out=%q stderr=%q want=%q", code, out, stderr, want)
				}
				if n := countSendKeyEnters(t, f); n != 0 {
					t.Fatalf("refusal sent %d Enter", n)
				}
			} else if code == 18 {
				t.Fatalf("quoted refusal rejected send: %s", stderr)
			}
		})
	}
}
