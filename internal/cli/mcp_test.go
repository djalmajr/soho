//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMcpConfigKeysAreSettableAndEnumerated(t *testing.T) {
	t.Run("config set accepts inherit and none for role and lane mcp keys", func(t *testing.T) {
		env, cwd := commandFixture(t)
		for _, kv := range [][]string{{"role.reviewer.mcp", "none"}, {"lane.build.mcp", "none"}, {"role.reviewer.mcp", "inherit"}} {
			code, out, errOut := runIn(t, []string{"config", "set", kv[0], kv[1]}, env, cwd)
			if code != 0 {
				t.Fatalf("config set %s=%s: code=%d stderr=%q", kv[0], kv[1], code, errOut)
			}
			if !strings.Contains(out, "set "+kv[0]+"="+kv[1]+" in ") {
				t.Fatalf("config set %s out=%q", kv[0], out)
			}
		}
		code, out, _ := runIn(t, []string{"config"}, env, cwd)
		if code != 0 {
			t.Fatalf("config code=%d", code)
		}
		if !strings.Contains(out, "role.reviewer.mcp") || !strings.Contains(out, "lane.build.mcp") {
			t.Fatalf("the config listing does not carry the mcp keys:\n%s", out)
		}
		found := false
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "role.reviewer.mcp") && strings.Contains(l, "inherit") {
				found = true
			}
		}
		if !found {
			t.Fatalf("the last set value is not listed:\n%s", out)
		}
	})

	t.Run("config set refuses any other value with the enumerated refusal", func(t *testing.T) {
		env, cwd := commandFixture(t)
		for _, kv := range [][]string{{"role.reviewer.mcp", "off"}, {"lane.build.mcp", "on"}, {"role.reviewer.mcp", "true"}} {
			code, _, errOut := runIn(t, []string{"config", "set", kv[0], kv[1]}, env, cwd)
			if code != 2 {
				t.Fatalf("config set %s=%s: code=%d stderr=%q", kv[0], kv[1], code, errOut)
			}
			if want := "soho: config set: invalid value '" + kv[1] + "' for " + kv[0] + "\n"; errOut != want {
				t.Fatalf("stderr=%q, want %q", errOut, want)
			}
		}
	})
}

func TestMcpFlagOnSpawnAndRun(t *testing.T) {
	t.Run("spawn --mcp off is a usage error before any herdr call", func(t *testing.T) {
		f := newTM11SpawnFixture(t, nil)
		code, out, errOut := runIn(t, []string{"spawn", "implementer", "--mcp", "off"}, f.env, f.cwd)
		if code != 2 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
		}
		if !strings.Contains(errOut, "spawn: invalid --mcp 'off' (inherit|none)") {
			t.Fatalf("stderr=%q", errOut)
		}
		got, _ := f.herdrCallLog()
		if strings.Contains(got, "agent start") {
			t.Fatalf("the usage error opened a pane:\n%s", got)
		}
	})

	t.Run("run forwards --mcp to the spawn parser", func(t *testing.T) {
		f := newTM11SpawnFixture(t, nil)
		code, _, errOut := runIn(t, []string{"run", "implementer", "brief.md", "--mcp", "off"}, f.env, f.cwd)
		if code != 2 {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if strings.Contains(errOut, "unknown option --mcp") {
			t.Fatalf("run did not forward the flag: %q", errOut)
		}
		if !strings.Contains(errOut, "spawn: invalid --mcp 'off' (inherit|none)") {
			t.Fatalf("the spawn validation never ran: %q", errOut)
		}
	})

	t.Run("run --mcp none follows the spawn to the dispatch with the warning", func(t *testing.T) {
		f := newRetirementFixture(t, []fakecli.Rule{
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
			{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"agent", "rename"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
			retireAgentGetRule("build", "idle"),
			retireScreenRule("build", "terminal-fallback"),
		}, func(f *retirementFixture) {
			retireWriteRoster(t, f, retireR12)
			if err := os.WriteFile(filepath.Join(f.repo, "brief.md"), []byte(retireFullBrief), 0o600); err != nil {
				f.t.Fatal(err)
			}
			if _, err := fakecli.Install(t, f.bin, "grok", []fakecli.Rule{{AnyArgs: true}}); err != nil {
				f.t.Fatal(err)
			}
		})
		code, out, errOut := f.run(t, retireStep{Args: []string{"run", "implementer", "brief.md", "--no-wait", "--mcp", "none"}, Env: map[string]string{"SOHO_LAYOUT": "tab"}})
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
		}
		if !strings.Contains(errOut, "cannot turn MCP servers off per process") || !strings.Contains(errOut, "keeps the user's MCP servers") {
			t.Fatalf("the frontmatter mcp=none warning is missing: %q", errOut)
		}
		if !strings.Contains(out, `"role": "implementer"`) {
			t.Fatalf("the run JSON=%q", out)
		}
	})
}
