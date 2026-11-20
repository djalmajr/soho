package spawn

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// spawnTimeoutFixture mirrors the hermetic workspace of
// TestSpawnShortTimeoutWarnsOnceAndKeepsTheValue.
func spawnTimeoutFixture(t *testing.T) (string, string, string, *core.Config, platform.Env) {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, roles, bin, filepath.Join(root, "skill", "roles")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\neffort: high\napprovals: full\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "ready\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil { // the state dir exists in a real workspace before the spawn
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH": bin, "HOME": root, "TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR": filepath.Join(root, "skill"), "SOHO_ROLES": roles,
		"SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "HERDR_ENV": "1",
		"HERDR_PANE_ID": "p1", "SOHO_LANES": "off", "SOHO_LAYOUT": "split",
		"SOHO_REGRID": "off", "SOHO_WAIT_POLL_MS": "1",
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	return root, cwd, bin, ctx, env
}

func TestSpawnTimeoutDurationUnits(t *testing.T) {
	t.Run("--timeout 1m reaches herdr agent start as 60000 milliseconds", func(t *testing.T) {
		_, cwd, bin, ctx, env := spawnTimeoutFixture(t)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		CmdSpawn([]string{"worker", "--timeout", "1m"}, ctx, env, cwd)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if strings.Contains(stderr.String(), "fakecli: no rule") {
			t.Fatalf("fake Herdr call failed: %s", stderr.String())
		}
		if !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
			t.Fatalf("spawn output=%q; the spawn should proceed", out.String())
		}
		if strings.Contains(stderr.String(), "--timeout is in milliseconds") {
			t.Fatalf("a unit value must not warn: %q", stderr.String())
		}
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var start []string
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				start = call.Argv
				break
			}
		}
		joined := strings.Join(start, " ")
		if !strings.Contains(joined, "--timeout 60000") || strings.Contains(joined, "--timeout 1m") {
			t.Fatalf("agent start argv=%q; the unit must be converted to milliseconds for herdr", joined)
		}
	})
	t.Run("an invalid duration exits 2 before any agent start", func(t *testing.T) {
		_, cwd, bin, ctx, env := spawnTimeoutFixture(t)
		code, msg := 0, ""
		func() {
			defer func() {
				r := recover()
				if ee, ok := r.(*platform.ExitError); ok {
					code, msg = ee.Code, ee.Msg
				} else if r != nil {
					t.Fatalf("unexpected panic: %v", r)
				}
			}()
			CmdSpawn([]string{"worker", "--timeout", "1h30m"}, ctx, env, cwd)
		}()
		if code != 2 || msg != "spawn: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '1h30m'" {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("herdr calls log: %v", err)
		}
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				t.Fatalf("an agent start ran before the refusal: %+v", call)
			}
		}
	})
}
