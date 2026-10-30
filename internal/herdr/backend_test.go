package herdr

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// backendCalls reads the fake's recorded calls, dropping the __ready__ probe
// so argv assertions see only the operations under test. A missing log means
// the fake was never invoked.
func backendCalls(t *testing.T, env platform.Env) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := calls[:0]
	for _, c := range calls {
		if len(c.Argv) == 1 && c.Argv[0] == "__ready__" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// wantArgv asserts the recorded call argvs, in order.
func wantArgv(t *testing.T, calls []fakecli.Call, want [][]string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("calls=%d want %d: %#v", len(calls), len(want), calls)
	}
	for i := range want {
		if !reflect.DeepEqual(calls[i].Argv, want[i]) {
			t.Fatalf("call %d argv %#v want %#v", i, calls[i].Argv, want[i])
		}
	}
}

// TestBackendListAndGet: the list/get family issues the same argv and returns
// the same result as the adapter functions it wraps (LiveAgents, AgentState).
func TestBackendListAndGet(t *testing.T) {
	t.Run("list: same argv and result as LiveAgents", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"a","pane_id":"p1","agent_status":"working","state_change_seq":3}]}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Runtime.List(context.Background(), transport.Scope{}, transport.Call{Timeout: fakeTimeout(time.Second)})
		raw := LiveAgents(env, fakeTimeout(time.Second))
		if !got.Valid || len(got.Value) != 1 || len(raw) != 1 {
			t.Fatalf("got=%#v raw=%d", got, len(raw))
		}
		obj := raw[0].(*jsonjs.Object)
		term := got.Value[0]
		if term.Name != str(get(obj, "name")) || term.PaneID != str(get(obj, "pane_id")) || term.State != str(get(obj, "agent_status")) {
			t.Fatalf("terminal=%#v raw=%#v", term, obj)
		}
		if term.Name != "a" || term.PaneID != "p1" || term.State != "working" || string(term.Sequence) != "3" {
			t.Fatalf("terminal=%#v", term)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}, {"agent", "list"}})
	})
	t.Run("get: same argv and result as AgentState", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":3}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Runtime.Get(context.Background(), transport.Target{ID: "x"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		raw := AgentState("x", env, fakeTimeout(time.Second), nil)
		if !got.Valid || got.Value.State != raw.State {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.State != "working" || string(got.Value.Sequence) != "3" {
			t.Fatalf("terminal=%#v", got.Value)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "get", "x"}, {"agent", "get", "x"}})
	})
	t.Run("get: a gone agent maps the adapter state, not a failure", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}})
		svc := New(env)
		got := svc.Runtime.Get(context.Background(), transport.Target{ID: "x"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		raw := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{})
		if !got.Valid || got.Value.State != raw.State || got.Value.State != "gone" {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
	})
	t.Run("get: an unavailable state keeps the terminal and its cause", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Code: 1, Stderr: `{"error":{"code":"agent_busy","message":"prompt in flight"}}`}})
		svc := New(env)
		got := svc.Runtime.Get(context.Background(), transport.Target{ID: "x"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		raw := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{})
		if raw.State != "unavailable" || raw.Cause == "" {
			t.Fatalf("adapter=%#v", raw)
		}
		if got.Err == nil || got.Err.Kind != transport.FailureUnavailable || got.Err.Message != raw.Cause {
			t.Fatalf("got=%#v want Err.Message=%q", got, raw.Cause)
		}
		if !got.Valid || got.Value.State != "unavailable" || got.Value.Target.ID != "x" {
			t.Fatalf("got=%#v", got)
		}
	})
	t.Run("list: a listed terminal's Target reads back through Get", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"a","pane_id":"p1","agent_status":"working","state_change_seq":3}]}}`},
			{Argv: []string{"agent", "get", "a"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":3}}}`},
		})
		readyProbe(t, env)
		svc := New(env)
		list := svc.Runtime.List(context.Background(), transport.Scope{}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !list.Valid || len(list.Value) != 1 || list.Value[0].Target.ID != "a" {
			t.Fatalf("list=%#v", list)
		}
		got := svc.Runtime.Get(context.Background(), list.Value[0].Target, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Valid || got.Value.State != "working" || string(got.Value.Sequence) != "3" || got.Value.Target.ID != "a" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}, {"agent", "get", "a"}})
	})
}

// TestBackendSendAndKeys: the send/keys family wraps AgentPrompt and
// AgentSendKeys/PaneSendKeys with the same argv.
func TestBackendSendAndKeys(t *testing.T) {
	t.Run("send: the agent prompt argv and merged output", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "prompt", "a", "hello"}, Stdout: "ok\n"}})
		svc := New(env)
		got := svc.Runtime.Send(context.Background(), transport.Target{ID: "a"}, transport.Send{Text: "hello"}, transport.Call{})
		if !got.Valid || got.Value != "ok" {
			t.Fatalf("got=%#v", got)
		}
		raw := AgentPrompt("a", "hello", env)
		if !raw.Ok || raw.Raw != got.Value {
			t.Fatalf("raw=%#v got=%#v", raw, got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "prompt", "a", "hello"}, {"agent", "prompt", "a", "hello"}})
	})
	t.Run("keys: the agent channel argv", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "send-keys", "a", "Enter"}}})
		svc := New(env)
		if got := svc.Runtime.Keys(context.Background(), transport.Target{ID: "a"}, "Enter", false, transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		if !AgentSendKeys("a", "Enter", env) {
			t.Fatal("the wrapped adapter reports failure")
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "send-keys", "a", "Enter"}, {"agent", "send-keys", "a", "Enter"}})
	})
	t.Run("keys: the pane channel argv", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "send-keys", "p1", "Enter"}}})
		svc := New(env)
		if got := svc.Runtime.Keys(context.Background(), transport.Target{ID: "p1"}, "Enter", true, transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		if !PaneSendKeys("p1", "Enter", env) {
			t.Fatal("the wrapped adapter reports failure")
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "send-keys", "p1", "Enter"}, {"pane", "send-keys", "p1", "Enter"}})
	})
	t.Run("keys: a failed send reports false", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "send-keys", "a", "Enter"}, Code: 1}})
		svc := New(env)
		if got := svc.Runtime.Keys(context.Background(), transport.Target{ID: "a"}, "Enter", false, transport.Call{}); got.Value {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendRead: the read family wraps AgentReadOK and PaneRead.
func TestBackendRead(t *testing.T) {
	lines := 20
	t.Run("the agent read argv and screen", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "read", "a", "--source", "visible", "--lines", "20"}, Stdout: "screen\n"}})
		svc := New(env)
		got := svc.Runtime.Read(context.Background(), transport.Target{ID: "a"}, transport.Read{Source: "visible", Lines: &lines}, transport.Call{})
		if !got.Valid || got.Value != "screen\n" {
			t.Fatalf("got=%#v", got)
		}
		if raw, ok := AgentReadOK(env, "a", "visible", &lines); !ok || raw != got.Value {
			t.Fatalf("raw=%q ok=%v got=%#v", raw, ok, got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "read", "a", "--source", "visible", "--lines", "20"}, {"agent", "read", "a", "--source", "visible", "--lines", "20"}})
	})
	t.Run("the pane read argv and screen", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "read", "p1", "--source", "visible"}, Stdout: "pane screen\n"}})
		svc := New(env)
		got := svc.Runtime.Read(context.Background(), transport.Target{ID: "p1"}, transport.Read{Source: "visible", Pane: true}, transport.Call{})
		if !got.Valid || got.Value != "pane screen\n" {
			t.Fatalf("got=%#v", got)
		}
		if raw, ok := PaneRead(env, "p1", "visible", nil); !ok || raw != got.Value {
			t.Fatalf("raw=%q ok=%v got=%#v", raw, ok, got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "read", "p1", "--source", "visible"}, {"pane", "read", "p1", "--source", "visible"}})
	})
	t.Run("a failed read is a failure, not an empty success", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "read"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"pane_not_found","message":"no pane"}}`}})
		svc := New(env)
		got := svc.Runtime.Read(context.Background(), transport.Target{ID: "p1"}, transport.Read{Source: "visible", Pane: true}, transport.Call{})
		if got.Valid || got.Err == nil || got.Value != "" {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendClose: the close family wraps PaneClose.
func TestBackendClose(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "close", "p1"}},
		{Argv: []string{"pane", "close", "p2"}, Code: 1},
	})
	svc := New(env)
	if got := svc.Runtime.Close(context.Background(), transport.Target{ID: "p1"}, transport.Call{}); !got.Value || !got.Valid {
		t.Fatalf("close p1: %#v", got)
	}
	if got := svc.Runtime.Close(context.Background(), transport.Target{ID: "p2"}, transport.Call{}); got.Value {
		t.Fatalf("close p2: %#v", got)
	}
	if !PaneClose("p1", env) || PaneClose("p2", env) {
		t.Fatal("the wrapped adapter disagrees")
	}
	wantArgv(t, backendCalls(t, env), [][]string{
		{"pane", "close", "p1"}, {"pane", "close", "p2"},
		{"pane", "close", "p1"}, {"pane", "close", "p2"},
	})
}

// TestBackendCallerCurrentCheck: the caller/current/check family. Caller reads
// the inherited markers without a server; Current wraps PaneCurrent;
// CheckCaller wraps CheckCallerPane.
func TestBackendCallerCurrentCheck(t *testing.T) {
	t.Run("caller: the inherited markers, pure", func(t *testing.T) {
		svc := New(platform.Env{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p9", "HERDR_WORKSPACE_ID": "w1", "HERDR_TAB_ID": "w1:t1"})
		if got := svc.Runtime.Caller(); got != (transport.Caller{Inside: true, PaneID: "w1:p9", WorkspaceID: "w1", TabID: "w1:t1"}) {
			t.Fatalf("Caller()=%#v", got)
		}
		svc = New(platform.Env{"HERDR_ENV": "0"})
		if got := svc.Runtime.Caller(); got != (transport.Caller{}) {
			t.Fatalf("outside Caller()=%#v", got)
		}
	})
	t.Run("current: the pane current argv and identity", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"pane_id":"w14:p88","tab_id":"w14:t1","workspace_id":"w14"}}}`}})
		svc := New(env)
		got := svc.Runtime.Current(context.Background(), transport.Call{})
		raw := PaneCurrent(env)
		if !got.Valid || got.Value.PaneID != raw.PaneID || got.Value.TabID != raw.TabID || got.Value.WorkspaceID != raw.WorkspaceID {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if !got.Value.Inside || got.Value.PaneID != "w14:p88" || got.Value.TabID != "w14:t1" || got.Value.WorkspaceID != "w14" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "current", "--current"}, {"pane", "current", "--current"}})
	})
	t.Run("current: an unresolved pane is a failure", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Code: 1, Stderr: "cannot resolve current pane: server is not running"}})
		svc := New(env)
		if got := svc.Runtime.Current(context.Background(), transport.Call{}); got.Valid || got.Err == nil {
			t.Fatalf("got=%#v", got)
		}
	})
	t.Run("check: the process-info argv and the verified state", func(t *testing.T) {
		ResetCallerCheck()
		const pane = "w1:p9"
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(pane, fakePID(4000))},
			map[int]int{selfPID(): fakePID(4000), fakePID(4000): 1})
		env["HERDR_PANE_ID"] = pane
		svc := New(env)
		got := svc.Runtime.CheckCaller(context.Background(), transport.Call{})
		raw := CheckCallerPane(env)
		if !got.Valid || got.Value.State != string(raw.State) || got.Value.ShellPID != raw.ShellPID {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.State != "verified" || got.Value.Pane != pane || got.Value.ShellPID != fakePID(4000) {
			t.Fatalf("got=%#v", got)
		}
		// The raw call is memoized per (pane, delegation), so the whole check
		// issues exactly one process-info call through either entry point.
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "process-info", "--pane", pane}})
	})
	t.Run("check: without HERDR_PANE_ID it is unknown with no herdr call", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t, nil, nil)
		svc := New(env)
		got := svc.Runtime.CheckCaller(context.Background(), transport.Call{})
		if !got.Valid || got.Value.State != "unknown" {
			t.Fatalf("got=%#v", got)
		}
		calls := backendCalls(t, env)
		if len(calls) != 0 {
			t.Fatalf("calls=%#v", calls)
		}
	})
	t.Run("check: a mismatch carries the session ids", func(t *testing.T) {
		ResetCallerCheck()
		const pane = "w1:p11"
		const own = "11111111-2222-3333-4444-555555555555"
		const paneSession = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		env := callerCheckFixture(t,
			[]fakecli.Rule{
				processInfoRule(pane, fakePID(4000)),
				{Argv: []string{"pane", "get", pane}, Stdout: `{"result":{"pane":{"agent_session":{"agent":"claude","kind":"id","value":"` + paneSession + `"}}}}`},
				{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			},
			map[int]int{selfPID(): fakePID(3000), fakePID(3000): 1})
		env["HERDR_PANE_ID"] = pane
		env["CLAUDE_CODE_SESSION_ID"] = own
		env["CLAUDE_CONFIG_DIR"] = t.TempDir() // no real ~/.claude transcript search
		svc := New(env)
		got := svc.Runtime.CheckCaller(context.Background(), transport.Call{})
		raw := CheckCallerPane(env)
		if !got.Valid || got.Value.State != string(raw.State) || got.Value.Session != raw.Session || got.Value.PaneSession != raw.PaneSession || got.Value.ShownIn != raw.ShownIn {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.State != "mismatch" || got.Value.Session != own || got.Value.PaneSession != paneSession || got.Value.ShownIn != "" || got.Value.Reason != "tree does not reach the pane's shell" {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendWindowPanesTabs: the window panes/tabs family wraps PaneList,
// PaneListAll, TabList, TabGet, TabCreate, TabRename, PaneLayout, PaneSplit
// and PaneMove with the same argv.
func TestBackendWindowPanesTabs(t *testing.T) {
	t.Run("list panes: the scoped and all argv", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"p1","tab_id":"t1","workspace_id":"ws","revision":7}]}}`},
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"p1"},{"pane_id":"p2","tab_id":"t2"}]}}`},
		})
		svc := New(env)
		got := svc.Window.ListPanes(context.Background(), transport.Scope{WorkspaceID: "ws"}, transport.Call{})
		if !got.Valid || len(got.Value) != 1 || got.Value[0].PaneID != "p1" || got.Value[0].TabID != "t1" || got.Value[0].WorkspaceID != "ws" || got.Value[0].Revision != 7 {
			t.Fatalf("scoped=%#v", got)
		}
		got = svc.Window.ListPanes(context.Background(), transport.Scope{All: true}, transport.Call{})
		if !got.Valid || len(got.Value) != 2 || got.Value[0].PaneID != "p1" || got.Value[1].PaneID != "p2" {
			t.Fatalf("all=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "list", "--workspace", "ws"}, {"pane", "list"}})
	})
	t.Run("list tabs: the tab list argv and labels", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"t1","label":"herd"}]}}`}})
		svc := New(env)
		got := svc.Window.ListTabs(context.Background(), "ws", transport.Call{})
		if !got.Valid || len(got.Value) != 1 || got.Value[0].TabID != "t1" || got.Value[0].Label != "herd" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"tab", "list", "--workspace", "ws"}})
	})
	t.Run("tab: the tab get argv and result", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"label"},"root_pane":{"pane_id":"p1"}}}`}})
		svc := New(env)
		got := svc.Window.Tab(context.Background(), "t1", transport.Call{})
		raw := TabGet("t1", env)
		if !got.Valid || got.Value.Label != raw.Label || got.Value.RootPane != raw.Root {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.ID != "t1" || got.Value.Label != "label" || got.Value.RootPane != "p1" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"tab", "get", "t1"}, {"tab", "get", "t1"}})
	})
	t.Run("create tab: the tab create argv and result", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"tab", "create", "--workspace", "ws", "--cwd", "/tmp/a b", "--label", "new", "--no-focus"}, Stdout: `{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}`}})
		svc := New(env)
		got := svc.Window.CreateTab(context.Background(), transport.Open{WorkspaceID: "ws", Cwd: "/tmp/a b", Label: "new"}, transport.Call{})
		raw := TabCreate(env, "ws", "/tmp/a b", "new")
		if !got.Valid || got.Value.ID != raw.Tab || got.Value.RootPane != raw.Root {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.ID != "t2" || got.Value.RootPane != "p2" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{
			{"tab", "create", "--workspace", "ws", "--cwd", "/tmp/a b", "--label", "new", "--no-focus"},
			{"tab", "create", "--workspace", "ws", "--cwd", "/tmp/a b", "--label", "new", "--no-focus"},
		})
	})
	t.Run("rename tab: the tab rename argv", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"tab", "rename", "t1", "herd"}}})
		svc := New(env)
		if got := svc.Window.RenameTab(context.Background(), "t1", "herd", transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"tab", "rename", "t1", "herd"}})
	})
	t.Run("layout: the pane layout argv and raw result", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "layout", "--current"}, Stdout: `{"layout":1}`},
			{Argv: []string{"pane", "layout", "--pane", "p1"}, Stdout: `{"layout":2}`},
		})
		svc := New(env)
		got := svc.Window.Layout(context.Background(), "", transport.Call{})
		if !got.Valid || string(got.Value) != `{"layout":1}` || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("current=%#v", got)
		}
		got = svc.Window.Layout(context.Background(), "p1", transport.Call{})
		if !got.Valid || string(got.Value) != `{"layout":2}` {
			t.Fatalf("pane=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "layout", "--current"}, {"pane", "layout", "--pane", "p1"}})
	})
	t.Run("split: the pane split argv and the new pane", func(t *testing.T) {
		ratio := "0.70"
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "split", "p1", "--direction", "right", "--cwd", "/tmp", "--no-focus", "--ratio", "0.70"}, Stdout: `{"result":{"pane":{"pane_id":"p3"}}}`}})
		svc := New(env)
		got := svc.Window.Split(context.Background(), transport.Open{Anchor: "p1", Direction: "right", Cwd: "/tmp", Ratio: &ratio}, transport.Call{})
		raw := PaneSplit("p1", "right", "/tmp", env, &ratio)
		if !got.Valid || got.Value.PaneID != raw.Pane {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.PaneID != "p3" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{
			{"pane", "split", "p1", "--direction", "right", "--cwd", "/tmp", "--no-focus", "--ratio", "0.70"},
			{"pane", "split", "p1", "--direction", "right", "--cwd", "/tmp", "--no-focus", "--ratio", "0.70"},
		})
	})
	t.Run("move: the pane move argv and result", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "t1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p2"}}}}`},
		})
		svc := New(env)
		got := svc.Window.Move(context.Background(), transport.Move{Pane: "p1", NewTab: true, Label: "herd-park", NoFocus: true}, transport.Call{})
		raw := PaneMove([]string{"p1", "--new-tab", "--label", "herd-park", "--no-focus"}, env)
		if !got.Valid || got.Value.PaneID != raw.Pane || got.Value.TabID != raw.Tab {
			t.Fatalf("got=%#v raw=%#v", got, raw)
		}
		if got.Value.PaneID != "p1" || got.Value.TabID != "park-1" {
			t.Fatalf("got=%#v", got)
		}
		got = svc.Window.Move(context.Background(), transport.Move{Pane: "p2", Tab: "t1", Split: "down", TargetPane: "p1", Ratio: "0.5", NoFocus: true}, transport.Call{})
		if !got.Valid || got.Value.PaneID != "p2" || got.Value.TabID != "" {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{
			{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"},
			{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"},
			{"pane", "move", "p2", "--tab", "t1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"},
		})
	})
}

// TestBackendWindowTitleFocusNotify: the window title/focus/notify family
// wraps PaneTitle, AgentFocus, PaneFocusBack and NotificationShow.
func TestBackendWindowTitleFocusNotify(t *testing.T) {
	t.Run("title: the report-metadata argv, set and clear", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "report-metadata", "p9", "--source", "soho", "--title", "task title"}},
			{Argv: []string{"pane", "report-metadata", "p9", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "report-metadata", "p9", "--source", "soho", "--title", "fails"}, Code: 1},
		})
		svc := New(env)
		title := "task title"
		fails := "fails"
		if got := svc.Window.Title(context.Background(), "p9", &title, transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("set: %#v", got)
		}
		if got := svc.Window.Title(context.Background(), "p9", nil, transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("clear: %#v", got)
		}
		if got := svc.Window.Title(context.Background(), "p9", &fails, transport.Call{}); got.Value {
			t.Fatalf("fail: %#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{
			{"pane", "report-metadata", "p9", "--source", "soho", "--title", "task title"},
			{"pane", "report-metadata", "p9", "--source", "soho", "--clear-title"},
			{"pane", "report-metadata", "p9", "--source", "soho", "--title", "fails"},
		})
	})
	t.Run("focus: the agent and pane focus argv", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "focus", "a"}},
			{Argv: []string{"pane", "focus", "--direction", "left", "--pane", "p1"}},
		})
		svc := New(env)
		if got := svc.Window.Focus(context.Background(), transport.Focus{Target: transport.Target{ID: "a"}}, transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("agent focus: %#v", got)
		}
		if got := svc.Window.Focus(context.Background(), transport.Focus{Direction: "left", PaneID: "p1"}, transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("pane focus: %#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{
			{"agent", "focus", "a"},
			{"pane", "focus", "--direction", "left", "--pane", "p1"},
		})
	})
	t.Run("notify: the notification show argv with the fixed sound", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"notification", "show", "title", "--body", "body", "--sound", "done"}}})
		svc := New(env)
		if got := svc.Window.Notify(context.Background(), "title", "body", transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		NotificationShow("title", "body", env)
		wantArgv(t, backendCalls(t, env), [][]string{
			{"notification", "show", "title", "--body", "body", "--sound", "done"},
			{"notification", "show", "title", "--body", "body", "--sound", "done"},
		})
	})
	t.Run("notify: a failing notification is a failure", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"notification", "show", "t", "--body", "b", "--sound", "done"}, Code: 1, Stderr: "no notification support"}})
		svc := New(env)
		got := svc.Window.Notify(context.Background(), "t", "b", transport.Call{})
		if got.Valid || got.Err == nil || got.Value {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendProcessInfo: the legacy family exposes the raw process-info read
// with the same argv the caller-pane check uses.
func TestBackendProcessInfo(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "process-info", "--pane", "p1"}, Stdout: `{"result":{"process_info":{"shell_pid":4000}}}`},
		{Argv: []string{"pane", "process-info", "--pane", "p2"}, Code: 1, Stderr: `{"error":{"code":"pane_not_found","message":"gone"}}`},
	})
	svc := New(env)
	got := svc.Legacy.ProcessInfo(context.Background(), transport.Target{ID: "p1"}, transport.Call{})
	if !got.Valid || string(got.Value) != `{"result":{"process_info":{"shell_pid":4000}}}` || got.Process.Status == nil || *got.Process.Status != 0 {
		t.Fatalf("got=%#v", got)
	}
	got = svc.Legacy.ProcessInfo(context.Background(), transport.Target{ID: "p2"}, transport.Call{})
	if got.Valid || got.Err == nil || got.Process.Status == nil || *got.Process.Status != 1 {
		t.Fatalf("got=%#v", got)
	}
	wantArgv(t, backendCalls(t, env), [][]string{
		{"pane", "process-info", "--pane", "p1"},
		{"pane", "process-info", "--pane", "p2"},
	})
}

// TestBackendAgentGetOK: the legacy raw existence check issues one agent get
// and reads only the exit status — an exit-0 answer without an agent_status
// still reports true, and neither a killed run nor a missing binary is
// retried.
func TestBackendAgentGetOK(t *testing.T) {
	t.Run("exit 0 without an agent_status reports true, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"reviewer"}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetOK(context.Background(), transport.Target{ID: "p1"}, transport.Call{})
		if !got.Value || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one call expected, got %#v", calls)
		}
	})
	t.Run("a killed run reports false, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "p1"}, Call: 1, Code: 143},
			{Argv: []string{"agent", "get", "p1"}, Call: 2, Stdout: `{"result":{"agent":{"name":"reviewer","agent_status":"working"}}}`},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetOK(context.Background(), transport.Target{ID: "p1"}, transport.Call{})
		if got.Value || got.Err == nil || got.Process.Status == nil || *got.Process.Status != 143 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports false without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.AgentGetOK(context.Background(), transport.Target{ID: "p1"}, transport.Call{})
		if got.Value || !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendAgentGetKnown: the legacy raw knowledge check issues one agent
// get (with the machine prefix for a non-local target) and reports known
// only for an exit-0 body with a non-empty string agent_status; a failed
// call is not retried.
func TestBackendAgentGetKnown(t *testing.T) {
	t.Run("local exit 0 with an agent_status reports known, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"reviewer","agent_status":"working"}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Value || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		calls := backendCalls(t, env)
		wantArgv(t, calls, [][]string{{"agent", "get", "p1"}})
	})
	t.Run("a non-local machine takes the machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"--machine", "m1", "agent", "get", "p1"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{Machine: "m1", ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Value || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		calls := backendCalls(t, env)
		wantArgv(t, calls, [][]string{{"--machine", "m1", "agent", "get", "p1"}})
	})
	t.Run("exit 0 without an agent_status is a well-formed false", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"reviewer"}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one call expected, got %#v", calls)
		}
	})
	t.Run("a non-string agent_status is not known", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"agent_status":5}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one call expected, got %#v", calls)
		}
	})
	t.Run("a failed call reports false with the cause, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"--machine", "m1", "agent", "get", "p1"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"agent_not_found"}}`},
			{Argv: []string{"--machine", "m1", "agent", "get", "p1"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{Machine: "m1", ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value || got.Err == nil || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports false without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value || !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendAgentReadRaw: the legacy raw agent read issues the exact
// `agent read <id> --source <source> --lines <lines>` argv once and returns
// the raw process result (stdout on Value, the full result on Process) so
// the caller keeps its own exit-code handling.
func TestBackendAgentReadRaw(t *testing.T) {
	t.Run("exit 0 returns the stdout, exact argv, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "read", "p1", "--source", "recent-unwrapped", "--lines", "120"}, Stdout: "line1\nline2\n"}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentReadRaw(context.Background(), transport.Target{ID: "p1"}, "recent-unwrapped", "120", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "line1\nline2\n" || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		calls := backendCalls(t, env)
		wantArgv(t, calls, [][]string{{"agent", "read", "p1", "--source", "recent-unwrapped", "--lines", "120"}})
	})
	t.Run("a failed call keeps the partial stdout and the raw result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "read", "p1", "--source", "recent-unwrapped", "--lines", "40"}, Call: 1, Code: 3, Stdout: "partial\n", Stderr: "boom"},
			{Argv: []string{"agent", "read", "p1", "--source", "recent-unwrapped", "--lines", "40"}, Call: 2, Stdout: "recovered\n"},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentReadRaw(context.Background(), transport.Target{ID: "p1"}, "recent-unwrapped", "40", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "partial\n" || got.Err == nil || got.Process.Stderr != "boom" || got.Process.Status == nil || *got.Process.Status != 3 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports false without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.AgentReadRaw(context.Background(), transport.Target{ID: "p1"}, "recent-unwrapped", "120", transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendSpawnRuntime: the spawn-slice capabilities (the Runtime methods
// Start, CallerAgentName, SendText, GetGone and the LegacyTransport's Rename)
// issue the exact argv the pre-transport spawn path used, with the same
// output handling: Start's merged output on the value, Rename's raw process
// result on the reply.
func TestBackendSpawnRuntime(t *testing.T) {
	t.Run("start: the agent start argv, args after --, the merged output", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "start", "worker", "--kind", "codex", "--pane", "p1", "--timeout", "90000"}, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"agent", "start", "w2", "--kind", "pi", "--pane", "p2", "--timeout", "120", "--", "-m", "fast"}, Stdout: "out", Stderr: "err", Code: 1},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Terminal.Start(context.Background(), transport.Start{Name: "worker", Kind: "codex", PaneID: "p1", Timeout: "90000"}, transport.Call{})
		if !got.Valid || got.Value != `{"result":{"started":true}}` || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		got = svc.Terminal.Start(context.Background(), transport.Start{Name: "w2", Kind: "pi", PaneID: "p2", Timeout: "120", Args: []string{"-m", "fast"}}, transport.Call{Timeout: fakeTimeout(90 * time.Second)})
		if got.Value != "outerr" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{
			{"agent", "start", "worker", "--kind", "codex", "--pane", "p1", "--timeout", "90000"},
			{"agent", "start", "w2", "--kind", "pi", "--pane", "p2", "--timeout", "120", "--", "-m", "fast"},
		})
	})
	t.Run("caller agent name: the agent get on the caller pane, and a failure without one", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"orchestrator"}}}`},
			{Argv: []string{"agent", "get", "p9"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"no agent"}}`},
		})
		env["HERDR_PANE_ID"] = "p1"
		readyProbe(t, env)
		svc := New(env)
		got := svc.Terminal.CallerAgentName(context.Background(), transport.Call{})
		if !got.Valid || got.Value != "orchestrator" || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		env["HERDR_PANE_ID"] = "p9"
		svc = New(env)
		got = svc.Terminal.CallerAgentName(context.Background(), transport.Call{})
		if got.Valid || got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureNotFound {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "get", "p1"}, {"agent", "get", "p9"}})
	})
	t.Run("send text: the pane send-text argv, without submitting", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "send-text", "p1", "hello world"}},
			{Argv: []string{"pane", "send-text", "p9", "x"}, Code: 1},
		})
		readyProbe(t, env)
		svc := New(env)
		if got := svc.Terminal.SendText(context.Background(), transport.Target{ID: "p1"}, "hello world", transport.Call{}); !got.Value || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		if got := svc.Terminal.SendText(context.Background(), transport.Target{ID: "p9"}, "x", transport.Call{}); got.Value {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "send-text", "p1", "hello world"}, {"pane", "send-text", "p9", "x"}})
	})
	t.Run("get gone: only the pane-not-found code reports gone", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "get", "p1"}, Stdout: `{"result":{"pane":{"pane_id":"p1"}}}`},
			{Argv: []string{"pane", "get", "p9"}, Code: 1, Stderr: `{"error":{"code":"pane_not_found","message":"gone"}}`},
			{Argv: []string{"pane", "get", "p8"}, Code: 1, Stderr: `{"error":{"code":"boom","message":"other"}}`},
		})
		readyProbe(t, env)
		svc := New(env)
		if got := svc.Terminal.GetGone(context.Background(), transport.Target{ID: "p1"}, transport.Call{}); got.Value {
			t.Fatalf("present pane=%#v", got)
		}
		if got := svc.Terminal.GetGone(context.Background(), transport.Target{ID: "p9"}, transport.Call{}); !got.Value {
			t.Fatalf("gone pane=%#v", got)
		}
		if got := svc.Terminal.GetGone(context.Background(), transport.Target{ID: "p8"}, transport.Call{}); got.Value {
			t.Fatalf("other failure=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "get", "p1"}, {"pane", "get", "p9"}, {"pane", "get", "p8"}})
	})
	t.Run("rename: the agent rename argv and the raw process result", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "rename", "p1", "worker"}},
			{Argv: []string{"agent", "rename", "p9", "taken"}, Code: 1, Stderr: `{"error":{"code":"agent_name_taken","message":"name taken"}}`},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.Rename(context.Background(), transport.Target{ID: "p1"}, "worker", transport.Call{})
		if !got.Value || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		got = svc.Legacy.Rename(context.Background(), transport.Target{ID: "p9"}, "taken", transport.Call{})
		if got.Value || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 || !strings.Contains(got.Process.Stderr, "agent_name_taken") {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "rename", "p1", "worker"}, {"agent", "rename", "p9", "taken"}})
	})
}

// TestBackendServicesWiring: New returns one backend behind all three
// capabilities, and the interfaces are satisfied at compile time (asserted
// by the var blocks in backend.go).
func TestBackendServicesWiring(t *testing.T) {
	svc := New(platform.Env{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p9"})
	if svc.Runtime == nil || svc.Window == nil || svc.Legacy == nil {
		t.Fatalf("services=%#v", svc)
	}
	var rt transport.Transport = svc.Runtime
	var win transport.Window = svc.Window
	var legacy transport.LegacyTransport = svc.Legacy
	if rt.Caller().PaneID != "w1:p9" || win == nil || legacy == nil {
		t.Fatalf("wiring: %#v", svc)
	}
}
