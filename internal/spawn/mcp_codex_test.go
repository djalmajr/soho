package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-392 fatia 2 R2 (P1): for kind codex with mcp=none the spawn runs
// `codex mcp list --json` (the spawn's own PATH and env, 10 s timeout) and
// adds, for every server the listing reports with enabled=true, the pair
// -c mcp_servers.<name>.enabled=false — in list order, at the item-5
// position (after the effort args, before the config and user args). A
// failed listing (codex not found, timeout, exit != 0, non-JSON output)
// warns once and the agent keeps the user's MCP servers; zero enabled
// servers add nothing and warn nothing. Only name and enabled are read:
// the transport command lines may carry credentials.

func codexMcpFixture(t *testing.T) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	f.env["SOHO_LAYOUT"] = "tab"
	return f
}

// installCodexMcpList installs a fake codex whose `mcp list --json` returns
// stdout; every other codex call is a silent success.
func installCodexMcpList(t *testing.T, f spawnFixture, stdout string) {
	t.Helper()
	if _, err := fakecli.Install(t, f.bin, "codex", []fakecli.Rule{
		{Argv: []string{"mcp", "list", "--json"}, Stdout: stdout},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
}

func installCodexMcpListFailing(t *testing.T, f spawnFixture, rule fakecli.Rule) {
	t.Helper()
	rule.Argv = []string{"mcp", "list", "--json"}
	if _, err := fakecli.Install(t, f.bin, "codex", []fakecli.Rule{rule, {AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
}

func codexMcpWarn(reason, agent string) string {
	return "soho: warning: spawn: codex could not list its MCP servers (" + reason + "); '" + agent + "' keeps the user's MCP servers"
}

func stdErrCount(stderr, line string) int {
	count := 0
	for _, l := range strings.Split(stderr, "\n") {
		if l == line {
			count++
		}
	}
	return count
}

func TestCmdSpawnMcpCodexPerServerTranslation(t *testing.T) {
	listing := `[
  {"name":"alpha","enabled":true,"transport":{"command":"/opt/alpha --api-key SECRET-1"}},
  {"name":"beta","enabled":true,"transport":{"url":"http://design.internal:8080"}},
  {"name":"gamma","enabled":false}
]`

	t.Run("exactly the enabled servers become per-server flags, in list order, at the item-5 position", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpList(t, f, listing)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none", "--", "--user-x"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		want := `-m gpt-5 -c model_reasoning_effort="high" -c mcp_servers.alpha.enabled=false -c mcp_servers.beta.enabled=false --user-x`
		if got := strings.Join(tail, " "); got != want {
			t.Fatalf("argv=%q, want %q", got, want)
		}
	})

	t.Run("only name and enabled leave the listing: no other field reaches argv, stdout, stderr or the roster", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpList(t, f, listing)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		roster, _ := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		all := strings.Join([]string{stdout, stderr, strings.Join(herdrCallArgv(t, f), " "), string(roster)}, "\n")
		for _, secret := range []string{"SECRET-1", "design.internal", "transport", "url"} {
			if strings.Contains(all, secret) {
				t.Fatalf("listing data %q leaked into argv/stdout/stderr/roster:\n%s", secret, all)
			}
		}
	})

	t.Run("a nonzero list exit warns with the code and adds no flag", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpListFailing(t, f, fakecli.Rule{Code: 1})
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, codexMcpWarn("exit 1", "worker")); got != 1 {
			t.Fatalf("the exact exit-1 warning is not on stderr once: %q", stderr)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != `-m gpt-5 -c model_reasoning_effort="high"` {
			t.Fatalf("argv=%q, want the listing-free argv", got)
		}
	})

	t.Run("malformed list output warns and adds no flag", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpListFailing(t, f, fakecli.Rule{Stdout: "not-json\n"})
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, codexMcpWarn("non-JSON output", "worker")); got != 1 {
			t.Fatalf("the exact non-JSON warning is not on stderr once: %q", stderr)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != `-m gpt-5 -c model_reasoning_effort="high"` {
			t.Fatalf("argv=%q, want the listing-free argv", got)
		}
	})

	t.Run("a missing codex binary warns and adds no flag", func(t *testing.T) {
		f := codexMcpFixture(t) // no fake codex installed
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, codexMcpWarn("codex not found in PATH", "worker")); got != 1 {
			t.Fatalf("the exact not-found warning is not on stderr once: %q", stderr)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != `-m gpt-5 -c model_reasoning_effort="high"` {
			t.Fatalf("argv=%q, want the listing-free argv", got)
		}
	})

	t.Run("a hung listing hits the 10 s timeout, warns, and adds no flag", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpListFailing(t, f, fakecli.Rule{Delay: 10500})
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, codexMcpWarn("timed out after 10 s", "worker")); got != 1 {
			t.Fatalf("the exact timeout warning is not on stderr once: %q", stderr)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != `-m gpt-5 -c model_reasoning_effort="high"` {
			t.Fatalf("argv=%q, want the listing-free argv", got)
		}
	})

	t.Run("zero enabled servers add nothing and warn nothing", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpList(t, f, `[{"name":"gamma","enabled":false},{"name":"delta","enabled":false}]`)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "could not list its MCP servers") {
			t.Fatalf("zero enabled servers warned: %q", stderr)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != `-m gpt-5 -c model_reasoning_effort="high"` {
			t.Fatalf("argv=%q, want the listing-free argv", got)
		}
	})

	t.Run("mcp=inherit never runs the listing", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpListFailing(t, f, fakecli.Rule{Code: 1}) // any listing call is detectable
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "inherit"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "could not list its MCP servers") {
			t.Fatalf("inherit ran the listing: %q", stderr)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != `-m gpt-5 -c model_reasoning_effort="high"` {
			t.Fatalf("argv=%q, want the listing-free argv", got)
		}
	})
}

func herdrCallArgv(t *testing.T, f spawnFixture) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{}
	for _, call := range calls {
		argv = append(argv, call.Argv...)
	}
	return argv
}

func TestCmdSpawnMcpRosterField(t *testing.T) {
	t.Run("the fresh spawn records the effective choice as the roster's last field", func(t *testing.T) {
		f := codexMcpFixture(t)
		code, _, _, _ := runCmdSpawn(t, f, []string{"implementer"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(roster)), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "#") {
			t.Fatalf("roster=%q", roster)
		}
		fields := strings.Split(lines[1], "\t")
		if len(fields) != 16 {
			t.Fatalf("row has %d fields, want 16: %q", len(fields), lines[1])
		}
		if fields[15] != "none" {
			t.Fatalf("last field=%q, want none (the implementer frontmatter mcp: none): %q", fields[15], lines[1])
		}
	})

	t.Run("a role without the mcp key records inherit", func(t *testing.T) {
		f := codexMcpFixture(t)
		code, _, _, _ := runCmdSpawn(t, f, []string{"designer"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(roster)), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "#") {
			t.Fatalf("roster=%q", roster)
		}
		fields := strings.Split(lines[1], "\t")
		if len(fields) != 16 || fields[15] != "inherit" {
			t.Fatalf("row=%q, want 16 fields ending in inherit", lines[1])
		}
	})
}
