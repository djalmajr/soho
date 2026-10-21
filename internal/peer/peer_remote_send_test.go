package peer_test

import (
	"bytes"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Remote-target send: the transcript proof is local to the session's machine
// (a machineless `agent get` plus a local file), so it must not be armed for
// a remote target at all — a local pane with the same id must not be read as
// the transcript of the remote claude. And the state-sequence proof must hold
// a remote idle claude whose short turn is over before the first state read.

const remotePane = "windows/w0test:p0a"

func TestSendRemoteClaudeTranscriptNotArmed(t *testing.T) {
	// P2: a local pane with the same id as the remote target. The seeded
	// local transcript holds the delivered marker line (appended by the
	// writer after the prompt, exactly as in the round-1 local proof): under
	// a transcript proof armed for the remote target the send would exit 0
	// on another claude's transcript. Correctly, the remote send is never
	// armed (no machineless get, no file read) and keeps today's result.
	id := "01020304"
	cwd := "/tmp/claude-work"
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	defer func() { rand.Reader = oldReader }()
	setSenderHostname(t, "Run2Biz.local")
	// D7: a remote target cannot resolve "local/-" back to this machine, so
	// the header names the pane on the sender's hostname.
	prompt := peer.PeerHeaderRemote("-", "Run2Biz.local", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	userLine := `{"type":"user","message":{"content":"` + peer.PeerEndLine(id) + `"}}`
	preScreen := "❯ \n"
	rules := []fakecli.Rule{
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: preScreen},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: preScreen},
		// The Delay keeps the fake herdr busy until the writer has appended
		// the planted line (it would be growth for a wrongly armed proof).
		{Argv: []string{"--machine", "windows", "agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
		// The local pane with the same id: the buggy arming consults it.
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("idle", "1", cwd)},
	}
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, userLine)
	code, _, stderr := f.run([]string{"send", remotePane, "hello"})
	// Today's result for a remote send with no sign in its own state or
	// screen: not 0 on the planted local transcript.
	want := "soho: send: windows/w0test:p0a did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane (herdr --machine windows agent read w0test:p0a --source visible) for #01020304 or a reply before sending again\n"
	if code != 15 || stderr != want {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !<-done {
		t.Fatal("the planted transcript line was not appended after the prompt call")
	}
	if gets := countLocalAgentGets(t, f); gets != 0 {
		// The proof must not be armed for a remote target: no machineless
		// `agent get` at all.
		t.Fatalf("local gets=%d want 0 (transcript proof not armed for a remote target)", gets)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("an unproved remote send presses nothing: %d enters", enters)
	}
}

func TestSendRemoteClaudeShortTurnSeqProof(t *testing.T) {
	// The 14:55 case: a remote idle Claude Code takes the prompt and its 4 s
	// turn is over before the first state read of the proof window. The state
	// sequence still moved (the agent ran a turn from an idle baseline), so
	// the send is proved from the seq change even though the status is done
	// again — no Enter goes to the remote idle agent.
	id := "01020304"
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	defer func() { rand.Reader = oldReader }()
	setSenderHostname(t, "Run2Biz.local")
	// D7: a remote target cannot resolve "local/-" back to this machine, so
	// the header names the pane on the sender's hostname.
	prompt := peer.PeerHeaderRemote("-", "Run2Biz.local", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	preScreen := "❯ \n"
	// The remote screen right after the turn: the message's end line in the
	// history and the turn's result, the input box only holds the
	// auto-suggestion.
	postScreen := peer.PeerEndLine(id) + "\n✻ Baked for 4s · done 2:55 PM\n❯ \n"
	rules := []fakecli.Rule{
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: preScreen},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: []string{"--machine", "windows", "agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		// The turn ran 4 s on the remote and is over before the first state
		// read of the window: status done again, sequence moved.
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "done", "2")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: postScreen},
		// The old code's path (seq proof requires working: the marker in the
		// visible history is not proof, the id in the box gets an Enter, the
		// second proof fails): the sequence stays done with the new seq.
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 1, Stdout: postScreen},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: postScreen},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: 4, Stdout: postScreen},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("claude", "done", "2")},
		{Argv: []string{"--machine", "windows", "agent", "send-keys", "w0test:p0a", "enter"}},
		{Argv: []string{"--machine", "windows", "agent", "get", "w0test:p0a"}, Call: 6, Stdout: agentJSONKind("claude", "done", "2")},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Call: 2, Stdout: postScreen},
		{Argv: []string{"--machine", "windows", "agent", "read", "w0test:p0a", "--source", "visible"}, Call: 5, Stdout: postScreen},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	code, out, stderr := f.run([]string{"send", remotePane, "hello"})
	if code != 0 || out != "sent to windows/w0test:p0a\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no Enter may go to a remote claude proved by its seq change: %d enters", enters)
	}
}

// countLocalAgentGets counts the machineless `agent get` calls (the local
// machine's CLI form); remote calls carry the --machine prefix.
func countLocalAgentGets(t *testing.T, f *fixture) int {
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

// countSendKeyEnters counts `agent send-keys … enter` calls on any machine.
func countSendKeyEnters(t *testing.T, f *fixture) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	enters := 0
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
			for _, a := range call.Argv {
				if a == "enter" {
					enters++
				}
			}
		}
	}
	return enters
}
