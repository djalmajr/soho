package hcbackend

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

// fakeHc installs a fake hc binary with the given rules and returns the
// environment (HC_TERMINAL_ID set to t1) and the recorded-calls reader.
func fakeHc(t *testing.T, rules []fakecli.Rule) (platform.Env, func() []fakecli.Call) {
	t.Helper()
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "hc", rules); err != nil {
		t.Fatal(err)
	}
	list := fakecli.Env(os.Environ(), dir)
	env := platform.Env{}
	for _, item := range list {
		if k, v, ok := strings.Cut(item, "="); ok {
			env[k] = v
		}
	}
	env["HC_TERMINAL_ID"] = "t1"
	return env, func() []fakecli.Call {
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "hc.json"))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		return calls
	}
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

// TestBackendGet: the state read issues one hc get, maps the record's
// status, and maps the failures (exit 3 is gone, another error is
// unavailable with the cause).
func TestBackendGet(t *testing.T) {
	const liveWorking = `{"address":"t7","name":"w7","state":"live","status":{"state":"working"},"turn":5}`

	t.Run("live working: one get, mapped state and turn", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: liveWorking}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if !rep.Valid {
			t.Fatal("not valid")
		}
		if rep.Value.State != "working" {
			t.Fatalf("state %q want working", rep.Value.State)
		}
		if string(rep.Value.Sequence) != "5" {
			t.Fatalf("sequence %q want 5", rep.Value.Sequence)
		}
		if rep.Value.PaneID != "t7" || rep.Value.Name != "w7" {
			t.Fatalf("pane/name %q/%q want t7/w7", rep.Value.PaneID, rep.Value.Name)
		}
		wantArgv(t, calls(), [][]string{{"get", "t7", "--json"}})
	})

	t.Run("exit 3 is gone with no error", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if !rep.Valid {
			t.Fatal("not valid")
		}
		if rep.Value.State != "gone" {
			t.Fatalf("state %q want gone", rep.Value.State)
		}
		wantArgv(t, calls(), [][]string{{"get", "t7", "--json"}})
	})

	t.Run("a finished terminal is gone", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished","status":null,"turn":9}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if rep.Value.State != "gone" {
			t.Fatalf("state %q want gone", rep.Value.State)
		}
		if string(rep.Value.Sequence) != "9" {
			t.Fatalf("sequence %q want 9", rep.Value.Sequence)
		}
	})

	t.Run("a live terminal with a null status is unknown", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","status":null}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if rep.Value.State != "unknown" {
			t.Fatalf("state %q want unknown", rep.Value.State)
		}
		if rep.Value.Sequence != nil {
			t.Fatalf("sequence %v want none", rep.Value.Sequence)
		}
	})

	t.Run("each 7501 word maps to itself", func(t *testing.T) {
		for _, word := range []string{"idle", "blocked", "done", "error"} {
			word := word
			t.Run(word, func(t *testing.T) {
				env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"` + word + `"},"turn":1}`}})
				b := &backend{env: env, client: hc.New(env)}
				rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
				if rep.Err != nil {
					t.Fatalf("err: %v", rep.Err)
				}
				if rep.Value.State != word {
					t.Fatalf("state %q want %s", rep.Value.State, word)
				}
			})
		}
	})

	t.Run("another failure is unavailable with the cause", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"core exploded"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil {
			t.Fatal("no err")
		}
		if rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("kind %q want unavailable", rep.Err.Kind)
		}
		if !strings.Contains(rep.Err.Message, "core exploded") {
			t.Fatalf("message %q lacks the cause", rep.Err.Message)
		}
		if rep.Value.State != "unavailable" {
			t.Fatalf("state %q want unavailable", rep.Value.State)
		}
	})

	t.Run("a machine target is the machine-prefixed address", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "m1/t7", "--json"}, Stdout: liveWorking}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, transport.Call{})
		if rep.Err != nil || rep.Value.State != "working" {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "m1/t7", "--json"}})
	})

	t.Run("a missing binary is unavailable", func(t *testing.T) {
		b := &backend{env: platform.Env{"HC_TERMINAL_ID": "t1"}, client: hc.New(platform.Env{})}
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			return platform.RunResult{NotFound: true}
		}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil {
			t.Fatal("no err")
		}
		if rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("kind %q want unavailable", rep.Err.Kind)
		}
		if !strings.Contains(rep.Err.Message, "hc CLI not found in PATH") {
			t.Fatalf("message %q", rep.Err.Message)
		}
		if rep.Value.State != "unavailable" {
			t.Fatalf("state %q want unavailable", rep.Value.State)
		}
	})

	t.Run("a non-object body is a protocol failure", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `[1,2]`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
			t.Fatalf("err: %+v", rep.Err)
		}
		if rep.Valid {
			t.Fatal("valid on a protocol failure")
		}
	})
}

// TestBackendList: the roster read issues one hc ls filtered on the
// soho.session label (mandatory), after the one session read.
func TestBackendList(t *testing.T) {
	const twoTerminals = `{"terminals":[` +
		`{"address":"t7","name":"w7","state":"live","status":{"state":"working"},"turn":5},` +
		`{"address":"t8","name":"w8","state":"live","status":{"state":"idle"},"turn":1}` +
		`],"hidden_finished":0}`

	t.Run("label present: the label filters the ls", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
			{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Stdout: twoTerminals},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if !rep.Valid || len(rep.Value) != 2 {
			t.Fatalf("rep: %+v", rep)
		}
		want := []struct{ id, name, state, seq string }{
			{"t7", "w7", "working", "5"},
			{"t8", "w8", "idle", "1"},
		}
		for i, w := range want {
			got := rep.Value[i]
			if got.Target.ID != w.id || got.PaneID != w.id || got.Name != w.name || got.State != w.state || string(got.Sequence) != w.seq {
				t.Fatalf("terminal %d: %+v want %v", i, got, w)
			}
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"ls", "--label", "soho.session=s9", "--json"},
		})
	})

	t.Run("label absent: the fallback id filters the ls", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live"}`},
			{Argv: []string{"ls", "--label", "soho.session=hc-t1", "--json"}, Stdout: `{"terminals":[]}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		if len(rep.Value) != 0 {
			t.Fatalf("len %d want 0", len(rep.Value))
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"ls", "--label", "soho.session=hc-t1", "--json"},
		})
	})

	t.Run("outside an hc terminal: no ls, unavailable", func(t *testing.T) {
		env, calls := fakeHc(t, nil)
		delete(env, "HC_TERMINAL_ID")
		b := &backend{env: env, client: hc.New(env)}
		rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err == nil {
			t.Fatal("no err")
		}
		if rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("kind %q want unavailable", rep.Err.Kind)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("calls %v want none", got)
		}
	})

	t.Run("a failed ls is unavailable with the cause", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live"}`},
			{Argv: []string{"ls", "--label", "soho.session=hc-t1", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"core down"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err: %+v", rep.Err)
		}
		if !strings.Contains(rep.Err.Message, "core down") {
			t.Fatalf("message %q", rep.Err.Message)
		}
	})

	t.Run("a non-object terminal in the list is a protocol failure", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live"}`},
			{Argv: []string{"ls", "--label", "soho.session=hc-t1", "--json"}, Stdout: `{"terminals":[7]}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
			t.Fatalf("err: %+v", rep.Err)
		}
	})
}

// TestBackendRead: the screen read maps the source to --lines, keeps the
// raw record on the Process, and refuses an unknown source without a launch.
func TestBackendRead(t *testing.T) {
	const readRec = `{"text":"screen line\n","complete":true,"how":"screen"}`

	t.Run("visible: --lines 0, text and raw record kept", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: readRec}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Read(context.Background(), transport.Target{ID: "t7"}, transport.Read{Source: "visible"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if rep.Value != "screen line\n" {
			t.Fatalf("value %q", rep.Value)
		}
		if rep.Process.Status == nil || *rep.Process.Status != 0 {
			t.Fatalf("process status %v", rep.Process.Status)
		}
		if !strings.Contains(rep.Process.Stdout, `"how":"screen"`) {
			t.Fatalf("raw record not kept: %q", rep.Process.Stdout)
		}
		wantArgv(t, calls(), [][]string{{"read", "t7", "--lines", "0", "--json"}})
	})

	t.Run("recent-unwrapped: the request's lines", func(t *testing.T) {
		lines := 40
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"read", "t7", "--lines", "40", "--json"}, Stdout: readRec}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Read(context.Background(), transport.Target{ID: "t7"}, transport.Read{Source: "recent-unwrapped", Lines: &lines}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		wantArgv(t, calls(), [][]string{{"read", "t7", "--lines", "40", "--json"}})
	})

	t.Run("recent-unwrapped without lines: the documented default", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"read", "t7", "--lines", "40", "--json"}, Stdout: readRec}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Read(context.Background(), transport.Target{ID: "t7"}, transport.Read{Source: "recent-unwrapped"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		wantArgv(t, calls(), [][]string{{"read", "t7", "--lines", "40", "--json"}})
	})

	t.Run("an unknown source is unsupported without a launch", func(t *testing.T) {
		env, calls := fakeHc(t, nil)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Read(context.Background(), transport.Target{ID: "t7"}, transport.Read{Source: "bogus"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnsupported {
			t.Fatalf("err: %+v", rep.Err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("calls %v want none", got)
		}
	})

	t.Run("exit 3 is not-found", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Read(context.Background(), transport.Target{ID: "t7"}, transport.Read{Source: "visible"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("err: %+v", rep.Err)
		}
	})
}

// TestBackendKeys: the key send maps soho's names to hc's lowercase names,
// one launch, and refuses an unmapped key without a launch.
func TestBackendKeys(t *testing.T) {
	t.Run("Enter maps to enter", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"keys", "t7", "enter", "--json"}, Stdout: `{"status":"working","turn":3}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Keys(context.Background(), transport.Target{ID: "t7"}, "Enter", false, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if !rep.Value || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"keys", "t7", "enter", "--json"}})
	})

	for _, pair := range []struct{ soho, hcName string }{
		{"enter", "enter"},
		{"ctrl+u", "ctrl+u"},
		{"shift+tab", "shift+tab"},
		{"esc", "esc"},
	} {
		pair := pair
		t.Run(pair.soho+" passes through as "+pair.hcName, func(t *testing.T) {
			env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"keys", "t7", pair.hcName, "--json"}, Stdout: `{"turn":3}`}})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Keys(context.Background(), transport.Target{ID: "t7"}, pair.soho, true, transport.Call{})
			if rep.Err != nil || !rep.Value {
				t.Fatalf("rep: %+v", rep)
			}
			wantArgv(t, calls(), [][]string{{"keys", "t7", pair.hcName, "--json"}})
		})
	}

	t.Run("an unmapped key is unsupported without a launch", func(t *testing.T) {
		env, calls := fakeHc(t, nil)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Keys(context.Background(), transport.Target{ID: "t7"}, "bogus+x", false, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnsupported {
			t.Fatalf("err: %+v", rep.Err)
		}
		if !strings.Contains(rep.Err.Message, "bogus+x") {
			t.Fatalf("message %q", rep.Err.Message)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("calls %v want none", got)
		}
	})

	t.Run("exit 3 is not-found", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"keys", "t7", "enter", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Keys(context.Background(), transport.Target{ID: "t7"}, "Enter", false, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("err: %+v", rep.Err)
		}
	})

	t.Run("another failure is unavailable with the cause", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"keys", "t7", "enter", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"no keyboard"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Keys(context.Background(), transport.Target{ID: "t7"}, "Enter", false, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err: %+v", rep.Err)
		}
		if !strings.Contains(rep.Err.Message, "no keyboard") {
			t.Fatalf("message %q", rep.Err.Message)
		}
	})
}

// TestBackendClose: the close is kill then rm (get first to tell a finished
// terminal from a live one), and closed means the record is gone.
func TestBackendClose(t *testing.T) {
	const live = `{"address":"t7","state":"live","status":{"state":"idle"},"turn":1}`
	const finished = `{"address":"t7","state":"finished","status":null,"turn":4}`

	t.Run("live: get, kill, rm", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Stdout: live},
			{Argv: []string{"kill", "t7", "--json"}, Stdout: live},
			{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"removed":"t7"}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Close(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if !rep.Value || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t7", "--json"},
			{"kill", "t7", "--json"},
			{"rm", "t7", "--json"},
		})
	})

	t.Run("a finished terminal goes straight to rm", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Stdout: finished},
			{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"removed":"t7"}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Close(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil || !rep.Value {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t7", "--json"},
			{"rm", "t7", "--json"},
		})
	})

	t.Run("an already removed record is closed", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Close(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil || !rep.Value {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t7", "--json"}})
	})

	t.Run("a kill failure stops before the rm", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Stdout: live},
			{Argv: []string{"kill", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"cannot signal"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Close(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err: %+v", rep.Err)
		}
		if rep.Value {
			t.Fatal("closed on a kill failure")
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t7", "--json"},
			{"kill", "t7", "--json"},
		})
	})

	t.Run("an rm failure is not closed", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Stdout: live},
			{Argv: []string{"kill", "t7", "--json"}, Stdout: live},
			{Argv: []string{"rm", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"still live"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Close(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil || !strings.Contains(rep.Err.Message, "still live") {
			t.Fatalf("err: %+v", rep.Err)
		}
		if rep.Value {
			t.Fatal("closed on an rm failure")
		}
	})
}

// TestBackendGetGone: gone when hc get exits 3 or the record is finished;
// a live terminal or another failure reports false.
func TestBackendGetGone(t *testing.T) {
	t.Run("exit 3 is gone", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.GetGone(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if !rep.Value || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t7", "--json"}})
	})

	t.Run("a finished record is gone", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.GetGone(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if !rep.Value {
			t.Fatalf("rep: %+v", rep)
		}
	})

	t.Run("a live record is not gone", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"idle"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.GetGone(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Value {
			t.Fatalf("rep: %+v", rep)
		}
	})

	t.Run("another failure reports false", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"down"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.GetGone(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Value {
			t.Fatalf("rep: %+v", rep)
		}
	})
}

// TestBackendCallerAgentName: one hc get on the caller's address, the
// record's name; an unnamed or absent terminal fails with an empty value.
func TestBackendCallerAgentName(t *testing.T) {
	t.Run("the caller's name", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","name":"orch","state":"live"}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.CallerAgentName(context.Background(), transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if rep.Value != "orch" || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})

	t.Run("an unnamed terminal fails with an empty value", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","name":null,"state":"live"}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.CallerAgentName(context.Background(), transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("err: %+v", rep.Err)
		}
		if rep.Value != "" {
			t.Fatalf("value %q want empty", rep.Value)
		}
	})

	t.Run("outside an hc terminal: no launch, unavailable", func(t *testing.T) {
		env, calls := fakeHc(t, nil)
		delete(env, "HC_TERMINAL_ID")
		b := &backend{env: env, client: hc.New(env)}
		rep := b.CallerAgentName(context.Background(), transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err: %+v", rep.Err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("calls %v want none", got)
		}
	})
}

// TestBackendAgentGetOK: one hc get, exit 0 only, no body parsing.
func TestBackendAgentGetOK(t *testing.T) {
	t.Run("exit 0 is true even without a status body", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7"}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentGetOK(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err != nil || !rep.Value || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t7", "--json"}})
	})

	t.Run("exit 3 fails as not-found", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentGetOK(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("err: %+v", rep.Err)
		}
		if rep.Value {
			t.Fatal("true on a lookup failure")
		}
	})

	t.Run("another failure is unavailable", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"down"}}`}})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentGetOK(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err: %+v", rep.Err)
		}
	})
}

// TestBackendCaller: the environment identity, with the session as the
// workspace id (the session read happens through the instance's runner).
func TestBackendCaller(t *testing.T) {
	t.Run("outside an hc terminal: no launch, not inside", func(t *testing.T) {
		var launched int
		b := &backend{env: platform.Env{}, client: hc.New(platform.Env{})}
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			launched++
			zero := 0
			return platform.RunResult{Status: &zero, Stdout: `{}`}
		}
		caller := b.Caller()
		if caller.Inside || caller.PaneID != "" || caller.WorkspaceID != "" || caller.TabID != "" {
			t.Fatalf("caller: %+v", caller)
		}
		if launched != 0 {
			t.Fatalf("launched %d want 0", launched)
		}
	})

	t.Run("inside: the address is the pane id, the session the workspace id", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t9"}
		b := &backend{env: env, client: hc.New(env)}
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			zero := 0
			return platform.RunResult{Status: &zero, Stdout: `{"address":"t9","state":"live"}`}
		}
		caller := b.Caller()
		if !caller.Inside || caller.PaneID != "t9" || caller.WorkspaceID != "hc-t9" || caller.TabID != "" {
			t.Fatalf("caller: %+v", caller)
		}
	})

	t.Run("inside with a label: the label is the workspace id", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t9"}
		b := &backend{env: env, client: hc.New(env)}
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			zero := 0
			return platform.RunResult{Status: &zero, Stdout: `{"address":"t9","state":"live","labels":{"soho.session":"sX"}}`}
		}
		caller := b.Caller()
		if caller.WorkspaceID != "sX" {
			t.Fatalf("workspace %q want sX", caller.WorkspaceID)
		}
	})
}
