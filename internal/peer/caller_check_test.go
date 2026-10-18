package peer_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const sendRefuseLine = "soho: send: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell 4000); the id was inherited from another process, such as a forked session. Nothing was changed. Run soho from the pane that hosts this agent, or set caller_check=warn\n"

// callerSendRules are the process-info rules of the claimed caller pane
// (the send target is w0test:p0a; the caller claims w1:p9).
func callerSendRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"pane", "process-info", "--pane", "w1:p9"}, Stdout: `{"result":{"process_info":{"shell_pid":4000}}}`},
	}
}

// installForkedTree scripts the ancestry of a process that is NOT in the
// claimed pane: the walk reaches the root without the pane's shell (4000).
// It goes through platform.OverrideAncestorParents, the same test point on
// every system (no ps, no process snapshot).
func installForkedTree(t *testing.T) {
	t.Helper()
	t.Cleanup(platform.OverrideAncestorParents(map[int]int{os.Getpid(): 3000, 3000: 1}))
}

// callerSendPrompt is the deterministic prompt the send delivers to the
// fixture target: the sender ref is the claimed caller pane (local/w1:p9)
// and the clock is the package test's fixed peerNow, so the header is
// byte-stable.
func callerSendPrompt(t *testing.T) string {
	t.Helper()
	id := "01020304"
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	t.Cleanup(func() { rand.Reader = oldReader })
	return peer.PeerHeader("local/w1:p9", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
}

func sendCallCount(t *testing.T, f *fixture, argv ...string) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, call := range calls {
		if len(call.Argv) < len(argv) {
			continue
		}
		same := true
		for i, want := range argv {
			if call.Argv[i] != want {
				same = false
				break
			}
		}
		if same {
			n++
		}
	}
	return n
}

// TestCmdSendCallerPaneCheck: the send verifies the caller pane before
// delivering: a forked tree refuses (exit 2, no agent prompt, no
// send-text), warns and delivers under caller_check=warn, and is skipped
// entirely under off.
func TestCmdSendCallerPaneCheck(t *testing.T) {
	t.Run("mismatch with refuse exits 2 before agent prompt or pane send-text", func(t *testing.T) {
		herdr.ResetCallerCheck()
		f := newFixture(t, callerSendRules())
		installForkedTree(t)
		f.env["HERDR_PANE_ID"] = "w1:p9"
		f.env["SOHO_CALLER_CHECK"] = "refuse"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 2 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if stderr != sendRefuseLine {
			t.Fatalf("stderr=%q want %q", stderr, sendRefuseLine)
		}
		if n := sendCallCount(t, f, "agent", "prompt"); n != 0 {
			t.Fatalf("agent prompt called %d times", n)
		}
		if n := sendCallCount(t, f, "pane", "send-text"); n != 0 {
			t.Fatalf("pane send-text called %d times", n)
		}
		if n := sendCallCount(t, f, "pane", "process-info", "--pane", "w1:p9"); n != 1 {
			t.Fatalf("process-info calls=%d want 1", n)
		}
	})
	t.Run("mismatch with warn delivers and logs the friction line", func(t *testing.T) {
		herdr.ResetCallerCheck()
		prompt := callerSendPrompt(t)
		pre := "✻ Cranking out changes…\n"
		queued := claudeSendScreen(prompt, true)
		rules := append([]fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("claude", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: queued},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		}, callerSendRules()...)
		f := newFixture(t, rules)
		installForkedTree(t)
		f.env["HERDR_PANE_ID"] = "w1:p9"
		f.env["SOHO_CALLER_CHECK"] = "warn"
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		if err := os.MkdirAll(filepath.Join(f.env["SOHO_DIR"], "ws-test"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, "soho: warning: send: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell 4000); the id was inherited from another process, such as a forked session. Continuing because caller_check=warn. Run soho from the pane that hosts this agent, or set caller_check=warn") {
			t.Fatalf("stderr missing the warn line: %q", stderr)
		}
		friction, err := os.ReadFile(filepath.Join(f.env["SOHO_DIR"], "ws-test", "friction.log"))
		if err != nil || !strings.Contains(string(friction), "Continuing because caller_check=warn.") {
			t.Fatalf("friction.log=%q err=%v", friction, err)
		}
	})
	t.Run("off makes no process-info call", func(t *testing.T) {
		herdr.ResetCallerCheck()
		prompt := callerSendPrompt(t)
		pre := "✻ Cranking out changes…\n"
		queued := claudeSendScreen(prompt, true)
		rules := append([]fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("claude", "working", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: queued},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		}, callerSendRules()...)
		f := newFixture(t, rules)
		installForkedTree(t)
		f.env["HERDR_PANE_ID"] = "w1:p9"
		f.env["SOHO_CALLER_CHECK"] = "off"
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
		if code != 0 || !strings.HasPrefix(out, "queued for local/w0test:p0a") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if n := sendCallCount(t, f, "pane", "process-info"); n != 0 {
			t.Fatalf("off still made %d process-info calls", n)
		}
	})
}

// TestSendWithoutCallerClaim: no HERDR_PANE_ID (the scheduled-task and
// LaunchAgent case, outside any pane): the check makes no
// `pane process-info` call and refuses nothing; the send proceeds as
// today (DJA-394 decision 5).
func TestSendWithoutCallerClaim(t *testing.T) {
	t.Setenv("SOHO_SEND_WINDOW_MS", "1")
	t.Setenv("SOHO_SEND_POLL_MS", "1")
	id := "01020304"
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	t.Cleanup(func() { rand.Reader = oldReader })
	prompt := peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> go\n" + peer.PeerEndLine(id)
	f := newFixture(t, sendRules(prompt, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSON("working", "2")},
	}))
	delete(f.env, "HERDR_PANE_ID")
	delete(f.env, "SOHO_DELEGATED_PANE")
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "go"})
	if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if n := sendCallCount(t, f, "pane", "process-info"); n != 0 {
		t.Fatalf("without HERDR_PANE_ID the check made %d process-info calls", n)
	}
}
