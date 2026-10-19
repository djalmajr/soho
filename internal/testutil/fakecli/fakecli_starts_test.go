package fakecli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// Mutation captured: taking the time after the rule's delay, or not recording
// it per call, breaks the origin the clamp tests measure from. The first call
// runs 120 ms, so the second one must start at least that long after it.
func TestCallStartsRecordTheStartOfEachCallInTheLogOrder(t *testing.T) {
	dir := t.TempDir()
	rules := []Rule{
		{Argv: []string{"first"}, Delay: 120},
		{Argv: []string{"second"}},
	}
	if _, err := Install(t, dir, "probe", rules); err != nil {
		t.Fatal(err)
	}
	env := waitFileEnvFor(t, dir)
	before := time.Now()
	for _, arg := range []string{"first", "second"} {
		r := platform.RunCli("probe", []string{arg}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 30000})
		if r.Status == nil || *r.Status != 0 {
			t.Fatalf("call %s: %+v", arg, r)
		}
	}
	after := time.Now()
	log := filepath.Join(dir, "probe.calls.jsonl")
	calls, err := ReadCalls(log)
	if err != nil {
		t.Fatal(err)
	}
	starts, err := ReadCallStarts(log)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(starts) != len(calls) {
		t.Fatalf("calls = %d, starts = %d, want two of each", len(calls), len(starts))
	}
	if calls[0].Argv[0] != "first" || calls[1].Argv[0] != "second" {
		t.Fatalf("log order = %v, %v, want first then second", calls[0].Argv, calls[1].Argv)
	}
	if !starts[0].Before(starts[1]) {
		t.Errorf("starts = %v, %v, want increasing", starts[0], starts[1])
	}
	if gap := starts[1].Sub(starts[0]); gap < 120*time.Millisecond {
		t.Errorf("second call started %v after the first, want at least its 120 ms delay", gap)
	}
	if starts[0].Before(before) || starts[1].After(after) {
		t.Errorf("starts %v, %v outside the run window %v .. %v", starts[0], starts[1], before, after)
	}
}

// The reader reports a missing starts file instead of returning no starts.
func TestReadCallStartsReportsAMissingFile(t *testing.T) {
	if starts, err := ReadCallStarts(filepath.Join(t.TempDir(), "none.calls.jsonl")); err == nil {
		t.Fatalf("starts = %v, want an error", starts)
	}
}
