package hcbackend

// DJA-605 stage 2c: the Waker capability — one `hc wait` on the terminal,
// the wake reason as hc reports it, "" on the timeout check-in, and the
// error mapping the backend uses for every launch.

import (
	"context"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	wakeTurnEnded  = `{"terminal":"t7","ended":true,"reason":"status","turn":6,"children":[]}`
	wakeTurnExited = `{"terminal":"t7","ended":true,"reason":"exit","turn":7,"children":[],"exit_code":1}`
	wakeCheckIn    = `{"terminal":"t7","ended":false,"reason":"timeout","turn":5,"children":[]}`
)

func TestBackendWakeArgv(t *testing.T) {
	for _, tc := range []struct {
		name     string
		max      time.Duration
		wantSecs string
	}{
		{name: "10s", max: 10 * time.Second, wantSecs: "10"},
		{name: "5s", max: 5 * time.Second, wantSecs: "5"},
		{name: "5.5s rounds up to 6", max: 5500 * time.Millisecond, wantSecs: "6"},
		{name: "sub-second rounds up to 1", max: 400 * time.Millisecond, wantSecs: "1"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, calls := fakeHc(t, []fakecli.Rule{
				{Argv: []string{"wait", "t7", "--timeout", tc.wantSecs, "--json"}, Stdout: wakeCheckIn},
			})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, tc.max, transport.Call{})
			if rep.Err != nil || !rep.Valid {
				t.Fatalf("rep=%+v, want a valid check-in", rep)
			}
			wantArgv(t, calls(), [][]string{{"wait", "t7", "--timeout", tc.wantSecs, "--json"}})
		})
	}
}

// TestBackendWakeArgvMachinePrefixed pins the remote address the backend
// encodes for a machine-prefixed target.
func TestBackendWakeArgvMachinePrefixed(t *testing.T) {
	env, calls := fakeHc(t, []fakecli.Rule{
		{Argv: []string{"wait", "m1/t7", "--timeout", "10", "--json"}, Stdout: wakeCheckIn},
	})
	b := &backend{env: env, client: hc.New(env)}
	if rep := b.Wake(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, 10*time.Second, transport.Call{}); rep.Err != nil {
		t.Fatalf("rep=%+v, want valid", rep)
	}
	wantArgv(t, calls(), [][]string{{"wait", "m1/t7", "--timeout", "10", "--json"}})
}

// TestBackendWakeProcessBudget pins the launch's deadline: max + 5 s,
// nested as the context deadline the runner inherits (no binary is
// launched; the swapped runner records the deadline).
func TestBackendWakeProcessBudget(t *testing.T) {
	env := platform.Env{"HC_TERMINAL_ID": "t1"}
	b := &backend{env: env, client: hc.New(env)}
	var deadline time.Time
	b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		d, _ := ctx.Deadline()
		deadline = d
		zero := 0
		return platform.RunResult{Status: &zero, Stdout: wakeCheckIn}
	}
	rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep=%+v, want a valid check-in", rep)
	}
	remaining := time.Until(deadline)
	if remaining < 14*time.Second || remaining > 16*time.Second {
		t.Fatalf("remaining %v, want the 10s bound plus the 5s slack", remaining)
	}
}

// TestBackendWakeReasons pins the reply's Value: the wake reason as hc
// reports it, and "" when the bound expired with no news.
func TestBackendWakeReasons(t *testing.T) {
	t.Run("the turn ended: the status reason", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"wait", "t7", "--timeout", "10", "--json"}, Stdout: wakeTurnEnded},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "status" {
			t.Fatalf("rep=%+v, want reason status", rep)
		}
	})
	t.Run("the terminal exited: the exit reason", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"wait", "t7", "--timeout", "10", "--json"}, Stdout: wakeTurnExited},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "exit" {
			t.Fatalf("rep=%+v, want reason exit", rep)
		}
	})
	t.Run("the timeout check-in: no news, not an error", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"wait", "t7", "--timeout", "10", "--json"}, Stdout: wakeCheckIn},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "" {
			t.Fatalf("rep=%+v, want the empty reason with no error", rep)
		}
	})
}

// TestBackendWakeErrors pins the failure mapping: the not-found exit,
// every other failed launch, and the stdout that is not a wait result.
func TestBackendWakeErrors(t *testing.T) {
	t.Run("the terminal is gone: the not-found exit", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"wait", "t7", "--timeout", "10", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("rep=%+v, want FailureNotFound", rep)
		}
	})
	t.Run("another failed launch: unavailable with the cause", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"wait", "t7", "--timeout", "10", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"boom"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep=%+v, want FailureUnavailable", rep)
		}
	})
	t.Run("an exit-0 stdout without a wait result: protocol", func(t *testing.T) {
		for _, stdout := range []string{`{}`, `{"ended":true}`, `[{"ended":true,"reason":"status"}]`, `not json`} {
			env, _ := fakeHc(t, []fakecli.Rule{
				{Argv: []string{"wait", "t7", "--timeout", "10", "--json"}, Stdout: stdout},
			})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, 10*time.Second, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
				t.Fatalf("stdout %q: rep=%+v, want FailureProtocol", stdout, rep)
			}
		}
	})
	t.Run("a non-positive bound is refused before any launch", func(t *testing.T) {
		env := platform.Env{"HC_TERMINAL_ID": "t1"}
		b := &backend{env: env, client: hc.New(env)}
		launches := 0
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			launches++
			zero := 0
			return platform.RunResult{Status: &zero, Stdout: wakeCheckIn}
		}
		for _, max := range []time.Duration{0, -time.Second} {
			rep := b.Wake(context.Background(), transport.Target{ID: "t7"}, max, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
				t.Fatalf("max %v: rep=%+v, want the refusal", max, rep)
			}
		}
		if launches != 0 {
			t.Fatalf("%d launch(es), want none", launches)
		}
	})
}
