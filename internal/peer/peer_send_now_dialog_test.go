package peer_test

// F19 (rc.14): --now skipped the busy-target wait, but the dialog wait loop
// in the send followed the --timeout budget — the 600 s default stalled a
// send to a blocked target with a dialog on screen for over 5 minutes. With
// --now, a dialog on the target's screen must exit 17 at once, with today's
// message and no key; without --now the waits are untouched.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The blocked target's visible screen: a trust dialog the send must never
// type over.
const sendNowDialogScreen = "Do you trust this folder?\n(y/n)\n"

func TestSendNowDialogExits17WithoutWaiting(t *testing.T) {
	// --now does not wait for the dialog to clear: the send exits 17 on the
	// first dialog check — well below the --timeout — with today's message,
	// no key, and no prompt.
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSON("blocked", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: sendNowDialogScreen},
	})
	started := time.Now()
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "20000", "hello"})
	elapsed := time.Since(started)
	if code != 17 || out != "" {
		t.Fatalf("a --now dialog exits 17 at once: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if want := "soho: send: local/w0test:p0a is showing a dialog; nothing was sent\n"; stderr != want {
		t.Fatalf("the --now dialog keeps today's message: %q", stderr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the --now dialog must not wait for the 20s --timeout, took %s", elapsed)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	// resolve get, visible read, dialog-check get: no wait-loop lap (a lap
	// re-reads the screen and re-checks the status), no prompt, no key.
	if len(calls) != 3 {
		t.Fatalf("the --now dialog exits on the first check, without waiting: %+v", calls)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no key may go to a dialog: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 0 {
		t.Fatalf("nothing was sent: %d prompts", prompts)
	}
}

func TestSendDialogWithoutNowKeepsTodayWaits(t *testing.T) {
	// Without --now a blocked target still waits: the send runs agent wait
	// with the --timeout, and the timeout exits 17 with today's "still
	// blocked" message and the dialog hint.
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSON("blocked", "1")},
		{Argv: []string{"agent", "wait", "w0test:p0a", "--until", "idle", "--until", "done", "--timeout", "3000"}, Code: 1, Stderr: `{"error":{"code":"timeout","message":"agent wait timed out after 3s"}}`},
	})
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--timeout", "3000ms", "hello"})
	if code != 17 || out != "" {
		t.Fatalf("without --now the blocked wait keeps today's 17: code=%d out=%q stderr=%q", code, out, stderr)
	}
	want := "soho: send: local/w0test:p0a is still blocked after 3s; nothing was sent (it may be showing a dialog or waiting for an approval: read its pane before sending anything)\n"
	if stderr != want {
		t.Fatalf("without --now the wait timeout keeps today's message: %q", stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	// resolve get, agent wait with the --timeout, timeout re-resolve get.
	if len(calls) != 3 || len(calls[1].Argv) != 9 || calls[1].Argv[1] != "wait" || calls[1].Argv[8] != "3000" {
		t.Fatalf("without --now the busy wait is still used with the --timeout: %+v", calls)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("the wait timeout presses no key: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 0 {
		t.Fatalf("the wait timeout sends nothing: %d prompts", prompts)
	}
}

func TestSendDialogWithoutNowWaitsTheDialogLoop(t *testing.T) {
	// Without --now, a target that reaches the dialog check (not working or
	// blocked, so no busy wait) still waits for the dialog to clear until the
	// --timeout: the loop laps (screen read + status check) and exits 17 with
	// today's dialog message when the dialog persists.
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: sendNowDialogScreen},
	})
	f.env["SOHO_SEND_POLL_MS"] = "50"
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--timeout", "1000ms", "hello"})
	if code != 17 || out != "" {
		t.Fatalf("the dialog loop without --now keeps today's 17: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if want := "soho: send: local/w0test:p0a is showing a dialog; nothing was sent\n"; stderr != want {
		t.Fatalf("the dialog loop without --now keeps today's message: %q", stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The pre-send get + visible read + get, then at least one loop lap
	// (visible read + get) before the 1 s deadline: the wait ran.
	if len(calls) < 5 {
		t.Fatalf("without --now the dialog loop laps until the --timeout: %+v", calls)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("the dialog loop presses no key: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 0 {
		t.Fatalf("the dialog loop sends nothing: %d prompts", prompts)
	}
}
