package spawn

// DJA-604 2b-2b: spawn through hc. The worker is a new hc terminal of the
// session: one terminal.run JSON-RPC call with the herdr path's agent argv,
// no pane work, the roster row holds the returned address, name_taken
// retries the existing suffix rule. Confirmed blocked starts preserve the
// terminal and managed row with exit 7; ordinary failed starts close it and
// exit 4. The native JSON-RPC transport uses hc.Client.Connect while existing
// scripted get/ls/cleanup calls go through Launch.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/hc"
	backend "github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn/codexhooks"
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
		case "get", "ls", "kill", "rm", "read":
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
	return spawnHcExitWithLaunch(t, bin, argv, ctx, env, cwd, conn, nil)
}

func spawnHcExitWithLaunch(t *testing.T, bin string, argv []string, ctx *core.Config, env platform.Env, cwd string, conn *fakeSpawnRPCConn, adjust func([]string, platform.RunResult) platform.RunResult) (exit *platform.ExitError, out, stderr *bytes.Buffer, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })

	client := hc.New(env)
	if conn != nil {
		client.Connect = conn.Connect
	} else {
		client.Connect = nil
	}
	origLaunch := client.Launch
	client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		if len(args) > 0 && (args[0] == "run" || args[0] == "connect") {
			t.Fatalf("unexpected CLI %s call: %v", args[0], args)
		}
		result := origLaunch(ctx, env, args...)
		if adjust != nil {
			result = adjust(args, result)
		}
		return result
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
			if runtime.GOOS == "windows" {
				env["PATHEXT"] = ".exe"
			}
			res := core.ResolveRoleSettings(tc.role, ctx, env, cwd, core.RoleFlags{})
			agent := hcAgentArgs(t, tc.role, res, ctx, env, cwd)
			if tc.kind == "codex" {
				agent = append(agent, "-c", "check_for_update_on_startup=false")
			}
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
			wantEnv := map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_REGISTRY_DIR": filepath.Join(state, "sX"), "SOHO_AGENT": tc.role}
			if !reflect.DeepEqual(params["env"], wantEnv) {
				t.Fatalf("env=%v, want exactly %v", params["env"], wantEnv)
			}
			exe := filepath.Join(bin, tc.kind)
			if runtime.GOOS == "windows" {
				exe += ".exe"
			}
			wantArgv := append([]string{exe}, agent...)
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

			if !spawnJSONHasField(t, out.String(), `"placement": "hc"`) || !spawnJSONHasField(t, out.String(), `"pane_id": "t7"`) || !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
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
	// Each retry forwards the name it asks for as SOHO_AGENT, so the Start
	// that succeeds carries the name the roster row records.
	for i, r := range runs {
		want := map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_REGISTRY_DIR": filepath.Join(state, "sX"), "SOHO_AGENT": names[i]}
		if got := r["params"].(map[string]any)["env"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d env=%v, want %v", i+1, got, want)
		}
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

func TestSpawnHcBlockedStartPreservesTerminalAndExitsSeven(t *testing.T) {
	// A confirmed native blocked start with message preserves the terminal,
	// performs exactly one RPC terminal creation, reads the screen via hc read
	// on the exact address, writes the managed roster row, emits status
	// blocked_at_startup with exit 7, and executes zero kill, rm, send or keys.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"Permission prompt on screen\n"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 7 {
		t.Fatalf("exit=%+v, want exit 7", exit)
	}
	runs := parseAllTerminalRunReqs(t, conn.getWritten())
	if len(runs) != 1 {
		t.Fatalf("terminal.run count = %d, want exactly 1 creation", len(runs))
	}
	for _, c := range calls {
		switch c.Argv[0] {
		case "kill", "rm", "send", "keys":
			t.Fatalf("unexpected %s call on blocked start: %v", c.Argv[0], c.Argv)
		}
	}
	assertHcOnlyReads(t, bin, calls)
	wantWarn := "soho: warning: agent 'worker' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: hc read t7\n"
	if !strings.Contains(stderr.String(), wantWarn) {
		t.Fatalf("stderr = %q, want warning %q", stderr.String(), wantWarn)
	}
	if strings.Contains(wantWarn, "herdr") {
		t.Fatalf("warning contains legacy herdr instruction: %q", wantWarn)
	}
	outStr := out.String()
	if !spawnJSONHasField(t, outStr, `"status": "blocked_at_startup"`) {
		t.Fatalf("stdout JSON missing status blocked_at_startup: %q", outStr)
	}
	if !spawnJSONHasField(t, outStr, `"pane_id": "t7"`) {
		t.Fatalf("stdout JSON missing pane_id t7: %q", outStr)
	}
	if !spawnJSONHasField(t, outStr, `"placement": "hc"`) {
		t.Fatalf("stdout JSON missing placement hc: %q", outStr)
	}
	if !spawnJSONHasField(t, outStr, `"created_pane": true`) {
		t.Fatalf("stdout JSON missing created_pane true: %q", outStr)
	}
	if !strings.Contains(outStr, "Permission prompt on screen\n") {
		t.Fatalf("stdout missing screen read output: %q", outStr)
	}
	// Exactly one screen read call on address t7
	readCalls := 0
	for _, c := range calls {
		if c.Argv[0] == "read" {
			readCalls++
			if len(c.Argv) < 2 || c.Argv[1] != "t7" {
				t.Fatalf("read call %v does not target address t7", c.Argv)
			}
		}
	}
	if readCalls != 1 {
		t.Fatalf("read calls = %d, want exactly 1 screen read", readCalls)
	}
	// Verify managed roster row
	f := rosterRow(t, state, "sX")
	if f[0] != "worker" || f[1] != "t7" || f[2] != "pi" || f[5] != "1" {
		t.Fatalf("roster row = %v, want worker, t7, pi, created_pane=1", f)
	}
}

func TestSpawnHcBlockedStartWithoutMsg(t *testing.T) {
	// A blocked startup without msg also preserves terminal and exits 7.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"Login screen\n"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, out, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 7 {
		t.Fatalf("exit=%+v, want exit 7", exit)
	}
	for _, c := range calls {
		if c.Argv[0] == "kill" || c.Argv[0] == "rm" {
			t.Fatalf("unexpected cleanup call: %v", c.Argv)
		}
	}
	if !spawnJSONHasField(t, out.String(), `"status": "blocked_at_startup"`) {
		t.Fatalf("stdout missing blocked_at_startup: %q", out.String())
	}
	if !strings.Contains(out.String(), "Login screen\n") {
		t.Fatalf("stdout missing screen text: %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "worker\tt7") {
		t.Fatalf("roster missing worker/t7: %q", string(data))
	}
}

func TestSpawnHcBlockedStartReadFailurePreservesDiagnostic(t *testing.T) {
	// When hc read fails, the original startup diagnostic is preserved in the output.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"Sign in required"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"read failed"}}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, out, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 7 {
		t.Fatalf("exit=%+v, want exit 7", exit)
	}
	for _, c := range calls {
		if c.Argv[0] == "kill" || c.Argv[0] == "rm" {
			t.Fatalf("unexpected cleanup call: %v", c.Argv)
		}
	}
	outStr := out.String()
	if !spawnJSONHasField(t, outStr, `"status": "blocked_at_startup"`) {
		t.Fatalf("stdout missing blocked_at_startup: %q", outStr)
	}
	wantDiag := "agent worker is blocked during startup: Sign in required"
	if !strings.Contains(outStr, wantDiag) {
		t.Fatalf("stdout %q does not contain preserved startup diagnostic %q", outStr, wantDiag)
	}
	data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "worker\tt7") {
		t.Fatalf("roster missing worker/t7: %q", string(data))
	}
}

func TestSpawnHcBlockedStartMissingAddressFailsSafely(t *testing.T) {
	// A blocked failure with empty address must fail safely with exit 4,
	// no fabricated identity or roster row, and zero cleanup/input/retries.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })

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
			svc := backend.New(e)
			fakeTerm := &missingAddrBlockedTerminal{Runtime: svc.Terminal}
			svc.Terminal = fakeTerm
			startHcAgentUsingServices(c, o, k, e, a, svc)
		})
	}()
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker is blocked during startup" {
		t.Fatalf("exit=%+v, want exit 4 with diagnostic", exit)
	}
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("unexpected roster row on missing address: %q", line)
			}
		}
	}
}

type missingAddrBlockedTerminal struct {
	transport.Runtime
}

func (m *missingAddrBlockedTerminal) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{
		Value: "",
		Err: &transport.Failure{
			Kind:    transport.FailureBlocked,
			Message: "agent " + start.Name + " is blocked during startup",
		},
	}
}

func TestSpawnHcBlockedStartPreservesNameConflictSuffix(t *testing.T) {
	// When initial name conflicts with an existing terminal (name_taken),
	// the suffixed candidate (worker-2) is preserved under blocked startup.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"Sign in required"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"Sign in on screen\n"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(
		rpcHelloSuccess+"\n"+rpcNameTakenFor("worker")+"\n",
		rpcHelloSuccess+"\n"+rpcRunSuccess+"\n",
	)
	exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 7 {
		t.Fatalf("exit=%+v, want exit 7", exit)
	}
	for _, c := range calls {
		if c.Argv[0] == "kill" || c.Argv[0] == "rm" {
			t.Fatalf("unexpected cleanup call: %v", c.Argv)
		}
	}
	wantWarn := "soho: warning: agent 'worker-2' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: hc read t7\n"
	if !strings.Contains(stderr.String(), wantWarn) {
		t.Fatalf("stderr = %q, want warning %q", stderr.String(), wantWarn)
	}
	outStr := out.String()
	if !spawnJSONHasField(t, outStr, `"name": "worker-2"`) {
		t.Fatalf("stdout JSON missing name worker-2: %q", outStr)
	}
	if !spawnJSONHasField(t, outStr, `"pane_id": "t7"`) {
		t.Fatalf("stdout JSON missing pane_id t7: %q", outStr)
	}
	if !spawnJSONHasField(t, outStr, `"status": "blocked_at_startup"`) {
		t.Fatalf("stdout JSON missing status blocked_at_startup: %q", outStr)
	}
	data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "worker-2\tt7") {
		t.Fatalf("roster missing worker-2/t7: %q", string(data))
	}
}

func TestSpawnHcOrdinaryFailureClosesTerminal(t *testing.T) {
	// Genuinely cleanup-eligible failure (exited during startup) kills and rms
	// the terminal, dies with exit 4 and writes no roster row.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished","exit_code":1,"reason":"exited"}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"live"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"removed":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker exited during startup (exit 1, exited)" {
		t.Fatalf("exit=%+v, want exit 4 with exited message", exit)
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
	if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("a roster row was written on ordinary failure: %q", line)
			}
		}
	}
}

func TestSpawnHcFailedCloseWarnsWithTheAddress(t *testing.T) {
	// A genuinely cleanup-eligible failure whose close fails still dies with exit 4,
	// and warns first that the terminal may still hold the name.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished","exit_code":1,"reason":"exited"}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"live"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"rm failed"}}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, stderr, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 4 || exit.Msg != "agent worker exited during startup (exit 1, exited)" {
		t.Fatalf("exit=%+v, want exit 4 with exited message", exit)
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
	// Synthetic consumer guard evidence: verifies spawn-side guard behavior when backend
	// Start reports an unknown outcome with an observed address (proves one Start submission,
	// zero Close calls, no cleanup, no roster registration, and exit 4 manual inspection diagnostic).
	// This is synthetic consumer guard evidence, not an address-bearing RPC reply or backend state transition.
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}

	client := hc.New(env)
	termFake := &fakeObservedAddressRuntime{observedAddress: "t99"}
	transFake := &fakeCloseCountingTransport{}

	var exit *platform.ExitError
	var calls []fakecli.Call
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })

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
			transFake.Transport = svc.Runtime
			svc.Terminal = termFake
			svc.Runtime = transFake
			startHcAgentUsingServices(c, o, k, e, a, svc)
		})
	}()
	calls = hcCallLog(t, bin)

	if exit == nil || exit.Code != 4 {
		t.Fatalf("exit=%+v, want exit 4", exit)
	}
	want := "spawn: terminal creation outcome is uncertain; terminal t99 preserved for manual inspection"
	if exit.Msg != want {
		t.Fatalf("exit.Msg=%q, want %q", exit.Msg, want)
	}
	if termFake.startCalls != 1 {
		t.Fatalf("startCalls = %d, want exactly 1 submission", termFake.startCalls)
	}
	if transFake.closeCalls != 0 {
		t.Fatalf("closeCalls = %d, want 0 close calls on uncertain outcome", transFake.closeCalls)
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

type fakeObservedAddressRuntime struct {
	observedAddress string
	startCalls      int
}

func (f *fakeObservedAddressRuntime) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	f.startCalls++
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

type fakeCloseCountingTransport struct {
	transport.Transport
	closeCalls int
}

func (f *fakeCloseCountingTransport) Close(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	f.closeCalls++
	return f.Transport.Close(ctx, target, call)
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

func TestSpawnHcNilConnectFailsLocally(t *testing.T) {
	// A test client with nil Connect fails locally before any Core call or external creation.
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "")
	rules := hcFixtureRules()
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi"}, ctx, env, cwd, nil)
	if exit == nil || exit.Code != 4 {
		t.Fatalf("exit=%+v, want exit 4", exit)
	}
	wantMsg := "terminal run: client connect seam is not configured"
	if exit.Msg != wantMsg {
		t.Fatalf("exit.Msg=%q, want %q", exit.Msg, wantMsg)
	}
	assertHcOnlyReads(t, bin, calls)
}

// SOH-201 phase B: the native hc+opencode primary profile at the spawn
// boundary. The profile is prepared with the explicit resolved model and
// the caller's inline configuration and directory (an empty or malformed
// value refuses before any terminal creation or successful reuse, and no
// other model is defaulted in), the start receives the profile name and
// its explicit env overrides (never the caller's environment), the argv
// carries --standalone and, for full or auto approvals, --auto, with no
// generated -m/--model, the reuse is qualified by the roster terminal's
// own reported record, and the fresh and reused JSON disclose only the
// known profile name.

const opencodePrimaryModel = "testprovider/testmodel"

// captureStartRuntime records the transport.Start the spawn submits and
// answers it with a ready address; it swaps out only svc.Terminal, so the
// rest of the spawn (caller and roster reads, the roster write, the result
// JSON) runs through the real backend services.
type captureStartRuntime struct {
	transport.Runtime
	start transport.Start
	got   bool
}

func (c *captureStartRuntime) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	c.start = start
	c.got = true
	return transport.Reply[string]{Value: "t7", Valid: true}
}

// gateGetTransport answers Transport.Get with one fixed terminal record and
// no screen, so the reuse gate's qualification reads are pinned at the
// consumer boundary.
type gateGetTransport struct {
	transport.Transport
	term transport.Terminal
}

func (g *gateGetTransport) Get(ctx context.Context, _ transport.Target, _ transport.Call) transport.Reply[transport.Terminal] {
	return transport.Reply[transport.Terminal]{Value: g.term, Valid: true}
}

func (g *gateGetTransport) Read(ctx context.Context, _ transport.Target, _ transport.Read, _ transport.Call) transport.Reply[string] {
	return transport.Reply[string]{}
}

// opencodePrimaryReadyTerminal is the exact terminal observation the reuse
// gate must accept for a reusable native opencode worker.
func opencodePrimaryReadyTerminal(targetID string) transport.Terminal {
	return transport.Terminal{
		Target:        transport.Target{ID: targetID},
		LaunchProfile: kinds.OpenCodePrimaryProfile,
		Reported:      true,
		ReporterApp:   "opencode",
		NativeState:   "live",
		State:         "idle",
	}
}

// opencodePrimaryRecord is the fake hc record the strict Get metadata
// projects into opencodePrimaryReadyTerminal for the address it reports.
func opencodePrimaryRecord(address string) string {
	return fmt.Sprintf(`{"address":%q,"state":"live","reported":true,"status":{"state":"idle","app":"opencode"},"labels":{"soho.session":"sX","soho.launch_profile":%q}}`, address, kinds.OpenCodePrimaryProfile)
}

// captureSpawn runs one spawn through the real services with only
// svc.Terminal replaced by a captureStartRuntime, and returns the submitted
// start and the spawn's stdout.
func captureSpawn(t *testing.T, bin string, env platform.Env, ctx *core.Config, cwd string, argv []string) (transport.Start, *bytes.Buffer) {
	t.Helper()
	cap := &captureStartRuntime{}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
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
		client := hc.New(env)
		cmdSpawnWithHcStarter(argv, ctx, env, cwd, func(c *core.Config, o *spawnOptions, k string, e platform.Env, a []string) {
			svc := backend.NewWithClient(e, client)
			svc.Terminal = cap
			startHcAgentUsingServices(c, o, k, e, a, svc)
		})
	}()
	if exit != nil {
		t.Fatalf("unexpected exit: %+v\nstderr: %s", exit, stderr.String())
	}
	return cap.start, out
}

func TestSpawnOpenCodePrimary(t *testing.T) {
	gateMsg := "spawn: reusable opencode worker is not a reported primary-profile worker ready for input"

	t.Run("fresh spawn submits the primary profile and standalone argv", func(t *testing.T) {
		cwd, bin, state, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		if runtime.GOOS == "windows" {
			env["PATHEXT"] = ".exe"
		}
		env["OPENCODE_CONFIG_CONTENT"] = `{"marker":"no-leak-canary"}`
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: opencodePrimaryRecord("t7")},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
		out, _, calls := spawnHcWithConn(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd, conn)
		assertHcOnlyReads(t, bin, calls)

		_, params := parseTerminalRunReq(t, conn.getWritten())
		if params["name"] != "writer" || params["cwd"] != cwd {
			t.Fatalf("name=%v cwd=%v, want writer in %s", params["name"], params["cwd"], cwd)
		}
		exe := filepath.Join(bin, "opencode")
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		wantArgv := []string{exe, "--auto", "--standalone"}
		gotArgv, ok := params["argv"].([]any)
		if !ok {
			t.Fatalf("argv not a slice: %v", params["argv"])
		}
		var strArgv []string
		for _, a := range gotArgv {
			strArgv = append(strArgv, fmt.Sprint(a))
		}
		if strings.Join(strArgv, "\x00") != strings.Join(wantArgv, "\x00") {
			t.Fatalf("argv=%q, want %q (no generated model flag)", strArgv, wantArgv)
		}
		if !spawnJSONHasField(t, out.String(), `"launch_profile": "`+kinds.OpenCodePrimaryProfile+`"`) ||
			!spawnJSONHasField(t, out.String(), `"status": "ready"`) ||
			!spawnJSONHasField(t, out.String(), `"placement": "hc"`) ||
			!spawnJSONHasField(t, out.String(), `"model": "testprovider/testmodel"`) {
			t.Fatalf("unexpected spawn result: %s", out.String())
		}
		if strings.Contains(out.String(), "OPENCODE") || strings.Contains(out.String(), "no-leak-canary") {
			t.Fatalf("the fresh JSON leaks profile environment: %s", out.String())
		}
		f := rosterRow(t, state, "sX")
		if len(f) != 16 || f[0] != "writer" || f[1] != "t7" || f[2] != "opencode" || f[8] != opencodePrimaryModel {
			t.Fatalf("roster row %v, want the 16-column writer/t7/opencode row", f)
		}
		if data, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv")); err == nil && strings.Contains(string(data), "OPENCODE") {
			t.Fatalf("the roster row leaks profile environment: %q", data)
		}
	})

	t.Run("start fields carry the profile and the explicit env only", func(t *testing.T) {
		cwd, bin, _, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		env["OPENCODE_CONFIG_CONTENT"] = `{"marker":"no-leak-canary"}`
		env["OPENCODE_CONFIG_DIR"] = "/cfg-dir-explicit"
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		start, out := captureSpawn(t, bin, env, ctx, cwd, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"})
		if start.Kind != "opencode" || start.Cwd != cwd || start.Name != "writer" || start.Timeout != "60000" {
			t.Fatalf("start fields = %+v, want the opencode start in %s", start, cwd)
		}
		if start.LaunchProfile != kinds.OpenCodePrimaryProfile {
			t.Fatalf("start.LaunchProfile=%q, want %q", start.LaunchProfile, kinds.OpenCodePrimaryProfile)
		}
		if strings.Join(start.Args, "\x00") != strings.Join([]string{"--auto", "--standalone"}, "\x00") {
			t.Fatalf("start.Args=%q, want the standalone plan without a generated model flag", start.Args)
		}
		if len(start.EnvOverrides) != 3 {
			t.Fatalf("start.EnvOverrides=%v, want exactly the three profile keys", start.EnvOverrides)
		}
		if start.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"] != "1" || start.EnvOverrides["OPENCODE_CONFIG_DIR"] != "/cfg-dir-explicit" {
			t.Fatalf("start.EnvOverrides=%v, want the explicit profile overrides", start.EnvOverrides)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(start.EnvOverrides["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
			t.Fatalf("OPENCODE_CONFIG_CONTENT is not an object: %v", err)
		}
		if cfg["model"] != opencodePrimaryModel || cfg["default_agent"] != "build" {
			t.Fatalf("the configuration does not pin the requested model: %v", cfg)
		}
		build, _ := cfg["agents"].(map[string]any)["build"].(map[string]any)
		if build["model"] != opencodePrimaryModel {
			t.Fatalf("agents.build.model=%v, want %s", build["model"], opencodePrimaryModel)
		}
		if !spawnJSONHasField(t, out.String(), `"launch_profile": "`+kinds.OpenCodePrimaryProfile+`"`) {
			t.Fatalf("the fresh JSON does not disclose the known launch profile: %s", out.String())
		}
	})

	t.Run("no ambient env reaches the profile", func(t *testing.T) {
		t.Setenv("OPENCODE_CONFIG_DIR", "/ambient-canary")
		cwd, bin, _, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		env["OPENCODE_CONFIG_CONTENT"] = `{"marker":"no-leak-canary"}`
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		start, _ := captureSpawn(t, bin, env, ctx, cwd, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"})
		if len(start.EnvOverrides) != 2 {
			t.Fatalf("start.EnvOverrides=%v, want exactly the two profile keys (no ambient config dir)", start.EnvOverrides)
		}
		if _, ok := start.EnvOverrides["OPENCODE_CONFIG_DIR"]; ok {
			t.Fatalf("the ambient OPENCODE_CONFIG_DIR leaked into the profile: %v", start.EnvOverrides)
		}
	})

	t.Run("empty or malformed data refuses before start and reuse", func(t *testing.T) {
		cwd, bin, state, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		// The fresh setup's readiness read of the just-created worker
		// must see the primary record, or the real Start refuses with
		// the readiness metadata mismatch before the reuse checks below
		// run.
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: opencodePrimaryRecord("t7")},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		// An empty resolved model refuses: the profile never defaults to
		// another model.
		conn := newFakeSpawnRPCConn()
		exit, out, _, _ := spawnHcExitWithConn(t, bin, []string{"writer", "--approvals", "full"}, ctx, env, cwd, conn)
		if exit == nil || exit.Code != 2 || exit.Msg != "launch profile: model reference is malformed" {
			t.Fatalf("exit=%+v, want the empty-model refusal", exit)
		}
		if conn.getConnectCount() != 0 || strings.Contains(out.String(), `"status"`) {
			t.Fatalf("connect=%d out=%q, want no terminal creation or spawn result", conn.getConnectCount(), out.String())
		}
		// Malformed inline configuration refuses the same way.
		env["OPENCODE_CONFIG_CONTENT"] = `{"marker": oops`
		conn = newFakeSpawnRPCConn()
		exit, out, _, _ = spawnHcExitWithConn(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd, conn)
		if exit == nil || exit.Code != 2 || exit.Msg != "launch profile: inline configuration is not a single non-null JSON object" {
			t.Fatalf("exit=%+v, want the malformed-configuration refusal", exit)
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connect count = %d, want 0 on the refusal", conn.getConnectCount())
		}
		// A fresh primary worker exists and is idle; an incompatible user
		// arg still refuses before the successful reuse it would take.
		env["OPENCODE_CONFIG_CONTENT"] = `{"marker":"no-leak-canary"}`
		fresh, _, _ := spawnHc(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd)
		if !spawnJSONHasField(t, fresh.String(), `"status": "ready"`) {
			t.Fatalf("the fresh primary worker did not start ready: %s", fresh.String())
		}
		rosterBefore, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		conn = newFakeSpawnRPCConn()
		exit, out, _, _ = spawnHcExitWithConn(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full", "--", "--remote"}, ctx, env, cwd, conn)
		if exit == nil || exit.Code != 2 || exit.Msg != "native launch profile is invalid" {
			t.Fatalf("exit=%+v, want the incompatible-arg refusal", exit)
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connect count = %d, want 0 on the refusal", conn.getConnectCount())
		}
		if spawnJSONHasField(t, out.String(), `"reused": true`) {
			t.Fatalf("the reuse was emitted before the refusal: %s", out.String())
		}
		rosterAfter, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if string(rosterBefore) != string(rosterAfter) {
			t.Fatalf("the roster changed on the refusal: %q -> %q", rosterBefore, rosterAfter)
		}
	})

	t.Run("qualified reuse emits ready with the profile", func(t *testing.T) {
		cwd, bin, state, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		// One rule set for both phases: the fresh start's readiness read and
		// the reuse gate's qualification read are the same address, and the
		// primary record satisfies both.
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "writer", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"idle"}}`},
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: opencodePrimaryRecord("t7")},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		fresh, _, _ := spawnHc(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd)
		if !spawnJSONHasField(t, fresh.String(), `"status": "ready"`) {
			t.Fatalf("the fresh primary worker did not start ready: %s", fresh.String())
		}
		rosterBefore, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		conn := newFakeSpawnRPCConn()
		out, stderr, calls := spawnHcWithConn(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd, conn)
		assertHcOnlyReads(t, bin, calls)
		if conn.getConnectCount() != 0 {
			t.Fatalf("connect count = %d, want 0 on the reuse", conn.getConnectCount())
		}
		if !spawnJSONHasField(t, out.String(), `"reused": true`) || !spawnJSONHasField(t, out.String(), `"status": "ready"`) ||
			!spawnJSONHasField(t, out.String(), `"launch_profile": "`+kinds.OpenCodePrimaryProfile+`"`) {
			t.Fatalf("the qualified reuse did not emit ready with the profile: %s", out.String())
		}
		if !strings.Contains(stderr.String(), "reusing idle worker 'writer'") {
			t.Fatalf("stderr=%q, want the reuse warning", stderr.String())
		}
		rosterAfter, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if string(rosterBefore) != string(rosterAfter) {
			t.Fatalf("the roster changed on the reuse: %q -> %q", rosterBefore, rosterAfter)
		}
	})

	t.Run("unqualified reuse refuses before emit", func(t *testing.T) {
		cwd, bin, state, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		// Per-call scripting on the exact get t7 argv (call numbers are
		// one-based per argv, not a global call number): the fresh
		// setup's readiness read is call one and must see the primary
		// record, or the real Start refuses before the reuse is
		// exercised; the reuse gate's qualification read is call two;
		// the same idle worker, but its record reports no launch
		// profile: the legacy form the gate must refuse.
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "writer", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"idle"}}`},
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: opencodePrimaryRecord("t7")},
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"idle","app":"opencode"}}`},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		fresh, _, _ := spawnHc(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd)
		if !spawnJSONHasField(t, fresh.String(), `"status": "ready"`) {
			t.Fatalf("the fresh primary worker did not start ready: %s", fresh.String())
		}
		// The recorded call log proves the fresh setup consumed exactly
		// one get t7 (call one, the primary record); a test that failed
		// the setup alone would never reach the qualification read.
		getT7 := func(calls []fakecli.Call) int {
			n := 0
			for _, c := range calls {
				if len(c.Argv) == 3 && c.Argv[0] == "get" && c.Argv[1] == "t7" && c.Argv[2] == "--json" {
					n++
				}
			}
			return n
		}
		if n := getT7(hcCallLog(t, bin)); n != 1 {
			t.Fatalf("fresh setup get t7 calls = %d, want exactly the one readiness read", n)
		}
		rosterBefore, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		conn := newFakeSpawnRPCConn()
		exit, out, _, calls := spawnHcExitWithConn(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd, conn)
		assertHcOnlyReads(t, bin, calls)
		if exit == nil || exit.Code != 4 || exit.Msg != gateMsg {
			t.Fatalf("exit=%+v, want the fixed reuse refusal", exit)
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connect count = %d, want 0 on the refusal", conn.getConnectCount())
		}
		// The log now carries the second get t7 as well: the refused
		// qualification really read the distinct unqualified record,
		// not the fresh setup's primary one.
		if n := getT7(calls); n != 2 {
			t.Fatalf("get t7 calls after the refusal = %d, want the readiness read plus the qualification read", n)
		}
		for _, c := range calls {
			if c.Argv[0] == "send" || c.Argv[0] == "keys" || c.Argv[0] == "kill" || c.Argv[0] == "rm" || c.Argv[0] == "run" || c.Argv[0] == "connect" {
				t.Fatalf("input or close on the refused reuse: %v", c.Argv)
			}
		}
		if spawnJSONHasField(t, out.String(), `"reused": true`) || spawnJSONHasField(t, out.String(), `"status": "ready"`) {
			t.Fatalf("the ready JSON was emitted on the refusal: %s", out.String())
		}
		rosterAfter, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if string(rosterBefore) != string(rosterAfter) {
			t.Fatalf("the roster role or history changed on the refusal: %q -> %q", rosterBefore, rosterAfter)
		}
	})

	t.Run("the gate requires the queried target identity", func(t *testing.T) {
		cwd, bin, _, env, ctx := hcSpawnFixture(t, "writer", "opencode")
		// The fresh setup's readiness read must see the primary record
		// so the real fresh start succeeds and the direct gate checks
		// below actually run.
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: opencodePrimaryRecord("t7")},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		fresh, _, _ := spawnHc(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd)
		if !spawnJSONHasField(t, fresh.String(), `"status": "ready"`) {
			t.Fatalf("the fresh primary worker did not start ready: %s", fresh.String())
		}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		platform.Stdout, platform.Stderr = out, stderr
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		// Every primary field is set, but the record names another terminal:
		// the identity must not be inferred from the display name.
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
			gateReuse(transport.Services{Runtime: &gateGetTransport{term: opencodePrimaryReadyTerminal("t9")}}, "writer", "writer", "opencode", ctx, env, cwd)
		}()
		if exit == nil || exit.Code != 4 || exit.Msg != gateMsg {
			t.Fatalf("exit=%+v, want the fixed refusal for the mismatched identity", exit)
		}
		// The matching identity is accepted by the gate.
		gateReuse(transport.Services{Runtime: &gateGetTransport{term: opencodePrimaryReadyTerminal("t7")}}, "writer", "writer", "opencode", ctx, env, cwd)
	})

	t.Run("legacy kinds keep the pass-through start", func(t *testing.T) {
		cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
		res := core.ResolveRoleSettings("worker", ctx, env, cwd, core.RoleFlags{})
		wantArgs := hcAgentArgs(t, "worker", res, ctx, env, cwd)
		rules := append(hcFixtureRules(),
			fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
		)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		start, out := captureSpawn(t, bin, env, ctx, cwd, []string{"worker"})
		if start.LaunchProfile != "" || start.EnvOverrides != nil {
			t.Fatalf("start.LaunchProfile=%q EnvOverrides=%v, want the pass-through zero values", start.LaunchProfile, start.EnvOverrides)
		}
		if strings.Join(start.Args, "\x00") != strings.Join(wantArgs, "\x00") {
			t.Fatalf("start.Args=%q, want the legacy argv %q", start.Args, wantArgs)
		}
		if strings.Contains(out.String(), `"launch_profile"`) {
			t.Fatalf("the legacy JSON discloses a launch profile: %s", out.String())
		}
		if f := rosterRow(t, state, "sX"); len(f) != 16 {
			t.Fatalf("roster row %v, want the legacy 16-column row", f)
		}
	})
}

func TestSpawnHcCodexHooksReview(t *testing.T) {
	menu := "Hooks need review\n3 hooks are new or changed.\nHooks can run outside the sandbox after you trust them.\n› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)\nenter confirm · esc skip\n"
	selected := strings.Replace(strings.Replace(menu, "› 1.", "  1.", 1), "  2.", "› 2.", 1)
	jsonScreen := func(s string) string { b, _ := json.Marshal(map[string]string{"text": s}); return string(b) }
	for _, tc := range []struct {
		name, kind, mode   string
		repeat, quote      bool
		wantExit, wantKeys int
	}{
		{"trust", "codex", "trust", false, false, 0, 2},
		{"block", "codex", "block", false, false, 7, 0},
		{"repeat", "codex", "trust", true, false, 7, 2},
		{"other", "pi", "trust", false, false, 4, 0},
		{"quoted", "codex", "trust", false, true, 4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, _, env, ctx := hcSpawnFixture(t, "reviewer", tc.kind)
			ctx.Entries["hooks_review"] = core.ConfigEntry{Value: tc.mode}
			working := `{"address":"t7","state":"live","reported":true,"status":{"state":"working"}}`
			read := []string{"read", "t7", "--lines", "0", "--json"}
			get := []string{"get", "t7", "--json"}
			screen := menu
			if tc.quote {
				screen += "› composer\n"
			}
			rules := hcFixtureRules()
			rules = append(rules, fakecli.Rule{Argv: get, Stdout: working})
			if tc.wantKeys > 0 {
				rules = append(rules, fakecli.Rule{Argv: read, Call: 1, Stdout: jsonScreen(menu)}, fakecli.Rule{Argv: read, Call: 2, Stdout: jsonScreen(selected)}, fakecli.Rule{Argv: read, Call: 3, Stdout: jsonScreen("› composer\n")})
			}
			rules = append(rules, fakecli.Rule{Argv: read, Stdout: jsonScreen(screen)}, fakecli.Rule{Argv: []string{"keys", "t7", "down", "--json"}}, fakecli.Rule{Argv: []string{"keys", "t7", "enter", "--json"}}, fakecli.Rule{Argv: []string{"kill", "t7", "--json"}}, fakecli.Rule{Argv: []string{"rm", "t7", "--json"}})
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			adjust := func(args []string, result platform.RunResult) platform.RunResult {
				if tc.name == "trust" && reflect.DeepEqual(args, get) {
					for _, c := range hcCallLog(t, bin) {
						if reflect.DeepEqual(c.Argv, []string{"keys", "t7", "enter", "--json"}) {
							result.Stdout = hcReadyWorker
						}
					}
				}
				return result
			}
			// The dialog cases end on their own (ready or blocked), so their
			// deadline is generous: under the race detector one fake hc call
			// can take longer than a short deadline. The cases that expect
			// the timeout keep a short one.
			timeout := "1600"
			if tc.wantExit != 4 {
				timeout = "60000"
			}
			exit, out, stderr, calls := spawnHcExitWithLaunch(t, bin, []string{"reviewer", "--timeout", timeout}, ctx, env, cwd, newFakeSpawnRPCConn(), adjust)
			code := 0
			if exit != nil {
				code = exit.Code
			}
			if code != tc.wantExit {
				t.Fatalf("exit=%d want=%d stderr=%s out=%s", code, tc.wantExit, stderr, out)
			}
			keys := 0
			for _, c := range calls {
				if c.Argv[0] == "keys" {
					keys++
				}
				if tc.wantExit == 7 && (c.Argv[0] == "kill" || c.Argv[0] == "rm") {
					t.Fatalf("blocked terminal removed: %v", c.Argv)
				}
			}
			if keys != tc.wantKeys {
				t.Fatalf("keys=%d want=%d calls=%v", keys, tc.wantKeys, calls)
			}
			if tc.wantKeys > 0 && !strings.Contains(stderr.String(), codexhooks.TrustedLine("spawn", "reviewer", "3")) {
				t.Fatalf("missing trusted warning: %s", stderr)
			}
			if tc.wantExit == 7 && !strings.Contains(stderr.String(), codexhooks.BlockedLine("spawn", "reviewer", tc.mode)) {
				t.Fatalf("missing blocked warning: %s", stderr)
			}
		})
	}
}

func TestSpawnHcCodexHooksStalledRedrawDeadline(t *testing.T) {
	if raceDetector {
		// Under the race detector one fake hc call can outlast the 200ms
		// deadline, so the dialog is never read and the case proves
		// nothing; the native jobs run it.
		t.Skip("needs a screen read inside a 200ms deadline")
	}
	menu := "Hooks need review\n3 hooks are new or changed.\n› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting\nenter confirm · esc skip\n"
	cwd, bin, _, env, cfg := hcSpawnFixture(t, "reviewer", "codex")
	cfg.Entries["hooks_review"] = core.ConfigEntry{Value: "trust"}
	env["SOHO_WAIT_POLL_MS"] = "500"
	body, _ := json.Marshal(map[string]string{"text": menu})
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: string(body)},
		fakecli.Rule{Argv: []string{"keys", "t7", "down", "--json"}},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	// The outcome is the timeout (exit 4), never blocked (exit 7): an
	// approval whose redraw pauses ran past the deadline used to report
	// blocked (R439). No wall-clock bound (fake-call overhead varies by
	// host), and exactly one "down": the approval path must run.
	exit, out, stderr, calls := spawnHcExitWithLaunch(t, bin, []string{"reviewer", "--timeout", "200"}, cfg, env, cwd, newFakeSpawnRPCConn(), nil)
	if exit == nil || exit.Code != 4 {
		t.Fatalf("exit=%v calls=%v out=%s stderr=%s", exit, calls, out, stderr)
	}
	keys := []string{}
	for _, c := range calls {
		if c.Argv[0] == "keys" {
			keys = append(keys, c.Argv[2])
		}
	}
	if !reflect.DeepEqual(keys, []string{"down"}) || strings.Contains(stderr.String(), "fakecli: no rule") {
		t.Fatalf("keys=%v calls=%v stderr=%s", keys, calls, stderr)
	}
}
