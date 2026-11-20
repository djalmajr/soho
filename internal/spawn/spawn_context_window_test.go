package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// countCallArgv counts the logged calls whose argv is exactly argv.
func countCallArgv(calls []fakecli.Call, argv ...string) int {
	want := strings.Join(argv, "\x00")
	n := 0
	for _, call := range calls {
		if strings.Join(call.Argv, "\x00") == want {
			n++
		}
	}
	return n
}

// lastCallIndex returns the index of the last call whose argv is exactly
// argv, or -1 when there is none.
func lastCallIndex(calls []fakecli.Call, argv ...string) int {
	want := strings.Join(argv, "\x00")
	idx := -1
	for i, call := range calls {
		if strings.Join(call.Argv, "\x00") == want {
			idx = i
		}
	}
	return idx
}

func sendKeysCalls(calls []fakecli.Call) []fakecli.Call {
	var out []fakecli.Call
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && (call.Argv[1] == "send-keys" || call.Argv[1] == "send-text") {
			out = append(out, call)
		}
	}
	return out
}

func spawnPane(calls []fakecli.Call) string {
	for _, call := range calls {
		if len(call.Argv) >= 3 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			for i := 2; i+1 < len(call.Argv); i++ {
				if call.Argv[i] == "--pane" {
					return call.Argv[i+1]
				}
			}
		}
	}
	return ""
}

// grokScreenRead returns the exact argv herdr.AgentRead uses on the visible
// 40-line screen of agent.
func grokScreenRead(agent string) []string {
	return []string{"agent", "read", agent, "--source", "visible", "--lines", "40"}
}

func TestSpawnGrokContextWindowConfirmed(t *testing.T) {
	// The start-window read (call 1) shows no confirmation; only the reads
	// after the /context-window command (call 2 on) carry grok's line.
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "grok-4.7 ready\n"},
		{Argv: grokScreenRead("worker"), Stdout: "❯ 500k 500000 tokens\nContext window set to 500k\n"},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	pane := spawnPane(calls)
	if pane == "" {
		t.Fatalf("no agent start call: %#v", calls)
	}
	if n := countCallArgv(calls, "pane", "send-text", pane, "/context-window 500k"); n != 1 {
		t.Fatalf("text sent %d times: %#v", n, calls)
	}
	if n := countCallArgv(calls, "pane", "send-keys", pane, "Enter"); n != 1 {
		t.Fatalf("Enter sent %d times: %#v", n, calls)
	}
	start := -1
	for i, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			start = i
			break
		}
	}
	text := lastCallIndex(calls, "pane", "send-text", pane, "/context-window 500k")
	enter := lastCallIndex(calls, "pane", "send-keys", pane, "Enter")
	if !(start >= 0 && start < text && text < enter) {
		t.Fatalf("order start=%d text=%d enter=%d", start, text, enter)
	}
	reads := 0
	for i := enter; i < len(calls); i++ {
		if strings.Join(calls[i].Argv, "\x00") == strings.Join(grokScreenRead("worker"), "\x00") {
			reads++
		}
	}
	if reads == 0 {
		t.Fatal("the screen was never read after the Enter (the confirmation window)")
	}
	if !spawnJSONHasField(t, stdout, `"context_window": "500k"`) || !spawnJSONHasField(t, stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
}

func TestSpawnGrokContextWindowUnconfirmedWarnsAndSucceeds(t *testing.T) {
	f := newSpawnFixture(t, freshSpawnRules()) // every visible read shows no confirmation line
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "500" // bound the reads inside the 10s window
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("unconfirmed window leaked into the JSON: %q", stdout)
	}
	want := `spawn: could not confirm grok's context window 500k for 'worker' (no "Context window set to" line); set it by hand with /context-window`
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr=%q, want %q", stderr, want)
	}
	pane := spawnPane(calls)
	if n := countCallArgv(calls, "pane", "send-text", pane, "/context-window 500k"); n != 1 {
		t.Fatalf("text sent %d times: %#v", n, calls)
	}
	if n := countCallArgv(calls, "pane", "send-keys", pane, "Enter"); n != 1 {
		t.Fatalf("Enter sent %d times: %#v", n, calls)
	}
	// The reads of the confirmation window come after the Enter.
	enter := lastCallIndex(calls, "pane", "send-keys", pane, "Enter")
	if reads := lastCallIndex(calls, grokScreenRead("worker")...); reads < enter {
		t.Fatalf("confirmation reads before the Enter (enter=%d read=%d)", enter, reads)
	}
}

func TestSpawnGrokWithoutContextWindowKeySendsNothing(t *testing.T) {
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("send-keys calls without the key: %#v", got)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("context_window in the JSON without the key: %q", stdout)
	}
}

func TestSpawnGrokContextWindowFromEnvironment(t *testing.T) {
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "grok-4.7 ready\n"},
		{Argv: grokScreenRead("worker"), Stdout: "Context window set to 500k\n"},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_CONTEXT_WINDOW_GROK"] = "500k"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	pane := spawnPane(calls)
	if n := countCallArgv(calls, "pane", "send-text", pane, "/context-window 500k"); n != 1 {
		t.Fatalf("text sent %d times: %#v", n, calls)
	}
	if !spawnJSONHasField(t, stdout, `"context_window": "500k"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
}

func TestSpawnReusedGrokGetsNoContextWindowCommand(t *testing.T) {
	rules := []fakecli.Rule{idleHerdrRule("worker")}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	row := strings.Join([]string{"worker", "p-worker", "grok", "worker", "xai", "1", f.cwd, "now", "grok-4.7", "ask", "worker", "", "", "", "high"}, "\t")
	f.roster(t, row)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"reused": true`) {
		t.Fatalf("expected the live grok to be reused, JSON=%q", stdout)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("a reused grok took the command: %#v", got)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("reused JSON carries context_window: %q", stdout)
	}
}

func TestSpawnNonGrokKindIgnoresContextWindowKey(t *testing.T) {
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: claude\nmodel: opus\neffort: high\napprovals: ask\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	f.ctx.Entries["context_window_claude"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"kind": "claude"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("a non-grok spawn took the command: %#v", got)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("non-grok JSON carries context_window: %q", stdout)
	}
}

func TestSpawnGrokInvalidContextWindowValueSendsNothing(t *testing.T) {
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "abc", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("an invalid value was sent: %#v", got)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("invalid value in the JSON: %q", stdout)
	}
}
