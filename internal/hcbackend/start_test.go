package hcbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	// startCaller is the fake's caller terminal (HC_TERMINAL_ID=t1): it
	// carries the soho.session label the run's --label must pick up.
	startCaller = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	// startReadyIdle is one readiness get: reported and idle.
	startReadyIdle = `{"address":"t7","state":"live","reported":true,"status":{"state":"idle"}}`

	rpcHelloSuccess = `{"jsonrpc":"2.0","id":1,"result":{"generation":1}}`
	rpcRunSuccess   = `{"jsonrpc":"2.0","id":2,"result":{"address":"t7"}}`
)

type fakeRPCConn struct {
	mu           sync.Mutex
	written      bytes.Buffer
	closed       bool
	reapCount    int
	responses    string
	connectErr   error
	connectCount int
	lastCtx      context.Context
	onConnect    func(ctx context.Context)
	beforeRead   func(req []byte)
}

func newFakeRPCConn(responses string) *fakeRPCConn {
	if responses == "" {
		responses = rpcHelloSuccess + "\n" + rpcRunSuccess + "\n"
	}
	return &fakeRPCConn{responses: responses}
}

func (fc *fakeRPCConn) Connect(ctx context.Context, _ platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.connectCount++
	fc.lastCtx = ctx
	if fc.onConnect != nil {
		fc.onConnect(ctx)
	}
	if fc.connectErr != nil {
		return nil, nil, nil, fc.connectErr
	}
	in := &fakeRPCIn{fc: fc}
	out := strings.NewReader(fc.responses)
	reap := func() error {
		fc.mu.Lock()
		fc.reapCount++
		fc.mu.Unlock()
		return nil
	}
	return in, out, reap, nil
}

func (fc *fakeRPCConn) getLastCtx() context.Context {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.lastCtx
}

type fakeRPCIn struct {
	fc *fakeRPCConn
}

func (in *fakeRPCIn) Write(p []byte) (int, error) {
	in.fc.mu.Lock()
	defer in.fc.mu.Unlock()
	n, err := in.fc.written.Write(p)
	if in.fc.beforeRead != nil {
		in.fc.beforeRead(p)
	}
	return n, err
}

func (in *fakeRPCIn) Close() error {
	in.fc.mu.Lock()
	in.fc.closed = true
	in.fc.mu.Unlock()
	return nil
}

func (fc *fakeRPCConn) getWritten() []byte {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	cp := make([]byte, fc.written.Len())
	copy(cp, fc.written.Bytes())
	return cp
}

func (fc *fakeRPCConn) isClosed() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.closed
}

func (fc *fakeRPCConn) getReapCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.reapCount
}

func (fc *fakeRPCConn) getConnectCount() int {
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

func toAnySlice(strs []string) []any {
	out := make([]any, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out
}

// startEnv builds the Start fixture: the caller read, the run answer and
// the given readiness rules for `hc get t7 --json`, in that order.
func startEnv(t *testing.T, readyRules []fakecli.Rule) (platform.Env, *fakeRPCConn, func() []fakecli.Call) {
	t.Helper()
	rules := []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: startCaller}}
	rules = append(rules, readyRules...)
	env, calls := fakeHc(t, rules)
	conn := newFakeRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	return env, conn, calls
}

func newBackend(env platform.Env, conn *fakeRPCConn) *backend {
	client := hc.New(env)
	if conn != nil {
		client.Connect = conn.Connect
	} else {
		client.Connect = nil
	}
	origLaunch := client.Launch
	client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		if len(args) > 0 && (args[0] == "run" || args[0] == "connect") {
			panic(fmt.Sprintf("unexpected CLI command %q; native RPC required", args[0]))
		}
		return origLaunch(ctx, env, args...)
	}
	return &backend{env: env, client: client}
}

// TestStartArgvPerKind records the launch for each of the five kinds and
// asserts the JSON-RPC terminal.run request fields element by element.
func TestStartArgvPerKind(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "pi", "grok", "agy"} {
		t.Run(kind, func(t *testing.T) {
			env, conn, calls := startEnv(t, []fakecli.Rule{
				{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
			})
			b := newBackend(env, conn)
			rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: kind, Timeout: "3000", Args: []string{"--model", "m"}}, transport.Call{})
			if rep.Err != nil {
				t.Fatalf("err: %v", rep.Err)
			}
			if !rep.Valid || rep.Value != "t7" {
				t.Fatalf("rep: %+v want Valid with Value t7", rep)
			}
			wantArgv(t, calls(), [][]string{
				{"get", "t1", "--json"},
				{"get", "t7", "--json"},
			})
			req, params := parseTerminalRunReq(t, conn.getWritten())
			if req["jsonrpc"] != "2.0" || req["method"] != "terminal.run" || req["id"] != float64(2) {
				t.Fatalf("unexpected envelope: %v", req)
			}
			if params["name"] != "w7" || params["caller"] != "t1" {
				t.Fatalf("name/caller: %v/%v", params["name"], params["caller"])
			}
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if params["cwd"] != wd {
				t.Fatalf("cwd: %v want %v", params["cwd"], wd)
			}
			if !reflect.DeepEqual(params["env"], map[string]any{"SOHO_TRANSPORT": "hc"}) {
				t.Fatalf("env: %v", params["env"])
			}
			if !reflect.DeepEqual(params["labels"], map[string]any{"soho.session": "sX"}) {
				t.Fatalf("labels: %v", params["labels"])
			}
			if params["no_notify"] != true || params["outlive_parent"] != true || params["wait"] != false {
				t.Fatalf("flags: no_notify=%v outlive=%v wait=%v", params["no_notify"], params["outlive_parent"], params["wait"])
			}
			if !reflect.DeepEqual(params["argv"], []any{kind, "--model", "m"}) {
				t.Fatalf("argv: %v want [%s --model m]", params["argv"], kind)
			}
			if conn.getReapCount() != 1 || !conn.isClosed() {
				t.Fatalf("reap=%d closed=%v", conn.getReapCount(), conn.isClosed())
			}
		})
	}
}

// TestStartArgvTokensByteForByte: the claude full-mode settings blob and the
// codex trust override go out as single argv elements, byte for byte, with
// no quoting added.
func TestStartArgvTokensByteForByte(t *testing.T) {
	t.Run("claude settings blob", func(t *testing.T) {
		args := []string{"--model", "m", "--permission-mode", "bypassPermissions", "--settings", `{"enableAllProjectMcpServers":true}`}
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000", Args: args}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
		_, params := parseTerminalRunReq(t, conn.getWritten())
		expected := append([]any{"claude"}, toAnySlice(args)...)
		if !reflect.DeepEqual(params["argv"], expected) {
			t.Fatalf("argv: %#v want %#v", params["argv"], expected)
		}
	})

	t.Run("codex trust token", func(t *testing.T) {
		args := []string{"-m", "m", "-s", "workspace-write", "-a", "on-request", "--no-daemon", `-c`, `projects={'/tmp/w7'={trust_level="trusted"}}`}
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "codex", Timeout: "3000", Args: args}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
		_, params := parseTerminalRunReq(t, conn.getWritten())
		expected := append([]any{"codex"}, toAnySlice(args)...)
		if !reflect.DeepEqual(params["argv"], expected) {
			t.Fatalf("argv: %#v want %#v", params["argv"], expected)
		}
	})
}

// TestStartCwd: a non-empty Cwd goes out as cwd; an empty one adopts process os.Getwd.
func TestStartCwd(t *testing.T) {
	t.Run("explicit cwd", func(t *testing.T) {
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Cwd: "/tmp/w7", Timeout: "3000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"get", "t7", "--json"},
		})
		_, params := parseTerminalRunReq(t, conn.getWritten())
		if params["cwd"] != "/tmp/w7" {
			t.Fatalf("cwd=%v want /tmp/w7", params["cwd"])
		}
	})

	t.Run("empty cwd adopts process cwd", func(t *testing.T) {
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Cwd: "", Timeout: "3000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"get", "t7", "--json"},
		})
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		_, params := parseTerminalRunReq(t, conn.getWritten())
		if params["cwd"] != wd {
			t.Fatalf("cwd=%v want %v", params["cwd"], wd)
		}
	})

	t.Run("getwd failure fails before connect", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("skipping directory deletion on Windows: native OS locks current working directory")
		}
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		disposable := t.TempDir()
		if err := os.Chdir(disposable); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(disposable, 0o700)
			if err := os.Chdir(origDir); err != nil {
				t.Errorf("failed to restore working directory: %v", err)
			}
		})
		_ = os.Chmod(disposable, 0o000)
		_ = os.Remove(disposable)

		env, conn, calls := startEnv(t, nil)
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Cwd: "", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep: %+v want FailureUnavailable", rep)
		}
		if !strings.HasPrefix(rep.Err.Message, "getwd: ") {
			t.Fatalf("message %q want prefix 'getwd: '", rep.Err.Message)
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connect count = %d, want 0 when getwd fails before connect", conn.getConnectCount())
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})
}

// TestStartRefusalsBeforeLaunch: an unsupported kind and every invalid
// name refuse before any hc launch or connect.
func TestStartRefusalsBeforeLaunch(t *testing.T) {
	t.Run("unsupported kind", func(t *testing.T) {
		env, conn, calls := startEnv(t, nil)
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "opencode", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnsupported {
			t.Fatalf("rep: %+v want FailureUnsupported", rep)
		}
		want := "kind opencode does not report status under transport=hc; use transport=herdr"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if got := len(calls()); got != 0 {
			t.Fatalf("launches=%d want 0: %#v", got, calls())
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connectCount=%d want 0", conn.getConnectCount())
		}
	})

	names := map[string]string{
		"":                      `worker name '' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		strings.Repeat("a", 33): `worker name '` + strings.Repeat("a", 33) + `' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		"Upper":                 `worker name 'Upper' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		"with.dot":              `worker name 'with.dot' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		"has space":             `worker name 'has space' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
	}
	for name, want := range names {
		t.Run("invalid name "+name, func(t *testing.T) {
			env, conn, calls := startEnv(t, nil)
			b := newBackend(env, conn)
			rep := b.Start(context.Background(), transport.Start{Name: name, Kind: "claude", Timeout: "3000"}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
				t.Fatalf("rep: %+v want FailureCommand", rep)
			}
			if rep.Err.Message != want {
				t.Fatalf("message %q want %q", rep.Err.Message, want)
			}
			if got := len(calls()); got != 0 {
				t.Fatalf("launches=%d want 0: %#v", got, calls())
			}
			if conn.getConnectCount() != 0 {
				t.Fatalf("connectCount=%d want 0", conn.getConnectCount())
			}
		})
	}
}

// TestStartNameBoundary32: a 32-character name is valid and starts.
func TestStartNameBoundary32(t *testing.T) {
	name := strings.Repeat("a", 32)
	env, conn, calls := startEnv(t, []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
	})
	b := newBackend(env, conn)
	rep := b.Start(context.Background(), transport.Start{Name: name, Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
		t.Fatalf("rep: %+v", rep)
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
	_, params := parseTerminalRunReq(t, conn.getWritten())
	if params["name"] != name {
		t.Fatalf("name=%v want %v", params["name"], name)
	}
}

// TestStartNameTaken: a run that fails with hc's name_taken error is a
// FailureConflict named after the worker, and the readiness poll never runs.
func TestStartNameTaken(t *testing.T) {
	env, conn, calls := startEnv(t, nil)
	conn.responses = rpcHelloSuccess + "\n" + `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"terminal name w7 is already in use","data":{"kind":"name_taken"}}}` + "\n"
	b := newBackend(env, conn)
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureConflict {
		t.Fatalf("rep: %+v want FailureConflict", rep)
	}
	if rep.Err.Message != "name_taken: w7" {
		t.Fatalf("message %q want %q", rep.Err.Message, "name_taken: w7")
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
}

// TestStartRunFailure: a run failure with any other hc error kind is a
// FailureCommand carrying the hc error's own text.
func TestStartRunFailure(t *testing.T) {
	env, conn, calls := startEnv(t, nil)
	conn.responses = rpcHelloSuccess + "\n" + `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"/nope is not a directory","data":{"kind":"cwd_missing"}}}` + "\n"
	b := newBackend(env, conn)
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
		t.Fatalf("rep: %+v want FailureCommand", rep)
	}
	want := "hc exited 0: cwd_missing: /nope is not a directory"
	if rep.Err.Message != want {
		t.Fatalf("message %q want %q", rep.Err.Message, want)
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
}

// TestStartSessionUnresolvable: outside an hc terminal the session cannot
// be labelled, so Start refuses before any launch.
func TestStartSessionUnresolvable(t *testing.T) {
	env, conn, calls := startEnv(t, nil)
	delete(env, "HC_TERMINAL_ID")
	b := newBackend(env, conn)
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
		t.Fatalf("rep: %+v want FailureUnavailable", rep)
	}
	if rep.Err.Message != "not inside an hc terminal: HC_TERMINAL_ID is not set" {
		t.Fatalf("message %q", rep.Err.Message)
	}
	if got := len(calls()); got != 0 {
		t.Fatalf("launches=%d want 0", got)
	}
	if conn.getConnectCount() != 0 {
		t.Fatalf("connectCount=%d want 0", conn.getConnectCount())
	}
}

// TestStartReadiness: the readiness poll, each case with a scripted fake
// get sequence on the new terminal's address.
func TestStartReadiness(t *testing.T) {
	startWorker := func(t *testing.T, readyRules []fakecli.Rule, timeout string) transport.Reply[string] {
		t.Helper()
		env, conn, _ := startEnv(t, readyRules)
		return newBackend(env, conn).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: timeout}, transport.Call{})
	}

	t.Run("null, working, then idle: ready, the address goes out", func(t *testing.T) {
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":false,"status":null}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 3, Stdout: startReadyIdle},
		})
		rep := newBackend(env, conn).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "10000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
		})
	})

	t.Run("done: ready", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"done"}}`},
		}, "3000")
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
	})

	t.Run("idle without reported: keeps polling until reported", func(t *testing.T) {
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"idle"}}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: startReadyIdle},
		})
		rep := newBackend(env, conn).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
		})
	})

	t.Run("a get that hangs ends with the startup deadline, not its own budget", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t1"}
		conn := newFakeRPCConn("")
		b := newBackend(env, conn)
		var remaining time.Duration
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			zero := 0
			switch {
			case args[1] == "t1":
				return platform.RunResult{Status: &zero, Stdout: startCaller}
			}
			if deadline, ok := ctx.Deadline(); ok {
				remaining = time.Until(deadline)
			}
			<-ctx.Done()
			exit := 124
			return platform.RunResult{Status: &exit, Stderr: `{"error":{"kind":"timeout","message":"timed out"}}`}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		begin := time.Now()
		rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "150"}, transport.Call{})
		elapsed := time.Since(begin)
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want the startup timeout", rep)
		}
		if remaining > 200*time.Millisecond {
			t.Fatalf("the hanging get had %v left, want at most the 150 ms startup deadline", remaining)
		}
		if elapsed > time.Second {
			t.Fatalf("elapsed %v, want the start to end near its 150 ms deadline", elapsed)
		}
	})

	t.Run("blocked with msg", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 is blocked during startup: permission: write to file"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("blocked without msg: ends at startup", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked"}}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 is blocked during startup"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("finished with exit code and reason", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"finished","exit_code":1,"reason":"exited"}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 exited during startup (exit 1, exited)"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("finished with exit code only", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"finished","exit_code":130}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 exited during startup (exit 130)"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("get exit 3: the exited failure without the parts hc does not report", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal t7"}}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 exited during startup"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("never ready: the timeout failure, bounded, no error to report", func(t *testing.T) {
		env, conn, _ := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		})
		b := newBackend(env, conn)
		start := time.Now()
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
		elapsed := time.Since(start)
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 still not ready after 0.3s"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
		if elapsed < 300*time.Millisecond {
			t.Fatalf("elapsed %v: the poll gave up before the 300 ms deadline", elapsed)
		}
		if elapsed >= 300*time.Millisecond+2*time.Second {
			t.Fatalf("elapsed %v: past the deadline plus 2 s", elapsed)
		}
	})

	t.Run("never ready and the gets keep failing: the last error goes in the timeout message", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
		}, "300")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 still not ready after 0.3s: hc exited 1: core_unreachable: connection refused"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("a cancelled context stops the poll", func(t *testing.T) {
		conn := newFakeRPCConn("")
		b := newBackend(platform.Env{"HC_TERMINAL_ID": "t1"}, conn)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b.client.Launch = func(lctx context.Context, env platform.Env, args ...string) platform.RunResult {
			zero := 0
			if args[1] == "t1" {
				return platform.RunResult{Status: &zero, Stdout: startCaller}
			}
			cancel()
			return platform.RunResult{Status: &zero, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`}
		}
		rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "10000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureCancelled {
			t.Fatalf("rep: %+v want FailureCancelled", rep)
		}
		if rep.Err.Message != "context canceled" {
			t.Fatalf("message %q", rep.Err.Message)
		}
		if rep.Value != "t7" {
			t.Fatalf("rep: %+v want the address t7 in Value so the caller can close it", rep)
		}
	})

	t.Run("a transient get failure, then idle: ready", func(t *testing.T) {
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Code: 1, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: startReadyIdle},
		})
		rep := newBackend(env, conn).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "10000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
		})
	})
}

// startReadinessRunner keeps Start, session resolution and hc decoding real;
// only the per-client process launcher is replaced. Every launch is targeted
// explicitly, so these context fixtures cannot invoke a real hc process.
func startReadinessRunner(t *testing.T, get func(context.Context) platform.RunResult) (*backend, func() []fakecli.Call) {
	t.Helper()
	env := platform.Env{"HC_TERMINAL_ID": "t1"}
	conn := newFakeRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	b := newBackend(env, conn)
	var calls []fakecli.Call
	b.client.Launch = func(ctx context.Context, _ platform.Env, args ...string) platform.RunResult {
		calls = append(calls, fakecli.Call{Argv: append([]string(nil), args...)})
		zero := 0
		switch {
		case len(args) > 1 && args[0] == "get" && args[1] == "t1":
			return platform.RunResult{Status: &zero, Stdout: startCaller}
		case len(args) > 1 && args[0] == "get" && args[1] == "t7":
			return get(ctx)
		default:
			t.Fatalf("unexpected hc argv: %#v", args)
			return platform.RunResult{}
		}
	}
	return b, func() []fakecli.Call { return calls }
}

func TestStartReadinessDeadlineInterruptedGet(t *testing.T) {
	zero, one, three := 0, 1, 3
	for _, tc := range []struct {
		name       string
		earlier    *platform.RunResult
		late       platform.RunResult
		diagnostic string
		causeKind  transport.FailureKind
	}{
		{name: "empty_exit_1_without_prior_error", late: platform.RunResult{Status: &one}},
		{name: "empty_exit_0_without_prior_error", late: platform.RunResult{Status: &zero}},
		{
			name:       "earlier_command_error_is_retained",
			earlier:    &platform.RunResult{Status: &one, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
			late:       platform.RunResult{Status: &one},
			diagnostic: "hc exited 1: core_unreachable: connection refused",
			causeKind:  transport.FailureUnavailable,
		},
		{
			name:       "earlier_command_error_survives_empty_output",
			earlier:    &platform.RunResult{Status: &one, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
			late:       platform.RunResult{Status: &zero},
			diagnostic: "hc exited 1: core_unreachable: connection refused",
			causeKind:  transport.FailureUnavailable,
		},
		{
			name:       "earlier_protocol_error_is_retained",
			earlier:    &platform.RunResult{Status: &zero, Stdout: "{"},
			late:       platform.RunResult{Status: &one},
			diagnostic: "hc get returned no terminal record",
			causeKind:  transport.FailureProtocol,
		},
		{name: "late_idle_is_not_ready", late: platform.RunResult{Status: &zero, Stdout: startReadyIdle}},
		{name: "late_not_found_is_not_exited", late: platform.RunResult{Status: &three, Stderr: `{"error":{"kind":"not_found","message":"no such terminal t7"}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				gets := 0
				var getErr error
				b, calls := startReadinessRunner(t, func(getCtx context.Context) platform.RunResult {
					gets++
					if getCtx.Err() != nil {
						t.Fatalf("get %d entered with error %v", gets, getCtx.Err())
					}
					if tc.earlier != nil && gets == 1 {
						return *tc.earlier
					}
					deadline, ok := getCtx.Deadline()
					if !ok || time.Until(deadline) > 300*time.Millisecond {
						t.Fatal("readiness get did not inherit the bounded startup deadline")
					}
					// The actual child deadline releases this receive. No manual
					// cancellation occurs before its error has been captured.
					<-getCtx.Done()
					getErr = getCtx.Err()
					return tc.late
				})
				rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
				if getErr != context.DeadlineExceeded || ctx.Err() != nil {
					t.Fatalf("child error=%v caller error=%v, want only the child deadline", getErr, ctx.Err())
				}
				wantCalls := [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}}
				if tc.earlier != nil {
					wantCalls = append(wantCalls, []string{"get", "t7", "--json"})
				}
				wantArgv(t, calls(), wantCalls)
				if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Valid || rep.Value != "t7" {
					t.Fatalf("rep: %+v want the startup timeout with address t7", rep)
				}
				want := "agent w7 still not ready after 0.3s"
				if tc.diagnostic != "" {
					want += ": " + tc.diagnostic
				}
				if rep.Err.Message != want {
					t.Fatalf("message %q want %q (child=%v, caller=%v, gets=%d)", rep.Err.Message, want, getErr, ctx.Err(), gets)
				}
				if tc.earlier != nil {
					cause, ok := rep.Err.Cause.(*transport.Failure)
					if !ok || cause == nil || cause.Kind != tc.causeKind || cause.Message != tc.diagnostic {
						t.Fatalf("cause: %#v want the earlier %s diagnostic", rep.Err.Cause, tc.causeKind)
					}
				}
			})
		})
	}
}

func TestStartReadinessTimeoutBetweenPolls(t *testing.T) {
	zero, one := 0, 1
	for _, tc := range []struct {
		name       string
		result     platform.RunResult
		diagnostic string
		causeKind  transport.FailureKind
	}{
		{
			name:       "earlier_command_error_retained_when_deadline_expires_between_polls",
			result:     platform.RunResult{Status: &one, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
			diagnostic: "hc exited 1: core_unreachable: connection refused",
			causeKind:  transport.FailureUnavailable,
		},
		{
			name:       "earlier_protocol_error_retained_when_deadline_expires_between_polls",
			result:     platform.RunResult{Status: &zero, Stdout: "{"},
			diagnostic: "hc get returned no terminal record",
			causeKind:  transport.FailureProtocol,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				gets := 0
				b, calls := startReadinessRunner(t, func(getCtx context.Context) platform.RunResult {
					gets++
					if getCtx.Err() != nil {
						t.Fatalf("get %d entered with error %v", gets, getCtx.Err())
					}
					return tc.result
				})
				// A 100ms startup budget with a 200ms poll interval expires while
				// sleeping for the remainder of the interval between polls.
				rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "100"}, transport.Call{})
				if gets != 1 || ctx.Err() != nil {
					t.Fatalf("gets=%d caller error=%v, want exactly 1 completed get before deadline between polls", gets, ctx.Err())
				}
				wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
				if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Valid || rep.Value != "t7" {
					t.Fatalf("rep: %+v want startup timeout with address t7", rep)
				}
				want := "agent w7 still not ready after 0.1s: " + tc.diagnostic
				if rep.Err.Message != want {
					t.Fatalf("message %q want %q", rep.Err.Message, want)
				}
				cause, ok := rep.Err.Cause.(*transport.Failure)
				if !ok || cause == nil || cause.Kind != tc.causeKind || cause.Message != tc.diagnostic {
					t.Fatalf("cause: %#v want the earlier %s diagnostic", rep.Err.Cause, tc.causeKind)
				}
			})
		})
	}
}

func TestStartReadinessCompletedGetDiagnostics(t *testing.T) {
	zero, one := 0, 1
	for _, tc := range []struct {
		name       string
		result     platform.RunResult
		diagnostic string
		causeKind  transport.FailureKind
	}{
		{"command_error", platform.RunResult{Status: &one, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`}, "hc exited 1: core_unreachable: connection refused", transport.FailureUnavailable},
		{"empty_exit_1", platform.RunResult{Status: &one}, "hc exited 1: ", transport.FailureUnavailable},
		{"empty_exit_0", platform.RunResult{Status: &zero}, "hc get returned no terminal record", transport.FailureProtocol},
		{"malformed_output", platform.RunResult{Status: &zero, Stdout: "{"}, "hc get returned no terminal record", transport.FailureProtocol},
		{"deadline_words_in_stderr", platform.RunResult{Status: &one, Stderr: "context deadline exceeded"}, "hc exited 1: context deadline exceeded", transport.FailureUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			gets := 0
			b, calls := startReadinessRunner(t, func(getCtx context.Context) platform.RunResult {
				gets++
				if err := getCtx.Err(); err != nil {
					t.Fatalf("completed get %d entered with error %v", gets, err)
				}
				return tc.result
			})
			rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
			if gets == 0 || ctx.Err() != nil {
				t.Fatalf("gets=%d caller error=%v, want completed gets and a live caller", gets, ctx.Err())
			}
			wantArgv(t, calls()[:2], [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Valid || rep.Value != "t7" {
				t.Fatalf("rep: %+v want the startup timeout with address t7", rep)
			}
			want := "agent w7 still not ready after 0.3s: " + tc.diagnostic
			if rep.Err.Message != want {
				t.Fatalf("message %q want %q", rep.Err.Message, want)
			}
			cause, ok := rep.Err.Cause.(*transport.Failure)
			if !ok || cause == nil || cause.Kind != tc.causeKind || cause.Message != tc.diagnostic {
				t.Fatalf("cause: %#v want the completed %s diagnostic", rep.Err.Cause, tc.causeKind)
			}
		})
	}
}

func TestStartReadinessCallerCancellationPriority(t *testing.T) {
	zero, one, three := 0, 1, 3
	for _, tc := range []struct {
		name   string
		result platform.RunResult
	}{
		{"empty_exit_1", platform.RunResult{Status: &one}},
		{"empty_exit_0", platform.RunResult{Status: &zero}},
		{"idle", platform.RunResult{Status: &zero, Stdout: startReadyIdle}},
		{"not_found", platform.RunResult{Status: &three, Stderr: `{"error":{"kind":"not_found","message":"no such terminal t7"}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var getErr error
			b, calls := startReadinessRunner(t, func(getCtx context.Context) platform.RunResult {
				cancel()
				getErr = getCtx.Err()
				return tc.result
			})
			rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
			if getErr != context.Canceled || ctx.Err() != context.Canceled {
				t.Fatalf("child error=%v caller error=%v, want caller cancellation", getErr, ctx.Err())
			}
			wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCancelled || rep.Valid || rep.Value != "t7" {
				t.Fatalf("rep: %+v want cancellation with address t7", rep)
			}
			if rep.Err.Message != "context canceled" || rep.Err.Cause != context.Canceled {
				t.Fatalf("failure: %+v want the caller cancellation message and cause", rep.Err)
			}
		})
	}
}

func TestStartReadinessTimelyGetResult(t *testing.T) {
	zero, three := 0, 3
	for _, tc := range []struct {
		name   string
		result platform.RunResult
		ready  bool
	}{
		{"idle", platform.RunResult{Status: &zero, Stdout: startReadyIdle}, true},
		{"done", platform.RunResult{Status: &zero, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"done"}}`}, true},
		{"not_found", platform.RunResult{Status: &three, Stderr: `{"error":{"kind":"not_found","message":"no such terminal t7"}}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			b, calls := startReadinessRunner(t, func(getCtx context.Context) platform.RunResult {
				if err := getCtx.Err(); err != nil {
					t.Fatalf("timely get entered with error %v", err)
				}
				return tc.result
			})
			rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
			wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
			if ctx.Err() != nil || rep.Value != "t7" || rep.Valid != tc.ready {
				t.Fatalf("rep: %+v caller error=%v, want address t7 and ready=%v", rep, ctx.Err(), tc.ready)
			}
			if tc.ready {
				if rep.Err != nil {
					t.Fatalf("timely ready result failed: %v", rep.Err)
				}
			} else if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Err.Message != "agent w7 exited during startup" {
				t.Fatalf("rep: %+v want the timely not-found startup exit", rep)
			}
		})
	}
}

func TestStartReadinessShorterCallDeadlineKeepsDiagnostic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const callBudget = 25 * time.Millisecond
	var firstErr error
	var firstFinished time.Time
	gets := 0
	b, calls := startReadinessRunner(t, func(launchCtx context.Context) platform.RunResult {
		gets++
		deadline, ok := launchCtx.Deadline()
		if !ok || time.Until(deadline) > callBudget || launchCtx.Err() != nil {
			t.Fatal("get did not enter with a live, bounded per-call context")
		}
		<-launchCtx.Done()
		if gets == 1 {
			firstErr = launchCtx.Err()
			firstFinished = time.Now()
		}
		exit := 124
		return platform.RunResult{Status: &exit, Stderr: `{"error":{"kind":"timeout","message":"timed out"}}`}
	})
	begin := time.Now()
	rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{Timeout: callBudget})
	elapsed := time.Since(begin)
	if firstErr != context.DeadlineExceeded || !firstFinished.Before(begin.Add(300*time.Millisecond)) || ctx.Err() != nil {
		t.Fatalf("first get error=%v finished after=%v caller error=%v, want a shorter call deadline", firstErr, firstFinished.Sub(begin), ctx.Err())
	}
	wantArgv(t, calls()[:2], [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
	if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Valid || rep.Value != "t7" {
		t.Fatalf("rep: %+v want the startup timeout with address t7", rep)
	}
	want := "agent w7 still not ready after 0.3s: hc exited 124: timeout: timed out"
	if rep.Err.Message != want {
		t.Fatalf("message %q want %q (gets=%d)", rep.Err.Message, want, gets)
	}
	cause, ok := rep.Err.Cause.(*transport.Failure)
	if !ok || cause == nil || cause.Kind != transport.FailureUnavailable || cause.Message != "hc exited 124: timeout: timed out" {
		t.Fatalf("cause: %#v want the shorter call timeout diagnostic", rep.Err.Cause)
	}
	if elapsed < 300*time.Millisecond || elapsed >= 300*time.Millisecond+2*time.Second {
		t.Fatalf("elapsed %v, want the unchanged 300 ms startup bound", elapsed)
	}
}

// TestStartTimeoutParsing: the deadline is milliseconds as a decimal
// string, as herdr takes it; empty, zero or invalid mean 60000.
func TestStartTimeoutParsing(t *testing.T) {
	if d := startTimeout(""); d != 60*time.Second {
		t.Fatalf("empty: %v want 60s", d)
	}
	if d := startTimeout("0"); d != 60*time.Second {
		t.Fatalf("zero: %v want 60s", d)
	}
	if d := startTimeout("abc"); d != 60*time.Second {
		t.Fatalf("invalid: %v want 60s", d)
	}
	if d := startTimeout("-5"); d != 60*time.Second {
		t.Fatalf("negative: %v want 60s", d)
	}
	if d := startTimeout("60000"); d != 60*time.Second {
		t.Fatalf("60000: %v want 60s", d)
	}
	if d := startTimeout("300"); d != 300*time.Millisecond {
		t.Fatalf("300: %v want 300ms", d)
	}
}

// TestStartDeadlineSeconds pins the timeout message's seconds rendering.
func TestStartDeadlineSeconds(t *testing.T) {
	if s := startDeadlineSeconds(60 * time.Second); s != "60" {
		t.Fatalf("%q want 60", s)
	}
	if s := startDeadlineSeconds(300 * time.Millisecond); s != "0.3" {
		t.Fatalf("%q want 0.3", s)
	}
	if s := startDeadlineSeconds(250 * time.Millisecond); s != "0.25" {
		t.Fatalf("%q want 0.25", s)
	}
}

// TestStartRunOutcomeUnknown tests unknown submission outcomes and cancellation order.
func TestStartRunOutcomeUnknown(t *testing.T) {
	t.Run("EOF after submit classifies as unknown", func(t *testing.T) {
		env, _, calls := startEnv(t, nil)
		conn := newFakeRPCConn(rpcHelloSuccess + "\n") // no run response line
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureRunOutcomeUnknown {
			t.Fatalf("rep: %+v want FailureRunOutcomeUnknown", rep)
		}
		if !errors.Is(rep.Err.Cause, hc.ErrRunOutcomeUnknown) {
			t.Fatalf("cause %v want ErrRunOutcomeUnknown", rep.Err.Cause)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})

	t.Run("malformed response after submit classifies as unknown", func(t *testing.T) {
		env, _, calls := startEnv(t, nil)
		conn := newFakeRPCConn(rpcHelloSuccess + "\n{not-json-rpc\n")
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureRunOutcomeUnknown {
			t.Fatalf("rep: %+v want FailureRunOutcomeUnknown", rep)
		}
		if !errors.Is(rep.Err.Cause, hc.ErrRunOutcomeUnknown) {
			t.Fatalf("cause %v want ErrRunOutcomeUnknown", rep.Err.Cause)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})

	t.Run("post-submit cancellation classifies as unknown", func(t *testing.T) {
		env, _, calls := startEnv(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn := newFakeRPCConn("")
		conn.beforeRead = func(req []byte) {
			if strings.Contains(string(req), "terminal.run") {
				cancel()
			}
		}
		b := newBackend(env, conn)
		rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureRunOutcomeUnknown {
			t.Fatalf("rep: %+v want FailureRunOutcomeUnknown", rep)
		}
		if !errors.Is(rep.Err.Cause, hc.ErrRunOutcomeUnknown) {
			t.Fatalf("cause %v want ErrRunOutcomeUnknown", rep.Err.Cause)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})

	t.Run("pre-submit cancellation classifies as cancelled", func(t *testing.T) {
		env, _, calls := startEnv(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		conn := newFakeRPCConn("")
		b := newBackend(env, conn)
		rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureCancelled {
			t.Fatalf("rep: %+v want FailureCancelled", rep)
		}
		if got := len(calls()); got != 0 {
			t.Fatalf("calls = %d want 0", got)
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connectCount = %d want 0", conn.getConnectCount())
		}
	})

	t.Run("connect failure classifies as unavailable", func(t *testing.T) {
		env, _, calls := startEnv(t, nil)
		conn := newFakeRPCConn("")
		conn.connectErr = errors.New("connection refused by core")
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep: %+v want FailureUnavailable", rep)
		}
		if rep.Err.Message != "connection refused by core" {
			t.Fatalf("message %q want %q", rep.Err.Message, "connection refused by core")
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})

	t.Run("pre-submission deadline expiry has no submitted run", func(t *testing.T) {
		env, _, calls := startEnv(t, nil)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Millisecond))
		defer cancel()
		conn := newFakeRPCConn("")
		b := newBackend(env, conn)
		rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureCancelled {
			t.Fatalf("rep: %+v want FailureCancelled", rep)
		}
		if got := len(calls()); got != 0 {
			t.Fatalf("calls = %d want 0", got)
		}
		if conn.getConnectCount() != 0 {
			t.Fatalf("connectCount = %d want 0", conn.getConnectCount())
		}
		if strings.Contains(string(conn.getWritten()), "terminal.run") {
			t.Fatal("terminal.run must not be written on pre-submission deadline expiry")
		}
	})

	t.Run("post-submission deadline expiry classifies as unknown", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t1"}
		parentCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var childCtx context.Context
		conn := newFakeRPCConn("")
		conn.onConnect = func(ctx context.Context) {
			childCtx = ctx
		}
		conn.beforeRead = func(req []byte) {
			if strings.Contains(string(req), "terminal.run") {
				if childCtx == nil {
					t.Fatal("child creation context was not captured by Connect before terminal.run write")
				}
				select {
				case <-childCtx.Done():
				case <-time.After(500 * time.Millisecond):
					t.Fatal("timed out waiting for child creation context deadline expiry")
				}
			}
		}
		b := newBackend(env, conn)
		var calls []fakecli.Call
		b.client.Launch = func(ctx context.Context, _ platform.Env, args ...string) platform.RunResult {
			calls = append(calls, fakecli.Call{Argv: append([]string(nil), args...)})
			zero := 0
			switch {
			case len(args) > 1 && args[0] == "get" && args[1] == "t1":
				return platform.RunResult{Status: &zero, Stdout: startCaller}
			default:
				t.Fatalf("unexpected hc argv: %#v", args)
				return platform.RunResult{}
			}
		}
		const callTimeout = 25 * time.Millisecond
		rep := b.Start(parentCtx, transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{Timeout: callTimeout})
		if rep.Err == nil || rep.Err.Kind != transport.FailureRunOutcomeUnknown {
			t.Fatalf("rep: %+v want FailureRunOutcomeUnknown", rep)
		}
		if !errors.Is(rep.Err.Cause, hc.ErrRunOutcomeUnknown) {
			t.Fatalf("cause %v want ErrRunOutcomeUnknown", rep.Err.Cause)
		}
		if !errors.Is(rep.Err.Cause, context.DeadlineExceeded) {
			t.Fatalf("cause %v want context.DeadlineExceeded", rep.Err.Cause)
		}
		if childCtx == nil {
			t.Fatal("expected child context to be captured by Connect")
		}
		if !errors.Is(childCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("child context error = %v, want context.DeadlineExceeded", childCtx.Err())
		}
		if parentCtx.Err() != nil {
			t.Fatalf("parent context was unexpectedly canceled or expired: %v", parentCtx.Err())
		}
		if got := strings.Count(string(conn.getWritten()), "terminal.run"); got != 1 {
			t.Fatalf("terminal.run occurrences = %d, want 1", got)
		}
		req, _ := parseTerminalRunReq(t, conn.getWritten())
		if req["method"] != "terminal.run" {
			t.Fatalf("method = %v, want terminal.run", req["method"])
		}
		wantArgv(t, calls, [][]string{{"get", "t1", "--json"}})
	})
}

func TestStartNilConnectFailsLocally(t *testing.T) {
	env, _, calls := startEnv(t, nil)
	b := newBackend(env, nil)
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
		t.Fatalf("rep: %+v want FailureUnavailable", rep)
	}
	wantMsg := "terminal run: client connect seam is not configured"
	if rep.Err.Message != wantMsg {
		t.Fatalf("message %q want %q", rep.Err.Message, wantMsg)
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
}

func TestStartCreationTimeoutsAndCancellation(t *testing.T) {
	t.Run("default 30s budget clamps run context", func(t *testing.T) {
		env, conn, _ := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		lastCtx := conn.getLastCtx()
		if lastCtx == nil {
			t.Fatal("lastCtx was nil; Connect was not called with context")
		}
		deadline, ok := lastCtx.Deadline()
		if !ok {
			t.Fatal("expected deadline on creation context from default 30s budget")
		}
		remaining := time.Until(deadline)
		if remaining > 30*time.Second || remaining < 20*time.Second {
			t.Fatalf("remaining deadline %v, want ~30s", remaining)
		}
	})

	t.Run("positive call timeout clamps run context", func(t *testing.T) {
		env, conn, _ := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{Timeout: 5 * time.Second})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		lastCtx := conn.getLastCtx()
		if lastCtx == nil {
			t.Fatal("lastCtx was nil")
		}
		deadline, ok := lastCtx.Deadline()
		if !ok {
			t.Fatal("expected deadline on creation context from call.Timeout")
		}
		remaining := time.Until(deadline)
		if remaining > 5*time.Second || remaining < 3*time.Second {
			t.Fatalf("remaining deadline %v, want ~5s", remaining)
		}
	})

	t.Run("earlier parent deadline is inherited by run context", func(t *testing.T) {
		env, conn, _ := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := newBackend(env, conn)
		parentDeadline := time.Now().Add(2 * time.Second)
		parentCtx, cancel := context.WithDeadline(context.Background(), parentDeadline)
		defer cancel()
		rep := b.Start(parentCtx, transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{Timeout: 10 * time.Second})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		lastCtx := conn.getLastCtx()
		if lastCtx == nil {
			t.Fatal("lastCtx was nil")
		}
		deadline, ok := lastCtx.Deadline()
		if !ok {
			t.Fatal("expected deadline on creation context")
		}
		if deadline.After(parentDeadline) {
			t.Fatalf("run deadline %v was later than parent deadline %v", deadline, parentDeadline)
		}
	})

	t.Run("child context promptly cancelled upon creation while readiness proceeds", func(t *testing.T) {
		var childCtx context.Context
		parentCtx := context.Background()
		readinessVerified := false

		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: startCaller},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		conn := newFakeRPCConn("")
		conn.onConnect = func(ctx context.Context) {
			childCtx = ctx
			if childCtx.Err() != nil {
				t.Fatalf("child context should not be canceled during Connect: %v", childCtx.Err())
			}
		}
		b := newBackend(env, conn)
		origLaunch := b.client.Launch
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			if len(args) >= 2 && args[0] == "get" && args[1] == "t7" {
				readinessVerified = true
				if childCtx == nil {
					t.Fatal("child context was not captured before readiness get")
				}
				if childCtx.Err() != context.Canceled {
					t.Fatalf("child context must be promptly canceled before readiness get: %v", childCtx.Err())
				}
				if ctx.Err() != nil {
					t.Fatalf("parent context must not be canceled during readiness get: %v", ctx.Err())
				}
			}
			return origLaunch(ctx, env, args...)
		}

		rep := b.Start(parentCtx, transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want success t7", rep)
		}
		if !readinessVerified {
			t.Fatal("readiness get t7 was not verified")
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
	})
}
