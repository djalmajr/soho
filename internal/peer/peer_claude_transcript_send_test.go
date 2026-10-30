package peer_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The delivery proof for a working Claude Code target comes from its session
// transcript: every message Claude takes is written as a "type":"user" line
// that holds the peer marker, even when the alternate screen has scrolled the
// marker out of the visible area. The fake herdr cannot append that line
// itself, so appendAfterPrompt starts a writer that adds it as soon as the
// `agent prompt` call is logged — strictly after the pre-count — and the
// prompt rule's Delay keeps the fake herdr busy until the line is visible to
// the window's first read.

const claudeTranscriptSessionValue = "3f2b8c1a-9d4e-4c7a-b1f0-5e6d7c8b9a0f"

// claudeSessionAgentJSON is agentJSONKind plus the agent_session and cwd that
// herdr.ClaudeTranscriptPath resolves from the same `agent get` call.
func claudeSessionAgentJSON(status, seq, cwd string) string {
	return fmt.Sprintf(`{"result":{"agent":{"pane_id":"w0test:p0a","agent":"claude","agent_status":%q,"state_change_seq":%q,"cwd":%q,"workspace_id":"","agent_session":{"source":"herdr","agent":"claude","kind":"id","value":"%s"}}}}`,
		status, seq, cwd, claudeTranscriptSessionValue)
}

func claudeTranscriptPath(t *testing.T, configRoot, cwd string) string {
	t.Helper()
	return filepath.Join(configRoot, "projects", herdr.ClaudeProjectDir(cwd), claudeTranscriptSessionValue+".jsonl")
}

func writeClaudeTranscript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// transcriptUserLine is the session transcript line Claude Code writes when
// it takes a message: the whole peer prompt (the opening tag line, the
// header, the quoted body, the closing line) as a JSON string value.
func transcriptUserLine(t *testing.T, prompt string) string {
	t.Helper()
	content, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	return `{"type":"user","message":{"content":` + string(content) + `}}`
}

// appendAfterPrompt appends the transcript line only after the fake herdr has
// logged the prompt call for the id and reports on done whether the append
// landed. It never calls t after returning, so a late run is harmless.
func appendAfterPrompt(t *testing.T, callsLog, transcriptPath, id, line string) <-chan bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		ok := false
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if calls, err := fakecli.ReadCalls(callsLog); err == nil {
				for _, c := range calls {
					// Find `agent prompt <pane> <msg>` with or without the
					// leading --machine prefix.
					for i := 0; i+3 < len(c.Argv); i++ {
						if c.Argv[i] == "agent" && c.Argv[i+1] == "prompt" && strings.Contains(c.Argv[i+3], "soho:peer:"+id) {
							f, werr := os.OpenFile(transcriptPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
							if werr == nil {
								_, werr = f.WriteString(line + "\n")
								_ = f.Close()
								ok = werr == nil
							}
							break
						}
					}
					if ok {
						break
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

func TestSendClaudeTranscriptProof(t *testing.T) {
	id := "01020304"
	cwd := "/tmp/claude-work"
	newPromptFixture := func(t *testing.T) string {
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		return peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	// The transcript line holds the whole peer prompt (the opening tag
	// line included), the way Claude Code records the taken message.
	userLine := transcriptUserLine(t, newPromptFixture(t))
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
	// countGets counts the local `agent get` calls: resolve, screen, pre,
	// the transcript-path get (claude targets only), the window poll, and the
	// pre-Enter one when it is reached.
	countGets := func(t *testing.T, f *fixture) int {
		t.Helper()
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		gets := 0
		for _, call := range calls {
			if len(call.Argv) == 3 && call.Argv[1] == "get" {
				gets++
			}
		}
		return gets
	}
	promptRule := func(prompt string) fakecli.Rule {
		// The Delay is the stand-in for Claude taking the message: it keeps
		// the fake herdr busy until the writer has appended the line.
		return fakecli.Rule{
			Argv:  []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"},
			Delay: 100,
		}
	}

	t.Run("claude working: the transcript gains the user line with the id and the screen has no sign: sent, exit 0, no Enter", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
		// The visible screen is Claude's working turn: no #id anywhere, the
		// marker already scrolled out of the visible area.
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, userLine)
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !<-done {
			t.Fatal("the transcript line was not appended after the prompt call")
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("a transcript-proven send sends no Enter: %d enters", enters)
		}
	})
	t.Run("claude working: the same send without the new transcript line keeps today's 15", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		// An earlier conversation line: it holds a user record but not this
		// message's marker, so the count does not move.
		writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation without the marker"}}`+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		// No sign of the id on the visible screen: today's path presses nothing.
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a screen without the id: %d enters", enters)
		}
	})
	t.Run("claude working: an assistant line with the id is not proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		// Claude citing the id back in its own reply: the line holds the
		// marker but not the "type":"user" record type.
		writeClaudeTranscript(t, transcriptPath,
			`{"type":"assistant","message":{"content":"did you mean `+peer.PeerPrefix+" #"+id+`? I am still working"}}`+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("an assistant-cited marker presses nothing: %d enters", enters)
		}
	})
	t.Run("claude working: a user line with the id already present before the send is not proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		// The transcript already holds a user line with this marker before
		// the send: the pre-send count is one, and a line that does not move
		// the count is not delivery.
		writeClaudeTranscript(t, transcriptPath, userLine+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("an already-present marker line presses nothing: %d enters", enters)
		}
	})
	t.Run("claude working: a new assistant line with the id after the send (Claude citing it in its reply) is not proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
		assistantLine := `{"type":"assistant","message":{"content":"did you mean ` + peer.PeerPrefix + " #" + id + `? I am still working"}}`
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, assistantLine)
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if !<-done {
			t.Fatal("the assistant line was not appended after the prompt call")
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("an assistant-cited marker presses nothing: %d enters", enters)
		}
	})
	t.Run("claude without agent_session: the screen path is kept and the transcript is never resolved", func(t *testing.T) {
		prompt := newPromptFixture(t)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("claude", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = t.TempDir() // present, but no session to resolve
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		// Exactly today's message: no new warning for the absent transcript.
		want := "soho: send: local/w0test:p0a did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane for #01020304 or a reply before sending again\n"
		if code != 15 || stderr != want {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if gets := countGets(t, f); gets != 6 {
			// resolve + screen + pre + the transcript-path get (which finds no
			// session) + window poll + pre-Enter: the send stays on the
			// screen path.
			t.Fatalf("gets=%d want 6 (session absent, screen path kept)", gets)
		}
	})
	t.Run("claude with a session but a missing transcript file: the screen path is kept", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir() // no transcript file under it
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if gets := countGets(t, f); gets != 6 {
			// the transcript-path get happens, then the missing file falls
			// back to the screen path.
			t.Fatalf("gets=%d want 6 (transcript path resolved, file missing)", gets)
		}
	})
	t.Run("claude with an open queue and the transcript line: still queued, no Enter", func(t *testing.T) {
		prompt := newPromptFixture(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
		// The message sits in the queue above the input box: the transcript
		// already holds the taken line, but the message is not read yet.
		screen := "✻ Cranking out changes…\n" + prompt + "\nctrl+enter to send now\nPress up to edit queued messages\n"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, userLine)
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !<-done {
			t.Fatal("the transcript line was not appended after the prompt call")
		}
		if enters := countEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a busy claude with an open queue: %d enters", enters)
		}
	})
	t.Run("a pi target: nothing changes and no transcript path is resolved", func(t *testing.T) {
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
		f.env["CLAUDE_CONFIG_DIR"] = t.TempDir() // present, but a pi target never resolves a transcript
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if gets := countGets(t, f); gets != 4 {
			// resolve + screen + pre + window poll: no get for a transcript
			// path.
			t.Fatalf("gets=%d want 4 (no transcript path get)", gets)
		}
	})
}
