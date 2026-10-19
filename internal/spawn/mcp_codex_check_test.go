package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-414: after building the per-server overrides, the codex spawn runs
// `codex <overrides> mcp list --json` once more. codex 0.163.0 refuses the
// whole config when an override names a server that is not under
// [mcp_servers.*] of config.toml ("invalid transport in `mcp_servers.<name>`"),
// and the pane then dies before drawing. A failing check that names one of the
// overrides drops that override and checks again; any other failure is the
// listing-failure path of today. Only the server name leaves the error output.

const codexMcpBaseArgv = `-m gpt-5 -c model_reasoning_effort="high"`

func codexOffArgv(names ...string) []string {
	argv := []string{}
	for _, name := range names {
		argv = append(argv, "-c", "mcp_servers."+name+".enabled=false")
	}
	return argv
}

func codexCheckArgv(names ...string) []string {
	return append(codexOffArgv(names...), "mcp", "list", "--json")
}

// codexBootstrapError is the shape codex 0.163.0 prints for a server it cannot
// load, with the transport spelled out the way a leaking printer would.
func codexBootstrapError(name, secret string) string {
	return "Error: failed to load bootstrap configuration\n\nCaused by:\n    invalid transport\n    in `mcp_servers." + name + "`\n    command: /opt/" + name + " --api-key " + secret + "\n"
}

func codexListing(secret string, names ...string) string {
	rows := []string{}
	for _, name := range names {
		rows = append(rows, `{"name":"`+name+`","enabled":true,"transport":{"command":"/opt/`+name+` --api-key `+secret+`"}}`)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func installCodexMcpScript(t *testing.T, f spawnFixture, rules ...fakecli.Rule) {
	t.Helper()
	if _, err := fakecli.Install(t, f.bin, "codex", append(rules, fakecli.Rule{AnyArgs: true})); err != nil {
		t.Fatal(err)
	}
}

func listRule(listing string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"mcp", "list", "--json"}, Stdout: listing}
}

func checkOK(names ...string) fakecli.Rule {
	return fakecli.Rule{Argv: codexCheckArgv(names...), Stdout: "[]"}
}

func checkNaming(bad, secret string, names ...string) fakecli.Rule {
	return fakecli.Rule{Argv: codexCheckArgv(names...), Code: 1, Stderr: codexBootstrapError(bad, secret)}
}

// codexMcpCalls returns the argv of every codex call the spawn made.
func codexMcpCalls(t *testing.T, f spawnFixture) [][]string {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "codex.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out := [][]string{}
	for _, c := range calls {
		out = append(out, c.Argv)
	}
	return out
}

func codexFriction(t *testing.T, f spawnFixture) string {
	t.Helper()
	body, _ := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
	return string(body)
}

func codexStayOnWarn(names, agent string) string {
	return "spawn: codex cannot turn off MCP server(s) " + names + " for '" + agent + "' (they are not defined in its config.toml); they stay on, the others are off"
}

func spawnCodexNone(t *testing.T, f spawnFixture) (code int, stdout, stderr string) {
	t.Helper()
	code, stdout, stderr, _ = runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"})
	return code, stdout, stderr
}

func wantTail(t *testing.T, f spawnFixture, want string) {
	t.Helper()
	if got := strings.Join(startTail(t, f), " "); got != want {
		t.Fatalf("argv=%q, want %q", got, want)
	}
}

func wantCodexCalls(t *testing.T, f spawnFixture, want ...[]string) {
	t.Helper()
	got := codexMcpCalls(t, f)
	if len(got) != len(want) {
		t.Fatalf("codex calls=%q, want %q", got, want)
	}
	for i := range want {
		if strings.Join(got[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Fatalf("codex call %d=%q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
}

var codexListArgv = []string{"mcp", "list", "--json"}

func TestCmdSpawnMcpCodexChecksTheOverrides(t *testing.T) {
	const secret = "SECRET-DJA414"

	t.Run("the real case: b is refused, a and c stay off, the warning names b", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "b", "c")),
			checkNaming("b", secret, "a", "b", "c"),
			checkOK("a", "c"),
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false -c mcp_servers.c.enabled=false")
		warn := codexStayOnWarn("b", "worker")
		if got := stdErrCount(stderr, "soho: warning: "+warn); got != 1 {
			t.Fatalf("the exact stay-on warning is not on stderr once: %q", stderr)
		}
		if got := strings.Count(codexFriction(t, f), warn); got != 1 {
			t.Fatalf("friction.log lacks the stay-on warning once:\n%s", codexFriction(t, f))
		}
		wantCodexCalls(t, f, codexListArgv, codexCheckArgv("a", "b", "c"), codexCheckArgv("a", "c"))
	})

	t.Run("two bad servers in a row: both come off the overrides and the warning lists them", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "b", "c")),
			checkNaming("b", secret, "a", "b", "c"),
			checkNaming("c", secret, "a", "c"),
			checkOK("a"),
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false")
		if got := stdErrCount(stderr, "soho: warning: "+codexStayOnWarn("b, c", "worker")); got != 1 {
			t.Fatalf("the exact two-server warning is not on stderr once: %q", stderr)
		}
		wantCodexCalls(t, f, codexListArgv, codexCheckArgv("a", "b", "c"), codexCheckArgv("a", "c"), codexCheckArgv("a"))
	})

	t.Run("the warning lists the servers in list order, not in the order codex named them", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "b", "c")),
			checkNaming("c", secret, "a", "b", "c"),
			checkNaming("b", secret, "a", "b"),
			checkOK("a"),
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, "soho: warning: "+codexStayOnWarn("b, c", "worker")); got != 1 {
			t.Fatalf("the warning is not in list order (b, c): %q", stderr)
		}
	})

	t.Run("every override refused: no overrides, the warning lists all, no check without overrides", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "b")),
			checkNaming("a", secret, "a", "b"),
			checkNaming("b", secret, "b"),
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv)
		if got := stdErrCount(stderr, "soho: warning: "+codexStayOnWarn("a, b", "worker")); got != 1 {
			t.Fatalf("the exact all-refused warning is not on stderr once: %q", stderr)
		}
		wantCodexCalls(t, f, codexListArgv, codexCheckArgv("a", "b"), codexCheckArgv("b"))
	})

	t.Run("every override valid at once: today's args, one check, no new warning", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "c")),
			checkOK("a", "c"),
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false -c mcp_servers.c.enabled=false")
		if strings.Contains(stderr, "cannot turn off MCP") || strings.Contains(stderr, "could not list") {
			t.Fatalf("a clean check warned: %q", stderr)
		}
		if strings.Contains(codexFriction(t, f), "cannot turn off MCP") {
			t.Fatalf("a clean check wrote friction:\n%s", codexFriction(t, f))
		}
		wantCodexCalls(t, f, codexListArgv, codexCheckArgv("a", "c"))
	})

	t.Run("no enabled server: no check, as today", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, listRule(`[{"name":"gamma","enabled":false}]`))
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv)
		if strings.Contains(stderr, "MCP server") {
			t.Fatalf("zero enabled servers warned: %q", stderr)
		}
		wantCodexCalls(t, f, codexListArgv)
	})

	t.Run("a check failing without naming a server is the listing-failure path", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			rule   fakecli.Rule
			reason string
		}{
			{"no server in the error", fakecli.Rule{Argv: codexCheckArgv("a", "b"), Code: 1, Stderr: "Error: something else broke\n"}, "override check exit 1"},
			{"another exit code", fakecli.Rule{Argv: codexCheckArgv("a", "b"), Code: 2}, "override check exit 2"},
			{"a server that is not among the overrides", fakecli.Rule{Argv: codexCheckArgv("a", "b"), Code: 1, Stderr: codexBootstrapError("zzz", secret)}, "override check exit 1"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := codexMcpFixture(t)
				installCodexMcpScript(t, f, listRule(codexListing(secret, "a", "b")), tc.rule)
				code, _, stderr := spawnCodexNone(t, f)
				if code != 0 {
					t.Fatalf("exit=%d stderr=%q", code, stderr)
				}
				if got := stdErrCount(stderr, codexMcpWarn(tc.reason, "worker")); got != 1 {
					t.Fatalf("the exact listing-failure warning is not on stderr once: %q", stderr)
				}
				wantTail(t, f, codexMcpBaseArgv)
				wantCodexCalls(t, f, codexListArgv, codexCheckArgv("a", "b"))
			})
		}
	})

	t.Run("a server already taken off that codex names again ends the check", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "b", "c")),
			checkNaming("b", secret, "a", "b", "c"),
			checkNaming("b", secret, "a", "c"),
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, codexMcpWarn("override check exit 1", "worker")); got != 1 {
			t.Fatalf("the exact listing-failure warning is not on stderr once: %q", stderr)
		}
		if strings.Contains(stderr, "cannot turn off MCP") {
			t.Fatalf("a failed check also warned about the servers that stay on: %q", stderr)
		}
		wantTail(t, f, codexMcpBaseArgv)
		wantCodexCalls(t, f, codexListArgv, codexCheckArgv("a", "b", "c"), codexCheckArgv("a", "c"))
	})

	t.Run("a hung check hits the same 10 s timeout, warns and adds no flag", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			listRule(codexListing(secret, "a", "b")),
			fakecli.Rule{Argv: codexCheckArgv("a", "b"), Delay: 10500},
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if got := stdErrCount(stderr, codexMcpWarn("override check timed out", "worker")); got != 1 {
			t.Fatalf("the exact check-timeout warning is not on stderr once: %q", stderr)
		}
		wantTail(t, f, codexMcpBaseArgv)
	})

	t.Run("mcp=inherit never runs a check", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, listRule(codexListing(secret, "a")), checkNaming("a", secret, "a"))
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "inherit"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv)
		wantCodexCalls(t, f)
	})

	// Both warning paths: the stay-on warning (check names b, the rest is
	// valid) and the listing-failure warning (the check fails without naming
	// a server). The fake prints the transport in the error output and in the
	// listing; none of it may reach what the spawn writes.
	for _, tc := range []struct {
		name  string
		rules []fakecli.Rule
	}{
		{"the stay-on warning", []fakecli.Rule{checkNaming("b", secret, "a", "b", "c"), checkOK("a", "c")}},
		{"the listing-failure warning", []fakecli.Rule{{Argv: codexCheckArgv("a", "b", "c"), Code: 1, Stderr: "Error: failed to load bootstrap configuration\n    command: /opt/x --api-key " + secret + "\n"}}},
	} {
		t.Run("neither "+tc.name+" nor the friction log carries the codex error output or a transport", func(t *testing.T) {
			f := codexMcpFixture(t)
			installCodexMcpScript(t, f, append([]fakecli.Rule{listRule(codexListing(secret, "a", "b", "c"))}, tc.rules...)...)
			code, stdout, stderr := spawnCodexNone(t, f)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if !strings.Contains(stderr, "soho: warning: spawn: codex ") {
				t.Fatalf("the path under test did not warn: %q", stderr)
			}
			roster, _ := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
			all := strings.Join([]string{stdout, stderr, codexFriction(t, f), strings.Join(herdrCallArgv(t, f), " "), string(roster)}, "\n")
			for _, leaked := range []string{secret, "--api-key", "failed to load bootstrap configuration", "invalid transport", "Caused by", "/opt/"} {
				if strings.Contains(all, leaked) {
					t.Fatalf("%q leaked into stdout/stderr/friction/argv/roster:\n%s", leaked, all)
				}
			}
		})
	}
}

// The check's error names a server only on a line that is exactly codex
// 0.163.0's, "in `mcp_servers.<name>`" between white space, with no backtick
// and no backslash in the name. Anything else, or a check that names two
// overrides, is no name, so the check takes its failure path instead of
// dropping the wrong override.
func TestMcpCodexNamedServerNeedsTheWholeName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		kept   []string
		want   string
	}{
		{"the codex 0.163.0 shape", codexBootstrapError("b", "SECRET-DJA414"), []string{"a", "b", "c"}, "b"},
		{"a dotted name", "    in `mcp_servers.a.b`\n", []string{"a", "a.b"}, "a.b"},
		{"text after the name on the line", "    in `mcp_servers.b` (plugin)\n", []string{"b"}, ""},
		{"text before the name on the line", "invalid transport in `mcp_servers.b`\n", []string{"b"}, ""},
		{"text before the prefix never shifts the name", "    Xin `mcp_servers.b`\n", []string{".b", "b"}, ""},
		{"no closing backtick", "    in `mcp_servers.bc\n", []string{"b", "bc"}, ""},
		{"a prefix inside a name with a backtick", "    in `mcp_servers.a in `mcp_servers.b`\n", []string{"a in `mcp_servers.b", "b", "c"}, ""},
		{"CRLF line ends", "Caused by:\r\n    in `mcp_servers.b`\r\n", []string{"b"}, "b"},
		{"the same name twice", "in `mcp_servers.b`\nin `mcp_servers.b`\n", []string{"a", "b"}, "b"},
		{"an escaped backtick: the prefix is another override", "    in `mcp_servers.a\\`b`\n", []string{"a\\", "a`b", "c"}, ""},
		{"an escaped backtick before a space", "    in `mcp_servers.a\\` b`\n", []string{"a\\", "a` b", "c"}, ""},
		{"an escaped backtick before a tab", "    in `mcp_servers.a\\`\tb`\n", []string{"a\\", "a`\tb", "c"}, ""},
		{"a name that ends with a backslash", "    in `mcp_servers.a\\`\n", []string{"a\\", "c"}, ""},
		{"a name with a bare backtick", "    in `mcp_servers.a`b`\n", []string{"a", "a`b"}, ""},
		{"followed by punctuation", "in `mcp_servers.b`.\n", []string{"b"}, ""},
		{"two overrides on one line", "in `mcp_servers.a` b`\n", []string{"a", "a` b"}, ""},
		{"two overrides on two lines", "in `mcp_servers.a`\nin `mcp_servers.b`\n", []string{"a", "b"}, ""},
		{"a server outside the overrides", codexBootstrapError("zzz", "SECRET-DJA414"), []string{"a", "b"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpCodexNamedServer(tc.output, tc.kept); got != tc.want {
				t.Fatalf("mcpCodexNamedServer(%q, %q) = %q, want %q", tc.output, tc.kept, got, tc.want)
			}
		})
	}
}

// codexPlainListing is a listing whose names may carry any character, with
// name and enabled only.
func codexPlainListing(t *testing.T, names ...string) string {
	t.Helper()
	rows := []map[string]any{}
	for _, name := range names {
		rows = append(rows, map[string]any{"name": name, "enabled": true})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCmdSpawnMcpCodexNeverDropsAnOverrideItCannotNameWhole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		shown string
	}{
		// The error shows a`b escaped; its text up to the escaped backtick
		// is the other override a\.
		{"an escaped backtick never drops the override its prefix equals", []string{"a\\", "a`b", "c"}, "a\\`b"},
		// The same, with a space or a tab after the escaped backtick.
		{"an escaped backtick before a space never drops the override its prefix equals", []string{"a\\", "a` b", "c"}, "a\\` b"},
		{"an escaped backtick before a tab never drops the override its prefix equals", []string{"a\\", "a`\tb", "c"}, "a\\`\tb"},
		// The name holds the line's own prefix; the text after it is b.
		{"a prefix inside a name never drops the override it ends with", []string{"a in `mcp_servers.b", "b", "c"}, "a in `mcp_servers.b"},
		// The one line names both a and a` b.
		{"a line naming two overrides drops neither", []string{"a", "a` b"}, "a` b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := codexMcpFixture(t)
			installCodexMcpScript(t, f,
				listRule(codexPlainListing(t, tc.names...)),
				checkNaming(tc.shown, "SECRET-DJA414", tc.names...),
			)
			code, _, stderr := spawnCodexNone(t, f)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if got := stdErrCount(stderr, codexMcpWarn("override check exit 1", "worker")); got != 1 {
				t.Fatalf("the exact listing-failure warning is not on stderr once: %q", stderr)
			}
			if strings.Contains(stderr, "cannot turn off MCP") {
				t.Fatalf("an override the check did not name whole was dropped: %q", stderr)
			}
			wantTail(t, f, codexMcpBaseArgv)
			wantCodexCalls(t, f, codexListArgv, codexCheckArgv(tc.names...))
		})
	}
}
