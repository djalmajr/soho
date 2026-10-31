package wait

// DJA-605 review fixes: the hc status message is redacted and made one
// printable line before it becomes a provider cause; an expired wait budget
// never becomes a new wake; and an hc error after a written report settles
// as done does, with the default report_working_max.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func TestWaitHcErrorCauseIsRedactedAndOneLine(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	record := `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"error","msg":"auth failed Bearer SYNTHETICTOKEN0123456789abcdef\n\u001b[31mnext line"}}`
	f.installHc(t, fakecliRuleGetWorker(record))
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d out=%q stderr=%q, want the provider-error exit 14", code, out, stderr)
	}
	cause, ok := readWaitFile(f.sd, "worker", "provider-cause")
	if !ok {
		t.Fatal("no provider-cause written")
	}
	for name, text := range map[string]string{"provider-cause": cause, "stdout": out, "stderr": stderr} {
		if strings.Contains(text, "SYNTHETICTOKEN") {
			t.Fatalf("%s keeps the synthetic secret: %q", name, text)
		}
		if strings.ContainsRune(text, 0x1b) {
			t.Fatalf("%s keeps an ESC byte: %q", name, text)
		}
	}
	if strings.Contains(cause, "\n") {
		t.Fatalf("provider-cause %q, want one line", cause)
	}
	if !strings.Contains(cause, "auth failed Bearer [redacted]") {
		t.Fatalf("provider-cause %q, want the redacted message", cause)
	}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "next line") || strings.HasPrefix(line, "[31mnext line") {
			t.Fatalf("stderr %q: the message injected a line of its own", stderr)
		}
	}
	f.assertNoHerdr(t)
}

// countingWaker is a runtime that only wakes; pauseForWake calls nothing
// else on it.
type countingWaker struct {
	transport.Transport
	wakes  int
	bounds []time.Duration
}

func (w *countingWaker) Wake(ctx context.Context, target transport.Target, max time.Duration, call transport.Call) transport.Reply[string] {
	w.wakes++
	w.bounds = append(w.bounds, max)
	return transport.Reply[string]{Valid: true}
}

func TestPauseForWakeNeverWakesOnAnExpiredBudget(t *testing.T) {
	env := platform.Env{"SOHO_TRANSPORT": "hc", "SOHO_WAIT_POLL_MS": "1"}
	t.Run("expired budget: no wake", func(t *testing.T) {
		w := &countingWaker{}
		pauseForWake(transport.Services{Runtime: w}, env, platform.Now().Add(-time.Second), 1000, []string{"worker"})
		if w.wakes != 0 {
			t.Fatalf("wakes = %d with an expired budget, want 0", w.wakes)
		}
	})
	t.Run("budget left: one wake, capped at 10s", func(t *testing.T) {
		w := &countingWaker{}
		pauseForWake(transport.Services{Runtime: w}, env, platform.Now().Add(time.Minute), 60000, []string{"worker"})
		if w.wakes != 1 || w.bounds[0] > 10*time.Second || w.bounds[0] <= 0 {
			t.Fatalf("wakes = %d bounds = %v, want one wake within (0, 10s]", w.wakes, w.bounds)
		}
	})
}

func TestWaitHcErrorAfterReportSettlesWithTheDefaultWorkingMax(t *testing.T) {
	f := newHcWakeFixture(t, "1", "file")
	// Only the settle grace is off: report_working_max keeps its default,
	// so the stable report is decided by the state read, where an hc
	// error must count as the end of the turn.
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"settled_grace": {Value: "0"},
	}}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
		fakecliRuleHcWaitAny(),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 0 || !strings.Contains(out, `"status":"done"`) {
		t.Fatalf("code=%d out=%q stderr=%q, want done on the written report", code, out, stderr)
	}
	if strings.Contains(out, "provider-error") {
		t.Fatalf("out=%q, want no provider-error over a finished task", out)
	}
	f.assertNoHerdr(t)
}
