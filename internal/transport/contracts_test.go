package transport

import (
	"context"
	"testing"
)

// memTransport is a small in-memory Transport used only by tests: it proves
// the contracts are usable by a consumer (it is assigned to the interface and
// driven through it) without a backend or a fake CLI. It keeps terminals in a
// map and records its side effects.
type memTransport struct {
	caller    Caller
	terminals map[string]Terminal
	sent      []string
	closed    []string
}

var _ Transport = (*memTransport)(nil)

func (m *memTransport) Caller() Caller { return m.caller }

func (m *memTransport) Current(ctx context.Context, call Call) Reply[Caller] {
	return Reply[Caller]{Value: m.caller, Valid: true}
}

func (m *memTransport) CheckCaller(ctx context.Context, call Call) Reply[CallerCheck] {
	return Reply[CallerCheck]{Value: CallerCheck{State: "verified", Pane: m.caller.PaneID}, Valid: true}
}

func (m *memTransport) List(ctx context.Context, scope Scope, call Call) Reply[[]Terminal] {
	out := make([]Terminal, 0, len(m.terminals))
	for _, t := range m.terminals {
		out = append(out, t)
	}
	return Reply[[]Terminal]{Value: out, Valid: true}
}

func (m *memTransport) Get(ctx context.Context, target Target, call Call) Reply[Terminal] {
	t, ok := m.terminals[target.ID]
	if !ok {
		return Reply[Terminal]{Err: &Failure{Kind: FailureNotFound, Message: "no such terminal"}}
	}
	return Reply[Terminal]{Value: t, Valid: true}
}

func (m *memTransport) Send(ctx context.Context, target Target, send Send, call Call) Reply[string] {
	m.sent = append(m.sent, send.Text)
	return Reply[string]{Value: "sent " + send.Text, Valid: true}
}

func (m *memTransport) Keys(ctx context.Context, target Target, keys string, pane bool, call Call) Reply[bool] {
	return Reply[bool]{Value: true, Valid: true}
}

func (m *memTransport) Read(ctx context.Context, target Target, read Read, call Call) Reply[string] {
	return Reply[string]{Value: "screen", Valid: true}
}

func (m *memTransport) Close(ctx context.Context, target Target, call Call) Reply[bool] {
	if _, ok := m.terminals[target.ID]; !ok {
		return Reply[bool]{Value: false, Valid: true}
	}
	delete(m.terminals, target.ID)
	m.closed = append(m.closed, target.ID)
	return Reply[bool]{Value: true, Valid: true}
}

// TestInMemoryFakeIsUsableByConsumer drives the interface through an
// in-memory implementation: the consumer sees only the interface type, calls
// it, and inspects the uniform Reply results.
func TestInMemoryFakeIsUsableByConsumer(t *testing.T) {
	ft := &memTransport{
		caller: Caller{Inside: true, PaneID: "p1", WorkspaceID: "w1"},
		terminals: map[string]Terminal{
			"a": {Target: Target{ID: "a"}, Name: "a", PaneID: "p1", State: "working"},
		},
	}

	var consumer Transport = ft // the consumer only sees the interface.

	if got := consumer.Caller(); !got.Inside || got.PaneID != "p1" || got.WorkspaceID != "w1" {
		t.Fatalf("Caller()=%#v", got)
	}
	if got := consumer.Current(context.Background(), Call{}); !got.Valid || got.Value.PaneID != "p1" {
		t.Fatalf("Current()=%#v", got)
	}
	if got := consumer.List(context.Background(), Scope{}, Call{}); !got.Valid || len(got.Value) != 1 || got.Value[0].State != "working" {
		t.Fatalf("List()=%#v", got)
	}
	if got := consumer.Get(context.Background(), Target{ID: "a"}, Call{}); !got.Valid || got.Value.Name != "a" {
		t.Fatalf("Get()=%#v", got)
	}
	// A missing target is a not-found failure, not a valid empty result.
	if got := consumer.Get(context.Background(), Target{ID: "nope"}, Call{}); got.Err == nil || got.Err.Kind != FailureNotFound || got.Valid {
		t.Fatalf("Get(missing)=%#v", got)
	}
	if got := consumer.Send(context.Background(), Target{ID: "a"}, Send{Text: "hi"}, Call{}); !got.Valid || got.Value != "sent hi" {
		t.Fatalf("Send()=%#v", got)
	}
	if got := consumer.Read(context.Background(), Target{ID: "a"}, Read{Source: "visible"}, Call{}); !got.Valid || got.Value != "screen" {
		t.Fatalf("Read()=%#v", got)
	}
	// Close removes the terminal; a second close reports false.
	if got := consumer.Close(context.Background(), Target{ID: "a"}, Call{}); !got.Value || !got.Valid {
		t.Fatalf("Close()=%#v", got)
	}
	if got := consumer.Close(context.Background(), Target{ID: "a"}, Call{}); got.Value {
		t.Fatalf("Close(again)=%#v", got)
	}
	if len(ft.sent) != 1 || ft.sent[0] != "hi" || len(ft.closed) != 1 || ft.closed[0] != "a" {
		t.Fatalf("side effects: sent=%#v closed=%#v", ft.sent, ft.closed)
	}
}

// TestFailureErrorContract checks the Failure error contract: Error renders
// the message, and Unwrap reaches the cause.
func TestFailureErrorContract(t *testing.T) {
	err := &Failure{Kind: FailureCancelled, Message: "deadline", Cause: context.DeadlineExceeded}
	if err.Error() != "deadline" {
		t.Fatalf("Error()=%q", err.Error())
	}
	if err.Unwrap() != context.DeadlineExceeded {
		t.Fatalf("Unwrap()=%v", err.Unwrap())
	}
	if got := (*Failure)(nil).Error(); got != "" {
		t.Fatalf("nil Error()=%q", got)
	}
	if got := (&Failure{}).Error(); got != "" {
		t.Fatalf("zero Error()=%q", got)
	}
	// With no message, the cause is rendered; with neither, the kind.
	if got := (&Failure{Kind: FailureUnsupported, Cause: context.Canceled}).Error(); got != "context canceled" {
		t.Fatalf("cause render=%q", got)
	}
	if got := (&Failure{Kind: FailureProtocol}).Error(); got != "protocol" {
		t.Fatalf("kind render=%q", got)
	}
}
