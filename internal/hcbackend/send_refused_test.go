package hcbackend

// SOH-409: which `hc send` results prove that no byte reached the terminal.
// Only the guarded send's exit 10 (the --if status mismatch) does: hc refuses
// to type when the status differs from the guard. Every other failure, and
// every Busy that soho itself synthesizes, stays uncertain.

import (
	"context"
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
