package spawn_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/cli"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const spawnRefuseLine = "soho: spawn: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. Nothing was changed. Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn\n"

// Windows may assign the test process PID 4000. Keep the claimed shell
// distinct from the start of the scripted ancestry even in that case.
func callerShellPID() int {
	if os.Getpid() == 4000 {
		return 4001
	}
	return 4000
}

// stubCallerProcessInfo keeps the actual caller check and JSON parser, but
// supplies the response in memory: starting a race-built fake herdr on a
// loaded Windows host must not turn a mismatch into unknown after 10 seconds.
// Each non-parallel subtest owns and restores its runner and call counter.
func stubCallerProcessInfo(t *testing.T) *int {
	t.Helper()
	calls := 0
	t.Cleanup(herdr.OverrideCallerProcessInfoRunner(func(exe string, args []string, opts platform.RunOptions) platform.RunResult {
		calls++
		if exe != "herdr" || !reflect.DeepEqual(args, []string{"pane", "process-info", "--pane", "w1:p9"}) || opts.TimeoutMs != 10000 || opts.Env.Get("HERDR_PANE_ID") != "w1:p9" {
			t.Fatalf("unexpected process-info request: exe=%q args=%q timeout=%d pane=%q", exe, args, opts.TimeoutMs, opts.Env.Get("HERDR_PANE_ID"))
		}
		status := 0
		return platform.RunResult{Status: &status, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, callerShellPID())}
	}))
	return &calls
}

// callerSpawnFixture builds a spawn fixture (the same shape as the
// internal newSpawnFixture) with the scripted ancestry the caller-pane
// check walks (platform.OverrideAncestorParents: no ps, no snapshot).
func callerSpawnFixture(t *testing.T, herdrRules []fakecli.Rule, parents map[int]int) (platform.Env, *core.Config, string, string) {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, bin, filepath.Join(root, "roles"), filepath.Join(root, "skill", "roles")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fakecli.Install(t, bin, "herdr", herdrRules); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.OverrideAncestorParents(parents))
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["HOME"] = root
	env["USERPROFILE"] = root
	env["SOHO_DIR"] = state
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_SKILL_DIR"] = filepath.Join(root, "skill")
	env["SOHO_ROLES"] = filepath.Join(root, "roles")
	env["SOHO_LANES"] = "off"
	env["SOHO_LAYOUT"] = "split"
	env["SOHO_REGRID"] = "off"
	env["SOHO_WAIT_POLL_MS"] = "1"
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "w1:p9"
	env["SOHO_DELEGATED_PANE"] = ""
	env["SOHO_CALLER_CHECK"] = "refuse"
	env["CLAUDE_CODE_SESSION_ID"] = ""
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The role file the spawn resolves (kind grok: the fake binary below).
	role := "---\nkind: grok\nmodel: grok-4.7\neffort: high\napprovals: ask\n---\n"
	if err := os.WriteFile(filepath.Join(root, "roles", "worker.md"), []byte(role), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
		t.Fatal(err)
	}
	sourceRoles := filepath.Join("..", "..", "skills", "soho", "roles")
	entries, err := os.ReadDir(sourceRoles)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sourceRoles, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "skill", "roles", entry.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	return env, ctx, cwd, bin
}

// freshSpawnRules is the same rule set the internal spawn fixture uses for
// a plain successful spawn (see spawn_cases_test.go), plus the
// process-info read of the claimed caller pane.
func freshSpawnRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
		{Argv: []string{"pane", "close"}, ArgvPrefix: true},
		{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"tab", "get"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
		{Argv: []string{"tab", "rename"}, ArgvPrefix: true},
		{Argv: []string{"pane", "process-info", "--pane", "w1:p9"}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, callerShellPID())},
	}
}

// forkedPS is the ancestry table of a process that is NOT in the claimed
// pane: the walk reaches the root without the pane's shell.
func forkedPS() map[int]int {
	return map[int]int{os.Getpid(): 1}
}

// spawnCallCount counts the fake herdr calls whose argv starts with argv.
func spawnCallCount(t *testing.T, bin string, argv ...string) int {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, call := range calls {
		if len(call.Argv) < len(argv) {
			continue
		}
		same := true
		for i, want := range argv {
			if call.Argv[i] != want {
				same = false
				break
			}
		}
		if same {
			n++
		}
	}
	return n
}

func runSpawnCLI(t *testing.T, env platform.Env, cwd string, args []string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := cli.Run(append([]string{"spawn"}, args...), env)
	return code, out.String(), errOut.String()
}

// TestCmdSpawnCallerPaneCheck: the spawn verifies the caller pane before
// any call that acts on the claimed pane: a forked tree refuses (exit 2,
// no split/start/rename), warns and continues under caller_check=warn,
// and is skipped entirely under off.
func TestCmdSpawnCallerPaneCheck(t *testing.T) {
	t.Run("mismatch with refuse exits 2 before pane split, agent start or agent rename", func(t *testing.T) {
		herdr.ResetCallerCheck()
		processInfoCalls := stubCallerProcessInfo(t)
		env, _, cwd, bin := callerSpawnFixture(t, freshSpawnRules(), forkedPS())
		code, _, errOut := runSpawnCLI(t, env, cwd, []string{"worker", "--pane", "w0test:p0a", "--name", "worker"})
		if *processInfoCalls != 1 {
			t.Fatalf("in-memory process-info calls=%d want 1; fake process startup dependency returned", *processInfoCalls)
		}
		if code != 2 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if want := fmt.Sprintf(spawnRefuseLine, callerShellPID()); errOut != want {
			t.Fatalf("stderr=%q want %q", errOut, want)
		}
		if n := spawnCallCount(t, bin, "pane", "split"); n != 0 {
			t.Fatalf("pane split called %d times", n)
		}
		if n := spawnCallCount(t, bin, "agent", "start"); n != 0 {
			t.Fatalf("agent start called %d times", n)
		}
		if n := spawnCallCount(t, bin, "agent", "rename"); n != 0 {
			t.Fatalf("agent rename called %d times", n)
		}
		if n := spawnCallCount(t, bin, "pane", "process-info"); n != 0 {
			t.Fatalf("process-info started %d fake processes; want only the in-memory read", n)
		}
	})
	t.Run("mismatch with warn continues and logs the friction line", func(t *testing.T) {
		herdr.ResetCallerCheck()
		processInfoCalls := stubCallerProcessInfo(t)
		env, _, cwd, bin := callerSpawnFixture(t, freshSpawnRules(), forkedPS())
		env["SOHO_CALLER_CHECK"] = "warn"
		code, stdout, errOut := runSpawnCLI(t, env, cwd, []string{"worker", "--pane", "w0test:p0a", "--name", "worker"})
		if *processInfoCalls != 1 {
			t.Fatalf("in-memory process-info calls=%d want 1; fake process startup dependency returned", *processInfoCalls)
		}
		if n := spawnCallCount(t, bin, "pane", "process-info"); n != 0 {
			t.Fatalf("process-info started %d fake processes; want only the in-memory read", n)
		}
		if code != 0 {
			t.Fatalf("code=%d stdout=%q err=%q", code, stdout, errOut)
		}
		if !strings.Contains(errOut, fmt.Sprintf("soho: warning: spawn: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. Continuing because caller_check=warn. Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn", callerShellPID())) {
			t.Fatalf("stderr missing the warn line: %q", errOut)
		}
		friction, err := os.ReadFile(filepath.Join(env["SOHO_DIR"], "ws", "friction.log"))
		if err != nil || !strings.Contains(string(friction), "Continuing because caller_check=warn.") {
			t.Fatalf("friction.log=%q err=%v", friction, err)
		}
	})
	t.Run("off makes no process-info call", func(t *testing.T) {
		herdr.ResetCallerCheck()
		processInfoCalls := stubCallerProcessInfo(t)
		env, _, cwd, bin := callerSpawnFixture(t, freshSpawnRules(), forkedPS())
		env["SOHO_CALLER_CHECK"] = "off"
		code, stdout, errOut := runSpawnCLI(t, env, cwd, []string{"worker", "--pane", "w0test:p0a", "--name", "worker"})
		if *processInfoCalls != 0 {
			t.Fatalf("off made %d in-memory process-info calls", *processInfoCalls)
		}
		if code != 0 {
			t.Fatalf("code=%d stdout=%q err=%q", code, stdout, errOut)
		}
		if n := spawnCallCount(t, bin, "pane", "process-info"); n != 0 {
			t.Fatalf("off still made %d process-info calls", n)
		}
	})
}
