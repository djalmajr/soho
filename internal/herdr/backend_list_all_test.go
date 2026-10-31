package herdr

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// listAllAgentsJSON is the Scope.All fixture: the four metadata shapes
// (foreground wins, plain cwd, foreground only, neither), the workspace_id
// present and absent, and the agent identifier present and absent.
const listAllAgentsJSON = `{"result":{"agents":[
{"name":"a","pane_id":"p1","agent_status":"working","state_change_seq":3,"cwd":"/proj","foreground_cwd":"/proj/wt","workspace_id":"w9","agent":"claude"},
{"name":"b","pane_id":"p2","agent_status":"idle","state_change_seq":1,"cwd":"/x","workspace_id":"w9"},
{"name":"c","pane_id":"p3","agent_status":"done","foreground_cwd":"/y"},
{"name":"d","pane_id":"p4","agent_status":"blocked"}
]}}`

// wantHints asserts the projected program hints of one listed terminal.
func wantHints(t *testing.T, term transport.Terminal, want []string) {
	t.Helper()
	if !reflect.DeepEqual(term.ProgramHints, want) {
		t.Fatalf("hints %#v want %#v", term.ProgramHints, want)
	}
}

// TestBackendListAllProjectsRecordMetadata: the All=true list runs the same
// agent-listing argv through the tolerant seam and projects the additive
// record metadata (cwd precedence, workspace_id, agent hint) per source
// field.
func TestBackendListAllProjectsRecordMetadata(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: listAllAgentsJSON}})
	readyProbe(t, env)
	svc := New(env)
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid {
		t.Fatal("not valid")
	}
	if len(rep.Value) != 4 {
		t.Fatalf("terms=%d want 4", len(rep.Value))
	}
	wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}})

	a := rep.Value[0]
	if a.Target.ID != "a" || a.Name != "a" || a.PaneID != "p1" || a.State != "working" || string(a.Sequence) != "3" {
		t.Fatalf("a = %+v", a)
	}
	if a.Cwd != "/proj/wt" {
		t.Fatalf("a cwd %q want the foreground_cwd /proj/wt", a.Cwd)
	}
	if a.WorkspaceID != "w9" {
		t.Fatalf("a workspace %q want w9", a.WorkspaceID)
	}
	wantHints(t, a, []string{"claude"})

	b := rep.Value[1]
	if b.Cwd != "/x" || b.WorkspaceID != "w9" {
		t.Fatalf("b cwd %q workspace %q want /x w9", b.Cwd, b.WorkspaceID)
	}
	wantHints(t, b, nil)

	c := rep.Value[2]
	if c.Cwd != "/y" || c.WorkspaceID != "" {
		t.Fatalf("c cwd %q workspace %q want /y \"\"", c.Cwd, c.WorkspaceID)
	}

	d := rep.Value[3]
	if d.Cwd != "" || d.State != "blocked" {
		t.Fatalf("d cwd %q state %q want \"\" blocked", d.Cwd, d.State)
	}
}

// TestBackendListAllEmptyListIsValid: a successful empty agent list is a
// valid empty listing, not a failure.
func TestBackendListAllEmptyListIsValid(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}})
	readyProbe(t, env)
	svc := New(env)
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid || len(rep.Value) != 0 {
		t.Fatalf("rep={%v valid:%v} want a valid empty list", rep.Value, rep.Valid)
	}
}

// TestBackendListAllNonObjectRowRefusesTheWholeList: under Scope.All a
// non-object row of the agent array refuses the whole observation — a
// protocol failure with an invalid reply and no partial rows (the valid row
// before the malformed one is not returned), and nothing is printed.
func TestBackendListAllNonObjectRowRefusesTheWholeList(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"a","pane_id":"p1","agent_status":"working"},"junk",{"name":"b","pane_id":"p2","agent_status":"idle"}]}}`}})
	readyProbe(t, env)
	svc := New(env)
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
		t.Fatalf("err=%v want FailureProtocol", rep.Err)
	}
	if rep.Err.Message != "herdr agent list returned a non-object agent" {
		t.Fatalf("message %q", rep.Err.Message)
	}
	if rep.Valid {
		t.Fatal("valid=true want false")
	}
	if len(rep.Value) != 0 {
		t.Fatalf("value %v want no partial rows (the valid row before the malformed one is not returned)", rep.Value)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("printed stdout=%q stderr=%q, want nothing", out.String(), errOut.String())
	}
	wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}})
}

// TestBackendListScopedSkipsNonObjectRows: All=false keeps the adapter's
// skip behavior — a non-object row is skipped and the object rows are
// listed with no error.
func TestBackendListScopedSkipsNonObjectRows(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":["junk",{"name":"a","pane_id":"p1","agent_status":"working"}]}}`}})
	readyProbe(t, env)
	svc := New(env)
	rep := svc.Runtime.List(context.Background(), transport.Scope{WorkspaceID: "w9"}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid || len(rep.Value) != 1 {
		t.Fatalf("rep={%v valid:%v} want the single object entry (skip preserved)", rep.Value, rep.Valid)
	}
	if rep.Value[0].Name != "a" {
		t.Fatalf("name %q want a", rep.Value[0].Name)
	}
}

// TestBackendListAllFailureKinds: the tolerant seam maps a missing CLI, a
// timeout, a nonzero command and a malformed or list-less body onto typed
// failures, prints nothing, and returns no partial rows.
func TestBackendListAllFailureKinds(t *testing.T) {
	t.Run("missing CLI is unavailable", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err=%v want FailureUnavailable", rep.Err)
		}
		if rep.Err.Message != "herdr CLI not found in PATH" {
			t.Fatalf("message %q", rep.Err.Message)
		}
		if len(rep.Value) != 0 {
			t.Fatalf("value %v want no partial rows", rep.Value)
		}
	})

	t.Run("nonzero command is unavailable with its cause, printed nowhere", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Code: 1, Stderr: "boom\n"}})
		readyProbe(t, env)
		svc := New(env)
		var out, errOut bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &out, &errOut
		rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err=%v want FailureUnavailable", rep.Err)
		}
		if rep.Err.Message != "boom" {
			t.Fatalf("message %q want the trimmed stderr", rep.Err.Message)
		}
		if len(rep.Value) != 0 {
			t.Fatalf("value %v want no partial rows", rep.Value)
		}
		if out.Len() != 0 || errOut.Len() != 0 {
			t.Fatalf("printed stdout=%q stderr=%q, want nothing", out.String(), errOut.String())
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}})
	})

	t.Run("timeout is unavailable", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Delay: 6000}})
		readyProbe(t, env)
		svc := New(env)
		rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{Timeout: fakeTimeout(150 * time.Millisecond)})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err=%v want FailureUnavailable", rep.Err)
		}
		if len(rep.Value) != 0 {
			t.Fatalf("value %v want no partial rows", rep.Value)
		}
	})

	for _, malformed := range []struct {
		stdout string
		kind   transport.FailureKind
	}{
		{`not json`, transport.FailureProtocol},
		{`{"result":{"agents":{"name":"a"}}}`, transport.FailureProtocol},
		{`{"other":[]}`, transport.FailureProtocol},
	} {
		t.Run("malformed body is "+string(malformed.kind), func(t *testing.T) {
			env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: malformed.stdout}})
			readyProbe(t, env)
			svc := New(env)
			rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != malformed.kind {
				t.Fatalf("err=%v want %v", rep.Err, malformed.kind)
			}
			if len(rep.Value) != 0 {
				t.Fatalf("value %v want no partial rows", rep.Value)
			}
		})
	}
}

// TestBackendListScopedKeepsTheAdapterPath: All=false keeps the adapter's
// LiveAgents argv and projection (now with the additive metadata), so the
// scoped listing is unchanged in behavior.
func TestBackendListScopedKeepsTheAdapterPath(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: listAllAgentsJSON}})
	readyProbe(t, env)
	svc := New(env)
	rep := svc.Runtime.List(context.Background(), transport.Scope{WorkspaceID: "w9"}, transport.Call{})
	if rep.Err != nil {
		t.Fatalf("err: %v", rep.Err)
	}
	if !rep.Valid || len(rep.Value) != 4 {
		t.Fatalf("rep={%v valid:%v} want the four entries", rep.Value, rep.Valid)
	}
	wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}})
	if got := rep.Value[0]; got.Cwd != "/proj/wt" || got.WorkspaceID != "w9" {
		t.Fatalf("a = %+v want the additive metadata projected", got)
	}
	wantHints(t, rep.Value[0], []string{"claude"})
}
