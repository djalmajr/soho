package spawn

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// SOH-253: a codex worker whose resolved MCP choice is none or a named list
// also gets the per-process Apps override -c features.apps=false, right after
// the effort args and before the per-server args, the configured args.codex
// and the user's native args. inherit and every other kind are unchanged.
// Later native arguments keep their precedence: nothing here claims that a
// user's own `-c features.apps=true` is overridden.

const codexAppsOff = "-c features.apps=false"

func codexAppsCount(tail []string) int {
	count := 0
	for _, a := range tail {
		if a == "features.apps=false" {
			count++
		}
	}
	return count
}

func TestCmdSpawnMcpCodexAppsOverride(t *testing.T) {
	isolateCodexMcpTemp(t)
	listing := `[{"name":"alpha","enabled":true,"transport":{"command":"/opt/alpha --api-key SECRET-1"}},{"name":"beta","enabled":true}]`
	base := `-m gpt-5 -c model_reasoning_effort="high"`

	run := func(t *testing.T, f spawnFixture, mcp string, user ...string) (string, string) {
		t.Helper()
		argv := []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", mcp}
		if len(user) > 0 {
			argv = append(append(argv, "--"), user...)
		}
		code, _, stderr, _ := runCmdSpawn(t, f, argv)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		tail := startTail(t, f)
		if n := codexAppsCount(tail); n > 1 {
			t.Fatalf("%d features.apps overrides in %q", n, tail)
		}
		return strings.Join(tail, " "), stderr
	}

	t.Run("none: one override after the effort and before the per-server args", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpList(t, f, listing)
		got, _ := run(t, f, "none")
		if want := base + " " + codexAppsOff + " -c mcp_servers.alpha.enabled=false -c mcp_servers.beta.enabled=false"; got != want {
			t.Fatalf("argv=%q, want %q", got, want)
		}
	})

	t.Run("named list: one override, the kept server stays out of the off args", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpList(t, f, listing)
		got, _ := run(t, f, "alpha")
		if want := base + " " + codexAppsOff + " -c mcp_servers.beta.enabled=false"; got != want {
			t.Fatalf("argv=%q, want %q", got, want)
		}
	})

	t.Run("zero native servers still get the override", func(t *testing.T) {
		for _, mcp := range []string{"none", "alpha"} {
			f := codexMcpFixture(t)
			installCodexMcpList(t, f, `[]`)
			got, _ := run(t, f, mcp)
			if want := base + " " + codexAppsOff; got != want {
				t.Fatalf("mcp=%s argv=%q, want %q", mcp, got, want)
			}
		}
	})

	t.Run("a failed listing keeps the warning and the override, no raw fields", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpListFailing(t, f, fakecli.Rule{Code: 1})
		got, stderr := run(t, f, "none")
		if want := base + " " + codexAppsOff; got != want {
			t.Fatalf("argv=%q, want %q", got, want)
		}
		if stdErrCount(stderr, codexMcpWarn("exit 1", "worker")) != 1 {
			t.Fatalf("listing warning missing or repeated: %q", stderr)
		}
	})

	t.Run("configured and user native args come after, in order, with later precedence", func(t *testing.T) {
		f := codexMcpFixture(t)
		f.ctx.Entries["args_codex"] = core.ConfigEntry{Value: "--owner-config -c features.apps=true", Source: "user"}
		installCodexMcpList(t, f, listing)
		got, _ := run(t, f, "none", "--user-x")
		want := base + " " + codexAppsOff + " -c mcp_servers.alpha.enabled=false -c mcp_servers.beta.enabled=false --owner-config -c features.apps=true --user-x"
		if got != want {
			t.Fatalf("argv=%q, want %q", got, want)
		}
	})

	t.Run("inherit adds no override and no listing", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpList(t, f, listing)
		got, _ := run(t, f, "inherit")
		if got != base {
			t.Fatalf("argv=%q, want %q", got, base)
		}
		for _, call := range codexMcpCalls(t, f) {
			if strings.Contains(strings.Join(call, " "), "features") {
				t.Fatalf("inherit probed features: %q", call)
			}
		}
	})
}

func TestCmdSpawnMcpAppsOverrideStaysCodexOnly(t *testing.T) {
	for _, kind := range []string{"claude", "pi"} {
		t.Run(kind, func(t *testing.T) {
			f := newSpawnFixture(t, freshSpawnRules())
			configureSpawnFixture(t, &f)
			f.env["SOHO_LAYOUT"] = "tab"
			if _, err := fakecli.Install(t, f.bin, kind, nil); err != nil {
				t.Fatal(err)
			}
			code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", kind, "--effort", "high", "--mcp", "none"})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if got := strings.Join(startTail(t, f), " "); strings.Contains(got, "features.apps") {
				t.Fatalf("%s argv carries the codex Apps override: %q", kind, got)
			}
		})
	}
}
