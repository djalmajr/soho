package hcbackend

import (
	"context"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// TestLaunchIsClampedToTheHerdrRule pins the timeout rule every hc launch
// runs under (DJA-604 stage 2b): the call's timeout when it names one,
// else 30s (the herdr default), nested as the context deadline the runner
// inherits. The swapped runner records the deadline of the context it is
// given; no binary is launched.
func TestLaunchIsClampedToTheHerdrRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		callTimeout time.Duration
		wantMin     time.Duration
		wantMax     time.Duration
	}{
		{name: "no call timeout: the 30s default", wantMin: 29 * time.Second, wantMax: 31 * time.Second},
		{name: "call timeout: the call's value", callTimeout: 5 * time.Second, wantMin: 4 * time.Second, wantMax: 6 * time.Second},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := platform.Env{"HC_TERMINAL_ID": "t1"}
			b := &backend{env: env, client: hc.New(env)}
			var deadline time.Time
			b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
				d, _ := ctx.Deadline()
				deadline = d
				zero := 0
				return platform.RunResult{Status: &zero, Stdout: `{}`}
			}
			_ = b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{Timeout: tc.callTimeout})
			if !deadline.After(time.Time{}) {
				t.Fatal("the launch ran without a deadline")
			}
			remaining := time.Until(deadline)
			if remaining < tc.wantMin || remaining > tc.wantMax {
				t.Fatalf("remaining %v outside [%v, %v]", remaining, tc.wantMin, tc.wantMax)
			}
		})
	}
}
