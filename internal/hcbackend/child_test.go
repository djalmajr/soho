package hcbackend

// R329: the child-run capability the wake arming uses. RunChild sends one
// terminal.run for a child of the caller with the flags the run names, and
// the worker spawn's request (Start) stays at its own flags; RemoveFinishedChild
// closes only a finished terminal of that exact name whose parent is the caller.

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

var _ transport.ChildRunner = (*backend)(nil)

const notFoundStderr = `{"error":{"kind":"not_found","message":"no such terminal"}}`

// TestBackendImplementsChildRunnerOnBothServices pins that the hc services
// expose the capability on the field the wake arming reads first and on the
// Runtime too, and that the herdr-shaped contract has no such method.
func TestBackendImplementsChildRunnerOnBothServices(t *testing.T) {
	env, _ := fakeHc(t, nil)
	svc := New(env)
	if _, ok := svc.Terminal.(transport.ChildRunner); !ok {
		t.Fatal("Services.Terminal does not implement transport.ChildRunner")
	}
	if _, ok := svc.Runtime.(transport.ChildRunner); !ok {
		t.Fatal("Services.Runtime does not implement transport.ChildRunner")
	}
}

// TestRunChildRequest: one terminal.run with the child's argv, the caller's
// terminal as caller, the caller's session label, the notify and outlive
// flags of the run, wait false, and no readiness poll or probe of the child.
func TestRunChildRequest(t *testing.T) {
	for _, tc := range []struct {
		name          string
		run           transport.ChildRun
		noNotify      bool
		outliveParent bool
	}{
		{"notify and end with the caller", transport.ChildRun{NotifyCaller: true, OutliveCaller: false}, false, false},
		{"notify and outlive the caller", transport.ChildRun{NotifyCaller: true, OutliveCaller: true}, false, true},
		{"quiet and end with the caller", transport.ChildRun{NotifyCaller: false, OutliveCaller: false}, true, false},
		{"quiet and outlive the caller are the worker spawn's flags", transport.ChildRun{NotifyCaller: false, OutliveCaller: true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, calls := startEnv(t, nil)
			b := newBackend(env, conn)
			run := tc.run
			run.Name, run.Program, run.Args, run.Cwd = "wake-w1", "/opt/soho", []string{"wait", "w1", "--observe"}, "/proj"
			rep := b.RunChild(context.Background(), run, transport.Call{})
			if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
				t.Fatalf("rep: %+v want Valid with Value t7", rep)
			}
			wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
			req, params := parseTerminalRunReq(t, conn.getWritten())
			if req["method"] != "terminal.run" || req["id"] != float64(2) {
				t.Fatalf("envelope: %v", req)
			}
			if params["name"] != "wake-w1" || params["caller"] != "t1" || params["cwd"] != "/proj" {
				t.Fatalf("name/caller/cwd: %v/%v/%v", params["name"], params["caller"], params["cwd"])
			}
			if !reflect.DeepEqual(params["argv"], []any{"/opt/soho", "wait", "w1", "--observe"}) {
				t.Fatalf("argv: %v", params["argv"])
			}
			if !reflect.DeepEqual(params["labels"], map[string]any{"soho.session": "sX"}) {
				t.Fatalf("labels: %v", params["labels"])
			}
			if !reflect.DeepEqual(params["env"], map[string]any{"SOHO_TRANSPORT": "hc"}) {
				t.Fatalf("env: %v", params["env"])
			}
			if params["no_notify"] != tc.noNotify || params["outlive_parent"] != tc.outliveParent || params["wait"] != false {
				t.Fatalf("flags: no_notify=%v outlive_parent=%v wait=%v", params["no_notify"], params["outlive_parent"], params["wait"])
			}
		})
	}
}

// TestRunChildNameTakenIsConflict: hc's name_taken answer is a
// FailureConflict, as in Start.
func TestRunChildNameTakenIsConflict(t *testing.T) {
	env, conn, _ := startEnv(t, nil)
	conn.responses = rpcHelloSuccess + "\n" + `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"terminal name wake-w1 is already in use","data":{"kind":"name_taken"}}}` + "\n"
	b := newBackend(env, conn)
	rep := b.RunChild(context.Background(), transport.ChildRun{Name: "wake-w1", Program: "/x", Cwd: "/p", NotifyCaller: true}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureConflict || rep.Err.Message != "name_taken: wake-w1" {
		t.Fatalf("err: %+v want conflict name_taken: wake-w1", rep.Err)
	}
}

// TestRunChildRefusesBadNameBeforeAnyLaunch: an invalid name is refused with
// no hc call and no connection.
func TestRunChildRefusesBadNameBeforeAnyLaunch(t *testing.T) {
	for _, name := range []string{"", "Wake-A", "wake w1", strings40()} {
		env, conn, calls := startEnv(t, nil)
		b := newBackend(env, conn)
		rep := b.RunChild(context.Background(), transport.ChildRun{Name: name, Program: "/x", Cwd: "/p"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("name %q: err %+v want a command failure", name, rep.Err)
		}
		if len(calls()) != 0 || conn.getConnectCount() != 0 {
			t.Fatalf("name %q: calls=%v connects=%d, want none", name, calls(), conn.getConnectCount())
		}
	}
}

func strings40() string {
	out := ""
	for i := 0; i < 40; i++ {
		out += "a"
	}
	return out
}

// TestRunChildUnknownOutcome: a run whose outcome is unknown carries that
// failure kind.
func TestRunChildUnknownOutcome(t *testing.T) {
	env, conn, _ := startEnv(t, nil)
	conn.responses = rpcHelloSuccess + "\n"
	b := newBackend(env, conn)
	rep := b.RunChild(context.Background(), transport.ChildRun{Name: "wake-w1", Program: "/x", Cwd: "/p"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureRunOutcomeUnknown {
		t.Fatalf("err: %+v want run-outcome-unknown", rep.Err)
	}
}

// TestRemoveFinishedChild covers each record the removal can meet: only a
// finished terminal of the exact name whose parent is the caller is closed.
func TestRemoveFinishedChild(t *testing.T) {
	finishedMine := `{"address":"t9","name":"wake-w1","state":"finished","exit_code":0,"parent":"t1"}`
	cases := []struct {
		name      string
		rules     []fakecli.Rule
		wantValue bool
		wantErr   bool
		wantCalls [][]string
	}{
		{
			name: "finished child of the caller is killed and removed",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Stdout: finishedMine},
				{Argv: []string{"kill", "t9", "--json"}, Stdout: finishedMine},
				{Argv: []string{"rm", "t9", "--json"}, Stdout: `{"removed":"t9"}`},
			},
			wantValue: true,
			wantCalls: [][]string{{"get", "wake-w1", "--json"}, {"kill", "t9", "--json"}, {"rm", "t9", "--json"}},
		},
		{
			name: "live child is left alone",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Stdout: `{"address":"t9","name":"wake-w1","state":"live","parent":"t1","status":{"state":"working"}}`},
			},
			wantCalls: [][]string{{"get", "wake-w1", "--json"}},
		},
		{
			name: "finished terminal of another parent is left alone",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Stdout: `{"address":"t9","name":"wake-w1","state":"finished","parent":"t2"}`},
			},
			wantCalls: [][]string{{"get", "wake-w1", "--json"}},
		},
		{
			name: "finished terminal without a parent field is left alone",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Stdout: `{"address":"t9","name":"wake-w1","state":"finished"}`},
			},
			wantCalls: [][]string{{"get", "wake-w1", "--json"}},
		},
		{
			name: "record of another name is left alone",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Stdout: `{"address":"t9","name":"wake-w2","state":"finished","parent":"t1"}`},
			},
			wantCalls: [][]string{{"get", "wake-w1", "--json"}},
		},
		{
			name: "no such terminal is not an error",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Code: 3, Stderr: notFoundStderr},
			},
			wantCalls: [][]string{{"get", "wake-w1", "--json"}},
		},
		{
			name: "a failing removal is the failure",
			rules: []fakecli.Rule{
				{Argv: []string{"get", "wake-w1", "--json"}, Stdout: finishedMine},
				{Argv: []string{"kill", "t9", "--json"}, Stdout: finishedMine},
				{Argv: []string{"rm", "t9", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"boom"}}`},
			},
			wantErr:   true,
			wantCalls: [][]string{{"get", "wake-w1", "--json"}, {"kill", "t9", "--json"}, {"rm", "t9", "--json"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, calls := fakeHc(t, tc.rules)
			b := newBackend(env, nil)
			rep := b.RemoveFinishedChild(context.Background(), "wake-w1", transport.Call{})
			if (rep.Err != nil) != tc.wantErr {
				t.Fatalf("err=%v want error=%v", rep.Err, tc.wantErr)
			}
			if !tc.wantErr && (!rep.Valid || rep.Value != tc.wantValue) {
				t.Fatalf("rep=%+v want Valid value %v", rep, tc.wantValue)
			}
			wantArgv(t, calls(), tc.wantCalls)
		})
	}
}

// TestRemoveFinishedChildNeedsACaller: outside an hc terminal nothing can be
// the caller's child, and no hc call is made.
func TestRemoveFinishedChildNeedsACaller(t *testing.T) {
	env, calls := fakeHcNoCaller(t, nil)
	b := newBackend(env, nil)
	rep := b.RemoveFinishedChild(context.Background(), "wake-w1", transport.Call{})
	if rep.Err == nil || !errors.Is(rep.Err.Cause, ErrNotInHcTerminal) {
		t.Fatalf("err: %+v want ErrNotInHcTerminal", rep.Err)
	}
	if len(calls()) != 0 {
		t.Fatalf("calls: %v want none", calls())
	}
}

// R330: RunChild hands the run's StateDir to the request as SOHO_STATE_DIR,
// and a run without one keeps the env at SOHO_TRANSPORT=hc alone.
func TestRunChildStateDirEnv(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "state")
	for _, tc := range []struct {
		name     string
		stateDir string
		want     map[string]any
	}{
		{"unset", "", map[string]any{"SOHO_TRANSPORT": "hc"}},
		{"set", abs, map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_STATE_DIR": abs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, _ := startEnv(t, nil)
			b := newBackend(env, conn)
			rep := b.RunChild(context.Background(), transport.ChildRun{Name: "wake-w1", Program: "/x", Cwd: "/p", StateDir: tc.stateDir}, transport.Call{})
			if rep.Err != nil || !rep.Valid {
				t.Fatalf("rep: %+v", rep)
			}
			_, params := parseTerminalRunReq(t, conn.getWritten())
			if !reflect.DeepEqual(params["env"], tc.want) {
				t.Fatalf("env=%v want %v", params["env"], tc.want)
			}
		})
	}
}
