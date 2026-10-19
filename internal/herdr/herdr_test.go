package herdr

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/codexenv"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

func TestAgentStateClassification(t *testing.T) {
	cases := []struct {
		name string
		rule fakecli.Rule
		want AgentStateResult
	}{
		{"JS: agentState: agent_not_found is the only gone", fakecli.Rule{Argv: []string{"agent", "get", "x"}, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}, AgentStateResult{"gone", "", ""}},
		{`JS: agentState: another error code is unavailable with "<code>: <message>"`, fakecli.Rule{Argv: []string{"agent", "get", "x"}, Stderr: `{"error":{"code":"server_not_running","message":"down"}}`, Code: 1}, AgentStateResult{"unavailable", "server_not_running: down", ""}},
		{"JS: agentState: success without agent_status is unavailable", fakecli.Rule{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"name":"x"}}}`}, AgentStateResult{"unavailable", "agent get returned no agent_status", ""}},
		{"JS: agentState: failure without JSON sanitizes the cause (tabs, control chars)", fakecli.Rule{Argv: []string{"agent", "get", "x"}, Stderr: "line\t two\n\x1b[31mred", Code: 1}, AgentStateResult{"unavailable", "line two [31mred", ""}},
		{"JS: agentState: normal success returns the agent_status", fakecli.Rule{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}, AgentStateResult{"working", "", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFake(t, "herdr", []fakecli.Rule{tc.rule})
			readyProbe(t, env)
			got := AgentState("x", env, fakeTimeout(100*time.Millisecond), nil)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
}

func TestAgentStateFalseStatusIsMissing(t *testing.T) { // JS: "agent_status false is absent, while JS stringification remains false"
	// Mutation captured: treating false as the string "false" accepts an unknown Herdr state.
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":false}}}`}})
	readyProbe(t, env)
	got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{})
	if got != (AgentStateResult{State: "unavailable", Cause: "agent get returned no agent_status", Seq: ""}) {
		t.Fatalf("AgentState(false)=%#v", got)
	}
	if jsString(false) != "false" {
		t.Fatalf("jsString(false)=%q", jsString(false))
	}
}

func TestAgentStateMissingCliAndNonIntegerSeq(t *testing.T) {
	t.Run("JS: agentState: never throws when herdr is missing (unavailable, exit 127)", func(t *testing.T) {
		got := AgentState("any", platform.Env{"PATH": t.TempDir()}, fakeTimeout(time.Second), nil)
		want := AgentStateResult{"unavailable", "herdr agent get failed (exit 127)", ""}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v want %#v", got, want)
		}
	})
	t.Run("JS: state_change_seq string is not an integer", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":"7"}}}`}})
		readyProbe(t, env)
		got := AgentState("x", env, fakeTimeout(time.Second), nil)
		if got.Seq != "" || got.State != "idle" {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("JS: state_change_seq fractional number is not an integer", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1.5}}}`}})
		readyProbe(t, env)
		got := AgentState("x", env, fakeTimeout(time.Second), nil)
		if got.Seq != "" || got.State != "idle" {
			t.Fatalf("got %#v", got)
		}
	})
}

// Mutation captured: omitting AgentState's configured retry pause returns too early.
func TestAgentStateSequenceAndRetries(t *testing.T) {
	t.Run("JS: agentState: state_change_seq is returned when it is an integer, else seq is empty", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":3}}}`}})
		readyProbe(t, env)
		got := AgentState("x", env, fakeTimeout(time.Second), nil)
		if got.Seq != int64(3) || got.State != "idle" {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("JS: agentState: a kill by a signal (exit 137) is retried before unavailable", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Call: 1, Code: 137}, {Argv: []string{"agent", "get", "x"}, Call: 2, Code: 137}, {Argv: []string{"agent", "get", "x"}, Call: 3, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
		got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{time.Millisecond, time.Millisecond})
		if got.State != "working" {
			t.Fatalf("got %#v", got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 3 {
			t.Fatalf("calls %d", len(calls))
		}
	})
	t.Run("JS: agentState retry waits for the configured pause", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Call: 1, Code: 137}, {Argv: []string{"agent", "get", "x"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
		readyProbe(t, env)
		start := time.Now()
		got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{25 * time.Millisecond})
		if got.State != "working" || time.Since(start) < 20*time.Millisecond {
			t.Fatalf("retry result/pause: %#v elapsed=%s", got, time.Since(start))
		}
	})
	t.Run("JS: agentState timeout is unavailable and is not retried", func(t *testing.T) {
		timeout := fakeTimeout(25 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Delay: timeoutDelay(timeout)}})
		readyProbe(t, env)
		got := AgentState("x", env, timeout, []time.Duration{time.Millisecond})
		if got.State != "unavailable" || got.Cause != timeoutMsg("agent get", timeout) {
			t.Fatalf("got %#v", got)
		}
	})
}

func TestHerdrCallsAndResults(t *testing.T) {
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"a","pane_id":"p1"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"p1"}]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"t1"}]}}`},
		{Argv: []string{"agent", "read", "a", "--source", "visible", "--lines", "20"}, Stdout: "screen\n"},
		{Argv: []string{"pane", "report-metadata", "p9", "--source", "soho", "--title", "task title"}},
		{Argv: []string{"pane", "report-metadata", "p9", "--source", "soho", "--clear-title"}},
		{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"label"},"root_pane":{"pane_id":"p1"}}}`},
		{Argv: []string{"tab", "create", "--workspace", "ws", "--cwd", "/tmp/a b", "--label", "new", "--no-focus"}, Stdout: `{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}`},
		{Argv: []string{"pane", "split", "p1", "--direction", "right", "--cwd", "/tmp", "--no-focus", "--ratio", "0.70"}, Stdout: `{"result":{"pane":{"pane_id":"p3"}}}`},
		{Argv: []string{"agent", "prompt", "a", "hello"}, Stdout: "ok\n"},
	}
	env := newFake(t, "herdr", rules)
	readyProbe(t, env)
	if got := LiveAgents(env, fakeTimeout(time.Second)); len(got) != 1 {
		t.Fatalf("agents: %#v", got)
	} else if obj, ok := got[0].(*jsonjs.Object); !ok || !reflect.DeepEqual(obj.Keys(), []string{"name", "pane_id"}) {
		t.Fatalf("ordered agent object: %#v", got[0])
	}
	if got := PaneList(env, "ws"); len(got) != 1 {
		t.Fatalf("panes: %#v", got)
	}
	if got := TabList(env, "ws"); len(got) != 1 {
		t.Fatalf("tabs: %#v", got)
	}
	lines := 20
	if got := AgentRead(env, "a", "visible", &lines); got != "screen\n" {
		t.Fatalf("read %q", got)
	}
	t.Run(`JS: paneTitle: pane id before the options, title and clear recorded verbatim`, func(t *testing.T) {
		title := "task title"
		if !PaneTitle("p9", &title, env) || !PaneTitle("p9", nil, env) {
			t.Fatal("pane metadata failed")
		}
	})
	if got := TabGet("t1", env); got != (TabGetResult{true, "label", "p1"}) {
		t.Fatalf("tabget %#v", got)
	}
	if got := TabCreate(env, "ws", "/tmp/a b", "new"); got != (TabCreateResult{true, "t2", "p2"}) {
		t.Fatalf("tabcreate %#v", got)
	}
	ratio := "0.70"
	if got := PaneSplit("p1", "right", "/tmp", env, &ratio); got != (PaneSplitResult{true, "p3"}) {
		t.Fatalf("split %#v", got)
	}
	if got := AgentPrompt("a", "hello", env); !got.Ok {
		t.Fatalf("prompt %#v", got)
	} else if got.Raw != "ok" {
		t.Fatalf("prompt raw output: %#v", got)
	}
}

func TestTM11HerdrRosterReads(t *testing.T) {
	t.Run("JS: liveAgents / paneList / tabList / agentRead: the JSON fields the roster reads", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":"p1","agent_status":"working"}]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"p1","tab_id":"t1"}]}}`},
			{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"t1","label":"herd"}]}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "screen\n"},
		})
		readyProbe(t, env)
		if got := LiveAgents(env, time.Second); len(got) != 1 {
			t.Fatalf("agents=%#v", got)
		}
		if got := PaneList(env, "ws"); len(got) != 1 {
			t.Fatalf("panes=%#v", got)
		}
		if got := TabList(env, "ws"); len(got) != 1 {
			t.Fatalf("tabs=%#v", got)
		}
		lines := 20
		if got := AgentRead(env, "worker", "visible", &lines); got != "screen\n" {
			t.Fatalf("agent read=%q", got)
		}
	})
	t.Run(`JS: "herdr timeout: agentState is unavailable and liveAgents throws DieError 4"`, func(t *testing.T) {
		timeout := fakeTimeout(25 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Delay: timeoutDelay(timeout)}})
		readyProbe(t, env)
		if got := AgentState("x", env, timeout, nil); got.State != "unavailable" || got.Cause != timeoutMsg("agent get", timeout) {
			t.Fatalf("agentState timeout=%#v", got)
		}
		env = newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Delay: timeoutDelay(timeout)}})
		readyProbe(t, env)
		defer catchDie(t, 4, timeoutMsg("agent list", timeout))
		LiveAgents(env, timeout)
	})
}

// Mutation captured: dropping a CLI flag or pane id changes the logged argument vectors.
func TestHerdrArgumentContracts(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "layout", "--current"}, Stdout: "{}"},
		{Argv: []string{"pane", "layout", "--pane", "p1"}, Stdout: "{}"},
		{Argv: []string{"agent", "get", "caller-pane"}, Stdout: `{"result":{"agent":{"name":"caller"}}}`},
		{Argv: []string{"notification", "show", "title", "--body", "body", "--sound", "done"}},
	})
	if r := PaneLayout(env, ""); r.Status == nil || *r.Status != 0 {
		t.Fatalf("current layout: %+v", r)
	}
	if r := PaneLayout(env, "p1"); r.Status == nil || *r.Status != 0 {
		t.Fatalf("pane layout: %+v", r)
	}
	env["HERDR_PANE_ID"] = "caller-pane"
	if got := CallerAgentName(env); got != "caller" {
		t.Fatalf("caller name %q", got)
	}
	NotificationShow("title", "body", env)
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"pane", "layout", "--current"},
		{"pane", "layout", "--pane", "p1"},
		{"agent", "get", "caller-pane"},
		{"notification", "show", "title", "--body", "body", "--sound", "done"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls: %#v", calls)
	}
	for i := range want {
		if !reflect.DeepEqual(calls[i].Argv, want[i]) {
			t.Errorf("call %d argv %#v want %#v", i, calls[i].Argv, want[i])
		}
	}
}

func TestPaneSendTextAndSendKeysContracts(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-text", "p2", "x"}, Code: 1},
	})
	if !PaneSendText("p1", "/compact", env) {
		t.Fatal("PaneSendText ok=false on exit 0")
	}
	if !PaneSendKeys("p1", "Enter", env) {
		t.Fatal("PaneSendKeys ok=false on exit 0")
	}
	if PaneSendText("p2", "x", env) {
		t.Fatal("PaneSendText ok=true on exit 1")
	}
	if PaneSendText("p1", "other", env) {
		t.Fatal("PaneSendText ok=true for an argv the fake has no rule for")
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"pane", "send-text", "p1", "/compact"},
		{"pane", "send-keys", "p1", "Enter"},
		{"pane", "send-text", "p2", "x"},
		{"pane", "send-text", "p1", "other"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls: %#v", calls)
	}
	for i := range want {
		if !reflect.DeepEqual(calls[i].Argv, want[i]) {
			t.Errorf("call %d argv %#v want %#v", i, calls[i].Argv, want[i])
		}
	}
}

func TestPaneMoveArgumentAndResultContract(t *testing.T) {
	// Mutation captured: changing --new-tab argument order or omitting --no-focus changes the fake CLI call record.
	env := newFake(t, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
		{Argv: []string{"pane", "move", "p2", "--tab", "t1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p2"}}}}`},
	})
	if got := PaneMove([]string{"p1", "--new-tab", "--label", "herd-park", "--no-focus"}, env); got != (PaneMoveResult{Ok: true, Pane: "p1", Tab: "park-1"}) {
		t.Fatalf("new tab move: %#v", got)
	}
	if got := PaneMove([]string{"p2", "--tab", "t1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}, env); got != (PaneMoveResult{Ok: true, Pane: "p2"}) {
		t.Fatalf("split move: %#v", got)
	}
}

func TestPaneMovePreservesFalseTabIDForParking(t *testing.T) {
	// Mutation captured: treating false as an empty tab id aborts the JavaScript-compatible parking sequence.
	env := newFake(t, "herdr", []fakecli.Rule{{
		Argv:   []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"},
		Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":false}}}}`,
	}})
	if got := PaneMove([]string{"p1", "--new-tab", "--label", "herd-park", "--no-focus"}, env); got != (PaneMoveResult{Ok: true, Pane: "p1", Tab: "false"}) {
		t.Fatalf("PaneMove()=%+v, want false tab id as string", got)
	}
}

// Mutation captured: mapping every signal status to a generic failure changes the exact cause and retry count.
func TestAgentStateSignalMessagesAndStructuredErrorRetry(t *testing.T) {
	for _, tc := range []struct {
		code int
		sig  string
	}{
		{129, "SIGHUP"}, {130, "SIGINT"}, {134, "SIGABRT"}, {137, "SIGKILL"}, {139, "SIGSEGV"}, {143, "SIGTERM"},
	} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Code: tc.code}})
			readyProbe(t, env)
			got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{})
			want := AgentStateResult{"unavailable", fmt.Sprintf("herdr agent get was killed (exit %d, %s: memory pressure or an external kill)", tc.code, tc.sig), ""}
			if got != want {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
	t.Run("structured error with code only is not retried", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "x"}, Call: 1, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 137},
			{Argv: []string{"agent", "get", "x"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		})
		got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{time.Millisecond})
		if got.State != "gone" {
			t.Fatalf("structured agent_not_found: %#v", got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil || len(calls) != 1 {
			t.Fatalf("calls=%d err=%v", len(calls), err)
		}
	})
	t.Run("timeout differs from signal exit", func(t *testing.T) {
		timeout := fakeTimeout(25 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Delay: timeoutDelay(timeout)}})
		readyProbe(t, env)
		got := AgentState("x", env, timeout, []time.Duration{})
		if got.Cause != timeoutMsg("agent get", timeout) {
			t.Fatalf("got %#v", got)
		}
	})
}

// Mutation captured: ignoring the JavaScript default retry pauses leaves a transiently killed agent unavailable.
func TestAgentStateDefaultRetryPauses(t *testing.T) {
	env := newFake(t, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "x"}, Call: 1, Code: 137},
		{Argv: []string{"agent", "get", "x"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
	})
	if got := AgentState("x", env, fakeTimeout(time.Second), nil); got.State != "working" {
		t.Fatalf("got %#v", got)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
}

// Mutation captured: case-insensitive JSON keys or Go formatting changes the structured error cause.
func TestAgentStateJSONErrorValuesMatchJavaScript(t *testing.T) {
	cases := []struct{ input, want string }{
		{`{"ERROR":{"CODE":"agent_not_found"}}`, `{"ERROR":{"CODE":"agent_not_found"}}`},
		{`{"error":{"code":1e20,"message":{"a":1}}}`, "100000000000000000000: [object Object]"},
		{`{"error":{"code":{"a":1},"message":[1,null,"x"]}}`, "[object Object]: 1,,x"},
	}
	for _, tc := range cases {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stderr: tc.input, Code: 1}})
		readyProbe(t, env)
		got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{})
		if got.Cause != tc.want {
			t.Errorf("input %s: cause %q want %q", tc.input, got.Cause, tc.want)
		}
	}
	t.Run("numeric zero code is an error value after successful CLI exit", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "x"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`, Stderr: `{"error":{"code":0}}`}})
		readyProbe(t, env)
		got := AgentState("x", env, fakeTimeout(time.Second), []time.Duration{})
		if got.State != "unavailable" || got.Cause != "0:" {
			t.Fatalf("got %#v", got)
		}
	})
}

// Mutation captured: a failed pane metadata request remains best effort and does not throw.
func TestPaneTitleFailureIsBestEffort(t *testing.T) {
	t.Run("JS: paneTitle: a failing herdr changes nothing (best effort, no throw)", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"pane", "report-metadata", "p9", "--source", "soho", "--title", "t"}, Code: 1}})
		title := "t"
		if PaneTitle("p9", &title, env) {
			t.Fatal("failure reported success")
		}
	})
}

func TestLiveAgentsFailureSemantics(t *testing.T) {
	t.Run("JS: herdr timeout: liveAgents throws DieError 4", func(t *testing.T) {
		timeout := fakeTimeout(25 * time.Millisecond)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Delay: timeoutDelay(timeout)}})
		readyProbe(t, env)
		defer catchDie(t, 4, timeoutMsg("agent list", timeout))
		LiveAgents(env, timeout)
	})
	t.Run("JS: success without an agents array dies 4", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{}}`}})
		readyProbe(t, env)
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			LiveAgents(env, fakeTimeout(time.Second))
		}()
		exitErr, ok := recovered.(*platform.ExitError)
		if !ok || exitErr.Code != 4 || !exitErr.Friction || !strings.Contains(exitErr.Msg, "returned no agent list") {
			t.Fatalf("recovered=%#v", recovered)
		}
	})
	t.Run("JS: liveAgents passes through output and herdr exit code", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: "out\n", Stderr: "err\n", Code: 3}})
		readyProbe(t, env)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		var panicValue any
		func() { defer func() { panicValue = recover() }(); LiveAgents(env, fakeTimeout(time.Second)) }()
		exitErr, ok := panicValue.(*platform.ExitError)
		if !ok || exitErr.Code != 3 {
			t.Fatalf("panic %#v", panicValue)
		}
		if stdout.String() != "out\n" || stderr.String() != "err\n" {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

// Mutation captured: returning a fixed exit code for a signaled child no longer returns 128 plus its signal number.
func TestLiveAgentsSignalExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX child signals")
	}
	env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Signal: "SIGKILL"}})
	r := platform.RunCli("herdr", []string{"agent", "list"}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 10000})
	if r.Signal != "SIGKILL" {
		t.Fatalf("signal result: %+v", r)
	}
	if got := dieError(r); got != 137 {
		t.Fatalf("dieError=%d", got)
	}
}

// Mutation captured: removing the HERDR_ENV validation changes its exit message.
func TestRequireEnv(t *testing.T) {
	t.Run("JS: requireEnv: outside Herdr dies 2; herdr missing from PATH dies 2", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "HERDR_ENV": ""}
		defer catchDie(t, 2, notRunning)
		RequireEnv(env, "darwin", 123, nil)
	})
	t.Run("JS: requireEnv missing CLI dies 2", func(t *testing.T) {
		env := platform.Env{"PATH": t.TempDir(), "HERDR_ENV": "1"}
		defer catchDie(t, 2, "herdr CLI not found in PATH")
		RequireEnv(env, "darwin", 123, nil)
	})
	t.Run("JS: Decision 2 case: um painel casa (local/<pane> e exit 2)", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Codex ancestor diagnostics use POSIX process ancestry and are unavailable on Windows")
		}
		payload := `{"result":{"snapshot":{"panes":[{"pane_id":"w:p1","agent":"codex"}]}}}`
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: payload}, {Argv: []string{"pane", "process-info", "--pane", "w:p1"}, Stdout: `{"result":{"process_info":{"foreground_processes":[{"pid":3000}]}}}`}})
		defer catchDie(t, 2, "this command runs under Codex in Herdr pane local/w:p1")
		RequireEnv(platform.Env{"PATH": env["PATH"], "HERDR_ENV": "", "SOHO_FAKECLI_CONFIG": env["SOHO_FAKECLI_CONFIG"]}, "darwin", 5000, []codexenv.Process{{PID: 3000, Name: "codex"}})
	})
}

func catchDie(t *testing.T, code int, contains string) {
	t.Helper()
	v := recover()
	e, ok := v.(*platform.ExitError)
	if !ok || e.Code != code || !strings.Contains(e.Msg, contains) {
		t.Fatalf("panic %#v; wanted exit %d containing %q", v, code, contains)
	}
}
func newFake(t *testing.T, name string, rules []fakecli.Rule) platform.Env {
	t.Helper()
	// The no-op readiness rule (argv ["__ready__"], no Call bound) backs
	// readyProbe. It matches only that argv, so it leaves every other rule's
	// per-argv call counting untouched.
	rules = append(rules, fakecli.Rule{Argv: []string{"__ready__"}})
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, name, rules); err != nil {
		t.Fatal(err)
	}
	list := fakecli.Env(os.Environ(), dir)
	env := platform.Env{}
	for _, item := range list {
		k, v, ok := strings.Cut(item, "=")
		if ok {
			env[k] = v
		}
	}
	env["HERDR_SOCKET_PATH"] = "/tmp/hs-go/build/none.sock"
	env["TMPDIR"] = t.TempDir()
	return env
}

func fakeTimeout(timeout time.Duration) time.Duration {
	if runtime.GOOS == "windows" && timeout < 5*time.Second {
		return 5 * time.Second
	}
	return timeout
}

// readyProbe issues one no-deadline fake CLI call (the dedicated no-op rule
// for argv ["__ready__"]) and waits for it right before a production timed
// call, so a launch spike of the fixture's fake image is absorbed outside
// the deadline. It is a readiness heuristic, not a deterministic guarantee:
// a spike can still hit the timed call inside its window.
func readyProbe(t *testing.T, env platform.Env) {
	t.Helper()
	name := "herdr"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cmd := exec.Command(filepath.Join(env.Get("PATH"), name), "__ready__")
	cmd.Env = env.List()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("readiness probe (no deadline) failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func timeoutDelay(timeout time.Duration) int {
	delay := 250 * time.Millisecond
	if runtime.GOOS == "windows" {
		delay = timeout + time.Second
	}
	return int(delay / time.Millisecond)
}

// TestAgentReadWorkingAltScreenFallsBackToVisible: Herdr refuses a recent read
// of a working full-screen TUI (agent_not_idle); the visible screen stands in.
// Any other failure, or a visible read, is not retried.
func TestAgentReadWorkingAltScreenFallsBackToVisible(t *testing.T) {
	notIdle := `{"error":{"code":"agent_not_idle","message":"cannot read 40 lines while x is working"}}`
	lines := 40
	t.Run("agent_not_idle on a recent read uses the visible screen", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "read", "x", "--source", "recent-unwrapped"}, ArgvPrefix: true, Code: 1, Stderr: notIdle},
			{Argv: []string{"agent", "read", "x", "--source", "recent"}, ArgvPrefix: true, Code: 1, Stderr: notIdle},
			{Argv: []string{"agent", "read", "x", "--source", "visible"}, Stdout: "visible screen\n"},
		})
		if got := AgentRead(env, "x", "recent-unwrapped", &lines); got != "visible screen\n" {
			t.Fatalf("AgentRead=%q", got)
		}
		if got, ok := AgentReadOK(env, "x", "recent", &lines); !ok || got != "visible screen\n" {
			t.Fatalf("AgentReadOK=%q %v", got, ok)
		}
	})
	t.Run("another error code is not retried", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "read", "x", "--source", "recent-unwrapped"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
			{Argv: []string{"agent", "read", "x", "--source", "visible"}, Stdout: "visible screen\n"},
		})
		if got, ok := AgentReadOK(env, "x", "recent-unwrapped", &lines); ok || got != "" {
			t.Fatalf("AgentReadOK=%q %v", got, ok)
		}
	})
	t.Run("a failed visible read is not retried", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "read", "x", "--source", "visible"}, Code: 1, Stderr: notIdle},
		})
		if got, ok := AgentReadOK(env, "x", "visible", nil); ok || got != "" {
			t.Fatalf("AgentReadOK=%q %v", got, ok)
		}
	})
}

// TestPaneReadRunsPaneRead: PaneRead reads the pane, not an agent, with the
// source and the line count it is given; a failed read is ok=false.
func TestPaneReadRunsPaneRead(t *testing.T) {
	lines := 40
	t.Run("the argv and the screen", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: boom\n"},
			{Argv: []string{"pane", "read", "w1:p2", "--source", "visible"}, Stdout: "visible\n"},
		})
		if got, ok := PaneRead(env, "w1:p2", "recent-unwrapped", &lines); !ok || got != "Error: boom\n" {
			t.Fatalf("PaneRead=%q %v", got, ok)
		}
		if got, ok := PaneRead(env, "w1:p2", "visible", nil); !ok || got != "visible\n" {
			t.Fatalf("PaneRead without --lines=%q %v", got, ok)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(env.Get("PATH"), "herdr.calls.jsonl"))
		if err != nil || len(calls) != 2 {
			t.Fatalf("calls=%#v err=%v", calls, err)
		}
	})
	t.Run("a failed read is not ok and returns no text", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{
			{Argv: []string{"pane", "read"}, ArgvPrefix: true, Code: 1, Stdout: "partial\n", Stderr: `{"error":{"code":"pane_not_found","message":"no pane"}}`},
		})
		if got, ok := PaneRead(env, "w1:p2", "recent-unwrapped", &lines); ok || got != "" {
			t.Fatalf("PaneRead=%q %v", got, ok)
		}
	})
}
