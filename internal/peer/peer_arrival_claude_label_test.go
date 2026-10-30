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

const claudeLabelTop = "────────────────────────────────────────────────────────── Session ─"
const claudeLabelHistory = `❯ [soho:peer:01020304]
  Sender: orchestrator-2 (w16:p1 on Example); kind claude.
  Sent: 2026-10-21T02:14:38Z
  Reply with: soho send <this machine's name in your herdr machine list>/w16:p1 "<your reply>" (this machine is Example)
  A message from another agent follows, each line quoted with "> ".

  > sample message

  [/soho:peer:01020304]`

func claudeLabelVisible(history, box string) string {
	return history + "\n\n⏺ Example response…\n\n· Booping… (51s · ↓ 3.5k tokens · thought for 3s)\n" +
		"                         ✔ Update installed · Restart to update\n" + claudeLabelTop + "\n" + box + "\n" +
		"─────────────────────────────────────────────────────────────────\n" +
		"  [Opus 5.5] █░░░░░░░░░ 10% [5h:6% 2h4m] [7d:9% 6d9h] [main]\n" +
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← 3 agents\n"
}

func claudeLabelFixture(t *testing.T, machine string, screen func(string) string, recentError string) (*fixture, string) {
	t.Helper()
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	t.Cleanup(func() { rand.Reader = oldReader })
	prompt := peer.PeerHeader("local/-", "-", "-", "-", "01020304") + "\n\n> hello\n" + peer.PeerEndLine("01020304")
	prefix := []string{}
	if machine != "local" {
		prefix = []string{"--machine", machine}
		prompt = remotePeerHeader(t, "01020304") + "\n\n> hello\n" + peer.PeerEndLine("01020304")
	}
	args := func(tail ...string) []string { return append(append([]string{}, prefix...), tail...) }
	rules := []fakecli.Rule{
		{Argv: args("agent", "get", "w0test:p0a"), ArgvPrefix: true, Stdout: agentJSONKind("claude", "working", "1")},
		{Argv: args("agent", "read", "w0test:p0a", "--source", "visible"), Call: 1, Stdout: claudeLabelVisible("Earlier response", "❯")},
		{Argv: args("agent", "read", "w0test:p0a", "--source", "visible"), ArgvPrefix: true, Stdout: screen(prompt)},
		{Argv: args("agent", "read", "w0test:p0a", "--source", "recent-unwrapped"), ArgvPrefix: true, Code: 1, Stderr: recentError},
		{Argv: args("agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000")},
		{Argv: args("agent", "send-keys", "w0test:p0a", "enter")},
	}
	f := newFixture(t, rules)
	f.env["USERPROFILE"], f.env["APPDATA"] = f.env["HOME"], f.env["XDG_CONFIG_HOME"]
	f.env["PATH"] = filepath.Dir(f.bin)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	return f, machine + "/w0test:p0a"
}

func TestSendArrivalBusyClaudeLabeled(t *testing.T) {
	for _, tc := range []struct {
		name         string
		screen       func(string) string
		code, enters int
	}{
		{"history", func(string) string { return claudeLabelVisible(claudeLabelHistory, "❯") }, 0, 0},
		{"typed", func(prompt string) string { return claudeLabelVisible(claudeLabelHistory, "❯ "+prompt) }, 15, 1},
		{"unrecognized-box", func(prompt string) string {
			return strings.Replace(claudeLabelVisible(claudeLabelHistory, "❯ "+prompt), claudeLabelTop, "a prose separator", 1)
		}, 15, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, target := claudeLabelFixture(t, "windows", tc.screen, notIdleErr)
			code, out, stderr := f.run([]string{"send", target, "--now", "hello"})
			t.Logf("code=%d stdout=%q stderr=%q", code, out, stderr)
			if code != tc.code || tc.code == 0 && (out != "sent to "+target+"\n" || stderr != "") || tc.code != 0 && out != "" {
				t.Fatalf("unexpected result: code=%d out=%q stderr=%q", code, out, stderr)
			}
			if n := remotePrompts(t, f); n != 1 {
				t.Fatalf("prompt count=%d want 1", n)
			}
			if n := remoteEnters(t, f); n != tc.enters {
				t.Fatalf("Enter count=%d want %d", n, tc.enters)
			}
			if countLocalAgentGets(t, f) != 0 {
				t.Fatal("remote send consulted a local transcript target")
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			recent, visible := 0, 0
			for _, call := range calls {
				a := strings.Join(call.Argv, " ")
				if strings.Contains(a, "agent wait") {
					t.Fatal("--now unexpectedly waited for idle")
				}
				if strings.Contains(a, "--source recent-unwrapped") {
					recent++
				}
				if strings.Contains(a, "--source visible") {
					visible++
				}
			}
			if recent == 0 || visible < 2 {
				t.Fatalf("missing recent-not-idle/visible proof: recent=%d visible=%d", recent, visible)
			}
		})
	}
}

func TestSendReadPaneHint(t *testing.T) {
	for _, machine := range []string{"windows", "local"} {
		for _, variant := range []string{"with-id", "without-id"} {
			t.Run(machine+"/"+variant, func(t *testing.T) {
				errText := notIdleErr
				if variant == "without-id" {
					errText = `{"error":{"code":"agent_read_failed","message":"denied"}}`
				}
				f, target := claudeLabelFixture(t, machine, func(string) string { return claudeLabelVisible("No receipt yet", "❯") }, errText)
				code, out, stderr := f.run([]string{"send", target, "--now", "hello"})
				hint := "read its pane"
				if machine != "local" {
					hint += " (herdr --machine windows agent read w0test:p0a --source visible)"
				}
				want := "soho: send: " + target + " did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; " + hint + " for #01020304 or a reply before sending again\n"
				if variant == "without-id" {
					want = "soho: send: could not confirm that " + target + " took the message (agent_read_failed: denied); " + hint + " before sending again\n"
				}
				if code != 15 || out != "" || stderr != want {
					t.Fatalf("code=%d out=%q stderr=%q want=%q", code, out, stderr, want)
				}
			})
		}
	}
}
