package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func mcpWarnPrefix(kind string) string {
	return "soho: warning: spawn: " + kind + " cannot turn MCP servers off per process; '"
}

// startTail returns the agent start argv after the bare "--" separator.
func startTail(t *testing.T, f spawnFixture) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if len(call.Argv) < 3 || call.Argv[0] != "agent" || call.Argv[1] != "start" {
			continue
		}
		for i, arg := range call.Argv {
			if arg == "--" {
				return call.Argv[i+1:]
			}
		}
		return nil
	}
	t.Fatal("no agent start call recorded")
	return nil
}

func TestCmdSpawnMcpPerRoleTranslation(t *testing.T) {
	t.Run("the frontmatter default mcp=none warns for an unsupported kind and changes no argv", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		warned := 0
		for _, line := range strings.Split(stderr, "\n") {
			if strings.HasPrefix(line, mcpWarnPrefix("grok")) && strings.HasSuffix(line, "' keeps the user's MCP servers") {
				warned++
			}
		}
		if warned != 1 {
			t.Fatalf("the exact MCP warning is not on stderr once: %q", stderr)
		}
		tail := startTail(t, f)
		for _, arg := range tail {
			if arg == "--no-mcp" || arg == "--strict-mcp-config" || arg == "mcp_servers={}" {
				t.Fatalf("grok argv carries an MCP flag: %q", tail)
			}
		}
	})

	t.Run("an unsupported kind with no mcp key (inherit) stays silent", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"designer"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "cannot turn MCP servers off per process") {
			t.Fatalf("inherit warned: %q", stderr)
		}
	})

	t.Run("--mcp inherit beats the frontmatter mcp=none", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "pi", "--effort", "high", "--mcp", "inherit"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "cannot turn MCP servers off per process") {
			t.Fatalf("the flag was ignored: %q", stderr)
		}
		tail := startTail(t, f)
		if len(tail) != 2 || tail[0] != "--thinking" || tail[1] != "high" {
			t.Fatalf("argv=%q, want [--thinking high] with no MCP flag", tail)
		}
	})

	t.Run("the role config beats the frontmatter", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		f.ctx.Entries["role_implementer_mcp"] = core.ConfigEntry{Value: "inherit", Source: "user"}
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"implementer"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "cannot turn MCP servers off per process") {
			t.Fatalf("the role config was ignored: %q", stderr)
		}
	})

	t.Run("the flag beats the role config", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		f.ctx.Entries["role_implementer_mcp"] = core.ConfigEntry{Value: "inherit", Source: "user"}
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "cannot turn MCP servers off per process") {
			t.Fatalf("the flag was ignored: %q", stderr)
		}
	})

	t.Run("--mcp off is a usage error before any herdr call", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"implementer", "--mcp", "off"})
		if code != 2 || !strings.Contains(stderr, "invalid --mcp 'off' (inherit|none)") {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if len(calls) != 0 {
			t.Fatalf("the usage error touched herdr: %#v", calls)
		}
	})

	t.Run("a given --pane spawn carries the flag too", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--pane", "w0test:p0a", "--kind", "pi", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		if len(tail) != 3 || tail[0] != "--thinking" || tail[1] != "high" || tail[2] != "--no-mcp" {
			t.Fatalf("argv=%q, want [--thinking high --no-mcp]", tail)
		}
	})
}

func TestCmdSpawnMcpArgvPosition(t *testing.T) {
	mk := func(t *testing.T) spawnFixture {
		t.Helper()
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		return f
	}

	t.Run("pi: the flag goes right after the effort args", func(t *testing.T) {
		f := mk(t)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != "--thinking high --no-mcp" {
			t.Fatalf("argv=%q, want --thinking high --no-mcp", got)
		}
	})

	t.Run("pi: the flag precedes the args.<kind> of the config", func(t *testing.T) {
		f := mk(t)
		f.ctx.Entries["args_pi"] = core.ConfigEntry{Value: "--config-x", Source: "project"}
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != "--thinking high --no-mcp --config-x" {
			t.Fatalf("argv=%q, want the flag before the config args", got)
		}
	})

	t.Run("codex: the per-server flags follow the effort config", func(t *testing.T) {
		f := mk(t)
		installCodexMcpList(t, f, `[{"name":"alpha","enabled":true,"transport":{"command":"/opt/x --api-key SECRET-1"}}]`)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		want := `-m gpt-5 -c model_reasoning_effort="high" -c mcp_servers.alpha.enabled=false`
		if got := strings.Join(tail, " "); got != want {
			t.Fatalf("argv=%q, want %q", got, want)
		}
	})

	t.Run("claude: the flag goes right after the effort flag", func(t *testing.T) {
		f := mk(t)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "claude", "--effort", "high", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != "--effort high --strict-mcp-config" {
			t.Fatalf("argv=%q, want --effort high --strict-mcp-config", got)
		}
	})

	t.Run("the user args after -- come after the flag", func(t *testing.T) {
		f := mk(t)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "none", "--", "--user-x"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != "--thinking high --no-mcp --user-x" {
			t.Fatalf("argv=%q, want --thinking high --no-mcp --user-x", got)
		}
	})

	t.Run("mcp=inherit adds nothing, with or without a frontmatter key", func(t *testing.T) {
		f := mk(t)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := startTail(t, f)
		if got := strings.Join(tail, " "); got != "--thinking high" {
			t.Fatalf("argv=%q, want --thinking high", got)
		}
	})
}

func TestResolveMcpPrecedenceChain(t *testing.T) {
	f := newSpawnFixture(t, nil)
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\nmcp: none\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.ctx.Entries["role_worker_mcp"] = core.ConfigEntry{Value: "inherit", Source: "user"}
	f.ctx.Entries["lane_build_mcp"] = core.ConfigEntry{Value: "none", Source: "user"}

	if got := ResolveMcp("", "", "worker", f.ctx, f.env, f.cwd); got != "inherit" {
		t.Fatalf("no flag, no lane: role config got=%q, want inherit (frontmatter is last)", got)
	}
	if got := ResolveMcp("", "build", "worker", f.ctx, f.env, f.cwd); got != "none" {
		t.Fatalf("lane beat the role config: got=%q, want none", got)
	}
	if got := ResolveMcp("inherit", "build", "worker", f.ctx, f.env, f.cwd); got != "inherit" {
		t.Fatalf("the flag beat the lane: got=%q, want inherit", got)
	}
	if got := ResolveMcp("none", "build", "worker", f.ctx, f.env, f.cwd); got != "none" {
		t.Fatalf("the flag none: got=%q", got)
	}
	// The frontmatter is the floor: it wins when no layer above it speaks.
	delete(f.ctx.Entries, "role_worker_mcp")
	if got := ResolveMcp("", "", "worker", f.ctx, f.env, f.cwd); got != "none" {
		t.Fatalf("frontmatter got=%q, want none", got)
	}
	// A role without the key and no config falls through to inherit.
	if got := ResolveMcp("", "", "scouter", f.ctx, f.env, f.cwd); got != "inherit" {
		t.Fatalf("absent got=%q, want inherit", got)
	}
}
