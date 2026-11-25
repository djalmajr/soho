package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnGroupRoleArgsOverride(t *testing.T) {
	for _, tc := range []struct {
		name, roleArgs, want string
		native               []string
	}{
		{"group fallback", "", "--thinking minimal", nil},
		{"role replaces group", "--thinking high", "--thinking high", nil},
		{"trailing native flag", "--thinking high", "--thinking high --thinking low", []string{"--", "--thinking", "low"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mcpListFixture(t, piHerdrIdentityRule("implementer", "p-new", "/sessions/implementer.jsonl"))
			f.env["SOHO_LANES"] = "on"
			f.ctx.Entries["lane_builders_kind"] = core.ConfigEntry{Value: "pi", Source: "project"}
			f.ctx.Entries["lane_builders_args"] = core.ConfigEntry{Value: "--thinking minimal", Source: "project"}
			f.ctx.Entries["role_implementer_args"] = core.ConfigEntry{Value: tc.roleArgs, Source: "user"}
			code, out, stderr, _ := runCmdSpawn(t, f, append([]string{"implementer"}, tc.native...))
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			got, want := startTail(t, f), strings.Fields(tc.want)
			if len(got) < len(want) || !slices.Equal(got[len(got)-len(want):], want) || (tc.roleArgs != "" && slices.Contains(got, "minimal")) {
				t.Fatalf("start argv=%q, want tail %q with only the selected scoped args", got, want)
			}
			t.Logf("start argv=%q", got)
		})
	}
}

func TestSpawnUsesMigratedCriticalArgsAndMcp(t *testing.T) {
	f := mcpListFixture(t, piHerdrIdentityRule("critical-implementer", "p-new", "/sessions/critical-implementer.jsonl"))
	f.env["SOHO_LANES"] = "on"
	conf := filepath.Join(f.cwd, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "panes=4\nlane.build.kind=pi\nlane.build.args=--thinking minimal\nlane.build.mcp=inherit\nlane.critical.args=--thinking high\nlane.critical.kind=pi\nlane.critical.mcp=none\n"
	if err := os.WriteFile(conf, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	core.ApplyLaneFile(conf, "4", f.env, f.cwd)
	ctx := core.LoadConfig(f.env, f.cwd)
	f.ctx = &ctx
	code, out, stderr, _ := runCmdSpawn(t, f, []string{"critical-implementer"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	got := startTail(t, f)
	if !containsArgPair(got, "--thinking", "high") || slices.Contains(got, "minimal") || !slices.Contains(got, "--no-mcp") {
		t.Fatalf("start argv=%q, want the migrated role's args and MCP", got)
	}
	t.Logf("start argv=%q", got)
}

func TestConfigNativeArgsCombinesKindAndRoleArgs(t *testing.T) {
	stubCodexHelp(t, false)
	root := t.TempDir()
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(roles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "designer.md"), []byte("---\nkind: codex\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"SOHO_ROLES": roles, "HOME": root, "SOHO_SKILL_DIR": root}
	if err := os.WriteFile(filepath.Join(root, "config.defaults"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{"args_codex": {Value: "--verbose", Source: "project"}, "role_designer_args": {Value: "-c x=y", Source: "project"}}, Order: []string{"args_codex", "role_designer_args"}}
	if got := ConfigNativeArgs("codex", "", "designer", &ctx, env, root, nil); got != "--verbose -c x=y" {
		t.Fatalf("got %q", got)
	}
}

// TestEnsureOrchestratorNameGateIsSingleAttempt: the gate is one agent get,
// like the pre-transport raw call. A killed get (exit 143, no JSON error)
// fails the gate: the function returns "" and makes exactly one agent get —
// it does not retry into a recovered get and rename a caller the old gate
// left unnamed. The recovered get, list, and rename rules exist so a retry
// would have succeeded: the assertions are on the recorded calls, not on the
// return alone.
func TestEnsureOrchestratorNameGateIsSingleAttempt(t *testing.T) {
	f := newSpawnFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "p1"}, Call: 1, Code: 143},
		{Argv: []string{"agent", "get", "p1"}, Call: 2, Stdout: `{"result":{"agent":{"name":"caller","agent_status":"working"}}}`},
		{Argv: []string{"agent", "get", "p1"}, Call: 3, Stdout: `{"result":{"agent":{"name":"caller","agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "rename", "p1", "orchestrator"}},
	})
	f.env["HERDR_PANE_ID"] = "p1"
	got := EnsureOrchestratorName(f.ctx, f.env, f.cwd, "test")
	if got != "" {
		t.Fatalf("a killed gate get must leave the caller unnamed, got %q", got)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	gets := 0
	for _, c := range calls {
		if len(c.Argv) < 2 || c.Argv[0] != "agent" {
			continue
		}
		switch c.Argv[1] {
		case "get":
			gets++
		case "rename":
			t.Fatalf("a killed gate get must not rename: %#v", calls)
		}
	}
	if gets != 1 {
		t.Fatalf("the gate must make one agent get, got %d: %#v", gets, calls)
	}
}

func TestHasCodexUpdateConfigAttachedForms(t *testing.T) {
	for _, tokens := range [][]string{
		{"-c", "check_for_update_on_startup=true"},
		{"--config", "check_for_update_on_startup=true"},
		{"-c=check_for_update_on_startup=true"},
		{"--config=check_for_update_on_startup=true"},
		{"-ccheck_for_update_on_startup=true"},
	} {
		if !hasCodexUpdateConfig(tokens) {
			t.Errorf("%q not recognized as an explicit update setting", tokens)
		}
	}
	for _, tokens := range [][]string{
		{"-c", "model=o3"},
		{"--config=model=o3"},
		{"--cd", "check_for_update_on_startup=true"},
		{"prompt", "-c", "check_for_update_on_startup=true"},
		{"-c", `"check_for_update_on_startup=false"`},
	} {
		if hasCodexUpdateConfig(tokens) {
			t.Errorf("%q wrongly recognized", tokens)
		}
	}
}
