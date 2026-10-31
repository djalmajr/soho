package wait

// DJA-605 amendment: under transport=hc the `error` state word takes
// wait's existing provider-error path (the provider-cause wait file, the
// provider-error line and warning, the existing provider-error exit), the
// report still wins over a finished task, and the wait hints the hc path
// can reach name hc instead of herdr. The fixtures reuse the stage 2c
// hc wake fixture: the fake hc, the sentinel fake herdr and the fake
// clock.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// fakecliRuleGetWorker is the fake hc get rule for the worker, answering
// the given terminal record on every call.
func fakecliRuleGetWorker(record string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: record}
}

// fakecliRuleHcWaitAny answers every `hc wait` with turn-ended news; the
// pause never consumes the reason, so the content only needs to be a
// well-formed wait result.
func fakecliRuleHcWaitAny() fakecli.Rule {
	return fakecli.Rule{Argv: []string{"wait", "worker"}, ArgvPrefix: true, Stdout: hcWakeWaitTurn(6)}
}

// DJA-605 acceptance 1: an error worker without a status message takes the
// default cause.
func TestWaitHcErrorStateDefaultCause(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d out=%q stderr=%q, want the existing provider-error exit 14", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"provider-error"`) {
		t.Fatalf("out=%q, want the provider-error line", out)
	}
	if !strings.Contains(out, `"cause":"the assistant reported an error"`) {
		t.Fatalf("out=%q, want the default cause", out)
	}
	if !strings.Contains(stderr, ": the assistant reported an error") {
		t.Fatalf("stderr=%q, want the warning to carry the default cause", stderr)
	}
	if cause, ok := readWaitFile(f.sd, "worker", "provider-cause"); !ok || cause != "the assistant reported an error" {
		t.Fatalf("provider-cause=%q ok=%v, want the default cause", cause, ok)
	}
	f.assertNoHerdr(t)
}

// DJA-605 decision 2: a report already written decides as today — the wait
// ends done on the stable report, and the error is not reported over a
// finished task.
func TestWaitHcErrorStateReportStillWins(t *testing.T) {
	f := newHcWakeFixture(t, "1", "file")
	// report_working_max 0 makes the stable-report decision the
	// report path's own (done before any state read), so the outcome
	// shows the report deciding — not the wait's timeout deciding for it.
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"settled_grace":      {Value: "0"},
		"report_working_max": {Value: "0"},
	}}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
		fakecliRuleHcWaitAny(),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q, want a done wait (a report already written wins)", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"done"`) {
		t.Fatalf("out=%q, want the done line", out)
	}
	if strings.Contains(out, "provider-error") {
		t.Fatalf("out=%q, want no provider-error outcome over a finished task", out)
	}
	if cause, ok := readWaitFile(f.sd, "worker", "provider-cause"); ok {
		t.Fatalf("provider-cause=%q, want none (the report path decides)", cause)
	}
	f.assertNoHerdr(t)
}

// DJA-605 decision 4: the not-received line names hc under transport=hc.
func TestWaitHcNotReceivedHintNamesHc(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.not-received"), []byte("1 - /tmp/worker-brief.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeIdle7),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 15 {
		t.Fatalf("code=%d out=%q stderr=%q, want the not-received exit 15", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"not-received"`) {
		t.Fatalf("out=%q, want the not-received line", out)
	}
	if !strings.Contains(stderr, "prompt to 'worker' never reached it: read the terminal (hc read worker), then dispatch again") {
		t.Fatalf("stderr=%q, want the hc not-received hint", stderr)
	}
	f.assertNoHerdr(t)
}

// DJA-605 decision 4: the stuck warning's inspect hint names hc under
// transport=hc. The fake clock advances one minute per worker state read
// and stuck_warn_minutes is 1, so the second working poll warns.
func TestWaitHcStuckHintNamesHc(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"settled_grace":      {Value: "0"},
		"stuck_warn_minutes": {Value: "1"},
	}}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeWorking5),
		fakecliRuleHcWaitAny(),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 9 {
		t.Fatalf("code=%d out=%q stderr=%q, want the timeout exit (a working worker never settles)", code, out, stderr)
	}
	if !strings.Contains(stderr, "Inspect: hc read worker") {
		t.Fatalf("stderr=%q, want the hc stuck inspect hint", stderr)
	}
	if strings.Contains(stderr, "herdr agent read") {
		t.Fatalf("stderr=%q, want no herdr hint under transport=hc", stderr)
	}
	f.assertNoHerdr(t)
}
