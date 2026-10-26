package spawn

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnStartupTrustHintExactNoInput(t *testing.T) {
	for _, tc := range []struct{ name, screen, want string }{
		{"cursor", cursorNativeTrustScreen(t), `soho: warning: agent 'implementer' (cursor) startup is not ready: it is showing the workspace trust dialog (state idle); no input was sent — read the pane (herdr agent read implementer --source visible); ask the user before answering; to trust it: herdr agent send-keys implementer a (answers "Trust this workspace"); then herdr agent wait implementer --timeout 60000` + "\n"},
		{"unknown", "Do you trust the contents of this directory?\nTrust this workspace\nQuit\n", "soho: warning: agent 'implementer' (cursor) startup is not ready: it is showing the workspace trust dialog (state idle); no input was sent — read the pane (herdr agent read implementer --source visible) and answer it yourself (herdr agent send-keys implementer <keys>; herdr agent wait implementer --timeout 60000)\n"},
		{"mcp", "MCP Server Approval Required\n[a] Approve all\n", "soho: warning: agent 'implementer' (cursor) startup is not ready: Cursor requires approval of MCP servers; the operator must answer in the pane (state idle); no input was sent — read the pane (herdr agent read implementer --source visible) and answer it yourself (herdr agent send-keys implementer <keys>; herdr agent wait implementer --timeout 60000)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, append(cursorFreshRules(tc.screen, false), freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			f.env["TMPDIR"] = f.root
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(key, f.env[key])
			}
			f.env["SOHO_WAIT_POLL_MS"] = "50"
			code, out, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
			if code != 7 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q want line=%q", code, out, stderr, tc.want)
			}
			assertNoInputIntoTheDialog(t, calls)
		})
	}
}

func TestSpawnGrokBlockedTrustHintAndGenericNoInput(t *testing.T) {
	for _, tc := range []struct{ name, screen, want string }{
		{"trust", grokTrustDialogScreen, `soho: warning: agent 'worker' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering; to trust it: herdr agent send-keys worker y (answers "Yes, proceed (y)"); then herdr agent wait worker --timeout 60000` + "\n"},
		{"unknown", "  Yes, proceed (y)\n  No, keep going (n)\n", "soho: warning: agent 'worker' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: herdr agent send-keys worker <keys>; herdr agent wait worker --timeout 60000\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: tc.screen}}
			if tc.name == "unknown" {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Code: 1, Stderr: "agent_not_ready: question\n"})
			}
			f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			f.env["TMPDIR"] = f.root
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(key, f.env[key])
			}
			code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
			if code != 7 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q want line=%q", code, out, stderr, tc.want)
			}
			if got := sendKeysCalls(calls); len(got) != 0 {
				t.Fatalf("unexpected keys: %#v", got)
			}
		})
	}
}

func TestReview322SpawnQuotedGrokUsesGenericHint(t *testing.T) {
	screen := "The report quotes this old dialog:\n" + grokTrustDialogScreen + "\n❯ Continue reviewing\n"
	f := newSpawnFixture(t, append([]fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: screen}}, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["TMPDIR"] = f.root
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(key, f.env[key])
	}
	code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 7 || !strings.Contains(out, "blocked_at_startup") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("sent input: %#v", got)
	}
	want := "soho: warning: agent 'worker' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: herdr agent send-keys worker <keys>; herdr agent wait worker --timeout 60000\n"
	t.Logf("spawn exit=%d; zero send-keys; warning=%s", code, stderr)
	if !strings.Contains(stderr, want) {
		t.Fatal("quoted report produced a trust key instead of unchanged generic line")
	}
}

func TestReview322LateGrokTrustNamesKey(t *testing.T) {
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "loading grok\n"},
		{Argv: grokScreenRead("worker"), Stdout: grokTrustDialogScreen},
	}
	f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["TMPDIR"] = f.root
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(key, f.env[key])
	}
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 7 || !strings.Contains(out, "blocked_at_startup") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("sent input: %#v", got)
	}
	if !strings.Contains(out, grokTrustDialogScreen) {
		t.Fatalf("dialog not printed: %q", out)
	}
	t.Logf("loading -> trust dialog; spawn exit=%d; zero send-keys; warning=%s", code, stderr)
	if !strings.Contains(stderr, "herdr agent send-keys worker y (answers \"Yes, proceed (y)\")") {
		t.Fatal("recognized late trust dialog did not acquire its exact key hint")
	}
}
