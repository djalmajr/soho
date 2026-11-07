package spawn

// DJA-604 2b-2b: spawn through hc. The worker is a new hc terminal of the
// session: one terminal.run JSON-RPC call with the herdr path's agent argv,
// no pane work, the roster row holds the returned address, name_taken
// retries the existing suffix rule, and a failed start dies like the herdr
// failed-start path (exit 4, the Start failure's message) after closing the
// terminal. The native JSON-RPC transport uses hc.Client.Connect while
// existing scripted get/ls/cleanup calls go through Launch.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/hc"
	backend "github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	hcCallerTerminal = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	hcReadyWorker    = `{"address":"t7","state":"live","reported":true,"status":{"state":"idle"}}`
	rpcHelloSuccess  = `{"jsonrpc":"2.0","id":1,"result":{"generation":1}}`
	rpcRunSuccess    = `{"jsonrpc":"2.0","id":2,"result":{"address":"t7"}}`
)

func rpcNameTakenFor(name string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"terminal name %s is already in use","data":{"kind":"name_taken"}}}`, name)
}

type fakeSpawnRPCConn struct {
	mu           sync.Mutex
	written      bytes.Buffer
	closed       bool
	reapCount    int
	connectCount int
	responses    []string
	connectErr   error
	beforeRead   func(req []byte)
}

func newFakeSpawnRPCConn(responses ...string) *fakeSpawnRPCConn {
	if len(responses) == 0 {
		responses = []string{rpcHelloSuccess + "\n" + rpcRunSuccess + "\n"}
	}
	return &fakeSpawnRPCConn{responses: responses}
}

func (fc *fakeSpawnRPCConn) Connect(ctx context.Context, _ platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.connectCount++
	if fc.connectErr != nil {
		return nil, nil, nil, fc.connectErr
	}
	idx := fc.connectCount - 1
	resp := ""
	if idx < len(fc.responses) {
		resp = fc.responses[idx]
	} else if len(fc.responses) > 0 {
		resp = fc.responses[len(fc.responses)-1]
	}
	in := &fakeSpawnRPCIn{fc: fc}
	out := strings.NewReader(resp)
	reap := func() error {
		fc.mu.Lock()
		fc.reapCount++
		fc.mu.Unlock()
		return nil
	}
	return in, out, reap, nil
}

type fakeSpawnRPCIn struct {
	fc *fakeSpawnRPCConn
}

func (in *fakeSpawnRPCIn) Write(p []byte) (int, error) {
	in.fc.mu.Lock()
	defer in.fc.mu.Unlock()
	n, err := in.fc.written.Write(p)
	if in.fc.beforeRead != nil {
		in.fc.beforeRead(p)
	}
	return n, err
}

func (in *fakeSpawnRPCIn) Close() error {
	in.fc.mu.Lock()
	in.fc.closed = true
	in.fc.mu.Unlock()
	return nil
}

func (fc *fakeSpawnRPCConn) getWritten() []byte {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	cp := make([]byte, fc.written.Len())
	copy(cp, fc.written.Bytes())
	return cp
}

func (fc *fakeSpawnRPCConn) getConnectCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.connectCount
}

func parseTerminalRunReq(t *testing.T, written []byte) (map[string]any, map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 JSON-RPC lines, got %d: %q", len(lines), string(written))
	}
	var helloObj map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &helloObj); err != nil {
		t.Fatalf("unmarshal hello: %v", err)
	}
	if helloObj["method"] != "hello" || helloObj["jsonrpc"] != "2.0" {
		t.Fatalf("invalid hello request: %v", helloObj)
	}
	var runObj map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &runObj); err != nil {
		t.Fatalf("unmarshal run: %v", err)
	}
	params, ok := runObj["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params object in terminal.run request: %v", runObj)
	}
	return runObj, params
}

func parseAllTerminalRunReqs(t *testing.T, written []byte) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	var runs []map[string]any
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("unmarshal json line: %v: %q", err, line)
		}
		if obj["method"] == "terminal.run" {
			runs = append(runs, obj)
		}
	}
	return runs
}

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

// assertHcOnlyReads pins acceptance of the slice: under transport=hc the spawn
// may only issue the session gets, the roster ls and cleanup (kill/rm) — no
// hc run or connect CLI creation, no hc tab, window, focus, keys or pane verb,
// and no herdr call.
func assertHcOnlyReads(t *testing.T, bin string, calls []fakecli.Call) {
	t.Helper()
	for _, c := range calls {
		switch c.Argv[0] {
		case "get", "ls", "kill", "rm":
		default:
			t.Fatalf("hc call outside reads and cleanup: %q", c.Argv)
		}
		if c.Argv[0] == "run" || c.Argv[0] == "connect" {
			t.Fatalf("unexpected hc CLI %s call: %v", c.Argv[0], c.Argv)
		}
	}
	if herdr, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl")); err == nil {
		if len(herdr) != 0 {
			t.Fatalf("herdr was called under transport=hc: %v", herdr)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func assertHcOnlyReadsAndRun(t *testing.T, bin string, calls []fakecli.Call) {
	assertHcOnlyReads(t, bin, calls)
}

// spawnHc runs one spawn with the fixture's stdout/stderr captured and
// returns the call log after asserting the fake never fell through.
func spawnHc(t *testing.T, bin string, argv []string, ctx *core.Config, env platform.Env, cwd string) (out, stderr *bytes.Buffer, calls []fakecli.Call) {
	t.Helper()
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	return spawnHcWithConn(t, bin, argv, ctx, env, cwd, conn)
}

func spawnHcWithConn(t *testing.T, bin string, argv []string, ctx *core.Config, env platform.Env, cwd string, conn *fakeSpawnRPCConn) (out, stderr *bytes.Buffer, calls []fakecli.Call) {
	t.Helper()
	exit, out, stderr, calls := spawnHcExitWithConn(t, bin, argv, ctx, env, cwd, conn)
	if exit != nil {
		t.Fatalf("unexpected exit: %+v\nstderr: %s", exit, stderr.String())
	}
	if strings.Contains(stderr.String(), "fakecli: no rule") {
		t.Fatalf("fake hc/herdr call failed: %s", stderr.String())
	}
	return out, stderr, calls
}

func spawnHcExitWithConn(t *testing.T, bin string, argv []string, ctx *core.Config, env platform.Env, cwd string, conn *fakeSpawnRPCConn) (exit *platform.ExitError, out, stderr *bytes.Buffer, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })

	client := hc.New(env)
	if conn != nil {
		client.Connect = conn.Connect
	}
	origLaunch := client.Launch
	client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		if len(args) > 0 && (args[0] == "run" || args[0] == "connect") {
			t.Fatalf("unexpected CLI %s call: %v", args[0], args)
		}
		return origLaunch(ctx, env, args...)
	}

	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					exit = e
				} else {
					panic(value)
				}
			}
		}()
		cmdSpawnWithHcStarter(argv, ctx, env, cwd, func(c *core.Config, o *spawnOptions, k string, e platform.Env, a []string) {
			svc := backend.NewWithClient(e, client)
			startHcAgentUsingServices(c, o, k, e, a, svc)
		})
	}()
	return exit, out, stderr, hcCallLog(t, bin)
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
	// reviewer each issue exactly one native terminal.run JSON-RPC call
	// with the worker's exact argv (the herdr path's agent args), no pane work,
	// no herdr call, no legacy CLI run, and the roster row holds the returned address.
	for _, tc := range []struct{ role, kind string }{
		{role: "implementer", kind: "pi"},
		{role: "reviewer", kind: "codex"},
	} {
		tc := tc
		t.Run(tc.kind+" "+tc.role, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, tc.role, tc.kind)
			res := core.ResolveRoleSettings(tc.role, ctx, env, cwd, core.RoleFlags{})
			agent := hcAgentArgs(t, tc.role, res, ctx, env, cwd)
			rules := append(hcFixtureRules(),
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
			out, _, calls := spawnHcWithConn(t, bin, []string{tc.role}, ctx, env, cwd, conn)
			assertHcOnlyReads(t, bin, calls)

			req, params := parseTerminalRunReq(t, conn.getWritten())
			if req["jsonrpc"] != "2.0" || req["method"] != "terminal.run" || req["id"] != float64(2) {
				t.Fatalf("unexpected envelope: %v", req)
			}
			if params["name"] != tc.role {
				t.Fatalf("name=%v, want %s", params["name"], tc.role)
			}
			if params["caller"] != "t1" {
				t.Fatalf("caller=%v, want t1", params["caller"])
			}
			if params["cwd"] != cwd {
				t.Fatalf("cwd=%v, want %s", params["cwd"], cwd)
			}
			labels, _ := params["labels"].(map[string]any)
			if labels["soho.session"] != "sX" {
				t.Fatalf("labels.soho.session=%v, want sX", labels["soho.session"])
			}
			childEnv, _ := params["env"].(map[string]any)
			if childEnv["SOHO_TRANSPORT"] != "hc" || len(childEnv) != 1 {
				t.Fatalf("env=%v, want exactly SOHO_TRANSPORT=hc", childEnv)
			}
			wantArgv := append([]string{tc.kind}, agent...)
			gotArgv, ok := params["argv"].([]any)
			if !ok {
				t.Fatalf("argv not a slice: %v", params["argv"])
			}
			var strArgv []string
			for _, a := range gotArgv {
				strArgv = append(strArgv, fmt.Sprint(a))
			}
			if strings.Join(strArgv, "\x00") != strings.Join(wantArgv, "\x00") {
				t.Fatalf("argv=%q, want %q", strArgv, wantArgv)
			}
			if params["no_notify"] != true || params["outlive_parent"] != true || params["wait"] != false {
				t.Fatalf("unexpected flags in params: %v", params)
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
	// DJA-604 2b-2b acceptance (b): --pane and --tab-label have no meaning
	// to hc; each degrades to the warning and the spawn still opens a new
	// terminal via RPC, with no pane call anywhere.
	for _, flag := range [][]string{{"--pane", "p9"}, {"--tab-label", "herd-x"}} {
		flag := flag
		t.Run(flag[0], func(t *testing.T) {
			cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
			rules := append(hcFixtureRules(),
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			_, stderr, calls := spawnHc(t, bin, append([]string{"worker", "--kind", "pi"}, flag...), ctx, env, cwd)
			assertHcOnlyReads(t, bin, calls)
			warn := "soho: warning: spawn: transport=hc has no panes; --pane and --tab-label are ignored"
			if !strings.Contains(stderr.String(), warn) {
				t.Fatalf("stderr=%q, want the pane warning %q", stderr.String(), warn)
			}
		})
	}
}

func TestSpawnHcNameTakenMovesToTheNextFreeSuffix(t *testing.T) {
	// DJA-604 2b-2b acceptance (c): name_taken twice, then success — the
	// third candidate's name is in the roster, with two "is taken"
	// warnings, one per retry.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(
		rpcHelloSuccess+"\n"+rpcNameTakenFor("worker")+"\n",
		rpcHelloSuccess+"\n"+rpcNameTakenFor("worker-2")+"\n",
		rpcHelloSuccess+"\n"+rpcRunSuccess+"\n",
	)
	_, stderr, calls := spawnHcWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	assertHcOnlyReads(t, bin, calls)
	runs := parseAllTerminalRunReqs(t, conn.getWritten())
	if len(runs) != 3 {
		t.Fatalf("run calls = %d, want 3", len(runs))
	}
	var names []string
	for _, r := range runs {
		p := r["params"].(map[string]any)
		names = append(names, fmt.Sprint(p["name"]))
	}
	if strings.Join(names, ",") != "worker,worker-2,worker-3" {
		t.Fatalf("run names %v, want worker,worker-2,worker-3", names)
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
	var responses []string
	for i := 1; i <= 10; i++ {
		name := "worker"
		if i > 1 {
			name = fmt.Sprintf("worker-%d", i)
		}
		responses = append(responses, rpcHelloSuccess+"\n"+rpcNameTakenFor(name)+"\n")
	}
	conn := newFakeSpawnRPCConn(responses...)
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	assertHcOnlyReads(t, bin, calls)
	if exit == nil || exit.Code != 4 || exit.Msg != "spawn: no free hc terminal name for 'worker' after 10 tries" {
		t.Fatalf("exit=%+v, want exit 4 with the pinned message", exit)
	}
	if conn.getConnectCount() != 10 {
		t.Fatalf("connect count = %d, want 10", conn.getConnectCount())
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

func TestSpawnHcSuffixSearchIsBounded(t *testing.T) {
	// The suffix search after a name_taken reads the live list for each
	// candidate; when every candidate is taken it gives up with exit 4
	// instead of chasing the list.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	var live []string
	for i := 2; i <= 2+hcNameCandidates; i++ {
		live = append(live, fmt.Sprintf(`{"address":"t%d","name":"worker-%d","state":"live","labels":{"soho.session":"sX"}}`, 100+i, i))
	}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: hcCallerTerminal},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: `{"terminals":[` + strings.Join(live, ",") + `]}`},
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcNameTakenFor("worker") + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	assertHcOnlyReads(t, bin, calls)
	want := fmt.Sprintf("spawn: no free hc terminal name for 'worker' among the next %d suffixes", hcNameCandidates)
	if exit == nil || exit.Code != 4 || exit.Msg != want {
		t.Fatalf("exit=%+v, want exit 4 with %q", exit, want)
	}
	if conn.getConnectCount() != 1 {
		t.Fatalf("connect count = %d, want only the first", conn.getConnectCount())
	}
}

func TestSpawnHcBlockedStartClosesTheTerminal(t *testing.T) {
	// DJA-604 2b-2b acceptance (e): a readiness blocked kills and rms the
	// address, dies with the herdr failed-start exit code and the Start
	// failure's message, and writes no roster row.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"live"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"removed":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker is blocked during startup: permission: write to file" {
		t.Fatalf("exit=%+v, want exit 4 with the Start failure's message", exit)
	}
	var kill, rm int
	for _, c := range calls {
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
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"live"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"rm failed"}}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, stderr, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker is blocked during startup: permission: write to file" {
		t.Fatalf("exit=%+v, want exit 4 with the Start failure's message", exit)
	}
	want := "spawn: could not close hc terminal t7 after the failed start; it may still hold the name 'worker'"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want the warning %q", stderr.String(), want)
	}
}

func TestSpawnHcGrokContextWindowIsNotTyped(t *testing.T) {
	// DJA-604 2b-2b acceptance (f): the configured grok context window is
	// not typed under hc — the spawn warns and nothing is sent or read
	// beyond the session reads and the run.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	env["SOHO_CONTEXT_WINDOW_GROK"] = "128k"
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	out, stderr, calls := spawnHc(t, bin, []string{"worker", "--kind", "grok"}, ctx, env, cwd)
	assertHcOnlyReads(t, bin, calls)
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
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	_, stderr, calls := spawnHc(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd)
	assertHcOnlyReads(t, bin, calls)
	warn := "soho: warning: spawn: transport=hc has no rename; the orchestrator keeps its terminal name"
	if !strings.Contains(stderr.String(), warn) {
		t.Fatalf("stderr=%q, want the rename warning %q", stderr.String(), warn)
	}
}

func TestSpawnHcUnknownOutcomeWithoutObservedAddress(t *testing.T) {
	// When terminal.run submission encounters EOF waiting for response,
	// the outcome is uncertain and no address was observed.
	// Spawn must die with exit 4, naming the requested terminal name for manual inspection,
	// with zero retries, zero close/kill calls, and zero roster row.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	// EOF after hello
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	assertHcOnlyReads(t, bin, calls)
	if exit == nil || exit.Code != 4 {
		t.Fatalf("exit=%+v, want exit 4", exit)
	}
	want := "spawn: terminal creation outcome is uncertain; requested terminal 'worker' preserved for manual inspection"
	if exit.Msg != want {
		t.Fatalf("exit.Msg=%q, want %q", exit.Msg, want)
	}
	if conn.getConnectCount() != 1 {
		t.Fatalf("connect count = %d, want 1", conn.getConnectCount())
	}
	for _, c := range calls {
		if c.Argv[0] == "kill" || c.Argv[0] == "rm" {
			t.Fatalf("unexpected cleanup call %v on uncertain outcome", c.Argv)
		}
	}
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("roster row written on uncertain outcome: %q", line)
			}
		}
	}
}

func TestSpawnHcUnknownOutcomeMalformedJSON(t *testing.T) {
	// Malformed JSON on terminal.run response is an uncertain outcome.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + `{"jsonrpc":"2.0",BROKEN` + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	assertHcOnlyReads(t, bin, calls)
	if exit == nil || exit.Code != 4 {
		t.Fatalf("exit=%+v, want exit 4", exit)
	}
	want := "spawn: terminal creation outcome is uncertain; requested terminal 'worker' preserved for manual inspection"
	if exit.Msg != want {
		t.Fatalf("exit.Msg=%q, want %q", exit.Msg, want)
	}
	if conn.getConnectCount() != 1 {
		t.Fatalf("connect count = %d, want 1", conn.getConnectCount())
	}
	for _, c := range calls {
		if c.Argv[0] == "kill" || c.Argv[0] == "rm" {
			t.Fatalf("unexpected cleanup call %v on uncertain outcome", c.Argv)
		}
	}
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("roster row written on uncertain outcome: %q", line)
			}
		}
	}
}

func TestSpawnHcUnknownOutcomeWithObservedAddress(t *testing.T) {
	// When terminal.run outcome is unknown but address was observed,
	// spawn dies with exit 4, naming the terminal address for manual inspection.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}

	client := hc.New(env)
	var exit *platform.ExitError
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					exit = e
				} else {
					panic(value)
				}
			}
		}()
		cmdSpawnWithHcStarter([]string{"worker", "--kind", "pi"}, ctx, env, cwd, func(c *core.Config, o *spawnOptions, k string, e platform.Env, a []string) {
			svc := backend.NewWithClient(e, client)
			// Wrap svc.Terminal to return FailureRunOutcomeUnknown with observed address t99
			origTerm := svc.Terminal
			_ = origTerm
			svc.Terminal = &fakeObservedAddressRuntime{observedAddress: "t99"}
			startHcAgentUsingServices(c, o, k, e, a, svc)
		})
	}()

	if exit == nil || exit.Code != 4 {
		t.Fatalf("exit=%+v, want exit 4", exit)
	}
	want := "spawn: terminal creation outcome is uncertain; terminal t99 preserved for manual inspection"
	if exit.Msg != want {
		t.Fatalf("exit.Msg=%q, want %q", exit.Msg, want)
	}
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("roster row written on uncertain outcome: %q", line)
			}
		}
	}
}

type fakeObservedAddressRuntime struct {
	observedAddress string
}

func (f *fakeObservedAddressRuntime) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{
		Value: f.observedAddress,
		Err: &transport.Failure{
			Kind:    transport.FailureRunOutcomeUnknown,
			Message: "unknown terminal run outcome",
			Cause:   errors.New("connection reset"),
		},
	}
}

func (f *fakeObservedAddressRuntime) CallerAgentName(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{}
}

func (f *fakeObservedAddressRuntime) SendText(ctx context.Context, target transport.Target, text string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{}
}

func (f *fakeObservedAddressRuntime) GetGone(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{}
}

func TestSpawnHcZeroConnectOnValidationRefusal(t *testing.T) {
	// When spawn options fail initial validation (e.g. non-existent cwd),
	// zero RPC connections must be made.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	conn := newFakeSpawnRPCConn()
	exit, _, _, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--cwd", filepath.Join(cwd, "nonexistent")}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 2 {
		t.Fatalf("exit=%+v, want exit 2 for invalid cwd", exit)
	}
	if conn.getConnectCount() != 0 {
		t.Fatalf("connect count = %d, want 0 on validation refusal", conn.getConnectCount())
	}
}
