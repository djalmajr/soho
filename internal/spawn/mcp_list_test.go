package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-693: the mcp choice is inherit, none, or a comma-separated list of
// server names. A list keeps the named servers and turns the others off,
// per kind: codex through the same per-server overrides as mcp=none (a name
// absent from the listing warns and the spawn continues), pi through
// `pi mcp list --json` (--no-mcp when none of the names is present, the
// cannot-limit warning when at least one is, and the listing-failure warning
// when the read fails), claude with the cannot-limit warning (the list needs
// the server definition in JSON, not proved), and every other kind with the
// existing no-flag warning. inherit and absence change no argv, byte for
// byte; an invalid value exits 2 before any pane.

func listWarnPrefix(kind string) string {
	return "soho: warning: spawn: " + kind + " cannot limit its MCP servers to the requested list per process; '"
}

func mcpListFixture(t *testing.T) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	f.env["SOHO_LAYOUT"] = "tab"
	return f
}

func tailHasMCPArg(tail []string) bool {
	for _, arg := range tail {
		if arg == "--no-mcp" || arg == "--strict-mcp-config" || strings.HasPrefix(arg, "mcp_servers.") || strings.HasPrefix(arg, "-c mcp_servers.") {
			return true
		}
	}
	return false
}

// Scenario 1: ui-reviewer, sub-orchestrator and planner carry mcp: none in
// the shipped frontmatter and open without MCP on claude, codex and pi,
// exactly like an explicit --mcp none. The planner never opens a pane (it
// exits 12 before the resolution: the orchestrator is the planner), so its
// value is proved through ResolveMcp over the shipped file.
func TestShippedNoneRolesOpenWithoutMcpLikeNone(t *testing.T) {
	isolateCodexMcpTemp(t)
	t.Run("the planner's frontmatter resolves to none", func(t *testing.T) {
		f := mcpListFixture(t)
		if got := ResolveMcp("", "", "planner", f.ctx, f.env, f.cwd); got != "none" {
			t.Fatalf("planner mcp=%q, want none", got)
		}
	})
	roles := []string{"ui-reviewer", "sub-orchestrator"}
	listing := `[{"name":"alpha","enabled":true},{"name":"beta","enabled":true}]`
	for _, role := range roles {
		for _, kind := range []string{"claude", "codex", "pi"} {
			t.Run(role+" as "+kind, func(t *testing.T) {
				f1 := mcpListFixture(t)
				f2 := mcpListFixture(t)
				if kind == "codex" {
					installCodexMcpList(t, f1, listing)
					installCodexMcpList(t, f2, listing)
				}
				kindArgs := []string{"--kind", kind, "--model", "grok-4.7"}
				if kind == "codex" {
					kindArgs = append(kindArgs, "--effort", "high")
				}
				code1, _, stderr1, _ := runCmdSpawn(t, f1, append([]string{role}, kindArgs...))
				if code1 != 0 {
					t.Fatalf("frontmatter spawn exit=%d stderr=%q", code1, stderr1)
				}
				code2, _, stderr2, _ := runCmdSpawn(t, f2, append(append([]string{role}, kindArgs...), "--mcp", "none"))
				if code2 != 0 {
					t.Fatalf("--mcp none spawn exit=%d stderr=%q", code2, stderr2)
				}
				if got, want := strings.Join(startTail(t, f1), " "), strings.Join(startTail(t, f2), " "); got != want {
					t.Fatalf("frontmatter argv=%q differs from --mcp none argv=%q", got, want)
				}
				switch kind {
				case "claude":
					if !strings.Contains(strings.Join(startTail(t, f1), " "), "--strict-mcp-config") {
						t.Fatalf("claude argv lacks --strict-mcp-config: %q", startTail(t, f1))
					}
				case "pi":
					if !strings.Contains(strings.Join(startTail(t, f1), " "), "--no-mcp") {
						t.Fatalf("pi argv lacks --no-mcp: %q", startTail(t, f1))
					}
				case "codex":
					tail := strings.Join(startTail(t, f1), " ")
					if !strings.Contains(tail, "-c mcp_servers.alpha.enabled=false -c mcp_servers.beta.enabled=false") {
						t.Fatalf("codex argv lacks the per-server off pairs: %q", tail)
					}
				}
			})
		}
	}
}

// Scenario 2: --mcp a,b and role.<r>.mcp=a,b resolve by the same precedence
// as the other values.
func TestResolveMcpListPrecedence(t *testing.T) {
	f := newSpawnFixture(t, nil)
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\nmcp: a,b\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.ctx.Entries["role_worker_mcp"] = core.ConfigEntry{Value: "c,d", Source: "user"}
	f.ctx.Entries["lane_build_mcp"] = core.ConfigEntry{Value: "e,f", Source: "user"}

	if got := ResolveMcp("", "build", "worker", f.ctx, f.env, f.cwd); got != "e,f" {
		t.Fatalf("lane got=%q, want e,f", got)
	}
	if got := ResolveMcp("", "", "worker", f.ctx, f.env, f.cwd); got != "c,d" {
		t.Fatalf("role config got=%q, want c,d", got)
	}
	if got := ResolveMcp("a,b", "build", "worker", f.ctx, f.env, f.cwd); got != "a,b" {
		t.Fatalf("flag got=%q, want a,b", got)
	}
	delete(f.ctx.Entries, "role_worker_mcp")
	if got := ResolveMcp("", "", "worker", f.ctx, f.env, f.cwd); got != "a,b" {
		t.Fatalf("frontmatter got=%q, want a,b", got)
	}
}

// Scenario 3: a name outside the rule exits 2 before any pane is opened,
// citing the filtered value — from the flag and from the resolved layers.
func TestMcpInvalidNameExitsTwoBeforePane(t *testing.T) {
	isolateCodexMcpTemp(t)
	for _, value := range []string{"a b", ",", "none,x", "inherit,x", "a,,b"} {
		t.Run("flag "+value, func(t *testing.T) {
			f := mcpListFixture(t)
			code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--mcp", value})
			if code != 2 {
				t.Fatalf("exit=%d, want 2: stderr=%q", code, stderr)
			}
			if !strings.Contains(stderr, "invalid --mcp '"+value+"'") {
				t.Fatalf("the message does not cite the value: %q", stderr)
			}
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			for _, call := range calls {
				if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
					t.Fatalf("the usage error opened a pane: %#v", call.Argv)
				}
			}
		})
	}
	t.Run("a resolved role config with an invalid name exits 2", func(t *testing.T) {
		f := mcpListFixture(t)
		f.ctx.Entries["role_worker_mcp"] = core.ConfigEntry{Value: "a b", Source: "user"}
		code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"worker"})
		if code != 2 || !strings.Contains(stderr, "invalid mcp 'a b'") {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				t.Fatalf("the usage error opened a pane: %#v", call.Argv)
			}
		}
	})
	t.Run("an empty --mcp value exits 2", func(t *testing.T) {
		f := mcpListFixture(t)
		code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--mcp", ""})
		if code != 2 || !strings.Contains(stderr, "empty name in the list") {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
	})
	t.Run("duplicated names are accepted and count once", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpList(t, f, `[{"name":"alpha","enabled":true},{"name":"beta","enabled":true}]`)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "alpha,alpha"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		if strings.Count(tail, "-c mcp_servers.alpha.enabled=false") != 0 || !strings.Contains(tail, "-c mcp_servers.beta.enabled=false") {
			t.Fatalf("argv=%q, want only beta off", tail)
		}
	})
}

// Scenario 4: codex with the list a turns off b and c, not a; stayOn still
// holds; the order is the listing's.
func TestCodexMcpListKeepsTheNamedServers(t *testing.T) {
	isolateCodexMcpTemp(t)
	listing := `[
  {"name":"alpha","enabled":true,"transport":{"command":"/opt/alpha --api-key SECRET-1"}},
  {"name":"beta","enabled":true,"transport":{"url":"http://beta.internal:8080"}},
  {"name":"gamma","enabled":false}
]`

	t.Run("the named server stays on, the enabled others are off in list order", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpScript(t, f,
			listRule(listing),
			checkOK("beta"),
		)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "alpha"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		want := `-m gpt-5 -c model_reasoning_effort="high" -c features.apps=false -c mcp_servers.beta.enabled=false`
		if tail != want {
			t.Fatalf("argv=%q, want %q", tail, want)
		}
	})

	t.Run("a list of two keeps both, in listing order, before the user args", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpScript(t, f,
			listRule(`[{"name":"alpha","enabled":true},{"name":"beta","enabled":true},{"name":"gamma","enabled":true},{"name":"delta","enabled":true}]`),
			checkOK("gamma", "delta"),
		)
		code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "beta,alpha", "--", "--user-x"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		tail := strings.Join(startTail(t, f), " ")
		want := `-m gpt-5 -c model_reasoning_effort="high" -c features.apps=false -c mcp_servers.gamma.enabled=false -c mcp_servers.delta.enabled=false --user-x`
		if tail != want {
			t.Fatalf("argv=%q, want %q", tail, want)
		}
	})

	t.Run("stayOn still drops the server the override check rejects", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpScript(t, f,
			listRule(`[{"name":"alpha","enabled":true},{"name":"beta","enabled":true},{"name":"sigma","enabled":true}]`),
			checkNaming("sigma", "SECRET-2", "beta", "sigma"),
			checkOK("beta"),
		)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "alpha"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		want := `-m gpt-5 -c model_reasoning_effort="high" -c features.apps=false -c mcp_servers.beta.enabled=false`
		if tail != want {
			t.Fatalf("argv=%q, want %q (sigma stays on)", tail, want)
		}
		wantWarn := "soho: warning: spawn: codex cannot turn off MCP server(s) sigma for 'worker' (they are not defined in its config.toml); they stay on; the others outside the list are off"
		if got := stdErrCount(stderr, wantWarn); got != 1 {
			t.Fatalf("the stayOn warning is not on stderr once: %q", stderr)
		}
	})

	// Scenario 5: a name that does not exist in the listing warns, and the
	// spawn continues.
	t.Run("a name absent from the listing warns and the spawn continues", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpScript(t, f,
			listRule(listing),
			checkOK("beta"),
		)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "alpha,zed"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantWarn := "soho: warning: spawn: codex has no MCP server named zed for 'worker'"
		if got := stdErrCount(stderr, wantWarn); got != 1 {
			t.Fatalf("the exact missing-name warning is not on stderr once: %q", stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		want := `-m gpt-5 -c model_reasoning_effort="high" -c features.apps=false -c mcp_servers.beta.enabled=false`
		if tail != want {
			t.Fatalf("argv=%q, want %q", tail, want)
		}
	})
}

// Scenario 6: pi with a list reads `pi mcp list --json`; none of the names
// present means --no-mcp, any present means the servers stay with the
// cannot-limit warning, and a failed read means the servers stay with the
// listing-failure warning. The read failure never drops the spawn.
func TestPiMcpList(t *testing.T) {
	installPi := func(t *testing.T, f spawnFixture, stdout string) {
		t.Helper()
		if _, err := fakecli.Install(t, f.bin, "pi", []fakecli.Rule{
			{Argv: []string{"mcp", "list", "--json"}, Stdout: stdout},
			{AnyArgs: true},
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("none of the names present: --no-mcp, no warning", func(t *testing.T) {
		f := mcpListFixture(t)
		installPi(t, f, `{"servers":[{"name":"gamma"},{"name":"delta"}]}`)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "alpha,beta"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		if tail != "--thinking high --no-mcp" {
			t.Fatalf("argv=%q, want --thinking high --no-mcp", tail)
		}
		if strings.Contains(stderr, "cannot limit") || strings.Contains(stderr, "could not list") {
			t.Fatalf("unexpected warning: %q", stderr)
		}
	})

	t.Run("one of the names present: no flag, the cannot-limit warning", func(t *testing.T) {
		f := mcpListFixture(t)
		installPi(t, f, `{"servers":[{"name":"gamma"},{"name":"beta"}]}`)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "alpha,beta"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		if tail != "--thinking high" {
			t.Fatalf("argv=%q, want the listing-free argv", tail)
		}
		wantWarn := listWarnPrefix("pi") + "worker' keeps the user's MCP servers"
		if got := stdErrCount(stderr, wantWarn); got != 1 {
			t.Fatalf("the exact cannot-limit warning is not on stderr once: %q", stderr)
		}
	})

	t.Run("a read failure keeps the servers with the listing-failure warning", func(t *testing.T) {
		f := mcpListFixture(t) // no fake pi: the read fails with not-found
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "alpha"})
		if code != 0 {
			t.Fatalf("a read failure dropped the spawn: exit=%d", code)
		}
		tail := strings.Join(startTail(t, f), " ")
		if tail != "--thinking high" {
			t.Fatalf("argv=%q, want the flag-free argv", tail)
		}
		wantWarn := "soho: warning: spawn: pi could not list its MCP servers (pi not found in PATH); 'worker' keeps the user's MCP servers"
		if got := stdErrCount(stderr, wantWarn); got != 1 {
			t.Fatalf("the exact read-failure warning is not on stderr once: %q", stderr)
		}
	})

	for _, tc := range []struct {
		name, reason string
		rule         fakecli.Rule
	}{
		{"a nonzero exit keeps the servers", "exit 3", fakecli.Rule{Argv: []string{"mcp", "list", "--json"}, Code: 3}},
		{"a non-JSON listing keeps the servers", "non-JSON output", fakecli.Rule{Argv: []string{"mcp", "list", "--json"}, Stdout: "not json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mcpListFixture(t)
			if _, err := fakecli.Install(t, f.bin, "pi", []fakecli.Rule{tc.rule, {AnyArgs: true}}); err != nil {
				t.Fatal(err)
			}
			code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "alpha"})
			if code != 0 {
				t.Fatalf("a read failure dropped the spawn: exit=%d", code)
			}
			if tail := strings.Join(startTail(t, f), " "); tail != "--thinking high" {
				t.Fatalf("argv=%q, want the flag-free argv", tail)
			}
			want := "soho: warning: spawn: pi could not list its MCP servers (" + tc.reason + "); 'worker' keeps the user's MCP servers"
			if got := stdErrCount(stderr, want); got != 1 {
				t.Fatalf("the exact read-failure warning is not on stderr once: %q", stderr)
			}
		})
	}

	t.Run("a hung read hits the 10 s timeout and keeps the servers", func(t *testing.T) {
		f := mcpListFixture(t)
		if _, err := fakecli.Install(t, f.bin, "pi", []fakecli.Rule{
			{Argv: []string{"mcp", "list", "--json"}, Delay: 10500},
			{AnyArgs: true},
		}); err != nil {
			t.Fatal(err)
		}
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "alpha"})
		if code != 0 {
			t.Fatalf("a timed-out read dropped the spawn: exit=%d", code)
		}
		wantWarn := "soho: warning: spawn: pi could not list its MCP servers (timed out after 10 s); 'worker' keeps the user's MCP servers"
		if got := stdErrCount(stderr, wantWarn); got != 1 {
			t.Fatalf("the exact timeout warning is not on stderr once: %q", stderr)
		}
	})
}

// Scenario 7: claude with a list keeps the servers with the cannot-limit
// warning and passes no --strict-mcp-config.
func TestClaudeMcpListKeepsServersWithWarning(t *testing.T) {
	f := mcpListFixture(t)
	code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "claude", "--effort", "high", "--mcp", "alpha,beta"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	tail := strings.Join(startTail(t, f), " ")
	if strings.Contains(tail, "--strict-mcp-config") {
		t.Fatalf("claude argv carries the off flag: %q", tail)
	}
	wantWarn := listWarnPrefix("claude") + "worker' keeps the user's MCP servers"
	if got := stdErrCount(stderr, wantWarn); got != 1 {
		t.Fatalf("the exact cannot-limit warning is not on stderr once: %q", stderr)
	}
}

// Scenario 8: grok and cursor with a list warn with the existing no-flag
// warning, as mcp=none does today.
func TestNoFlagKindsWarnOnMcpList(t *testing.T) {
	for _, kind := range []string{"grok", "cursor"} {
		t.Run(kind, func(t *testing.T) {
			f := mcpListFixture(t)
			args := []string{"worker", "--kind", kind}
			if kind == "cursor" {
				args = append(args, "--model", "grok-4.7")
			}
			code, _, stderr, _ := runCmdSpawn(t, f, append(args, "--mcp", "alpha"))
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			wantWarn := "soho: warning: spawn: " + kind + " cannot turn MCP servers off per process; 'worker' keeps the user's MCP servers"
			if got := stdErrCount(stderr, wantWarn); got != 1 {
				t.Fatalf("the exact no-flag warning is not on stderr once: %q", stderr)
			}
			if tailHasMCPArg(startTail(t, f)) {
				t.Fatalf("argv carries an MCP flag: %q", startTail(t, f))
			}
		})
	}
}

// Scenario 9: inherit and an absent key change no argv, byte for byte.
func TestMcpInheritLeavesTheArgvByteForByte(t *testing.T) {
	isolateCodexMcpTemp(t)
	for _, kind := range []string{"pi", "grok", "codex"} {
		t.Run(kind, func(t *testing.T) {
			f1 := mcpListFixture(t)
			f2 := mcpListFixture(t)
			if kind == "codex" {
				// Any accidental listing call is detectable on both sides.
				installCodexMcpListFailing(t, f1, fakecli.Rule{Code: 1})
				installCodexMcpListFailing(t, f2, fakecli.Rule{Code: 1})
			}
			args := []string{"worker", "--kind", kind}
			if kind == "codex" {
				args = append(args, "--model", "gpt-5", "--effort", "high")
			}
			code1, _, _, _ := runCmdSpawn(t, f1, append(args, "--mcp", "inherit"))
			if code1 != 0 {
				t.Fatalf("inherit exit=%d", code1)
			}
			code2, _, _, _ := runCmdSpawn(t, f2, args)
			if code2 != 0 {
				t.Fatalf("absent exit=%d", code2)
			}
			if got, want := strings.Join(startTail(t, f1), " "), strings.Join(startTail(t, f2), " "); got != want {
				t.Fatalf("inherit argv=%q differs from the absent argv=%q", got, want)
			}
			if tailHasMCPArg(startTail(t, f1)) {
				t.Fatalf("inherit argv carries an MCP flag: %q", startTail(t, f1))
			}
		})
	}
	t.Run("inherit never runs the pi listing", func(t *testing.T) {
		f := mcpListFixture(t)
		if _, err := fakecli.Install(t, f.bin, "pi", []fakecli.Rule{
			{Argv: []string{"mcp", "list", "--json"}, Code: 1},
			{AnyArgs: true},
		}); err != nil {
			t.Fatal(err)
		}
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "inherit"})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if strings.Contains(stderr, "could not list its MCP servers") {
			t.Fatalf("inherit ran the listing: %q", stderr)
		}
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "pi.calls.jsonl"))
		for _, call := range calls {
			if len(call.Argv) > 0 && call.Argv[0] == "mcp" {
				t.Fatalf("inherit ran the listing: %#v", call.Argv)
			}
		}
	})
}

// Scenario 10: the shipped designer keeps open-pencil on codex and turns the
// others off; the inspector keeps the two browser servers.
func TestShippedListRolesKeepTheNamedServers(t *testing.T) {
	isolateCodexMcpTemp(t)

	t.Run("designer keeps open-pencil and turns the others off", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpScript(t, f,
			listRule(`[{"name":"open-pencil","enabled":true},{"name":"alpha","enabled":true},{"name":"beta","enabled":true}]`),
			checkOK("alpha", "beta"),
		)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"designer", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--approvals", "ask"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		if strings.Contains(tail, "mcp_servers.open-pencil.enabled=false") {
			t.Fatalf("open-pencil was turned off: %q", tail)
		}
		if !strings.Contains(tail, "-c mcp_servers.alpha.enabled=false -c mcp_servers.beta.enabled=false") {
			t.Fatalf("the other enabled servers are not off in listing order: %q", tail)
		}
	})

	t.Run("inspector keeps the two browser servers", func(t *testing.T) {
		f := mcpListFixture(t)
		installCodexMcpScript(t, f,
			listRule(`[{"name":"playwright","enabled":true},{"name":"chrome-devtools","enabled":true},{"name":"gamma","enabled":true}]`),
			checkOK("gamma"),
		)
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"inspector", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--approvals", "ask"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := strings.Join(startTail(t, f), " ")
		want := `-m gpt-5 -c model_reasoning_effort="high" -c features.apps=false -c mcp_servers.gamma.enabled=false`
		if tail != want {
			t.Fatalf("argv=%q, want %q", tail, want)
		}
	})
}

// The roster records the effective choice verbatim: a list is stored as the
// resolved value, and reuse compares that value string for string.
func TestMcpListRosterField(t *testing.T) {
	f := mcpListFixture(t)
	code, _, _, _ := runCmdSpawn(t, f, []string{"inspector", "--kind", "grok"})
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
	if len(fields) != 16 || fields[15] != "playwright,chrome-devtools" {
		t.Fatalf("row=%q, want the list verbatim as the last field", lines[1])
	}
}

// Scenario 11: a worker reused from a slot with another mcp follows the
// existing none behavior — the roster column 15 must match the request.
func TestMcpListReuseFollowsTheNoneBehavior(t *testing.T) {
	t.Run("an idle worker opened with mcp=none is not reused for a list", func(t *testing.T) {
		f := a12Fixture(t, append([]fakecli.Rule{idleHerdrRule("old")}, freshSpawnRules()...), "2")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "none"))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "a,b"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"created_pane": true`) || strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("the idle worker opened with another MCP was reused: %s", stdout)
		}
	})

	t.Run("an idle worker opened with the same list is reused", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "a,b"))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "a,b"})
		if code != 0 || !strings.Contains(stdout, `"name": "old"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("the same list in another spelling is the same choice", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "a,b"))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "a, b,a"})
		if code != 0 || !strings.Contains(stdout, `"name": "old"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("no slot and another list is 13 with the MCP message", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "none"))
		code, message, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "a,b"})
		want := "spawn: lane 'build' has no idle worker matching MCP 'a,b' (idle: 'old' was opened with MCP 'none'); release it or raise the lane's panes"
		if code != 13 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
	})
}

// The roster keeps one form of the value, whatever spaces it had around its
// names.
func TestMcpListRosterKeepsOneCanonicalValue(t *testing.T) {
	for _, value := range []string{"alpha, beta", " alpha , beta ", "alpha, beta,alpha"} {
		t.Run(value, func(t *testing.T) {
			f := mcpListFixture(t)
			code, _, _, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--mcp", value})
			if code != 0 {
				t.Fatalf("exit=%d", code)
			}
			roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(roster)), "\n")
			if len(lines) != 2 {
				t.Fatalf("roster=%q", roster)
			}
			fields := strings.Split(lines[1], "\t")
			if len(fields) != 16 || fields[15] != "alpha,beta" {
				t.Fatalf("row=%q, want 16 fields ending in alpha,beta", lines[1])
			}
		})
	}
}

// A tab or a newline never reaches the roster: the value is refused first.
func TestMcpFieldAndLineSeparatorsExitTwo(t *testing.T) {
	for _, value := range []string{"alpha,\tbeta", "alpha,\nbeta"} {
		f := mcpListFixture(t)
		code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--mcp", value})
		if code != 2 {
			t.Fatalf("--mcp %q: exit=%d stderr=%q", value, code, stderr)
		}
		if strings.ContainsAny(stderr, "\t") {
			t.Fatalf("--mcp %q: the message carries the tab: %q", value, stderr)
		}
	}
}

func TestMcpInvalidValueMessagesAreFiltered(t *testing.T) {
	const value = "a\x1b[31mb"
	t.Run("flag", func(t *testing.T) {
		f := mcpListFixture(t)
		code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--mcp", value})
		if code != 2 || strings.ContainsRune(stderr, 0x1b) {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
	})
	t.Run("resolved role config", func(t *testing.T) {
		f := mcpListFixture(t)
		f.ctx.Entries["role_worker_mcp"] = core.ConfigEntry{Value: value, Source: "user"}
		code, stderr, _ := runCmdSpawnExpectError(t, f, []string{"worker"})
		if code != 2 || strings.ContainsRune(stderr, 0x1b) {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
	})
}
