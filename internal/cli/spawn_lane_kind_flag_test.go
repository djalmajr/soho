package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// laneKindFlagEnv is the DJA-397 scenario through the command entry: the lane
// "build" holds the role "scouter" and has lane.build.kind=pi, and a roster
// worker Herdr no longer lists is on the lane (a RosterRemove if the lane
// decision ran).
func laneKindFlagEnv(t *testing.T) (env platform.Env, bin, state string) {
	t.Helper()
	root := t.TempDir()
	bin, state = filepath.Join(root, "bin"), filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		// agent_not_found for the ghost: the lane decision lists it as gone.
		{Argv: []string{"agent", "get", "ghost"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
		{Argv: []string{"pane", "close"}, ArgvPrefix: true},
		{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"tab", "get"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
		{Argv: []string{"tab", "rename"}, ArgvPrefix: true},
		// A caller without the orchestrator name gets renamed on the normal
		// path; the rename succeeds so a refusal after it would show here.
		{Argv: []string{"agent", "rename"}, ArgvPrefix: true, Stdout: `{"result":{"renamed":true}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"pi", "codex"} {
		if _, err := fakecli.Install(t, bin, kind, nil); err != nil {
			t.Fatal(err)
		}
	}
	ghost := strings.Join([]string{"ghost", "p-ghost", "pi", "scouter", "alibaba", "1", "/tmp/work", "now", "", "ask", "scouter", "build", "", "", ""}, "\t")
	body := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tburst\targs\teffort\n" + ghost + "\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env = commandSpawnEnv(t, root, state, bin)
	env["HERDR_PANE_ID"] = "p1"
	env["SOHO_LANES"] = "on"
	env["SOHO_LANE_BUILD_ROLES"] = "scouter"
	env["SOHO_LANE_BUILD_KIND"] = "pi"
	env["SOHO_LANE_BUILD_PANES"] = "2"
	return env, bin, state
}

type laneKindFlagRun struct {
	code           int
	stdout, stderr string
	acts           []string
	roster         string
}

func runLaneKindFlag(t *testing.T, env platform.Env, bin, state string, argv ...string) laneKindFlagRun {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run(argv, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	acts := []string{}
	for _, c := range calls {
		if len(c.Argv) < 2 {
			continue
		}
		switch c.Argv[0] + " " + c.Argv[1] {
		case "pane split", "pane close", "tab create", "agent start", "agent rename":
			acts = append(acts, strings.Join(c.Argv, " "))
		}
	}
	roster, _ := os.ReadFile(filepath.Join(state, "ws", "agents.tsv"))
	return laneKindFlagRun{code, out.String(), errOut.String(), acts, string(roster)}
}

const laneKindFlagText = "spawn: role 'scouter' runs in lane 'build', whose kind is pi; --kind codex would put a codex worker in it and block the lane's next spawns. Spawn with --kind pi, change lane.build.kind, or set SOHO_LANES=off for this spawn to open it outside the lanes."

// DJA-397 through `soho spawn`: exit 13, the JSON line and the message exact,
// no pane or agent opened, the roster file untouched, and the next spawn of
// the lane's own kind unchanged.
func TestSpawnKindFlagDifferentFromTheLaneKindIsRefusedBeforeAnyPane(t *testing.T) {
	env, bin, state := laneKindFlagEnv(t)
	before, _ := os.ReadFile(filepath.Join(state, "ws", "agents.tsv"))
	r := runLaneKindFlag(t, env, bin, state, "spawn", "scouter", "--kind", "codex")
	if r.code != 13 {
		t.Fatalf("exit=%d, want 13\nstdout=%q\nstderr=%q", r.code, r.stdout, r.stderr)
	}
	wantJSON := `{"status":"kind-mismatch","lane":"build","role":"scouter","lane_kind":"pi","requested_kind":"codex"}` + "\n"
	if r.stdout != wantJSON {
		t.Fatalf("stdout=%q\nwant   %q", r.stdout, wantJSON)
	}
	wantErr := "soho: warning: " + laneKindFlagText + "\n"
	if strings.Count(r.stderr, wantErr) != 1 || !strings.HasSuffix(r.stderr, wantErr) {
		t.Fatalf("stderr=%q\nwant it to end with %q once", r.stderr, wantErr)
	}
	if len(r.acts) != 0 {
		t.Fatalf("herdr acted before the refusal: %v", r.acts)
	}
	if r.roster != string(before) {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, r.roster)
	}

	env, bin, state = laneKindFlagEnv(t)
	next := runLaneKindFlag(t, env, bin, state, "spawn", "scouter")
	if next.code != 0 || !strings.Contains(next.stdout, `"kind": "pi"`) || len(next.acts) == 0 {
		t.Fatalf("the next spawn of the lane's kind is not the normal path: exit=%d stdout=%q stderr=%q acts=%v", next.code, next.stdout, next.stderr, next.acts)
	}
}

// DJA-397 through `soho run`, which spawns first: the same refusal, the same
// exit and message, and no pane or agent opened. run keeps the spawn JSON in
// a buffer it drops when the spawn dies, so the refusal's JSON line is not on
// run's stdout (the same for every spawn failure that prints JSON, such as
// the existing kind-mismatch).
func TestRunKindFlagDifferentFromTheLaneKindIsRefusedBeforeAnyPane(t *testing.T) {
	env, bin, state := laneKindFlagEnv(t)
	brief := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(brief, []byte("# brief\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(state, "ws", "agents.tsv"))
	r := runLaneKindFlag(t, env, bin, state, "run", "scouter", brief, "--kind", "codex")
	if r.code != 13 {
		t.Fatalf("exit=%d, want 13\nstdout=%q\nstderr=%q", r.code, r.stdout, r.stderr)
	}
	wantErr := "soho: warning: " + laneKindFlagText + "\n"
	if strings.Count(r.stderr, wantErr) != 1 || !strings.HasSuffix(r.stderr, wantErr) {
		t.Fatalf("stderr=%q\nwant it to end with %q once", r.stderr, wantErr)
	}
	if len(r.acts) != 0 {
		t.Fatalf("herdr acted before the refusal: %v", r.acts)
	}
	if r.roster != string(before) {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, r.roster)
	}
	t.Logf("run stdout on the refusal: %q", r.stdout)
}

// DJA-397 review 2: a role listed in the lane without a role file is still the
// usage error of today (exit 3, unknown role) and never reads as a lane kind
// conflict; nothing acts before it, the caller rename included.
func TestSpawnUnknownRoleInALaneStaysAUsageError(t *testing.T) {
	env, bin, state := laneKindFlagEnv(t)
	env["SOHO_LANE_BUILD_ROLES"] = "scouter,ghostrole"
	r := runLaneKindFlag(t, env, bin, state, "spawn", "ghostrole", "--kind", "codex")
	if r.code != 3 || !strings.Contains(r.stderr, "unknown role 'ghostrole'") {
		t.Fatalf("exit=%d stderr=%q, want 3 and the unknown role error", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "kind-mismatch") {
		t.Fatalf("an unknown role read as a lane kind conflict: %q", r.stdout)
	}
	if len(r.acts) != 0 {
		t.Fatalf("herdr acted before the usage error: %v", r.acts)
	}
}
