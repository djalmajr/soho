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

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The delivery proof for a working local pi target comes from its session
// file: every message pi takes is written as a "type":"message" line whose
// role is "user" and that holds the peer marker, even when the screen shows
// no sign of it. The fake herdr cannot append that line itself, so
// appendAfterPrompt starts a writer that adds it as soon as the `agent
// prompt` call is logged — strictly after the pre-count — and the prompt
// rule's Delay keeps the fake herdr busy until the line is visible to the
// window's first read.

const piTranscriptSession = "2026-10-02T06-18-06-537Z_01a0fb43-4b49-70d7-8f62-5e7a10390e69.jsonl"

func piTranscriptSessionPath(root string) string {
	return filepath.Join(root, "agent", "sessions", "--tmp-pi-work--", piTranscriptSession)
}

// piSessionAgentJSON is agentJSONKind with the agent_session herdr reports
// for a pi agent (kind "path", the session file value).
func piSessionAgentJSON(status, seq, value string) string {
	return fmt.Sprintf(`{"result":{"agent":{"pane_id":"w0test:p0a","agent":"pi","agent_status":%q,"state_change_seq":%q,"cwd":"/tmp/pi-work","workspace_id":"","agent_session":{"source":"herdr:pi","agent":"pi","kind":"path","value":%q}}}}`,
		status, seq, value)
}

func piSessionAgentJSONKind(status, seq, kind, value string) string {
	return fmt.Sprintf(`{"result":{"agent":{"pane_id":"w0test:p0a","agent":"pi","agent_status":%q,"state_change_seq":%q,"cwd":"/tmp/pi-work","workspace_id":"","agent_session":{"source":"herdr:pi","agent":"pi","kind":%q,"value":%q}}}}`,
		status, seq, kind, value)
}

func writePiSessionFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// piSessionLine writes a session line the way pi writes it: the whole line
// is JSON, so the text inside its string is escaped the way JSON.stringify
// writes it.
func piSessionLine(role, text string) string {
	body, err := json.Marshal(text)
	if err != nil {
		// Marshal cannot fail for a string; make the error explicit.
		panic(fmt.Sprintf("piSessionLine: %v", err))
	}
	return `{"type":"message","id":"9a234373","parentId":"7dfce086","message":{"role":"` + role + `","content":[{"type":"text","text":` + string(body) + `}]}}`
}

const piWorkingScreen = "working on the task…\n"

func TestSendPiTranscriptProof(t *testing.T) {
	id := "01020304"
	newPromptFixture := func(t *testing.T) string {
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		return peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
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
		// The Delay is the stand-in for pi taking the message: it keeps the
		// fake herdr busy until the writer has appended the line.
		return fakecli.Rule{
			Argv:  []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"},
			Delay: 100,
		}
	}

	t.Run("pi working: the session gains the user line with the id and the screen has no sign: sent, exit 0, no Enter", func(t *testing.T) {
		prompt := newPromptFixture(t)
		sessionsRoot := t.TempDir()
		sessionPath := piTranscriptSessionPath(sessionsRoot)
		writePiSessionFile(t, sessionPath, piSessionLine("user", "earlier conversation")+"\n")
		// The visible screen shows a working turn with no #id anywhere.
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		userLine := piSessionLine("user", prompt)
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), sessionPath, id, userLine)
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !<-done {
			t.Fatal("the session line was not appended after the prompt call")
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("a transcript-proven send sends no Enter: %d enters", enters)
		}
	})
	t.Run("pi working: an assistant line with the id is not proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		sessionsRoot := t.TempDir()
		sessionPath := piTranscriptSessionPath(sessionsRoot)
		writePiSessionFile(t, sessionPath, piSessionLine("user", "earlier conversation")+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assistantLine := piSessionLine("assistant", peer.PeerPrefix+" #"+id+"? I am still working")
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), sessionPath, id, assistantLine)
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if !<-done {
			t.Fatal("the assistant line was not appended after the prompt call")
		}
		// The result is today's screen-based one, with no new warning and no
		// key: a working pi is not put in the claude busy wait.
		if strings.Contains(stderr, "is busy; waiting for its transcript") {
			t.Fatalf("a pi busy wait warning went out: %q", stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("an assistant-cited marker presses nothing: %d enters", enters)
		}
	})
	t.Run("pi working: a toolResult line with the id is not proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		sessionsRoot := t.TempDir()
		sessionPath := piTranscriptSessionPath(sessionsRoot)
		writePiSessionFile(t, sessionPath, piSessionLine("user", "earlier conversation")+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		toolLine := piSessionLine("toolResult", "the brief says "+peer.PeerPrefix+" #"+id+" but I have not started")
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), sessionPath, id, toolLine)
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if !<-done {
			t.Fatal("the toolResult line was not appended after the prompt call")
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("a tool-cited marker presses nothing: %d enters", enters)
		}
	})
	t.Run("pi working: an assistant tool call whose arguments hold a nested user role and the id is not proof", func(t *testing.T) {
		// The tool call's arguments are the tool's own data: a nested
		// "role":"user" and the marker inside them must not be read as the
		// message's role, so the line does not prove delivery and the result
		// is today's screen-based one, not sent.
		prompt := newPromptFixture(t)
		sessionsRoot := t.TempDir()
		sessionPath := piTranscriptSessionPath(sessionsRoot)
		writePiSessionFile(t, sessionPath, piSessionLine("user", "earlier conversation")+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		toolCallLine := `{"type":"message","id":"9a234373","parentId":"7dfce086","message":{"role":"assistant","content":[{"type":"text","text":"running the tool"},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"herdr-soho send w0test:p0a hi","context":{"role":"user","content":[{"type":"text","text":"` + peer.PeerPrefix + ` #` + id + ` nested in the arguments"}]}}}]}}`
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), sessionPath, id, toolCallLine)
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if !<-done {
			t.Fatal("the tool call line was not appended after the prompt call")
		}
		if strings.Contains(stderr, "is busy; waiting for its transcript") {
			t.Fatalf("a pi busy wait warning went out: %q", stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("a nested-role marker presses nothing: %d enters", enters)
		}
	})
	t.Run("pi working: a user line with the id already present before the send is not proof", func(t *testing.T) {
		prompt := newPromptFixture(t)
		sessionsRoot := t.TempDir()
		sessionPath := piTranscriptSessionPath(sessionsRoot)
		// The session already holds a user line with this marker before the
		// send: the pre-send count is one, and a line that does not move the
		// count is not delivery.
		writePiSessionFile(t, sessionPath, piSessionLine("user", peer.PeerOpenTag(id))+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("an already-present marker line presses nothing: %d enters", enters)
		}
	})
	t.Run("pi working: the count growth while the Steering queue line still holds the id is queued, not sent", func(t *testing.T) {
		prompt := newPromptFixture(t)
		sessionsRoot := t.TempDir()
		sessionPath := piTranscriptSessionPath(sessionsRoot)
		writePiSessionFile(t, sessionPath, piSessionLine("user", "earlier conversation")+"\n")
		// The message sits in the Steering queue above the input box: the
		// session already holds the taken line, but the message is not read
		// yet, so the count growth is not arrival.
		screen := piSendScreen("old turn output\nSteering: "+peer.PeerOpenTag("01020304")+" Message from another agent", "")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		userLine := piSessionLine("user", prompt)
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), sessionPath, id, userLine)
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !<-done {
			t.Fatal("the session line was not appended after the prompt call")
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("no Enter may go to a busy pi with an open queue: %d enters", enters)
		}
	})
	t.Run("pi without an agent_session: the screen path is kept and the transcript is never resolved", func(t *testing.T) {
		prompt := newPromptFixture(t)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("pi", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		// Exactly today's message: no new warning for the absent session.
		want := "soho: send: local/w0test:p0a did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane for #01020304 or a reply before sending again\n"
		if code != 15 || stderr != want {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if gets := countGets(t, f); gets != 5 {
			// resolve + screen + pre + the window poll + the pre-Enter one:
			// without an agent_session the pi target makes no transcript get.
			t.Fatalf("gets=%d want 5 (session absent, screen path kept)", gets)
		}
	})
	t.Run("pi with an agent_session kind that is not path: the screen path is kept", func(t *testing.T) {
		prompt := newPromptFixture(t)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSONKind("working", "1", "id", "3f2b8c1a-9d4e-4c7a-b1f0-5e6d7c8b9a0f")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if gets := countGets(t, f); gets != 6 {
			// the transcript-path get happens, finds a kind that is not a
			// local file, and the send falls back to the screen path.
			t.Fatalf("gets=%d want 6 (kind not path, screen path kept)", gets)
		}
	})
	t.Run("pi with a session path but a missing file: the screen path is kept", func(t *testing.T) {
		prompt := newPromptFixture(t)
		sessionPath := piTranscriptSessionPath(t.TempDir()) // the file is never created
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("working", "1", sessionPath)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: piWorkingScreen},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			promptRule(prompt),
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 15 || !strings.Contains(stderr, "did not confirm taking the message (no sign of it in its state or screen)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if gets := countGets(t, f); gets != 6 {
			// the transcript-path get happens, then the missing file falls
			// back to the screen path.
			t.Fatalf("gets=%d want 6 (session path resolves, file missing)", gets)
		}
	})
}

// TestSendRemotePiTranscriptNotArmed: the pi transcript proof is local to the
// session's machine (a machineless `agent get` plus a local file), so it must
// not be armed for a remote target at all — a local pane with the same id
// must not be read as the transcript of the remote pi.
func TestSendRemotePiTranscriptNotArmed(t *testing.T) {
	id := "01020304"
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	defer func() { rand.Reader = oldReader }()
	setSenderHostname(t, "Run2Biz.local")
	// D7: a remote target cannot resolve "local/-" back to this machine, so
	// the header names the pane on the sender's hostname.
	prompt := peer.PeerHeaderRemote("-", "Run2Biz.local", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	userLine := piSessionLine("user", prompt)
	preScreen := "❯ \n"
	// The local pane with the same id: a wrongly armed proof would consult it.
	// Its session file holds the delivered marker line (appended by the
	// writer after the prompt), so the wrong arming would exit 0 on another
	// pi's session.
	sessionsRoot := t.TempDir()
	localSessionPath := filepath.Join(sessionsRoot, "agent", "sessions", "--local-pi--", piTranscriptSession)
	writePiSessionFile(t, localSessionPath, piSessionLine("user", "earlier conversation")+"\n")
	rules := []fakecli.Rule{
		{Argv: []string{"--machine", "windows", "agent", "get", "w3:p1"}, ArgvPrefix: true, Stdout: piSessionAgentJSONRemote("idle", "1")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w3:p1", "--source", "visible"}, ArgvPrefix: true, Stdout: preScreen},
		{Argv: []string{"--machine", "windows", "agent", "read", "w3:p1", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: preScreen},
		// The Delay keeps the fake herdr busy until the writer has appended
		// the planted line (it would be growth for a wrongly armed proof).
		{Argv: []string{"--machine", "windows", "agent", "prompt", "w3:p1", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
		{Argv: []string{"agent", "get", "w3:p1"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("idle", "1", localSessionPath)},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), localSessionPath, id, userLine)
	code, _, stderr := f.run([]string{"send", "windows/w3:p1", "hello"})
	// Today's result for a remote send with no sign in its own state or
	// screen: not 0 on the planted local session.
	want := "soho: send: windows/w3:p1 did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane (herdr --machine windows agent read w3:p1 --source visible) for #01020304 or a reply before sending again\n"
	if code != 15 || stderr != want {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !<-done {
		t.Fatal("the planted session line was not appended after the prompt call")
	}
	if gets := countLocalAgentGets(t, f); gets != 0 {
		// The proof must not be armed for a remote target: no machineless
		// `agent get` at all (and therefore no local file read).
		t.Fatalf("local gets=%d want 0 (transcript proof not armed for a remote target)", gets)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("an unproved remote send presses nothing: %d enters", enters)
	}
}

// piSessionAgentJSONRemote is the remote get's agent JSON: the same shape,
// pane id w3:p1.
func piSessionAgentJSONRemote(status, seq string) string {
	return fmt.Sprintf(`{"result":{"agent":{"pane_id":"w3:p1","agent":"pi","agent_status":%q,"state_change_seq":%q,"cwd":"/remote/pi-work","workspace_id":"","agent_session":{"source":"herdr:pi","agent":"pi","kind":"path","value":"C:/Users/x/.pi/agent/sessions/--remote--/`+piTranscriptSession+`"}}}}`,
		status, seq)
}
