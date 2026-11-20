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

func TestSpawnShortTimeoutWarnsOnceAndKeepsTheValue(t *testing.T) { // --timeout 45 means 45 ms: one warning, and the value still goes to `herdr agent start --timeout`
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
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	CmdSpawn([]string{"worker", "--timeout", "45"}, ctx, env, cwd)
	if strings.Contains(stderr.String(), "fakecli: no rule") {
		t.Fatalf("fake Herdr call failed: %s", stderr.String())
	}
	if n := strings.Count(stderr.String(), "spawn: --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)"); n != 1 {
		t.Fatalf("warning count=%d stderr=%q", n, stderr.String())
	}
	if !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
		t.Fatalf("spawn output=%q; the spawn should proceed", out.String())
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
	if joined := strings.Join(start, " "); !strings.Contains(joined, "--timeout 45") {
		t.Fatalf("agent start argv=%q; the value must pass through as given", joined)
	}
	log, err := os.ReadFile(filepath.Join(state, "ws", "friction.log"))
	if err != nil || !strings.Contains(string(log), "warning\tspawn\tspawn: --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)") {
		t.Fatalf("friction log=%q err=%v", log, err)
	}
}
