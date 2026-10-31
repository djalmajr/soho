package herdr

import (
	"context"
	"encoding/json"
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

// TestBackendAgentListRaw: the legacy raw list issues the same `agent list`
// call as LiveAgents and returns the raw objects one JSON value per agent,
// so a field the terminal projection does not carry survives the round
// trip.
func TestBackendAgentListRaw(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"a","pane_id":"p1","agent_status":"working","state_change_seq":3,"cwd":"/work"},{"name":"b","pane_id":"p2"}]}}`}})
	readyProbe(t, env)
	svc := New(env)
	got := svc.Legacy.AgentListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
	raw := LiveAgents(env, fakeTimeout(time.Second))
	if !got.Valid || got.Err != nil || len(got.Value) != 2 || len(raw) != 2 {
		t.Fatalf("got=%#v raw=%d", got, len(raw))
	}
	for i, want := range raw {
		if string(got.Value[i]) != jsonjs.Stringify(want) {
			t.Fatalf("agent %d raw=%s want=%s", i, got.Value[i], jsonjs.Stringify(want))
		}
	}
	v, err := jsonjs.Parse(got.Value[0])
	if err != nil {
		t.Fatalf("the raw agent does not parse: %v", err)
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		t.Fatalf("the raw agent is not an object: %#v", v)
	}
	cwd, hasCwd := obj.Get("cwd")
	if !hasCwd || cwd != "/work" {
		t.Fatalf("the raw agent lost the projection-free fields: %s", got.Value[0])
	}
	wantArgv(t, backendCalls(t, env), [][]string{{"agent", "list"}, {"agent", "list"}})
}

// TestBackendAgentGetRaw: the legacy raw agent get issues one agent get and
// returns the raw stdout (or the raw process result on failure) so the
// caller keeps its own fail-open and body-parsing handling, and never
// retries.
func TestBackendAgentGetRaw(t *testing.T) {
	const body = `{"result":{"agent":{"agent_status":"working","interactive_ready":true}}}`
	t.Run("exit 0 returns the raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetRaw(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one call expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "p9"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
			{Argv: []string{"agent", "get", "p9"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetRaw(context.Background(), transport.Target{ID: "p9"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.AgentGetRaw(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendTabGetRaw: the parsed label and root pane are kept whatever the
// exit status — a failed tab get can still print a tab with a root pane —
// and the raw process result comes back so the caller keeps its own
// exit-status judgment; the Value holds the fields even when Err is set.
func TestBackendTabGetRaw(t *testing.T) {
	const body = `{"result":{"tab":{"label":"workers"},"root_pane":{"pane_id":"rootp"}}}`
	t.Run("exit 0 returns the parsed fields, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"tab", "get", "th"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.TabGetRaw(context.Background(), "th", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value.Label != "workers" || got.Value.Root != "rootp" || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one call expected, got %#v", calls)
		}
	})
	t.Run("a nonzero exit keeps the parsed fields on the Value", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"tab", "get", "th"}, Call: 1, Code: 1, Stdout: body},
			{Argv: []string{"tab", "get", "th"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.TabGetRaw(context.Background(), "th", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value.Label != "workers" || got.Value.Root != "rootp" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("a nonzero exit without output keeps zero fields", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"tab", "get", "th"}, Code: 1, Stderr: "boom"}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.TabGetRaw(context.Background(), "th", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value.Label != "" || got.Value.Root != "" || got.Err == nil || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendTranscriptPaths: the legacy transcript path resolutions wrap the
// adapter's one agent get plus local file derivation, and report "" when any
// data is missing.
func TestBackendTranscriptPaths(t *testing.T) {
	const session = "11111111-2222-3333-4444-555555555555"
	t.Run("claude: the agent get plus the local transcript file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "projects", ClaudeProjectDir("/work/proj"), session+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "claude-a"}, Stdout: `{"result":{"agent":{"agent_session":{"kind":"id","value":"` + session + `"},"cwd":"/work/proj"}}}`}})
		env["CLAUDE_CONFIG_DIR"] = dir
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.ClaudeTranscriptPath(context.Background(), transport.Target{ID: "claude-a"})
		want := ClaudeTranscriptPath("claude-a", env)
		if !got.Valid || got.Value != want || got.Value != path {
			t.Fatalf("got=%#v want=%s", got, path)
		}
	})
	t.Run("claude: a missing file resolves nothing", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "claude-a"}, Stdout: `{"result":{"agent":{"agent_session":{"kind":"id","value":"` + session + `"},"cwd":"/work/proj"}}}`}})
		env["CLAUDE_CONFIG_DIR"] = t.TempDir()
		svc := New(env)
		got := svc.Legacy.ClaudeTranscriptPath(context.Background(), transport.Target{ID: "claude-a"})
		if got.Value != "" {
			t.Fatalf("got=%#v", got)
		}
	})
	t.Run("pi: the agent get plus the local session file", func(t *testing.T) {
		dir := t.TempDir()
		sess := filepath.Join(dir, "session.jsonl")
		if err := os.WriteFile(sess, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The path goes in as a JSON string: a Windows path holds backslashes.
		quoted, err := json.Marshal(sess)
		if err != nil {
			t.Fatal(err)
		}
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "pi-a"}, Stdout: `{"result":{"agent":{"agent_session":{"kind":"path","value":` + string(quoted) + `}}}}`}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PiSessionPath(context.Background(), transport.Target{ID: "pi-a"})
		want := PiSessionPath("pi-a", env)
		if !got.Valid || got.Value != want || got.Value != sess {
			t.Fatalf("got=%#v want=%s", got, sess)
		}
	})
	t.Run("pi: a non-path session kind resolves nothing", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "pi-a"}, Stdout: `{"result":{"agent":{"agent_session":{"kind":"id","value":"` + session + `"}}}}`}})
		svc := New(env)
		got := svc.Legacy.PiSessionPath(context.Background(), transport.Target{ID: "pi-a"})
		if got.Value != "" {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendPaneCurrentRaw: the legacy raw pane current issues the exact
// `pane current --current` call once with the call's timeout, returns the
// raw stdout, and never parses the body or retries.
func TestBackendPaneCurrentRaw(t *testing.T) {
	const body = `{"result":{"pane":{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1","extra":"kept"}}}`
	t.Run("exit 0 returns the raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneCurrentRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "current", "--current"}})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "current", "--current"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"server_down","message":"down"}}`},
			{Argv: []string{"pane", "current", "--current"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneCurrentRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneCurrentRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.PaneCurrentRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendPaneGetRaw: the legacy raw pane get issues one `pane get <id>`
// with the call's timeout, returns the raw stdout (or the raw process result
// on failure) so the caller keeps its own exit and body handling, and never
// retries.
func TestBackendPaneGetRaw(t *testing.T) {
	const body = `{"result":{"pane":{"pane_id":"w1:p1","title":"t","revision":3,"extra":"kept"}}}`
	t.Run("exit 0 returns the raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "get", "w1:p1"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneGetRaw(context.Background(), transport.Target{ID: "w1:p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "get", "w1:p1"}})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "get", "w1:p9"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"pane_not_found","message":"gone"}}`},
			{Argv: []string{"pane", "get", "w1:p9"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneGetRaw(context.Background(), transport.Target{ID: "w1:p9"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "get", "w1:p1"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneGetRaw(context.Background(), transport.Target{ID: "w1:p1"}, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.PaneGetRaw(context.Background(), transport.Target{ID: "w1:p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendJobStepTimeoutRoundsUp: the job step's deadline is the call's
// timeout rounded up to whole milliseconds (never truncated), and an
// explicit zero stays 0, which the platform reads as "no deadline" — the
// exact conversion the job's runStep applies.
func TestBackendJobStepTimeoutRoundsUp(t *testing.T) {
	if got := timeoutMsUp(0); got != 0 {
		t.Fatalf("zero stays 0 (no deadline), got %d", got)
	}
	if got := timeoutMsUp(-time.Millisecond); got != 0 {
		t.Fatalf("negative stays 0 (no deadline), got %d", got)
	}
	if got := timeoutMsUp(30 * time.Second); got != 30000 {
		t.Fatalf("whole milliseconds stay exact, got %d", got)
	}
	if got := timeoutMsUp(30*time.Second + time.Microsecond); got != 30001 {
		t.Fatalf("a fraction of a millisecond rounds up, got %d", got)
	}
	if got := timeoutMsUp(1500*time.Millisecond + 999*time.Microsecond); got != 1501 {
		t.Fatalf("a fraction of a millisecond rounds up, got %d", got)
	}
}

// TestBackendWorkspaceCreateRaw: the legacy raw workspace create issues the
// exact `workspace create` argv once — the --env pairs sorted by key, then
// --no-focus — with the call's round-up timeout, returns the raw stdout,
// and never parses the body or retries.
func TestBackendWorkspaceCreateRaw(t *testing.T) {
	const body = `{"result":{"workspace":{"workspace_id":"w1","root_pane":{"pane_id":"w1:p1"}}}}`
	// Five pairs: a non-sorted map order misses the rule with odds near
	// (1/120)^3 across the subtests below.
	createArgv := []string{"workspace", "create", "--cwd", "/repo", "--label", "job-x", "--env", "A=1", "--env", "B=2", "--env", "C=3", "--env", "D=4", "--env", "E=5", "--no-focus"}
	createEnv := map[string]string{"E": "5", "C": "3", "A": "1", "B": "2", "D": "4"}
	t.Run("exit 0 returns the raw stdout, env pairs sorted by key, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: createArgv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCreateRaw(context.Background(), "/repo", "job-x", createEnv, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{createArgv})
	})
	t.Run("no env pairs leaves the argv without --env and keeps --no-focus", func(t *testing.T) {
		argv := []string{"workspace", "create", "--cwd", "/repo", "--label", "job-x", "--no-focus"}
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCreateRaw(context.Background(), "/repo", "job-x", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: createArgv, Call: 1, Code: 1, Stderr: `{"error":{"code":"server_down","message":"down"}}`},
			{Argv: createArgv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCreateRaw(context.Background(), "/repo", "job-x", createEnv, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: createArgv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCreateRaw(context.Background(), "/repo", "job-x", createEnv, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.WorkspaceCreateRaw(context.Background(), "/repo", "job-x", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound || got.Err == nil {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendWorkspaceCloseRaw: the legacy raw workspace close issues one
// `workspace close <id>` with the call's timeout, returns the raw stdout
// (or the raw process result on failure) so the caller keeps its own exit
// handling, and never retries.
func TestBackendWorkspaceCloseRaw(t *testing.T) {
	const body = `{"result":{"closed":"w1"}}`
	t.Run("exit 0 returns the raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"workspace", "close", "w1"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCloseRaw(context.Background(), "w1", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"workspace", "close", "w1"}})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"workspace", "close", "w1"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"workspace_not_found","message":"gone"}}`},
			{Argv: []string{"workspace", "close", "w1"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCloseRaw(context.Background(), "w1", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"workspace", "close", "w1"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceCloseRaw(context.Background(), "w1", transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.WorkspaceCloseRaw(context.Background(), "w1", transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound || got.Err == nil {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendWorkspaceListRaw: the legacy raw workspace list issues one
// `workspace list` with the call's timeout, returns the raw stdout (or the
// raw process result on failure) so the caller keeps its own exit and body
// handling, and never retries.
func TestBackendWorkspaceListRaw(t *testing.T) {
	const body = `{"result":{"workspaces":[{"workspace_id":"w1","label":"job-x","pane_count":1}]}}`
	t.Run("exit 0 returns the raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"workspace", "list"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"workspace", "list"}})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"workspace", "list"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"server_down","message":"down"}}`},
			{Argv: []string{"workspace", "list"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"workspace", "list"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.WorkspaceListRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.WorkspaceListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound || got.Err == nil {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendPaneListRaw: the legacy raw pane list issues one
// `pane list --workspace <id>` with the call's timeout, returns the raw
// stdout (or the raw process result on failure) so the caller keeps its own
// exit and body handling, and never retries.
func TestBackendPaneListRaw(t *testing.T) {
	const body = `{"result":{"panes":[{"pane_id":"w1:p1","revision":3}]}}`
	t.Run("exit 0 returns the raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "list", "--workspace", "w1"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneListRaw(context.Background(), "w1", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"pane", "list", "--workspace", "w1"}})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "list", "--workspace", "w1"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"workspace_not_found","message":"gone"}}`},
			{Argv: []string{"pane", "list", "--workspace", "w1"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneListRaw(context.Background(), "w1", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "list", "--workspace", "w1"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneListRaw(context.Background(), "w1", transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.PaneListRaw(context.Background(), "w1", transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound || got.Err == nil {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendPaneRunRaw: the legacy raw pane run issues one
// `pane run <pane> <argv...>` with the call's timeout, returns the raw
// stdout (or the raw process result on failure) so the caller keeps its own
// exit handling, and never retries.
func TestBackendPaneRunRaw(t *testing.T) {
	const body = `{"result":{"pane":"w1:p1"}}`
	argv := []string{"pane", "run", "w1:p1", "soho", "wait", "--id", "job1"}
	t.Run("exit 0 returns the raw stdout, argv after the pane, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneRunRaw(context.Background(), "w1:p1", []string{"soho", "wait", "--id", "job1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("an empty argv runs the bare pane run", func(t *testing.T) {
		bare := []string{"pane", "run", "w1:p1"}
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: bare, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneRunRaw(context.Background(), "w1:p1", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{bare})
	})
	t.Run("a failed call keeps the raw process result and never retries", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: `{"error":{"code":"pane_not_found","message":"gone"}}`},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneRunRaw(context.Background(), "w1:p1", []string{"soho", "wait", "--id", "job1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PaneRunRaw(context.Background(), "w1:p1", []string{"soho", "wait", "--id", "job1"}, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.PaneRunRaw(context.Background(), "w1:p1", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound || got.Err == nil {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendAgentGetMachineRaw: the legacy raw machine-prefixed agent get
// issues one `agent get <id>` (with --machine when the target names a
// non-local machine) with the call's timeout, returns the raw stdout and the
// raw process result, and never retries or parses the body.
func TestBackendAgentGetMachineRaw(t *testing.T) {
	const body = `{"result":{"agent":{"agent_status":"working"}}}`
	t.Run("local argv: one call, raw stdout", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetMachineRaw(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "get", "p1"}})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"--machine", "box1", "agent", "get", "w9:p1"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetMachineRaw(context.Background(), transport.Target{Machine: "box1", ID: "w9:p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"--machine", "box1", "agent", "get", "w9:p1"}})
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "p9"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
			{Argv: []string{"agent", "get", "p9"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetMachineRaw(context.Background(), transport.Target{ID: "p9"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 || got.Process.Stderr == "" {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "p1"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentGetMachineRaw(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.AgentGetMachineRaw(context.Background(), transport.Target{ID: "p1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendAgentWaitRaw: the legacy raw agent wait issues one
// `agent wait <id> --until idle --until done --timeout <timeoutMS>` (with
// --machine when the target names a non-local machine) under the call's
// timeout as the process timeout, returns the raw result, and never retries.
func TestBackendAgentWaitRaw(t *testing.T) {
	const body = `{"result":{}}`
	const logical = 1234
	waitArgv := []string{"agent", "wait", "w0:p1", "--until", "idle", "--until", "done", "--timeout", "1234"}
	t.Run("local argv with the logical bound, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: waitArgv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, logical, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{waitArgv})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: append([]string{"--machine", "box1"}, waitArgv...), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentWaitRaw(context.Background(), transport.Target{Machine: "box1", ID: "w0:p1"}, logical, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{append([]string{"--machine", "box1"}, waitArgv...)})
	})
	t.Run("the process timeout is the call's, not the logical bound", func(t *testing.T) {
		// The call budget (2s) exceeds the fake's 1.2s sleep while the
		// logical bound (100ms) does not: only a process timeout taken from
		// the call lets the run finish.
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "wait", "w0:p1", "--until", "idle", "--until", "done", "--timeout", "100"}, Delay: 1200, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, 100, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if got.Process.TimedOut || !got.Valid {
			t.Fatalf("got=%#v; the 1.2s run must fit the call's 2s process timeout", got)
		}
	})
	t.Run("a timed-out run keeps the raw TimedOut result, one call", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: waitArgv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, logical, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: waitArgv, Call: 1, Code: 3, Stderr: `{"error":{"code":"herdr_down","message":"socket"}}`},
			{Argv: waitArgv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, logical, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 3 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendAgentPromptWaitRaw: the legacy raw waiting agent prompt issues
// one `agent prompt <id> <body> --wait --until working --until blocked
// --until idle --until done --timeout <timeoutMS>` with merged output, under
// the call's timeout as the process timeout, and never retries or parses.
func TestBackendAgentPromptWaitRaw(t *testing.T) {
	const body = `{"result":{"sent":true}}`
	const logical = 15000
	promptArgv := []string{"agent", "prompt", "w0:p1", "hello world", "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}
	t.Run("local argv with the wait flags, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: promptArgv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, "hello world", logical, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{promptArgv})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: append([]string{"--machine", "box1"}, promptArgv...), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptWaitRaw(context.Background(), transport.Target{Machine: "box1", ID: "w0:p1"}, "hello world", logical, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{append([]string{"--machine", "box1"}, promptArgv...)})
	})
	t.Run("the output is merged into the raw stdout", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: promptArgv, Stdout: "out-marker", Stderr: "err-marker"}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, "hello world", logical, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if !strings.Contains(got.Process.Stdout, "out-marker") || !strings.Contains(got.Process.Stdout, "err-marker") {
			t.Fatalf("merged stdout=%q; both streams must reach the raw stdout", got.Process.Stdout)
		}
		if got.Process.Stderr != "" {
			t.Fatalf("merged stderr=%q; the merged run keeps one stream", got.Process.Stderr)
		}
	})
	t.Run("the process timeout is the call's, not the logical bound", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "prompt", "w0:p1", "slow", "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "100"}, Delay: 1200, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, "slow", 100, transport.Call{Timeout: fakeTimeout(2 * time.Second)})
		if got.Process.TimedOut || !got.Valid {
			t.Fatalf("got=%#v; the 1.2s run must fit the call's 2s process timeout", got)
		}
	})
	t.Run("the call's process timeout cuts an over-budget run, one call", func(t *testing.T) {
		timeout := fakeTimeout(25 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "prompt", "w0:p1", "slow", "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "100"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, "slow", 100, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut {
			t.Fatalf("got=%#v; the call's process timeout must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: promptArgv, Call: 1, Code: 1, Stderr: `{"error":{"code":"agent_blocked","message":"busy"}}`},
			{Argv: promptArgv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptWaitRaw(context.Background(), transport.Target{ID: "w0:p1"}, "hello world", logical, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendAgentPromptRaw: the legacy bare agent prompt issues one
// `agent prompt <id> <body>` with no wait flags and no merged output, with
// the machine prefix when the target names a non-local machine, and never
// retries or parses the body.
func TestBackendAgentPromptRaw(t *testing.T) {
	const body = `{"result":{"sent":true}}`
	t.Run("local argv without wait flags, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "prompt", "w0:p1", "deliver"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptRaw(context.Background(), transport.Target{ID: "w0:p1"}, "deliver", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "prompt", "w0:p1", "deliver"}})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"--machine", "box1", "agent", "prompt", "w9:p1", "deliver"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptRaw(context.Background(), transport.Target{Machine: "box1", ID: "w9:p1"}, "deliver", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"--machine", "box1", "agent", "prompt", "w9:p1", "deliver"}})
	})
	t.Run("the streams stay separate", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "prompt", "w0:p1", "deliver"}, Stdout: "out-marker", Stderr: "err-marker"}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptRaw(context.Background(), transport.Target{ID: "w0:p1"}, "deliver", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Process.Stdout != "out-marker" || got.Process.Stderr != "err-marker" {
			t.Fatalf("got=%#v; the bare prompt keeps the streams separate", got.Process)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "prompt", "w0:p1", "deliver"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
			{Argv: []string{"agent", "prompt", "w0:p1", "deliver"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentPromptRaw(context.Background(), transport.Target{ID: "w0:p1"}, "deliver", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendAgentReadSourceRaw: the legacy raw agent read issues one
// `agent read <id> --source <source>` with an optional `--lines <n>` (omitted
// for a nil count) and the machine prefix when the target names a non-local
// machine, and never retries.
func TestBackendAgentReadSourceRaw(t *testing.T) {
	const body = "screen-lines"
	t.Run("no line count omits the flag, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "read", "w0:p1", "--source", "visible"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentReadSourceRaw(context.Background(), transport.Target{ID: "w0:p1"}, "visible", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "read", "w0:p1", "--source", "visible"}})
	})
	t.Run("a line count is rendered as --lines <n>, one call", func(t *testing.T) {
		lines := 67
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "read", "w0:p1", "--source", "recent-unwrapped", "--lines", "67"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentReadSourceRaw(context.Background(), transport.Target{ID: "w0:p1"}, "recent-unwrapped", &lines, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "read", "w0:p1", "--source", "recent-unwrapped", "--lines", "67"}})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"--machine", "box1", "agent", "read", "w9:p1", "--source", "visible"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentReadSourceRaw(context.Background(), transport.Target{Machine: "box1", ID: "w9:p1"}, "visible", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"--machine", "box1", "agent", "read", "w9:p1", "--source", "visible"}})
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "read", "w0:p1", "--source", "recent"}, Call: 1, Code: 1, Stderr: `{"error":{"code":"agent_not_idle","message":"working"}}`},
			{Argv: []string{"agent", "read", "w0:p1", "--source", "recent"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentReadSourceRaw(context.Background(), transport.Target{ID: "w0:p1"}, "recent", nil, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendAgentSendKeysRaw: the legacy raw agent send-keys issues one
// `agent send-keys <id> <key>` with the machine prefix when the target names
// a non-local machine, reports success on the reply and the raw result on it,
// and never retries.
func TestBackendAgentSendKeysRaw(t *testing.T) {
	t.Run("local argv, exit 0 is success, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "send-keys", "w0:p1", "enter"}}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentSendKeysRaw(context.Background(), transport.Target{ID: "w0:p1"}, "enter", transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Value || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"agent", "send-keys", "w0:p1", "enter"}})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"--machine", "box1", "agent", "send-keys", "w9:p1", "escape"}}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentSendKeysRaw(context.Background(), transport.Target{Machine: "box1", ID: "w9:p1"}, "escape", transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Value || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"--machine", "box1", "agent", "send-keys", "w9:p1", "escape"}})
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "send-keys", "w0:p1", "enter"}, Call: 1, Code: 1, Stderr: "gone"},
			{Argv: []string{"agent", "send-keys", "w0:p1", "enter"}, Call: 2},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.AgentSendKeysRaw(context.Background(), transport.Target{ID: "w0:p1"}, "enter", transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
	t.Run("a missing binary reports the lookup failure without reaching the CLI", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}
		svc := New(env)
		got := svc.Legacy.AgentSendKeysRaw(context.Background(), transport.Target{ID: "w0:p1"}, "enter", transport.Call{Timeout: fakeTimeout(time.Second)})
		if !got.Process.NotFound || got.Value {
			t.Fatalf("got=%#v", got)
		}
	})
}

// TestBackendSnapshotRaw: the legacy raw session snapshot issues one
// `api snapshot` (with --machine when the target names a non-local machine)
// under the call's timeout, returns the raw result, and never retries.
func TestBackendSnapshotRaw(t *testing.T) {
	const body = `{"result":{"snapshot":{"panes":[]}}}`
	t.Run("local argv, one call, raw stdout", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SnapshotRaw(context.Background(), transport.Target{}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"api", "snapshot"}})
	})
	t.Run("a named machine keeps the --machine prefix, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"--machine", "box1", "api", "snapshot"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SnapshotRaw(context.Background(), transport.Target{Machine: "box1"}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"--machine", "box1", "api", "snapshot"}})
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SnapshotRaw(context.Background(), transport.Target{}, transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Call: 1, Code: 1, Stderr: "socket gone"},
			{Argv: []string{"api", "snapshot"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SnapshotRaw(context.Background(), transport.Target{}, transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendMachineListRaw: the legacy raw machine list issues one
// `machine list --json` under the call's timeout, returns the raw result, and
// never retries.
func TestBackendMachineListRaw(t *testing.T) {
	const body = `[{"label":"local"},{"label":"box1"}]`
	t.Run("local argv, one call, raw stdout", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"machine", "list", "--json"}, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.MachineListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{{"machine", "list", "--json"}})
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"machine", "list", "--json"}, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.MachineListRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"machine", "list", "--json"}, Call: 1, Code: 1, Stderr: "herdr down"},
			{Argv: []string{"machine", "list", "--json"}, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.MachineListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// captureRun records every CLI call the backend routes through its runner
// without starting a process: the binary, argv, and run options of each
// call, and the fixed result every call returns. A test builds a backend
// around it and asserts the exact run the contract promises — most
// importantly the TimeoutMs the deadline conversion produced — with no
// sleep: a method that maps a zero call timeout to the bounded 30000 ms
// default fails on the captured TimeoutMs immediately.
type captureRun struct {
	binaries []string
	argv     [][]string
	opts     []platform.RunOptions
	result   platform.RunResult
}

func (c *captureRun) run(exe string, args []string, opts platform.RunOptions) platform.RunResult {
	c.binaries = append(c.binaries, exe)
	c.argv = append(c.argv, append([]string(nil), args...))
	c.opts = append(c.opts, opts)
	return c.result
}

// assertRawContract asserts, on a capture-runner backend, that one raw method
// issued exactly one `herdr` call with the exact argv and the expected
// deadline, and passed the runner's raw result through unchanged.
func assertRawContract(t *testing.T, got transport.Reply[string], cr *captureRun, argv []string, body string, timeoutMs int) {
	t.Helper()
	if len(cr.argv) != 1 {
		t.Fatalf("one call expected, got %#v", cr.argv)
	}
	if cr.binaries[0] != "herdr" {
		t.Fatalf("binary %q expected \"herdr\"", cr.binaries[0])
	}
	if !reflect.DeepEqual(cr.argv[0], argv) {
		t.Fatalf("argv %#v want %#v", cr.argv[0], argv)
	}
	if cr.opts[0].TimeoutMs != timeoutMs {
		t.Fatalf("TimeoutMs=%d want %d", cr.opts[0].TimeoutMs, timeoutMs)
	}
	if got.Value != body || !got.Valid || got.Err != nil || got.Process.Stdout != body || got.Process.Status == nil || *got.Process.Status != 0 || got.Process.TimedOut {
		t.Fatalf("got=%#v; the runner's raw result must pass through unchanged", got)
	}
}

// TestBackendVersionRaw: the legacy raw version read issues one `--version`,
// keeps no deadline for a zero call timeout (the doctor's legacy contract),
// and returns the raw stdout with the raw process result.
func TestBackendVersionRaw(t *testing.T) {
	const body = "herdr 0.9.1\n"
	argv := []string{"--version"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.VersionRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a zero call timeout routes a zero TimeoutMs through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.VersionRaw(context.Background(), transport.Call{})
		assertRawContract(t, got, cr, argv, body, 0)
	})
	t.Run("a positive timeout still bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.VersionRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "herdr down"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.VersionRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendStatusServerRaw: the legacy raw status server read issues one
// `status server`, keeps no deadline for a zero call timeout (the doctor's
// legacy contract), and returns the raw stdout with the raw process result.
func TestBackendStatusServerRaw(t *testing.T) {
	const body = `{"result":{"server":"0.9.1"}}`
	argv := []string{"status", "server"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.StatusServerRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a zero call timeout routes a zero TimeoutMs through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.StatusServerRaw(context.Background(), transport.Call{})
		assertRawContract(t, got, cr, argv, body, 0)
	})
	t.Run("a positive timeout still bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.StatusServerRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 3, Stderr: "server is not running"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.StatusServerRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 3 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendSkillRaw: the legacy raw skill read issues one `--skill`, keeps
// no deadline for a zero call timeout (the doctor's legacy contract), and
// returns the raw stdout with the raw process result.
func TestBackendSkillRaw(t *testing.T) {
	const body = "# herdr skill\n"
	argv := []string{"--skill"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SkillRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a zero call timeout routes a zero TimeoutMs through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.SkillRaw(context.Background(), transport.Call{})
		assertRawContract(t, got, cr, argv, body, 0)
	})
	t.Run("a positive timeout still bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SkillRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "no skill"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.SkillRaw(context.Background(), transport.Call{Timeout: fakeTimeout(time.Second)})
		if got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// pluginCallTimeout is the bounded call the setup migration's plugin steps
// run with (herdrPluginTimeoutMs, 30 s).
var pluginCallTimeout = 30 * time.Second

// TestBackendPluginListRaw: the legacy raw plugin list issues one
// `plugin list --json` with the call's timeout and returns the raw stdout
// with the raw process result.
func TestBackendPluginListRaw(t *testing.T) {
	const body = `{"id":"cli:plugin","result":{"plugins":[]}}`
	argv := []string{"plugin", "list", "--json"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a 30 s call timeout routes a 30000 ms deadline through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.PluginListRaw(context.Background(), transport.Call{Timeout: pluginCallTimeout})
		assertRawContract(t, got, cr, argv, body, 30000)
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginListRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "not installed"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginListRaw(context.Background(), transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendPluginInstallRefRaw: the legacy raw plugin install issues one
// `plugin install djalmajr/soho/plugin --ref <ref> --yes` with the call's
// timeout and returns the raw stdout with the raw process result.
func TestBackendPluginInstallRefRaw(t *testing.T) {
	const body = `{"result":{"plugin_id":"djalmajr.soho"}}`
	argv := []string{"plugin", "install", "djalmajr/soho/plugin", "--ref", "v1.2.3", "--yes"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginInstallRefRaw(context.Background(), "v1.2.3", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a 30 s call timeout routes a 30000 ms deadline through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.PluginInstallRefRaw(context.Background(), "v1.2.3", transport.Call{Timeout: pluginCallTimeout})
		assertRawContract(t, got, cr, argv, body, 30000)
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginInstallRefRaw(context.Background(), "v1.2.3", transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "install failed"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginInstallRefRaw(context.Background(), "v1.2.3", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendPluginConfigDirRaw: the legacy raw plugin config dir issues one
// `plugin config-dir <pluginID>` with the call's timeout and returns the raw
// stdout with the raw process result.
func TestBackendPluginConfigDirRaw(t *testing.T) {
	const body = "/home/u/.local/share/herdr/plugins/djalmajr.soho\n"
	argv := []string{"plugin", "config-dir", "djalmajr.soho"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginConfigDirRaw(context.Background(), "djalmajr.soho", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a 30 s call timeout routes a 30000 ms deadline through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.PluginConfigDirRaw(context.Background(), "djalmajr.soho", transport.Call{Timeout: pluginCallTimeout})
		assertRawContract(t, got, cr, argv, body, 30000)
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginConfigDirRaw(context.Background(), "djalmajr.soho", transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "unknown plugin"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginConfigDirRaw(context.Background(), "djalmajr.soho", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendPluginRemoveRaw: the legacy raw plugin remove issues one
// `plugin <verb> <pluginID>` (the caller's uninstall/unlink choice) with the
// call's timeout and returns the raw stdout with the raw process result.
func TestBackendPluginRemoveRaw(t *testing.T) {
	const body = `{"result":{"removed":true}}`
	t.Run("uninstall argv and raw stdout, one call", func(t *testing.T) {
		argv := []string{"plugin", "uninstall", "djalmajr.herdr-soho"}
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginRemoveRaw(context.Background(), "uninstall", "djalmajr.herdr-soho", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a 30 s call timeout routes a 30000 ms deadline through the runner", func(t *testing.T) {
		argv := []string{"plugin", "uninstall", "djalmajr.herdr-soho"}
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.PluginRemoveRaw(context.Background(), "uninstall", "djalmajr.herdr-soho", transport.Call{Timeout: pluginCallTimeout})
		assertRawContract(t, got, cr, argv, body, 30000)
	})
	t.Run("unlink argv, one call", func(t *testing.T) {
		argv := []string{"plugin", "unlink", "djalmajr.herdr-soho"}
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginRemoveRaw(context.Background(), "unlink", "djalmajr.herdr-soho", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != body || !got.Valid || got.Err != nil {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		argv := []string{"plugin", "uninstall", "djalmajr.herdr-soho"}
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginRemoveRaw(context.Background(), "uninstall", "djalmajr.herdr-soho", transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		argv := []string{"plugin", "uninstall", "djalmajr.herdr-soho"}
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "still in use"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.PluginRemoveRaw(context.Background(), "uninstall", "djalmajr.herdr-soho", transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}

// TestBackendServerReloadConfigRaw: the legacy raw server config reload
// issues one `server reload-config` with the call's timeout and returns the
// raw stdout with the raw process result.
func TestBackendServerReloadConfigRaw(t *testing.T) {
	const body = `{"result":{"reloaded":true}}`
	argv := []string{"server", "reload-config"}
	t.Run("argv and raw stdout, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.ServerReloadConfigRaw(context.Background(), transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != body || !got.Valid || got.Err != nil || got.Process.Status == nil || *got.Process.Status != 0 {
			t.Fatalf("got=%#v", got)
		}
		wantArgv(t, backendCalls(t, env), [][]string{argv})
	})
	t.Run("a 30 s call timeout routes a 30000 ms deadline through the runner", func(t *testing.T) {
		zero := 0
		cr := &captureRun{result: platform.RunResult{Status: &zero, Stdout: body}}
		b := &backend{env: platform.Env{}, runner: cr.run}
		got := b.ServerReloadConfigRaw(context.Background(), transport.Call{Timeout: pluginCallTimeout})
		assertRawContract(t, got, cr, argv, body, 30000)
	})
	t.Run("the call's timeout bounds the run", func(t *testing.T) {
		timeout := fakeTimeout(200 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: argv, Delay: timeoutDelay(timeout), Stdout: body}})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.ServerReloadConfigRaw(context.Background(), transport.Call{Timeout: timeout})
		if !got.Process.TimedOut || got.Err == nil {
			t.Fatalf("got=%#v; the deadline must cut the run", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a failed call keeps the raw process result, one call", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: argv, Call: 1, Code: 1, Stderr: "server is not running"},
			{Argv: argv, Call: 2, Stdout: body},
		})
		readyProbe(t, env)
		svc := New(env)
		got := svc.Legacy.ServerReloadConfigRaw(context.Background(), transport.Call{Timeout: fakeTimeout(pluginCallTimeout)})
		if got.Value != "" || got.Err == nil || got.Err.Kind != transport.FailureCommand || got.Process.Status == nil || *got.Process.Status != 1 {
			t.Fatalf("got=%#v", got)
		}
		if calls := backendCalls(t, env); len(calls) != 1 {
			t.Fatalf("no retry expected, got %#v", calls)
		}
	})
}
