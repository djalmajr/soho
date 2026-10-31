package hcbackend

import (
	"context"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// fakeHcNoCaller is fakeHc without the caller context: no HC_TERMINAL_ID and
// no Herdr marker, so a listing that answers proves it resolved no caller
// session.
func fakeHcNoCaller(t *testing.T, rules []fakecli.Rule) (platform.Env, func() []fakecli.Call) {
	t.Helper()
	env, calls := fakeHc(t, rules)
	delete(env, "HC_TERMINAL_ID")
	for _, key := range []string{"HERDR_ENV", "HERDR_PANE_ID", "HERDR_WORKSPACE_ID", "HERDR_TAB_ID"} {
		delete(env, key)
	}
	return env, calls
}

// listAllFixture is the unfiltered `hc ls` answer: two labeled live
// terminals (one with a status app matching its argv[0] basename, one with
// an app-less status and a backslashed argv[0]), one unlabeled terminal with
// no status and no argv, one finished terminal (excluded), and one
// machine-prefixed remote address (excluded).
const listAllFixture = `{"terminals":[
{"address":"t1","name":"orch","state":"live","cwd":"/proj","labels":{"soho.session":"sX"},"status":{"state":"working","app":"codex"},"argv":["codex"],"turn":4},
{"address":"t2","name":"w2","state":"live","cwd":"/proj","labels":{"soho.session":"sX"},"status":{"state":"idle","msg":"waiting"},"argv":["C:\\opt\\pi.exe"],"turn":1},
{"address":"t3","state":"live","cwd":"/p","labels":{},"status":null},
{"address":"t4","name":"fin","state":"finished","labels":{"soho.session":"sX"},"status":{"state":"done"},"argv":["x"]},
{"address":"box/t9","name":"remote","state":"live","labels":{"soho.session":"sX"},"status":{"state":"working"},"argv":["y"]}
]}`

// wantListHints asserts the projected program hints of one listed terminal.
func wantListHints(t *testing.T, term transport.Terminal, want []string) {
	t.Helper()
	if !reflect.DeepEqual(term.ProgramHints, want) {
		t.Fatalf("hints %#v want %#v", term.ProgramHints, want)
	}
}

// TestBackendListAllLocal: the All=true list answers with one unfiltered
// hc ls and no caller read, projects the additive record metadata per
// source field, keeps unknown states, and drops finished and remote records.
func TestBackendListAllLocal(t *testing.T) {
	env, calls := fakeHcNoCaller(t, []fakecli.Rule{{Argv: []string{"ls", "--json"}, Stdout: listAllFixture}})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid {
		t.Fatal("not valid")
	}
	wantArgv(t, calls(), [][]string{{"ls", "--json"}})
	if len(rep.Value) != 3 {
		t.Fatalf("terms=%d want 3 (finished and remote excluded): %+v", len(rep.Value), rep.Value)
	}

	a := rep.Value[0]
	if a.Target.ID != "t1" || a.PaneID != "t1" || a.Name != "orch" || a.State != "working" || string(a.Sequence) != "4" {
		t.Fatalf("t1 = %+v", a)
	}
	if a.Cwd != "/proj" || a.WorkspaceID != "sX" || a.Msg != "" {
		t.Fatalf("t1 cwd %q workspace %q msg %q want /proj sX \"\"", a.Cwd, a.WorkspaceID, a.Msg)
	}
	wantListHints(t, a, []string{"codex"}) // the argv[0] basename equals the app

	bt := rep.Value[1]
	if bt.State != "idle" || string(bt.Sequence) != "1" || bt.Msg != "waiting" {
		t.Fatalf("t2 = %+v", bt)
	}
	if bt.WorkspaceID != "sX" || bt.Cwd != "/proj" {
		t.Fatalf("t2 workspace %q cwd %q want sX /proj", bt.WorkspaceID, bt.Cwd)
	}
	wantListHints(t, bt, []string{"pi.exe"}) // backslash path basename, no app

	c := rep.Value[2]
	if c.Target.ID != "t3" || c.Name != "" || c.State != "unknown" || c.Sequence != nil {
		t.Fatalf("t3 = %+v want the unnamed unknown terminal kept", c)
	}
	if c.Cwd != "/p" || c.WorkspaceID != "hc-t3" || c.Msg != "" {
		t.Fatalf("t3 cwd %q workspace %q msg %q want /p hc-t3 \"\"", c.Cwd, c.WorkspaceID, c.Msg)
	}
	wantListHints(t, c, nil)
}

// TestBackendListAllLocalEmptyIsValid: a successful empty unfiltered list is
// a valid empty listing.
func TestBackendListAllLocalEmptyIsValid(t *testing.T) {
	env, _ := fakeHcNoCaller(t, []fakecli.Rule{{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`}})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid || len(rep.Value) != 0 {
		t.Fatalf("rep={%v valid:%v} want a valid empty list", rep.Value, rep.Valid)
	}
}

// TestBackendListAllLocalFailureKinds: a failed launch is unavailable and a
// malformed answer (bad JSON, a non-array terminals list, a non-object
// record) is a protocol refusal of the whole list — no partial rows.
func TestBackendListAllLocalFailureKinds(t *testing.T) {
	t.Run("failed launch is unavailable", func(t *testing.T) {
		env, _ := fakeHcNoCaller(t, []fakecli.Rule{{Argv: []string{"ls", "--json"}, Code: 1, Stderr: "ls exploded"}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.List(context.Background(), transport.Scope{All: true}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err=%v want FailureUnavailable", rep.Err)
		}
		if len(rep.Value) != 0 {
			t.Fatalf("value %v want no partial rows", rep.Value)
		}
	})

	for _, malformed := range []string{
		`nope`,
		`{"terminals":{"a":1}}`,
		`{"terminals":[7]}`,
	} {
		t.Run("malformed answer is protocol", func(t *testing.T) {
			env, _ := fakeHcNoCaller(t, []fakecli.Rule{{Argv: []string{"ls", "--json"}, Stdout: malformed}})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.List(context.Background(), transport.Scope{All: true}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
				t.Fatalf("err=%v want FailureProtocol", rep.Err)
			}
			if len(rep.Value) != 0 {
				t.Fatalf("value %v want no partial rows", rep.Value)
			}
		})
	}
}

// TestBackendListScopedStillResolvesTheCallerSession: All=false keeps the
// caller-scoped form — one session get, then the label-filtered ls (no
// unfiltered listing).
func TestBackendListScopedStillResolvesTheCallerSession(t *testing.T) {
	env, calls := fakeHc(t, []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","labels":{"soho.session":"s9"}}`},
		{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Stdout: `{"terminals":[{"address":"t7","name":"w7","state":"live","status":{"state":"working"},"turn":2}]}`},
	})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid || len(rep.Value) != 1 {
		t.Fatalf("rep={%v valid:%v} want the session terminal", rep.Value, rep.Valid)
	}
	wantArgv(t, calls(), [][]string{
		{"get", "t1", "--json"},
		{"ls", "--label", "soho.session=s9", "--json"},
	})
	if rep.Value[0].Cwd != "" || rep.Value[0].WorkspaceID != "" || rep.Value[0].Msg != "" {
		t.Fatalf("scoped row %v: the default projection is unchanged (no additive metadata)", rep.Value[0])
	}
}

// TestBackendListScopedRefusedWithoutCallerSession: All=false without a
// caller terminal refuses as unavailable (the soho.session label is
// mandatory) and issues no hc call.
func TestBackendListScopedRefusedWithoutCallerSession(t *testing.T) {
	env, calls := fakeHcNoCaller(t, nil)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
		t.Fatalf("err=%v want FailureUnavailable", rep.Err)
	}
	if rep.Err.Message == "" || rep.Err.Message == "cannot resolve the soho.session label: " {
		t.Fatalf("message %q want the cause named", rep.Err.Message)
	}
	if n := len(calls()); n != 0 {
		t.Fatalf("calls=%d want none (no caller to resolve)", n)
	}
}
