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

// TestSessionMemoizationEdges pins the three edges of the failure memo: a
// cancelled request (with no deadline) does not memoize its failure; a
// request that launched with the full default budget memoizes its failure
// even when the read itself took time; and a caller never waits on another
// caller's read in flight.
func TestSessionMemoizationEdges(t *testing.T) {
	failing := func(gets *int) func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		return func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			*gets++
			if *gets == 1 {
				exit := 1
				return platform.RunResult{Status: &exit, Stderr: `{"error":{"kind":"internal","message":"core down"}}`}
			}
			zero := 0
			return platform.RunResult{Status: &zero, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s1"},"terminals":[]}`}
		}
	}

	t.Run("a cancelled request's failure is not memoized", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t1"}
		b := &backend{env: env, client: hc.New(env)}
		var gets int
		b.client.Launch = failing(&gets)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := b.session(ctx, transport.Call{}); err == nil {
			t.Fatal("the cancelled read succeeded, want its failure")
		}
		if session := b.Caller().WorkspaceID; session != "s1" || gets != 2 {
			t.Fatalf("later Caller session = %q after %d gets, want s1 read again", session, gets)
		}
	})

	t.Run("a full-budget failure is memoized even when the read took time", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t1"}
		b := &backend{env: env, client: hc.New(env)}
		var gets int
		inner := failing(&gets)
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			time.Sleep(40 * time.Millisecond)
			return inner(ctx, env, args...)
		}
		ctx, cancel := context.WithTimeout(context.Background(), hcTimeoutDefault+20*time.Millisecond)
		defer cancel()
		if _, err := b.session(ctx, transport.Call{}); err == nil {
			t.Fatal("the first read succeeded, want its failure")
		}
		if _, err := b.session(context.Background(), transport.Call{}); err == nil || gets != 1 {
			t.Fatalf("second read err = %v after %d gets, want the memoized failure and 1 get", err, gets)
		}
	})

	t.Run("a cancelled caller does not wait on another caller's read", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t1"}
		b := &backend{env: env, client: hc.New(env)}
		release := make(chan struct{})
		entered := make(chan struct{}, 2)
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			exit := 124
			return platform.RunResult{Status: &exit, Stderr: `{"error":{"kind":"timeout","message":"timed out"}}`}
		}
		first := make(chan struct{})
		go func() {
			defer close(first)
			_, _ = b.session(context.Background(), transport.Call{})
		}()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		second := make(chan struct{})
		go func() {
			defer close(second)
			_ = b.List(ctx, transport.Scope{}, transport.Call{})
		}()
		cancel()
		select {
		case <-second:
		case <-time.After(2 * time.Second):
			t.Error("List waited on the other caller's session read after its own cancellation")
		}
		close(release)
		<-first
		<-second
	})
}
