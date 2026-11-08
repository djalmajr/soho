package hcbackend

import (
	"context"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func TestStartReadinessParentDeadlinePriority(t *testing.T) {
	zero, one, three := 0, 1, 3
	for _, tc := range []struct {
		name   string
		result platform.RunResult
	}{
		{"idle", platform.RunResult{Status: &zero, Stdout: startReadyIdle}},
		{"not_found", platform.RunResult{Status: &three, Stderr: `{"error":{"kind":"not_found","message":"no such terminal t7"}}`}},
		{"command_error", platform.RunResult{Status: &one}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			entered := false
			b, calls := startReadinessRunner(t, func(getCtx context.Context) platform.RunResult {
				entered = true
				if ctx.Err() != nil || getCtx.Err() != nil {
					t.Fatal("caller deadline expired before the controlled readiness get")
				}
				<-getCtx.Done()
				if ctx.Err() != context.DeadlineExceeded || getCtx.Err() != context.DeadlineExceeded {
					t.Fatal("the caller and child must both report their actual inherited deadline")
				}
				return tc.result
			})
			rep := b.Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
			if !entered {
				t.Fatal("the readiness boundary was not exercised")
			}
			wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCancelled || rep.Valid || rep.Value != "t7" {
				t.Fatalf("parent deadline was mistaken for a startup result: %+v", rep)
			}
			if rep.Err.Message != context.DeadlineExceeded.Error() || rep.Err.Cause != context.DeadlineExceeded {
				t.Fatalf("parent deadline message/cause lost: %+v", rep.Err)
			}
		})
	}
}
