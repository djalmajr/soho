package peer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionref"
	"github.com/djalmajr/soho/internal/transcript"
	"github.com/djalmajr/soho/internal/transport"
)

// ReadScreen is a test-only export so a test can time readScreen at the phase
// where its budget starts.
var ReadScreen = readScreen

// SendBusyBudgetMS is a test-only export of the busy wait's budget after an
// agent_not_ready wait (DJA-377).
var SendBusyBudgetMS = sendBusyBudgetMS

// ReceiptBudgetCall records the actual budget handed to each validation call.
type ReceiptBudgetCall struct {
	Operation string
	Timeout   time.Duration
}

type receiptBudgetLegacy struct {
	transport.LegacyTransport
	agent string
	calls *[]ReceiptBudgetCall
}

func (f receiptBudgetLegacy) AgentGetMachineRaw(_ context.Context, _ transport.Target, call transport.Call) transport.Reply[string] {
	*f.calls = append(*f.calls, ReceiptBudgetCall{"binding", call.Timeout})
	zero := 0
	return transport.Reply[string]{Process: transport.ProcessResult{Status: &zero, Stdout: f.agent}}
}

// ReceiptBudgetProbe exercises the real receipt validation with an expired
// window and captures the transport options before scheduler latency matters.
func ReceiptBudgetProbe(t *testing.T, home, session, journal, record, closing, id, visible, agent string) (bool, []ReceiptBudgetCall) {
	t.Helper()
	reader := transcript.NewCodexReceipt(home, session, closing)
	if reader == nil {
		t.Fatal("receipt budget probe could not arm journal")
	}
	defer reader.Close()
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(record + "\n")
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append receipt probe record: write=%v close=%v", err, closeErr)
	}
	var calls []ReceiptBudgetCall
	legacy := receiptBudgetLegacy{agent: agent, calls: &calls}
	target := resolvedTarget{Machine: sessionref.LocalMachine, TargetArg: "w0test:p0a", Kind: "codex"}
	before := agentInfo{OK: true, Status: "working", PaneID: target.TargetArg, Kind: "codex", SessionKind: "id", SessionAgent: "codex", SessionValue: session}
	read := func(source string, _ int, timeout int) (string, string) {
		calls = append(calls, ReceiptBudgetCall{source, time.Duration(timeout) * time.Millisecond})
		return visible, ""
	}
	ok := codexReceiptTaken(reader, before, transport.Services{Legacy: legacy}, target, visible, id, before.Status, 0, time.Now().Add(-time.Second), read)
	return ok, calls
}

// NotReadyRetryOutOfBudget is a test-only export of the agent_not_ready
// retry's budget check, built from the fields it reads (DJA-377).
func NotReadyRetryOutOfBudget(ok bool, code string, timedOut bool, deadline, now time.Time) bool {
	return notReadyRetryOutOfBudget(resolvedTarget{OK: ok, Code: code, TimedOut: timedOut}, deadline, now)
}

// ResolveTargetWithinTimedOut is a test-only export: whether a resolution
// bounded by timeoutMS reports its get as timed out.
func ResolveTargetWithinTimedOut(target string, env platform.Env, timeoutMS int) bool {
	return resolveTargetWithin(target, env, timeoutMS).TimedOut
}
