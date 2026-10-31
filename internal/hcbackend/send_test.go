package hcbackend

// DJA-604 2b-3a: `hc send` and the raw session listing.

import (
	"context"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const sendPrompt = "Read the file /tmp/brief.md in full."

func TestBackendSend(t *testing.T) {
	argv := []string{"send", "--if", "idle", "--json", "--", "worker", sendPrompt}
	t.Run("exit 0 returns the raw SendResult record", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: argv, Stdout: `{"turn":7,"started":true}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != `{"turn":7,"started":true}` {
			t.Fatalf("rep=%+v", rep)
		}
		wantArgv(t, calls(), [][]string{argv})
	})
	t.Run("a text that starts with a dash is a value, not a flag", func(t *testing.T) {
		dash := []string{"send", "--if", "idle", "--json", "--", "worker", "-force"}
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: dash, Stdout: `{"turn":1,"started":true}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: "-force"}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep=%+v", rep)
		}
		wantArgv(t, calls(), [][]string{dash})
	})
	t.Run("--queue drops the --if idle guard", func(t *testing.T) {
		queueArgv := []string{"send", "--json", "--", "worker", sendPrompt}
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: queueArgv, Stdout: `{"turn":8,"started":false}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt, Queue: true}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != `{"turn":8,"started":false}` {
			t.Fatalf("rep=%+v", rep)
		}
		wantArgv(t, calls(), [][]string{queueArgv})
	})
	t.Run("exit 9 is FailureBlocked with the person-must-answer message", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: argv, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"worker is blocked"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureBlocked || rep.Value != "" {
			t.Fatalf("rep=%+v", rep)
		}
		if want := "worker worker is blocked: a person must answer its prompt"; rep.Err.Message != want {
			t.Fatalf("message=%q, want %q", rep.Err.Message, want)
		}
	})
	t.Run("exit 10 is FailureBusy with the not-idle message", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: argv, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureBusy || rep.Value != "" {
			t.Fatalf("rep=%+v", rep)
		}
		if want := "worker worker is not idle"; rep.Err.Message != want {
			t.Fatalf("message=%q, want %q", rep.Err.Message, want)
		}
	})
	t.Run("exit 11 (finished) is FailureNotFound", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: argv, Code: 11, Stderr: `{"error":{"kind":"finished","message":"no such running terminal"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("rep=%+v", rep)
		}
	})
	t.Run("exit 3 (not found) is FailureNotFound", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: argv, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("rep=%+v", rep)
		}
	})
	t.Run("any other exit takes the backend's usual mapping", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: argv, Code: 1, Stderr: "boom"},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep=%+v", rep)
		}
		if !strings.Contains(rep.Err.Message, "boom") {
			t.Fatalf("message=%q, want the hc error text", rep.Err.Message)
		}
	})
}

func TestBackendAgentListRaw(t *testing.T) {
	const list = `{"terminals":[{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}},{"address":"t9","name":"reviewer","state":"live","turn":2,"reported":true,"status":{"state":"working"}}]}`
	t.Run("the raw terminal records of the session", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: list},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentListRaw(context.Background(), transport.Call{})
		if rep.Err != nil || !rep.Valid || len(rep.Value) != 2 {
			t.Fatalf("rep=%+v", rep)
		}
		// The raw records keep the fields beyond the terminal projection
		// (name, address, turn, labels a reader may need).
		for _, needle := range []string{`"address":"t7"`, `"name":"worker"`, `"turn":5`} {
			if !strings.Contains(string(rep.Value[0]), needle) {
				t.Fatalf("record 0 = %s, want to hold %s", rep.Value[0], needle)
			}
		}
		if !strings.Contains(string(rep.Value[1]), `"name":"reviewer"`) {
			t.Fatalf("record 1 = %s, want the reviewer", rep.Value[1])
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"ls", "--label", "soho.session=sX", "--json"},
		})
	})
	t.Run("an empty session has an empty, valid listing", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentListRaw(context.Background(), transport.Call{})
		if rep.Err != nil || !rep.Valid || len(rep.Value) != 0 {
			t.Fatalf("rep=%+v", rep)
		}
	})
	t.Run("a failed listing is unavailable, never not-found", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such label"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentListRaw(context.Background(), transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep=%+v", rep)
		}
	})
}
