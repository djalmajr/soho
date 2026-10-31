package hcbackend

import (
	"context"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// TestNilContextIsTheBackground pins the nil-context boundary: transport
// consumers may pass a nil context (core.Orphans lists with one), and the
// launch then runs under the default budget instead of panicking.
func TestNilContextIsTheBackground(t *testing.T) {
	env := platform.Env{"HC_TERMINAL_ID": "t1"}
	b := &backend{env: env, client: hc.New(env)}
	var launches int
	b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		launches++
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("launch %v has no deadline", args)
		}
		zero := 0
		return platform.RunResult{Status: &zero, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s1"},"terminals":[]}`}
	}
	_ = b.List(nil, transport.Scope{}, transport.Call{})
	_ = b.Get(nil, transport.Target{ID: "t7"}, transport.Call{})
	if launches != 3 {
		t.Fatalf("launches = %d, want the session get, the ls and the get", launches)
	}
}

// TestListSessionHonorsTheRequestBudget pins the session read inside the
// request's budget: the first List's session get runs under the parent's
// earlier deadline, not a fresh 30s, and a failure under that shorter
// budget is not memoized, so a later read with the full budget reads again.
func TestListSessionHonorsTheRequestBudget(t *testing.T) {
	env := platform.Env{"HC_TERMINAL_ID": "t1"}
	b := &backend{env: env, client: hc.New(env)}
	var gets int
	b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		deadline, _ := ctx.Deadline()
		if args[0] == "get" {
			gets++
			if gets == 1 {
				if remaining := time.Until(deadline); remaining > time.Second {
					t.Errorf("session get remaining = %s, want at most the parent's 100ms", remaining)
				}
				exit := 124
				return platform.RunResult{Status: &exit, Stderr: `{"error":{"kind":"timeout","message":"timed out"}}`}
			}
		}
		zero := 0
		return platform.RunResult{Status: &zero, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s1"},"terminals":[]}`}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if reply := b.List(ctx, transport.Scope{}, transport.Call{}); reply.Err == nil {
		t.Fatalf("List under a failed session read = %+v, want an error", reply)
	}
	if session := b.Caller().WorkspaceID; session != "s1" {
		t.Fatalf("Caller session = %q, want s1 read again with the full budget", session)
	}
	if gets != 2 {
		t.Fatalf("session gets = %d, want 2 (the short-budget failure is not memoized)", gets)
	}
	_ = b.Caller()
	if gets != 2 {
		t.Fatalf("session gets = %d after a memoized success, want 2", gets)
	}
}
