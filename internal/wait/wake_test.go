package wait

// R329: `soho wait <agent>... --wake` arms one child terminal of the caller
// that runs the non-settling `soho wait ... --observe`, and exits when the
// agents settle. These tests drive CmdWait, the command entry point, with the
// fake hc (reads) and a fake terminal.run connection behind the real hc
// backend, never a live Core.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/hc"
	backend "github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	wakeRPCHello   = `{"jsonrpc":"2.0","id":1,"result":{"generation":1}}`
	wakeRPCRun     = `{"jsonrpc":"2.0","id":2,"result":{"address":"t9"}}`
	wakeRPCTaken   = `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"terminal name is already in use","data":{"kind":"name_taken"}}}`
	wakeNotFound   = `{"error":{"kind":"not_found","message":"no such terminal"}}`
	wakeSessionSW  = "sW"
	wakeHcNameRule = `^[a-z0-9_-]{1,32}$`
)

func TestWaitParentRosterHint(t *testing.T) {
	for _, holds := range []bool{true, false} {
		t.Run(fmt.Sprintf("parent-holds-%t", holds), func(t *testing.T) {
			f := newHcWakeFixture(t, "1", "none")
			parentDir := filepath.Join(f.base, "state", "parent-session")
			if err := os.MkdirAll(parentDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if holds {
				if err := os.WriteFile(filepath.Join(parentDir, "agents.tsv"), []byte("other\tp2\tclaude\trole\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := fakecli.Install(t, f.bin, "hc", []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","parent":"t2","state":"live","labels":{"soho.session":"sW"}}`},
				{Argv: []string{"get", "t2", "--json"}, Stdout: `{"address":"t2","state":"live","labels":{"soho.session":"parent-session"}}`},
			})
			if err != nil {
				t.Fatal(err)
			}
			code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, "other", "--wake")
			base := "agent 'other' is not in the roster (state dir: " + f.sd + ")"
			if holds {
				base += "; this terminal (t1) is a child of t2, whose roster holds 'other': run 'soho wait other --wake' in t2's terminal (--wake creates its own child)"
			}
			if code != 3 || msg != base {
				t.Fatalf("code=%d msg=%q want 3 %q", code, msg, base)
			}
		})
	}
}

// wakeRPCConn is the fake terminal.run connection: it answers the hello and
// the run from a canned response and records what the client wrote and how
// many times it connected. onRun runs when the terminal.run line is written.
type wakeRPCConn struct {
	mu       sync.Mutex
	response string
	connects int
	written  bytes.Buffer
	onRun    func()
}

func newWakeRPCConn(runResponse string) *wakeRPCConn {
	return &wakeRPCConn{response: wakeRPCHello + "\n" + runResponse + "\n"}
}

func (c *wakeRPCConn) Connect(ctx context.Context, _ platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
	c.mu.Lock()
	c.connects++
	c.mu.Unlock()
	return &wakeRPCIn{c: c}, strings.NewReader(c.response), func() error { return nil }, nil
}

type wakeRPCIn struct{ c *wakeRPCConn }

func (in *wakeRPCIn) Write(p []byte) (int, error) {
	in.c.mu.Lock()
	n, err := in.c.written.Write(p)
	hook := in.c.onRun
	in.c.mu.Unlock()
	if hook != nil && strings.Contains(string(p), `"method":"terminal.run"`) {
		hook()
	}
	return n, err
}

func (in *wakeRPCIn) Close() error { return nil }

func (c *wakeRPCConn) connectCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connects
}

// runs returns the decoded params of every terminal.run request written.
func (c *wakeRPCConn) runs(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	text := c.written.String()
	c.mu.Unlock()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if req.Method == "terminal.run" {
			out = append(out, req.Params)
		}
	}
	return out
}

type wakeFixture struct {
	*hcWakeFixture
	conn *wakeRPCConn
}

// newWakeFixture is the hc fixture of the wake tests: the caller terminal t1
// of session sW, a roster of the given agents, the fake hc with the given
// rules behind the session read, and the fake terminal.run connection put
// behind the services the arming uses.
func newWakeFixture(t *testing.T, conn *wakeRPCConn, agents []string, rules ...fakecli.Rule) *wakeFixture {
	t.Helper()
	f := newHcWakeFixture(t, "1", "none")
	roster := ""
	for _, agent := range agents {
		// A role without a role file: the timeout is the configured default.
		roster += agent + "\tp0a\tclaude\tnorole\n"
	}
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.ctx.Entries["dispatch_timeout"] = core.ConfigEntry{Value: "123000"}
	rules = append(rules, fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`})
	f.installHc(t, rules...)
	client := hc.New(f.env)
	client.Connect = conn.Connect
	orig := client.Launch
	client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		if len(args) > 0 && (args[0] == "run" || args[0] == "connect") {
			t.Errorf("unexpected hc %s call: %v", args[0], args)
		}
		return orig(ctx, env, args...)
	}
	old := wakeServices
	wakeServices = func(env platform.Env) transport.Services { return backend.NewWithClient(env, client) }
	t.Cleanup(func() { wakeServices = old })
	return &wakeFixture{hcWakeFixture: f, conn: conn}
}

// cmd runs `soho wait <argv>` through CmdWait: the exit code (from the
// return or the exit panic), the exit message, stdout and stderr.
func (f *wakeFixture) cmd(t *testing.T, argv ...string) (code int, msg, out, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	oldOut, oldErr, oldFriction := platform.Stdout, platform.Stderr, frictionLogFile
	platform.Stdout, platform.Stderr = &o, &e
	SetFrictionLogFile(filepath.Join(f.base, "friction.log"))
	defer func() {
		platform.Stdout, platform.Stderr = oldOut, oldErr
		SetFrictionLogFile(oldFriction)
	}()
	func() {
		defer func() {
			if value := recover(); value != nil {
				exit, ok := value.(*platform.ExitError)
				if !ok {
					panic(value)
				}
				code, msg = exit.Code, exit.Msg
			}
		}()
		code = CmdWait(argv, f.ctx, f.env, f.base)
	}()
	return code, msg, o.String(), e.String()
}

// hcArgvs is the argv of every recorded fake hc call.
func (f *wakeFixture) hcArgvs(t *testing.T) [][]string {
	t.Helper()
	out := [][]string{}
	for _, c := range f.hcCalls(t) {
		out = append(out, c.Argv)
	}
	return out
}

func wakeJSON(t *testing.T, line string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(line), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one JSON line, got %d: %q", len(lines), line)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &obj); err != nil {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
	return obj
}

func notFoundRule(name string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"get", name, "--json"}, Code: 3, Stderr: wakeNotFound}
}

func stringsOf(v any) []string {
	items, _ := v.([]any)
	out := make([]string, len(items))
	for i, item := range items {
		out[i], _ = item.(string)
	}
	return out
}

// Acceptance 1: one terminal.run for a child of the caller with the decided
// argv and flags; the arming JSON; exit 0; no probe of the worker.
func TestWakeArmsOneChildTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		argv     []string
		wantArgs []string
	}{
		{"explicit timeout", []string{"w1", "--wake", "--timeout", "600000"}, []string{"wait", "w1", "--timeout", "600000", "--observe"}},
		{"default timeout is the foreground default", []string{"w1", "--wake"}, []string{"wait", "w1", "--timeout", "123000", "--observe"}},
		{"unit timeout is passed in milliseconds", []string{"w1", "--wake", "--timeout", "10m"}, []string{"wait", "w1", "--timeout", "600000", "--observe"}},
		{"--any rides along before --observe", []string{"w1", "--wake", "--any", "--timeout", "600000"}, []string{"wait", "w1", "--timeout", "600000", "--any", "--observe"}},
		{"flag order does not matter", []string{"--wake", "--timeout", "600000", "w1"}, []string{"wait", "w1", "--timeout", "600000", "--observe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
			code, msg, out, stderr := f.cmd(t, tc.argv...)
			if code != 0 || msg != "" {
				t.Fatalf("code=%d msg=%q stderr=%q, want exit 0", code, msg, stderr)
			}
			runs := conn.runs(t)
			if conn.connectCount() != 1 || len(runs) != 1 {
				t.Fatalf("connects=%d runs=%d, want exactly one terminal.run", conn.connectCount(), len(runs))
			}
			p := runs[0]
			wantArgv := append([]string{platform.LauncherPath(f.env)}, tc.wantArgs...)
			if got := stringsOf(p["argv"]); !reflect.DeepEqual(got, wantArgv) {
				t.Fatalf("argv=%q want %q", got, wantArgv)
			}
			for _, arg := range stringsOf(p["argv"]) {
				if arg == "--wake" {
					t.Fatalf("the child's argv holds --wake: %q", p["argv"])
				}
			}
			if p["caller"] != "t1" || p["name"] != "wake-w1" || p["cwd"] != f.base {
				t.Fatalf("caller/name/cwd = %v/%v/%v", p["caller"], p["name"], p["cwd"])
			}
			if !reflect.DeepEqual(p["labels"], map[string]any{"soho.session": wakeSessionSW}) {
				t.Fatalf("labels=%v, want the caller's session", p["labels"])
			}
			if p["no_notify"] != false || p["outlive_parent"] != false || p["wait"] != false {
				t.Fatalf("flags no_notify=%v outlive_parent=%v wait=%v, want false/false/false", p["no_notify"], p["outlive_parent"], p["wait"])
			}
			arm := wakeJSON(t, out)
			wantTimeout := wantArgs(tc.wantArgs, "--timeout")
			want := map[string]any{"wake": "armed", "agents": []any{"w1"}, "terminal": "t9", "name": "wake-w1", "timeout_ms": wantTimeout}
			if !reflect.DeepEqual(arm, want) {
				t.Fatalf("arming JSON %v, want %v", arm, want)
			}
			// No probe of the worker: the only hc reads are the caller's
			// session (the state dir's, then the run's own), and the
			// finished-record check of the child's own name.
			wantCalls := [][]string{{"get", "t1", "--json"}, {"ls", "--json"}, {"get", "wake-w1", "--json"}, {"get", "t1", "--json"}}
			if got := f.hcArgvs(t); !reflect.DeepEqual(got, wantCalls) {
				t.Fatalf("hc calls %v, want %v", got, wantCalls)
			}
			f.assertNoHerdr(t)
		})
	}
}

// wantArgs reads the number that follows flag in the child's args.
func wantArgs(args []string, flag string) any {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			var n float64
			_ = json.Unmarshal([]byte(args[i+1]), &n)
			return n
		}
	}
	return nil
}

// Several agents arm one child that waits on all of them, with a hash name.
func TestWakeArmsSeveralAgentsWithTheHashName(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	hashName := "wake-5eeaadf3"
	f := newWakeFixture(t, conn, []string{"w2", "w1"}, notFoundRule(hashName))
	code, msg, out, stderr := f.cmd(t, "w2", "w1", "--wake", "--timeout", "90s")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	runs := conn.runs(t)
	if len(runs) != 1 || runs[0]["name"] != hashName {
		t.Fatalf("runs=%v, want one run named %s", runs, hashName)
	}
	wantArgv := []string{platform.LauncherPath(f.env), "wait", "w2", "w1", "--timeout", "90000", "--observe"}
	if got := stringsOf(runs[0]["argv"]); !reflect.DeepEqual(got, wantArgv) {
		t.Fatalf("argv=%q want %q", got, wantArgv)
	}
	arm := wakeJSON(t, out)
	if got := stringsOf(arm["agents"]); !reflect.DeepEqual(got, []string{"w2", "w1"}) || arm["name"] != hashName || arm["timeout_ms"] != float64(90000) {
		t.Fatalf("arming JSON %v", arm)
	}
}

// Acceptance 3: each refusal happens before any terminal is started.
func TestWakeRefusalsStartNoTerminal(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		legacy   bool
		wantCode int
		wantMsg  string
	}{
		{"unknown agent", []string{"ghost", "--wake"}, false, 3, "agent 'ghost' is not in the roster"},
		{"one unknown agent among known ones", []string{"w1", "ghost", "--wake"}, false, 3, "agent 'ghost' is not in the roster"},
		{"bad timeout", []string{"w1", "--wake", "--timeout", "soon"}, false, 2, "wait: --timeout expects milliseconds or a duration"},
		{"missing timeout value", []string{"w1", "--wake", "--timeout"}, false, 2, "wait: --timeout expects a value"},
		{"no agent", []string{"--wake"}, false, 2, "wait: give at least one agent name"},
		{"unknown option", []string{"w1", "--wake", "--sideways"}, false, 2, "wait: unknown option --sideways"},
		{"wake and observe do not combine", []string{"w1", "--wake", "--observe"}, false, 2, "wait: --wake and --observe do not combine"},
		{"legacy transport", []string{"w1", "--wake", "--timeout", "600000"}, true, 2, "wait: --wake needs the native transport (SOHO_TRANSPORT=hc)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
			if tc.legacy {
				f.env["SOHO_TRANSPORT"] = "herdr"
				f.env["HERDR_WORKSPACE_ID"] = wakeSessionSW
				delete(f.env, "HC_TERMINAL_ID")
			}
			before := treeListing(t, f.base)
			code, msg, out, _ := f.cmd(t, tc.argv...)
			if code != tc.wantCode || !strings.HasPrefix(msg, tc.wantMsg) {
				t.Fatalf("code=%d msg=%q, want %d with %q", code, msg, tc.wantCode, tc.wantMsg)
			}
			if out != "" {
				t.Fatalf("stdout %q, want nothing", out)
			}
			if conn.connectCount() != 0 || len(conn.runs(t)) != 0 {
				t.Fatalf("connects=%d runs=%d, want no terminal.run", conn.connectCount(), len(conn.runs(t)))
			}
			for _, argv := range f.hcArgvs(t) {
				if len(argv) > 1 && argv[0] != "get" {
					t.Fatalf("hc call %v after a refusal", argv)
				}
				if len(argv) > 1 && argv[1] == "wake-w1" {
					t.Fatalf("hc looked at the child's name after a refusal: %v", argv)
				}
			}
			if after := treeListing(t, f.base); !reflect.DeepEqual(before, after) {
				t.Fatalf("files changed by a refused --wake:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// treeListing is every file under root with its size, excluding the fakes'
// own call logs and the friction log the refusal path owns. Directories are
// not listed: the state dir's own layout is created by the command's
// ordinary validation, as for a foreground wait that names an unknown agent.
func treeListing(t *testing.T, root string) []string {
	t.Helper()
	out := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if strings.HasSuffix(base, ".calls.jsonl") || base == "friction.log" || filepath.Dir(path) == filepath.Join(root, "bin") {
			return nil
		}
		if !info.IsDir() {
			out = append(out, fmt.Sprintf("%s\t%d", path, info.Size()))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Acceptance 4: a live child of that name answers name_taken: already-armed,
// exit 0, nothing started.
func TestWakeAlreadyArmed(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCTaken)
	liveChild := `{"address":"t9","name":"wake-w1","state":"live","parent":"t1","status":{"state":"working"}}`
	f := newWakeFixture(t, conn, []string{"w1"}, fakecli.Rule{Argv: []string{"get", "wake-w1", "--json"}, Stdout: liveChild})
	code, msg, out, stderr := f.cmd(t, "w1", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q, want exit 0", code, msg, stderr)
	}
	want := map[string]any{"wake": "already-armed", "agents": []any{"w1"}, "name": "wake-w1"}
	if got := wakeJSON(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON %v, want %v", got, want)
	}
	for _, argv := range f.hcArgvs(t) {
		if argv[0] == "kill" || argv[0] == "rm" {
			t.Fatalf("a live child was closed: %v", argv)
		}
	}
}

func TestWakeMembershipReconciliation(t *testing.T) {
	live := `{"terminals":[{"address":"t9","name":"wake-w1","state":"live","parent":"t1"}]}`
	for _, tc := range []struct {
		name               string
		first, second      []string
		secondList         string
		wantRuns           int
		wantArgs           []string
		wantWarning, stale bool
	}{
		{"same agent", []string{"w1", "--wake"}, []string{"w1", "--wake"}, live, 1, nil, true, false},
		{"partial any", []string{"w1", "--wake"}, []string{"w1", "w2", "--any", "--wake"}, live, 2, []string{"wait", "w2", "--timeout", "123000", "--any", "--observe"}, true, false},
		{"stale child", []string{"w1", "--wake"}, []string{"w1", "--wake"}, `{"terminals":[]}`, 2, []string{"wait", "w1", "--timeout", "123000", "--observe"}, false, true},
		{"hashed membership", []string{"w1", "w2", "--any", "--wake"}, []string{"w2", "--wake"}, live, 1, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			firstName := WakeName([]string{"w1"})
			if tc.name == "hashed membership" {
				firstName = WakeName([]string{"w1", "w2"})
			}
			f := newWakeFixture(t, conn, []string{"w1", "w2"},
				fakecli.Rule{Argv: []string{"ls", "--json"}, Call: 1, Stdout: `{"terminals":[]}`},
				fakecli.Rule{Argv: []string{"ls", "--json"}, Call: 2, Stdout: tc.secondList},
				notFoundRule(firstName), notFoundRule("wake-w2"))
			code, msg, _, stderr := f.cmd(t, tc.first...)
			if code != 0 {
				t.Fatalf("first: code=%d msg=%q stderr=%q", code, msg, stderr)
			}
			record := filepath.Join(f.sd, "wakes", "t9")
			data, err := os.ReadFile(record)
			wantRecord := "w1\n"
			if tc.name == "hashed membership" {
				wantRecord = "w1\nw2\n"
			}
			if err != nil || string(data) != wantRecord {
				t.Fatalf("record=%q err=%v want %q", data, err, wantRecord)
			}
			if info, err := os.Stat(record); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("record mode: %v %v", info, err)
			}
			if info, err := os.Stat(filepath.Dir(record)); err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("directory mode: %v %v", info, err)
			}
			conn.response = wakeRPCHello + "\n" + `{"jsonrpc":"2.0","id":2,"result":{"address":"t10"}}` + "\n"
			code, msg, out, stderr := f.cmd(t, tc.second...)
			if code != 0 {
				t.Fatalf("second: code=%d msg=%q stderr=%q", code, msg, stderr)
			}
			warnAgent := "w1"
			if tc.name == "hashed membership" {
				warnAgent = "w2"
			}
			warning := fmt.Sprintf("soho: wait: a wake for '%s' is already armed (t9); not arming another for it\n", warnAgent)
			if (stderr == warning) != tc.wantWarning {
				t.Fatalf("stderr=%q want warning %v (%q)", stderr, tc.wantWarning, warning)
			}
			runs := conn.runs(t)
			if len(runs) != tc.wantRuns {
				t.Fatalf("runs=%d want=%d out=%q", len(runs), tc.wantRuns, out)
			}
			if runs[0]["name"] != firstName {
				t.Fatalf("first wake name=%v want %q", runs[0]["name"], firstName)
			}
			if tc.wantArgs != nil {
				want := append([]string{platform.LauncherPath(f.env)}, tc.wantArgs...)
				if got := stringsOf(runs[1]["argv"]); !reflect.DeepEqual(got, want) {
					t.Fatalf("argv=%q want %q", got, want)
				}
				if got := stringsOf(wakeJSON(t, out)["agents"]); !reflect.DeepEqual(got, tc.wantArgs[1:2]) {
					t.Fatalf("armed agents=%v want %v", got, tc.wantArgs[1:2])
				}
			}
			if tc.stale {
				if _, err := os.Stat(record); !os.IsNotExist(err) {
					t.Fatalf("stale record remains: %v", err)
				}
			}
		})
	}
}

// Any other failure of the run is an error exit 4 with the name in the
// message and nothing on stdout; an unknown outcome says the child may run.
func TestWakeRunFailureIsExit4(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantMsg string
	}{
		{"core refusal", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"no room","data":{"kind":"other"}}}`, "wait: --wake could not start its child terminal 'wake-w1': "},
		{"lost reply", "", "the child may be running: check `hc get wake-w1` before arming again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			if tc.response != "" {
				conn.response = wakeRPCHello + "\n" + tc.response + "\n"
			} else {
				conn.response = wakeRPCHello + "\n"
			}
			f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
			code, msg, out, _ := f.cmd(t, "w1", "--wake", "--timeout", "600000")
			if code != 4 || !strings.Contains(msg, tc.wantMsg) || out != "" {
				t.Fatalf("code=%d msg=%q out=%q, want exit 4 containing %q and no stdout", code, msg, out, tc.wantMsg)
			}
		})
	}
}

// Acceptance 5: the names.
func TestWakeName(t *testing.T) {
	sum := func(joined string) string {
		s := sha256.Sum256([]byte(joined))
		return "wake-" + hex.EncodeToString(s[:])[:8]
	}
	long27 := strings.Repeat("a", 27) // wake- + 27 = 32: the longest that fits
	long28 := strings.Repeat("a", 28) // 33: too long
	cases := []struct {
		name   string
		agents []string
		want   string
	}{
		{"single short agent", []string{"w1"}, "wake-w1"},
		{"single agent with dash and underscore", []string{"impl_a-1"}, "wake-impl_a-1"},
		{"single agent at the limit", []string{long27}, "wake-" + long27},
		{"single over-long agent", []string{long28}, sum(long28)},
		{"single agent with an upper-case letter", []string{"Worker"}, sum("Worker")},
		{"single agent with a dot", []string{"w.1"}, sum("w.1")},
		{"two agents", []string{"w1", "w2"}, "wake-5eeaadf3"},
		{"two agents in the other order are the same child", []string{"w2", "w1"}, "wake-5eeaadf3"},
		{"two agents hash the sorted join", []string{"b", "a"}, "wake-1eb7c54d"},
	}
	rule := regexp.MustCompile(wakeHcNameRule)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WakeName(tc.agents)
			if got != tc.want {
				t.Fatalf("WakeName(%q)=%q, want %q", tc.agents, got, tc.want)
			}
			if !rule.MatchString(got) || !transport.ValidTerminalName(got) {
				t.Fatalf("%q does not satisfy the hc name rule", got)
			}
		})
	}
	if got := WakeName([]string{"a", "b"}); got == WakeName([]string{"a", "c"}) {
		t.Fatalf("different agent sets share the name %q", got)
	}
}

// Acceptance 5, end to end: the over-long agent arms with the hash name.
func TestWakeOverLongAgentArmsWithTheHashName(t *testing.T) {
	long := strings.Repeat("a", 28)
	name := WakeName([]string{long})
	conn := newWakeRPCConn(wakeRPCRun)
	f := newWakeFixture(t, conn, []string{long}, notFoundRule(name))
	if code, msg, _, _ := f.cmd(t, long, "--wake", "--timeout", "5000"); code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q", code, msg)
	}
	runs := conn.runs(t)
	if len(runs) != 1 || runs[0]["name"] != name || strings.Contains(name, long) {
		t.Fatalf("runs=%v, want one run named %s", runs, name)
	}
}

// Acceptance 6: a finished child of the caller with that name is removed
// before the run; a live one, or one of another parent, is not; a failing
// removal does not change the result.
func TestWakeRemovesTheFinishedChildBeforeArming(t *testing.T) {
	const finished = `{"address":"t5","name":"wake-w1","state":"finished","exit_code":0,"parent":"t1"}`
	cases := []struct {
		name        string
		get         fakecli.Rule
		extra       []fakecli.Rule
		wantRemoved bool
	}{
		{"finished child of the caller", fakecli.Rule{Argv: []string{"get", "wake-w1", "--json"}, Stdout: finished}, []fakecli.Rule{
			{Argv: []string{"kill", "t5", "--json"}, Stdout: finished},
			{Argv: []string{"rm", "t5", "--json"}, Stdout: `{"removed":"t5"}`},
		}, true},
		{"live child", fakecli.Rule{Argv: []string{"get", "wake-w1", "--json"}, Stdout: `{"address":"t5","name":"wake-w1","state":"live","parent":"t1"}`}, nil, false},
		{"finished terminal of another parent", fakecli.Rule{Argv: []string{"get", "wake-w1", "--json"}, Stdout: `{"address":"t5","name":"wake-w1","state":"finished","parent":"t2"}`}, nil, false},
		{"removal that fails", fakecli.Rule{Argv: []string{"get", "wake-w1", "--json"}, Stdout: finished}, []fakecli.Rule{
			{Argv: []string{"kill", "t5", "--json"}, Stdout: finished},
			{Argv: []string{"rm", "t5", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"boom"}}`},
		}, true},
		{"get that fails", fakecli.Rule{Argv: []string{"get", "wake-w1", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"boom"}}`}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			f := newWakeFixture(t, conn, []string{"w1"}, append([]fakecli.Rule{tc.get}, tc.extra...)...)
			var callsAtRun [][]string
			conn.onRun = func() { callsAtRun = f.hcArgvs(t) }
			code, msg, out, stderr := f.cmd(t, "w1", "--wake", "--timeout", "600000")
			if code != 0 || msg != "" {
				t.Fatalf("code=%d msg=%q stderr=%q, want the same exit 0 whatever the removal did", code, msg, stderr)
			}
			if got := wakeJSON(t, out); got["wake"] != "armed" || got["terminal"] != "t9" {
				t.Fatalf("JSON %v, want armed", got)
			}
			removedBeforeRun := false
			for _, argv := range callsAtRun {
				if argv[0] == "rm" {
					removedBeforeRun = true
				}
			}
			if removedBeforeRun != tc.wantRemoved {
				t.Fatalf("rm before the run = %v, want %v (calls at the run: %v)", removedBeforeRun, tc.wantRemoved, callsAtRun)
			}
			if !tc.wantRemoved {
				for _, argv := range f.hcArgvs(t) {
					if argv[0] == "kill" || argv[0] == "rm" {
						t.Fatalf("a terminal that is not a finished child of the caller was closed: %v", argv)
					}
				}
			}
			if len(conn.runs(t)) != 1 {
				t.Fatalf("runs=%d, want one", len(conn.runs(t)))
			}
		})
	}
}

// R330: the armed child resolves the caller's state root. When the caller's
// environment sets SOHO_STATE_DIR, the terminal.run env is exactly
// SOHO_TRANSPORT=hc plus SOHO_STATE_DIR=<the caller's resolved root>; when it
// does not, the env is SOHO_TRANSPORT=hc alone. No other SOHO_* variable of
// the caller (SOHO_WAIT_POLL_MS is set by the fixture) is forwarded.
func TestWakeForwardsTheCallersStateRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup edits the fixture env and returns the SOHO_STATE_DIR value
		// the child must receive ("" for no such variable).
		setup func(t *testing.T, f *wakeFixture) string
	}{
		{"neither set: the child env is the transport marker alone", func(t *testing.T, f *wakeFixture) string {
			delete(f.env, "SOHO_STATE_DIR")
			delete(f.env, "SOHO_DIR")
			// The caller now resolves the default root under the project, so
			// the roster moves there for the arming to find the agent.
			root := filepath.Join(platform.StateProjectRoot(f.env, f.base), ".soho", "sW")
			if err := os.MkdirAll(filepath.Join(root, "wait"), 0o700); err != nil {
				t.Fatal(err)
			}
			roster, err := os.ReadFile(filepath.Join(f.sd, "agents.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "agents.tsv"), roster, 0o600); err != nil {
				t.Fatal(err)
			}
			return ""
		}},
		{"SOHO_DIR alone is forwarded as the resolved root", func(t *testing.T, f *wakeFixture) string {
			delete(f.env, "SOHO_STATE_DIR")
			return filepath.Dir(f.sd)
		}},
		{"an absolute value is forwarded cleaned", func(t *testing.T, f *wakeFixture) string {
			delete(f.env, "SOHO_DIR")
			f.env["SOHO_STATE_DIR"] = filepath.Join(f.base, "other", "..", "state") + string(filepath.Separator)
			return filepath.Join(f.base, "state")
		}},
		{"a relative value is the absolute root resolved as the caller's own", func(t *testing.T, f *wakeFixture) string {
			delete(f.env, "SOHO_DIR")
			f.env["SOHO_STATE_DIR"] = "state"
			return filepath.Join(platform.StateProjectRoot(f.env, f.base), "state")
		}},
		{"SOHO_DIR keeps its precedence: the forwarded root is the caller's resolved one", func(t *testing.T, f *wakeFixture) string {
			f.env["SOHO_STATE_DIR"] = filepath.Join(f.base, "ignored")
			return filepath.Join(f.base, "state")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
			want := map[string]any{"SOHO_TRANSPORT": "hc"}
			if root := tc.setup(t, f); root != "" {
				if !filepath.IsAbs(root) {
					t.Fatalf("test root %q is not absolute", root)
				}
				want["SOHO_STATE_DIR"] = root
			}
			code, msg, _, stderr := f.cmd(t, "w1", "--wake", "--timeout", "600000")
			if code != 0 || msg != "" {
				t.Fatalf("code=%d msg=%q stderr=%q, want exit 0", code, msg, stderr)
			}
			runs := conn.runs(t)
			if len(runs) != 1 {
				t.Fatalf("runs=%d, want exactly one terminal.run", len(runs))
			}
			if got := runs[0]["env"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("child env %v, want %v", got, want)
			}
		})
	}
}

// R330: a state root with a CR or LF is refused with exit 2 before any
// terminal.run (zero connections) and before the finished-child cleanup. The
// roster is copied under the odd root so the refusal is the --wake one, not
// the unknown-agent exit.
func TestWakeRefusesAStateRootWithALineBreak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a path with CR or LF cannot be created on Windows")
	}
	for _, tc := range []struct{ name, suffix string }{{"LF", "\n"}, {"CR", "\r"}} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newWakeRPCConn(wakeRPCRun)
			f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
			odd := filepath.Join(f.base, "odd"+tc.suffix+"root")
			if err := os.MkdirAll(filepath.Join(odd, "sW", "wait"), 0o700); err != nil {
				t.Fatal(err)
			}
			roster, err := os.ReadFile(filepath.Join(f.sd, "agents.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(odd, "sW", "agents.tsv"), roster, 0o600); err != nil {
				t.Fatal(err)
			}
			delete(f.env, "SOHO_DIR")
			f.env["SOHO_STATE_DIR"] = odd
			code, msg, out, _ := f.cmd(t, "w1", "--wake", "--timeout", "600000")
			if code != 2 || !strings.Contains(msg, "SOHO_STATE_DIR") || strings.ContainsAny(msg, "\r\n") {
				t.Fatalf("code=%d msg=%q, want exit 2 with a one-line message naming SOHO_STATE_DIR", code, msg)
			}
			if out != "" {
				t.Fatalf("stdout %q, want nothing", out)
			}
			if conn.connectCount() != 0 || len(conn.runs(t)) != 0 {
				t.Fatalf("connects=%d runs=%d, want no terminal.run", conn.connectCount(), len(conn.runs(t)))
			}
			for _, argv := range f.hcArgvs(t) {
				if len(argv) > 1 && argv[1] == "wake-w1" {
					t.Fatalf("hc looked at the child's name after a refusal: %v", argv)
				}
			}
		})
	}
}

func TestWakeOtherCallerPreservesLiveRecord(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	live := `{"terminals":[{"address":"t9","name":"wake-5eeaadf3","state":"live","parent":"t1"}]}`
	f := newWakeFixture(t, conn, []string{"w1", "w2"},
		fakecli.Rule{Argv: []string{"ls", "--json"}, Call: 1, Stdout: `{"terminals":[]}`},
		fakecli.Rule{Argv: []string{"ls", "--json"}, Call: 2, Stdout: live},
		fakecli.Rule{Argv: []string{"ls", "--json"}, Call: 3, Stdout: live},
		notFoundRule(WakeName([]string{"w1", "w2"})), notFoundRule("wake-w1"))
	if code, msg, _, _ := f.cmd(t, "w1", "w2", "--wake"); code != 0 {
		t.Fatalf("first: %d %s", code, msg)
	}
	root, err := openWakeStorage(f.sd)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	remaining, err := uncoveredWakeAgents(wakeServices(f.env), "t2", root, []string{"w1"})
	if err != nil || !reflect.DeepEqual(remaining, []string{"w1"}) {
		t.Fatalf("other caller: %v %v", remaining, err)
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wakes", "t9")); err != nil {
		t.Fatal(err)
	}
	code, msg, _, stderr := f.cmd(t, "w1", "--wake")
	if code != 0 || !strings.Contains(stderr, "already armed (t9)") || len(conn.runs(t)) != 1 {
		t.Fatalf("repeat: %d %s stderr=%q runs=%d", code, msg, stderr, len(conn.runs(t)))
	}
}

func TestWakeRefusesLinkedStorage(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.sd, "wakes")); err != nil {
		t.Fatal(err)
	}
	code, msg, out, stderr := f.cmd(t, "w1", "--wake")
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "keep"))
	if code != 0 || wakeJSON(t, out)["wake"] != "armed" || len(conn.runs(t)) != 1 || strings.Count(stderr, "duplicate detection") != 1 || err != nil || string(data) != "unrelated" || len(entries) != 1 {
		t.Fatalf("code=%d msg=%q stderr=%q entries=%v data=%q err=%v", code, msg, stderr, entries, data, err)
	}
}
