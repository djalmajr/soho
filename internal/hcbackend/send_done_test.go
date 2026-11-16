package hcbackend

import (
	"context"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

func TestSendIdleGuardDoneStatus(t *testing.T) {
	for _, tc := range []struct {
		name, record      string
		getCode, sendCode int
		unguarded         bool
		failure           transport.FailureKind
	}{
		{name: "done", record: `{"state":"live","status":{"state":"done"}}`, unguarded: true},
		{name: "idle", record: `{"state":"live","status":{"state":"idle"}}`},
		{name: "working", record: `{"state":"live","status":{"state":"working"}}`, sendCode: 10, failure: transport.FailureBusy},
		{name: "blocked", record: `{"state":"live","status":{"state":"blocked"}}`, sendCode: 9, failure: transport.FailureBlocked},
		{name: "get fails", getCode: 3},
		{name: "invalid JSON", record: `{`},
		{name: "missing status", record: `{"state":"live"}`},
		{name: "unknown", record: `{"state":"live","status":{"state":"Done"}}`},
		{name: "whitespace", record: `{"state":"live","status":{"state":"done "}}`},
		{name: "string status", record: `{"state":"live","status":"done"}`},
		{name: "finished", record: `{"state":"finished","status":{"state":"done"}}`, sendCode: 11, failure: transport.FailureNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := []string{"get", "worker", "--json"}
			send := idleSendArgv("worker", sendPrompt)
			if tc.unguarded {
				send = []string{"send", "--json", "--", "worker", sendPrompt}
			}
			rules := append(probeRules("", sendHelpOld, schemaDoc(`"idle","working"`, true, false)),
				fakecli.Rule{Argv: get, Stdout: tc.record, Code: tc.getCode},
				fakecli.Rule{Argv: send, Stdout: `{"turn":7,"started":true}`, Code: tc.sendCode})
			env, calls := fakeHc(t, rules)
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
			if tc.failure != "" {
				if rep.Err == nil || rep.Err.Kind != tc.failure {
					t.Fatalf("rep=%+v want failure %s", rep, tc.failure)
				}
			} else if rep.Err != nil || !rep.Valid || rep.Value != `{"turn":7,"started":true}` {
				t.Fatalf("rep=%+v", rep)
			}
			wantArgv(t, calls(), append(idleProbesArgv(), get, send))
		})
	}
}

func TestSendDoneRemoteIdentity(t *testing.T) {
	for _, target := range []transport.Target{{Machine: "m1", ID: "t7"}, {ID: "m1/t7"}} {
		rules := append(probeRules("m1", sendHelpOld, schemaDoc(`"idle","working"`, true, true), schemaDoc(`"idle","working"`, true, false)),
			fakecli.Rule{Argv: []string{"get", "m1/t7", "--json"}, Stdout: `{"state":"live","status":{"state":"done"}}`},
			fakecli.Rule{Argv: []string{"send", "--json", "--", "m1/t7", sendPrompt}, Stdout: `{"started":true}`})
		env, calls := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), target, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("target=%+v rep=%+v", target, rep)
		}
		wantArgv(t, calls(), [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}, {"schema", "--on", "m1", "--json"}, {"get", "m1/t7", "--json"}, {"send", "--json", "--", "m1/t7", sendPrompt}})
	}
}

func TestSendDoneGetAndSendShareDeadline(t *testing.T) {
	rules := append(probeRules("", sendHelpOld, schemaDoc(`"idle","working"`, true, false)),
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Delay: 450, Stdout: `{"state":"live","status":{"state":"done"}}`},
		fakecli.Rule{Argv: []string{"send", "--json", "--", "worker", sendPrompt}, Delay: 600, Stdout: `{"started":true}`})
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	start := time.Now()
	rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{Timeout: 800 * time.Millisecond})
	elapsed := time.Since(start)
	if rep.Err == nil || rep.Valid || elapsed > 1400*time.Millisecond {
		t.Fatalf("rep=%+v elapsed=%v, want cancellation within shared budget (get=450ms send=600ms timeout=800ms)", rep, elapsed)
	}
	wantArgv(t, calls(), append(idleProbesArgv(), []string{"get", "worker", "--json"}, []string{"send", "--json", "--", "worker", sendPrompt}))
}
