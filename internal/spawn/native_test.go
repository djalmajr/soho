package spawn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

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
