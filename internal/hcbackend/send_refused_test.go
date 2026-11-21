package hcbackend

// SOH-409 and SOH-466: which `hc send` results prove that no byte reached the terminal.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

func TestSendRefusedBeforeWrite(t *testing.T) {
	idleProbes := probeRules("", sendHelpOld, schemaDoc(`"idle","working"`, true, false))
	readyProbes := probeRules("", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, false))
	mismatch := `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`
	for _, tc := range []struct {
		name   string
		rules  []fakecli.Rule
		send   transport.Send
		kind   transport.FailureKind
		refuse bool
	}{
		{"guarded exit 10 under the idle guard", append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: idleSendArgv("worker", sendPrompt), Code: 10, Stderr: mismatch}), transport.Send{Text: sendPrompt}, transport.FailureBusy, true},
		{"guarded exit 10 under the ready guard", append(append([]fakecli.Rule{}, readyProbes...), fakecli.Rule{Argv: readySendArgv("worker", sendPrompt), Code: 10, Stderr: mismatch}), transport.Send{Text: sendPrompt}, transport.FailureBusy, true},
		{"exit 9 blocked", append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: idleSendArgv("worker", sendPrompt), Code: 9, Stderr: `{"error":{"kind":"blocked","message":"blocked"}}`}), transport.Send{Text: sendPrompt}, transport.FailureBlocked, false},
		{"exit 11 finished", append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: idleSendArgv("worker", sendPrompt), Code: 11, Stderr: `{"error":{"kind":"finished","message":"gone"}}`}), transport.Send{Text: sendPrompt}, transport.FailureNotFound, false},
		{"exit 1 unavailable after the probes", append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: idleSendArgv("worker", sendPrompt), Code: 1, Stderr: `{"error":{"kind":"unavailable","message":"core went away"}}`}), transport.Send{Text: sendPrompt}, transport.FailureUnavailable, false},
		{"exit 10 on the queue send (no guard)", []fakecli.Rule{{Argv: []string{"send", "--json", "--", "worker", sendPrompt}, Code: 10, Stderr: mismatch}}, transport.Send{Text: sendPrompt, Queue: true}, transport.FailureBusy, false},
		{"guard refusal (no proven guard) is a capability failure, never resent", probeRules("", sendHelpOld, schemaDoc(`"working"`, true, false)), transport.Send{Text: sendPrompt}, transport.FailureProtocol, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := fakeHc(t, tc.rules)
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Send(context.Background(), transport.Target{ID: "worker"}, tc.send, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != tc.kind {
				t.Fatalf("rep=%+v, want kind %s", rep, tc.kind)
			}
			if got := SendRefusedBeforeWrite(rep.Err); got != tc.refuse {
				t.Fatalf("SendRefusedBeforeWrite=%v, want %v (err=%+v)", got, tc.refuse, rep.Err)
			}
		})
	}
	t.Run("nil and failures soho builds itself are not proof", func(t *testing.T) {
		if SendRefusedBeforeWrite(nil) {
			t.Fatal("nil failure must not prove anything")
		}
		for _, f := range []*transport.Failure{
			{Kind: transport.FailureBusy, Message: "worker worker is still working after the idle wait; the send was not retried"},
			{Kind: transport.FailureBusy, Message: "worker worker is not idle", Cause: errors.New("not an hc exit")},
			{Kind: transport.FailureBusy, Message: "worker worker is not idle", Cause: &hc.Error{Exit: hc.ExitBlocked}},
			{Kind: transport.FailureUnavailable, Message: "x", Cause: &hc.Error{Exit: hc.ExitStatusMismatch}},
		} {
			if SendRefusedBeforeWrite(f) {
				t.Fatalf("%+v must not prove a refusal before the write", f)
			}
		}
	})
}

func TestSendStartingRefusalBeforeWrite(t *testing.T) {
	const unsettled = "t7 is still starting: its output has not settled"
	const noInput = "nothing was typed; check hc read t7, then retry"
	const resolved = `{"address":"t7","name":"worker","state":"live","status":{"state":"idle"}}`
	for _, tc := range []struct {
		name    string
		exit    int
		kind    string
		message string
		hint    string
		lookup  string
		probe   bool
		refuse  bool
	}{
		{name: "structured hint", exit: 5, kind: "unavailable", message: unsettled, hint: noInput, refuse: true},
		{name: "inline hint", exit: 5, kind: "unavailable", message: unsettled + " (" + noInput + ")", refuse: true},
		{name: "record does not bind selector", exit: 5, kind: "unavailable", message: unsettled, hint: noInput, lookup: `{"address":"t7","name":"other","state":"live"}`},
		{name: "finished record cannot bind an alias", exit: 5, kind: "unavailable", message: unsettled, hint: noInput, lookup: `{"address":"t7","name":"worker","state":"finished"}`},
		{name: "unqualified selector cannot bind a remote record", exit: 5, kind: "unavailable", message: "lab/t7 is still starting: its output has not settled", hint: "nothing was typed", lookup: `{"address":"lab/t7","name":"worker","state":"live"}`},
		{name: "missing no-write proof", exit: 5, kind: "unavailable", message: unsettled},
		{name: "missing startup refusal", exit: 5, kind: "unavailable", message: "core went away", hint: noInput},
		{name: "other kind", exit: 5, kind: "command", message: unsettled, hint: noInput},
		{name: "other exit", exit: 1, kind: "unavailable", message: unsettled, hint: noInput},
		{name: "quoted no-write phrase", exit: 5, kind: "unavailable", message: unsettled, hint: "cannot confirm that " + noInput},
		{name: "guard probe error", exit: 5, kind: "unavailable", message: unsettled, hint: noInput, probe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := json.Marshal(map[string]any{"error": map[string]string{"kind": tc.kind, "message": tc.message, "hint": tc.hint}})
			if err != nil {
				t.Fatal(err)
			}
			argv := idleSendArgv("worker", sendPrompt)
			rules := probeRules("", sendHelpOld, schemaDoc(`"idle","working"`, true, false))
			if tc.probe {
				argv = []string{"schema", "--json"}
				rules = rules[:1] // only send --help succeeds
			} else {
				lookup := tc.lookup
				if lookup == "" {
					lookup = resolved
				}
				rules = append(rules, fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: lookup})
			}
			rules = append(rules, fakecli.Rule{Argv: argv, Code: tc.exit, Stderr: string(stderr)})
			env, calls := fakeHc(t, rules)
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
				t.Fatalf("rep=%+v, want unavailable", rep)
			}
			var cause *hc.Error
			if !errors.As(rep.Err.Cause, &cause) || cause.Exit != tc.exit || cause.Kind != tc.kind || cause.Message != tc.message || cause.Hint != tc.hint {
				t.Fatalf("cause=%+v, want the original hc error", cause)
			}
			if tc.probe {
				for _, call := range calls() {
					if len(call.Argv) > 1 && call.Argv[0] == "send" && call.Argv[1] != "--help" {
						t.Fatalf("failed guard probe issued a send: %q", call.Argv)
					}
				}
			} else if rep.Err.Message != cause.Error() || rep.Err.Cause.Error() != cause.Error() || rep.Err.Code != "" {
				t.Fatalf("transport failure changed the hc diagnostic: %+v", rep.Err)
			}
			if got := SendRefusedBeforeWrite(rep.Err); got != tc.refuse {
				t.Fatalf("SendRefusedBeforeWrite=%v, want %v (err=%+v)", got, tc.refuse, rep.Err)
			}
		})
	}
}
