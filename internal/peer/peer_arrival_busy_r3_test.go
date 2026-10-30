package peer_test

import (
	"bytes"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSendArrivalBusyR3GrowthUnreadableWarning(t *testing.T) {
	// P2: the transcript wait applied only to windows of at least a second.
	// A sub-second window is a valid configuration: with the transcript
	// proof armed and the target still working, the user line that lands
	// after the 500 ms window must prove the arrival before the deadline.
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
	// The transcript line holds the whole peer prompt (the opening tag
	// line included), the way Claude Code records the taken message.
	userLine := transcriptUserLine(t, prompt)
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: r2WorkingScreen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stderr: "capture failed", Code: 1},
		// A working claude's alternate-screen history cannot be captured:
		// the recent read fails and falls back to the visible screen.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
		{Argv: promptArgv(prompt)},
	}
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "500", "50"
	done := appendAfterPromptDelayed(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, userLine, 100)
	started := time.Now()
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1200", "hello"})
	t.Logf("elapsed=%s code=%d out=%q stderr=%q", time.Since(started), code, out, stderr)
	if code != 15 || out != "" {
		t.Fatalf("expected unreadable failure: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if time.Since(started) < 1200*time.Millisecond {
		t.Fatal("fixture did not reach real wait")
	}
	if !strings.Contains(stderr, "is busy; waiting") {
		t.Fatalf("real wait was silent after transcript growth: %q", stderr)
	}
	if !<-done {
		t.Fatal("the transcript line was not appended after the prompt call")
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no key is sent in the extended wait: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the busy path never resends the text: %d prompts", prompts)
	}
}
