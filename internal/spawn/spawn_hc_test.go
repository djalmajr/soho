package spawn

// DJA-604 2b-2b: spawn through hc. The worker is a new hc terminal of the
// session: one hc run with the herdr path's agent argv, no pane work, the
// roster row holds the returned address, name_taken retries the existing
// suffix rule, and a failed start dies like the herdr failed-start path
// (exit 4, the Start failure's message) after closing the terminal. The
// fake-hc technique is the one the spawn tests already use for a fake
// herdr (internal/spawn/spawn_test.go: fakecli.Install on PATH plus
// SOHO_FAKECLI_CONFIG): the fake records every call in a JSONL log and
// answers with scripted JSON.

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcCallerTerminal = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	hcRunResult      = `{"address":"t7","name":"worker","state":"live"}`
	hcReadyWorker    = `{"address":"t7","state":"live","reported":true,"status":{"state":"idle"}}`
	hcNameTaken      = `{"error":{"kind":"name_taken","message":"name_taken"}}`
)

// hcSpawnFixture sets up a spawn for transport=hc in root: the fake agent
// binary (when kind is non-empty) and the fake herdr on PATH (herdr must
// never be called), the caller terminal t1 of session sX, the fixture
// state dir, and the session's empty roster. The role file carries the
// kind when kind is non-empty; otherwise the spawn passes --kind. The
// fake hc is installed by the tests once they know the worker's exact run
// argv.
func hcSpawnFixture(t *testing.T, role, kind string) (cwd, bin, state string, env platform.Env, ctx *core.Config) {
	t.Helper()
	root := t.TempDir()
	cwd = filepath.Join(root, "repo")
	state = filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	bin = filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, roles, bin, filepath.Join(root, "skill", "roles")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roleFile := "---\n---\n"
	if kind != "" {
		roleFile = "---\nkind: " + kind + "\n---\n"
	}
	if err := os.WriteFile(filepath.Join(roles, role+".md"), []byte(roleFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if kind != "" {
		if _, err := fakecli.Install(t, bin, kind, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env = platform.Env{
		"PATH": bin, "HOME": root, "TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR": filepath.Join(root, "skill"), "SOHO_ROLES": roles,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_LANES": "off", "SOHO_NOWRITE": "0", "SOHO_WAIT_POLL_MS": "1",
	}
	ctx = &core.Config{Entries: map[string]core.ConfigEntry{}}
	return cwd, bin, state, env, ctx
}

// hcAgentArgs mirrors the herdr path's agent argv for the fixture's
// inputs: the same builders spawn uses (the resolved role settings,
// buildArgs, the per-process MCP args, the configured native args), so an
// argv drift between the herdr start and the hc run fails by
// construction, not by a copied literal.
func hcAgentArgs(t *testing.T, role string, res core.RoleSettings, ctx *core.Config, env platform.Env, cwd string) []string {
	t.Helper()
	native := ConfigNativeArgs(res.Kind, res.Lane, role, ctx, env, cwd, func(key, own string) {})
	args := buildArgs(res.Kind, res.Approvals, res.ModelSpec, res.Effort, ctx, env)
	if mcpArgs, ok := kinds.KindMcpArgs(res.Kind, ""); ok {
		args = append(args, mcpArgs...)
	}
	if native != "" {
		args = append(args, strings.Fields(native)...)
	}
	return args
}

func hcRunArgv(cwd, name, kind string, agent []string) []string {
	argv := []string{"run", "--bg", "--json", "--name", name, "--cwd", cwd, "--label", "soho.session=sX", "--no-notify", "--outlive-parent", "--", kind}
	return append(argv, agent...)
}

// hcFixtureRules is the fake hc rule set every spawn test needs before
// the worker's run: the session's caller get and the session's roster ls.
func hcFixtureRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: hcCallerTerminal},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: `{"terminals":[]}`},
	}
}

// hcCallLog reads the fake hc call log; a missing log means zero calls.
func hcCallLog(t *testing.T, bin string) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return calls
}

// assertHcOnlyReadsAndRun pins acceptance (c) of the slice: under
// transport=hc the spawn may only issue the session gets, the roster ls
// and the one worker run — no hc tab, window, focus, keys or pane verb,
// and no herdr call.
func assertHcOnlyReadsAndRun(t *testing.T, bin string, calls []fakecli.Call) {
	t.Helper()
	runs := 0
	for _, c := range calls {
		switch c.Argv[0] {
		case "get", "ls", "run":
		default:
			t.Fatalf("hc call outside reads and the run: %q", c.Argv)
		}
		if c.Argv[0] == "run" {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("hc run calls = %d, want exactly 1: %v", runs, calls)
	}
	if herdr, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl")); err == nil {
		if len(herdr) != 0 {
			t.Fatalf("herdr was called under transport=hc: %v", herdr)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// spawnHc runs one spawn with the fixture's stdout/stderr captured and
// returns the call log after asserting the fake never fell through.
func spawnHc(t *testing.T, bin string, argv []string, ctx *core.Config, env platform.Env, cwd string) (out, stderr *bytes.Buffer, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	CmdSpawn(argv, ctx, env, cwd)
	if strings.Contains(stderr.String(), "fakecli: no rule") {
		t.Fatalf("fake hc/herdr call failed: %s", stderr.String())
	}
	return out, stderr, hcCallLog(t, bin)
}

// rosterRow returns the roster's last row: the file carries a comment
// header line above the rows.
func rosterRow(t *testing.T, state, session string) []string {
	t.Helper()
	roster, err := os.ReadFile(filepath.Join(state, session, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	row := ""
	for _, line := range strings.Split(string(roster), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("no roster row: %q", roster)
	}
	return strings.Split(row, "\t")
}

func TestSpawnHcWorkersRunWithTheHerdrArgv(t *testing.T) {
	// DJA-604 2b-2b acceptance (a): the pi implementer and the codex
	// reviewer each issue exactly one hc run with the worker's exact argv
	// (the herdr path's agent args), no pane work, no herdr call, and the
	// roster row holds the returned address.
	for _, tc := range []struct{ role, kind string }{
		{role: "implementer", kind: "pi"},
		{role: "reviewer", kind: "codex"},
	} {
		tc := tc
		t.Run(tc.kind+" "+tc.role, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, tc.role, tc.kind)
			res := core.ResolveRoleSettings(tc.role, ctx, env, cwd, core.RoleFlags{})
			agent := hcAgentArgs(t, tc.role, res, ctx, env, cwd)
			wantRun := hcRunArgv(cwd, tc.role, tc.kind, agent)
			rules := append(hcFixtureRules(),
				fakecli.Rule{Argv: wantRun, Stdout: `{"address":"t7","name":"` + tc.role + `","state":"live"}`},
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			out, _, calls := spawnHc(t, bin, []string{tc.role}, ctx, env, cwd)
			assertHcOnlyReadsAndRun(t, bin, calls)
			var run []string
			for _, c := range calls {
				if c.Argv[0] == "run" {
					run = c.Argv
				}
			}
			if strings.Join(run, "\x00") != strings.Join(wantRun, "\x00") {
				t.Fatalf("hc run argv=%q, want %q", run, wantRun)
			}
			if !strings.Contains(out.String(), `"placement": "hc"`) || !strings.Contains(out.String(), `"pane_id": "t7"`) || !strings.Contains(out.String(), `"status": "ready"`) {
				t.Fatalf("unexpected spawn result: %s", out.String())
			}
			f := rosterRow(t, state, "sX")
			if f[0] != tc.role || f[1] != "t7" || f[2] != tc.kind {
				t.Fatalf("roster row %v, want %s in pane t7 with kind %s", f, tc.role, tc.kind)
			}
		})
	}
}

func TestSpawnHcPaneFlagIsIgnored(t *testing.T) {
	// DJA-604 2b-2b acceptance (b): --pane has no meaning to hc; the flag
	// degrades to the warning and the spawn still opens a new terminal,
	// with no pane call anywhere.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "pi"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	wantRun := hcRunArgv(cwd, "worker", "pi", agent)
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: wantRun, Stdout: hcRunResult},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	_, stderr, calls := spawnHc(t, bin, []string{"worker", "--kind", "pi", "--pane", "p9"}, ctx, env, cwd)
	assertHcOnlyReadsAndRun(t, bin, calls)
	warn := "soho: warning: spawn: transport=hc has no panes; --pane and --tab are ignored"
	if !strings.Contains(stderr.String(), warn) {
		t.Fatalf("stderr=%q, want the pane warning %q", stderr.String(), warn)
	}
}

func TestSpawnHcNameTakenMovesToTheNextFreeSuffix(t *testing.T) {
	// DJA-604 2b-2b acceptance (c): name_taken twice, then success — the
	// third candidate's name is in the roster, with two "is taken"
	// warnings, one per retry.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "pi"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker", "pi", agent), Code: 1, Stderr: hcNameTaken},
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker-2", "pi", agent), Code: 1, Stderr: hcNameTaken},
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker-3", "pi", agent), Stdout: `{"address":"t7","name":"worker-3","state":"live"}`},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	_, stderr, calls := spawnHc(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd)
	names := []string{}
	for _, c := range calls {
		if c.Argv[0] == "run" {
			names = append(names, c.Argv[4])
		}
	}
	if strings.Join(names, ",") != "worker,worker-2,worker-3" {
		t.Fatalf("run names %v, want worker, worker-2, worker-3", names)
	}
	if n := strings.Count(stderr.String(), "is taken by another pane or workspace; using"); n != 2 {
		t.Fatalf("stderr=%q, want two is-taken warnings", stderr.String())
	}
	for _, warn := range []string{
		"soho: warning: agent name 'worker' is taken by another pane or workspace; using 'worker-2'",
		"soho: warning: agent name 'worker-2' is taken by another pane or workspace; using 'worker-3'",
	} {
		if !strings.Contains(stderr.String(), warn) {
			t.Fatalf("stderr=%q, want %q", stderr.String(), warn)
		}
	}
	f := rosterRow(t, state, "sX")
	if f[0] != "worker-3" || f[1] != "t7" {
		t.Fatalf("roster row %v, want worker-3 in t7", f)
	}
}

func TestSpawnHcNameTakenDiesAfterTenTries(t *testing.T) {
	// DJA-604 2b-2b acceptance (d): name_taken 10 times dies with the
	// exact line and exit 4, no roster row, and each of the nine retries
	// spent its "is taken" warning.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "pi"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	names := []string{"worker"}
	for i := 2; i <= 10; i++ {
		names = append(names, "worker-"+strconv.Itoa(i))
	}
	rules := hcFixtureRules()
	for _, name := range names {
		rules = append(rules, fakecli.Rule{Argv: hcRunArgv(cwd, name, "pi", agent), Code: 1, Stderr: hcNameTaken})
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	var exit *platform.ExitError
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					exit = e
				}
			}
		}()
		CmdSpawn([]string{"worker", "--kind", "pi"}, ctx, env, cwd)
	}()
	if exit == nil || exit.Code != 4 || exit.Msg != "spawn: no free hc terminal name for 'worker' after 10 tries" {
		t.Fatalf("exit=%+v, want exit 4 with the pinned message", exit)
	}
	runs := 0
	for _, c := range hcCallLog(t, bin) {
		if c.Argv[0] == "run" {
			runs++
		}
	}
	if runs != 10 {
		t.Fatalf("hc run calls = %d, want 10", runs)
	}
	// The roster file may exist (the stale-row read materializes its
	// header); a row is the failure.
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("a roster row was written on the exhausted start: %q", line)
			}
		}
	}
}

func TestSpawnHcBlockedStartClosesTheTerminal(t *testing.T) {
	// DJA-604 2b-2b acceptance (e): a readiness blocked kills and rms the
	// address, dies with the herdr failed-start exit code and the Start
	// failure's message, and writes no roster row.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "pi"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker", "pi", agent), Stdout: hcRunResult},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"live"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"removed":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	var exit *platform.ExitError
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					exit = e
				}
			}
		}()
		CmdSpawn([]string{"worker", "--kind", "pi"}, ctx, env, cwd)
	}()
	// The herdr failed-start path dies with exit 4; the message is the
	// Start failure's own message.
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker is blocked during startup: permission: write to file" {
		t.Fatalf("exit=%+v, want exit 4 with the Start failure's message", exit)
	}
	var kill, rm int
	for _, c := range hcCallLog(t, bin) {
		switch c.Argv[0] {
		case "kill":
			kill++
		case "rm":
			rm++
		}
	}
	if kill != 1 || rm != 1 {
		t.Fatalf("kill=%d rm=%d, want the address closed once each", kill, rm)
	}
	// The roster file may exist (the stale-row read materializes its
	// header); a row is the failure.
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("a roster row was written on the blocked start: %q", line)
			}
		}
	}
}

func TestSpawnHcFailedCloseWarnsWithTheAddress(t *testing.T) {
	// A blocked start whose close fails still dies with the Start failure,
	// and warns first that the terminal may still hold the name.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "pi"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker", "pi", agent), Stdout: hcRunResult},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"live"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"rm failed"}}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &bytes.Buffer{}, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	var exit *platform.ExitError
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					exit = e
				}
			}
		}()
		CmdSpawn([]string{"worker", "--kind", "pi"}, ctx, env, cwd)
	}()
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker is blocked during startup: permission: write to file" {
		t.Fatalf("exit=%+v, want exit 4 with the Start failure's message", exit)
	}
	want := "spawn: could not close hc terminal t7 after the failed start; it may still hold the name 'worker'"
	if !strings.Contains(errOut.String(), want) {
		t.Fatalf("stderr = %q, want the warning %q", errOut.String(), want)
	}
}

func TestSpawnHcGrokContextWindowIsNotTyped(t *testing.T) {
	// DJA-604 2b-2b acceptance (f): the configured grok context window is
	// not typed under hc — the spawn warns and nothing is sent or read
	// beyond the session reads and the run.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "grok"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	env["SOHO_CONTEXT_WINDOW_GROK"] = "128k"
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker", "grok", agent), Stdout: hcRunResult},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	out, stderr, calls := spawnHc(t, bin, []string{"worker", "--kind", "grok"}, ctx, env, cwd)
	assertHcOnlyReadsAndRun(t, bin, calls)
	warn := "soho: warning: spawn: transport=hc cannot type /context-window into grok; the context window stays at grok's default"
	if !strings.Contains(stderr.String(), warn) {
		t.Fatalf("stderr=%q, want the context-window warning %q", stderr.String(), warn)
	}
	if strings.Contains(out.String(), `"context_window"`) {
		t.Fatalf("the JSON records a context window that was never set: %s", out.String())
	}
}

func TestSpawnHcOrchestratorRenameIsSkipped(t *testing.T) {
	// DJA-604 2b-2b decision 3: the orchestrator rename is skipped under
	// hc with the warning; the spawn still succeeds and issues no rename
	// of any kind (hc has none).
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{Kind: "pi"})
	agent := hcAgentArgs(t, "worker", res, ctx, env, cwd)
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: hcRunArgv(cwd, "worker", "pi", agent), Stdout: hcRunResult},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	_, stderr, calls := spawnHc(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd)
	assertHcOnlyReadsAndRun(t, bin, calls)
	warn := "soho: warning: spawn: transport=hc has no rename; the orchestrator keeps its terminal name"
	if !strings.Contains(stderr.String(), warn) {
		t.Fatalf("stderr=%q, want the rename warning %q", stderr.String(), warn)
	}
}
