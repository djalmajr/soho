package job

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// finishing_step_clamp_test.go pins that the finishing clamp is applied to
// each real subprocess of a multi-command GitSync step, not once per step:
// a later command gets only what is left of the step when it starts. On
// Windows every scenario duration is multiplied by clampScale, and the
// step-consuming command is shortened by spawnAllowance: opening a fake
// process there costs close to a second, and each scenario opens two.

// clampSlack is the slack over a step's bound the regressions allow: the
// platform WaitDelay is 1 s per killed command, plus 0.5 s for the process
// spawn, the git push and the store writes around it. It scales with
// clampScale like the scenario durations.
const clampSlack = 1500 * time.Millisecond

// clampScale is the Windows factor of the scenario durations: the step,
// the command delays and the slack are all multiplied by it, keeping the
// proportions between them, and the assertions scale with them. On Unix
// the durations are unchanged.
var clampScale = 1

// spawnAllowance is the Windows margin of the step-consuming command: it
// runs for its scenario proportion of the scaled step minus the allowance
// (0.6 x the scaled step for the 2 s scenarios, 0.8 for the 5 s one), so
// the next command still opens inside the step. It is 3 s on Windows and
// zero on Unix, where the durations are unchanged.
var spawnAllowance = 0 * time.Second

func init() {
	if runtime.GOOS == "windows" {
		clampScale = 4
		spawnAllowance = 3 * time.Second
	}
}

// scaledDuration scales a scenario duration by clampScale.
func scaledDuration(d time.Duration) time.Duration {
	return time.Duration(int64(d) * int64(clampScale))
}

// scaledMS scales a fake command delay, in milliseconds, by clampScale.
func scaledMS(ms int) int { return ms * clampScale }

// stepConsumingDelay is the delay of the command that consumes the step:
// its scenario proportion of the step, scaled, minus the spawn allowance
// (zero on Unix: the durations are unchanged there).
func stepConsumingDelay(ms int) int { return scaledMS(ms) - int(spawnAllowance/time.Millisecond) }

// stepDeadlineLimit models the supervisor's finCap for a GitSync used on its
// own: every timeout is the default or what is left of the step, never below
// one millisecond and rounded up to whole milliseconds.
func stepDeadlineLimit(deadline time.Time) func(time.Duration) time.Duration {
	return func(d time.Duration) time.Duration {
		remaining := time.Until(deadline)
		if remaining < time.Millisecond {
			remaining = time.Millisecond
		}
		remaining = (remaining + time.Millisecond - 1) / time.Millisecond * time.Millisecond
		if remaining < d {
			return remaining
		}
		return d
	}
}

// firstGhStart returns when the fake gh began its first call, as the fake
// itself recorded it. The time is the call's own start, so it does not depend
// on when the test reads it, however late; the test fails when no gh call
// was recorded, or when the start record does not hold one start per call.
func firstGhStart(t *testing.T, f *supGitFixture) time.Time {
	t.Helper()
	log := filepath.Join(f.job.base.fakeDir, "gh.calls.jsonl")
	calls, err := fakecli.ReadCalls(log)
	if err != nil {
		t.Fatalf("read the gh calls: %v", err)
	}
	starts, err := fakecli.ReadCallStarts(log)
	if err != nil {
		t.Fatalf("read the gh call starts: %v", err)
	}
	first, err := alignedFirstStart(len(calls), starts)
	if err != nil {
		t.Fatal(err)
	}
	return first
}

// alignedFirstStart is the first call's start when the record holds exactly
// one start per logged call. A record that lost a write (say the first one)
// would otherwise move the origin to a later call and shorten the measure.
func alignedFirstStart(calls int, starts []time.Time) (time.Time, error) {
	if calls == 0 {
		return time.Time{}, fmt.Errorf("no gh call was recorded")
	}
	if len(starts) != calls {
		return time.Time{}, fmt.Errorf("the gh start record holds %d starts for %d calls; a lost write would move the origin", len(starts), calls)
	}
	return starts[0], nil
}

// frictionTimes wraps the fixture's friction sink so it also keeps when each
// line arrived, on the supervisor's own call.
func frictionTimes(f *supGitFixture) func() []time.Time {
	var mu sync.Mutex
	var at []time.Time
	sink := f.sup.Friction
	f.sup.Friction = func(message string) {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		sink(message)
	}
	return func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), at...)
	}
}

// firstAtOrAfter is the earliest time of times at or after from.
func firstAtOrAfter(times []time.Time, from time.Time) (time.Time, error) {
	var first time.Time
	for _, at := range times {
		if !at.Before(from) && (first.IsZero() || at.Before(first)) {
			first = at
		}
	}
	if first.IsZero() {
		return time.Time{}, fmt.Errorf("no friction line came after the first gh call (%d lines in all)", len(times))
	}
	return first, nil
}

// A slow first gh command followed by a hung second one stays inside the gh
// step: `gh pr create` receives the remainder at its own start, not the whole
// step again.
func TestFinishingGhStepBoundsTheSecondCommandByTheRemainder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		step  time.Duration
		delay int
		// lateRead holds the test back after the job ends, before it reads
		// the recorded start: the measure must not depend on that delay.
		lateRead time.Duration
	}{
		{"step 2s, list 1.8s", 2 * time.Second, 1800, 0},
		{"step 5s, list 4s", 5 * time.Second, 4000, 0},
		{"step 2s, list 1.8s, start read 4s after the job", 2 * time.Second, 1800, 4 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := scaledDuration(tc.step)
			delay := stepConsumingDelay(tc.delay)
			rules := []fakecli.Rule{
				{Argv: ghListArgv(), Stdout: "[]\n", Delay: delay},
				{Argv: ghCreateArgv("feat: first", supGitPRBody), Stdout: gitSyncPRURL + "\n", Delay: 60000},
			}
			f := newSupGitFixture(t, rules)
			f.job.commitIn(t, "feat: first")
			finishingRealClock(f, finishingLimits{Budget: 60 * time.Second, Git: 30 * time.Second, GH: step})
			frictionAt := frictionTimes(f)
			exit, err, elapsed := finishDone(t, f)
			time.Sleep(tc.lateRead)
			// The bound is the gh step, so it is measured over the step
			// alone. It starts at the first gh call, as the fake recorded
			// it: the final push before it runs real git and takes as long
			// as the runner lets it. It ends at the first friction line
			// after that start, which the supervisor writes as soon as the
			// cut `gh pr create` returns: the release, the cleanup and the
			// report written after it are not part of the step.
			ghStart := firstGhStart(t, f)
			stepEnd, ferr := firstAtOrAfter(frictionAt(), ghStart)
			if ferr != nil {
				t.Fatal(ferr)
			}
			ghElapsed := stepEnd.Sub(ghStart)
			t.Logf("gh step %v, list %dms, create hangs: terminal in %v, step over %v after the first gh call", step, delay, elapsed, ghElapsed)
			if ghElapsed > step+scaledDuration(clampSlack) {
				t.Errorf("gh step took %v from the first gh call, want at most the %v gh step + %v", ghElapsed, step, scaledDuration(clampSlack))
			}
			requireBudgetFailed(t, f, exit, err, "the draft pull request")
			if got := len(f.job.base.ghCalls()); got != 2 {
				t.Errorf("gh calls = %d, want list and create (the scenario never reached create otherwise)", got)
			}
		})
	}
}

// A slow early checkpoint command followed by a hung commit stays inside the
// step: `git commit` receives the remainder at its own start. The checkpoint
// runs on its own GitSync whose Limit models a step deadline, with a fake git
// that delays `add`, reports a staged diff and hangs on `commit`.
func TestCheckpointBoundsTheLaterCommandsByTheStepRemainder(t *testing.T) {
	for _, tc := range []struct {
		// The 2 s cases run the add for 60% of the step (1.2 s, down from
		// 1.8 s): opening the fake git and running the add can take close
		// to half a second under the race detector and CI load, and the old
		// 200 ms remainder let the step end before `diff` (or `commit`)
		// opened inside it. 60% keeps the intent — the add uses most of the
		// step, and diff and commit run on what is left — with a remainder
		// wider than that start cost; the 5 s case keeps its 4 s add (80%),
		// whose 1 s remainder already is.
		name  string
		step  time.Duration
		delay int
		// hangDiff hangs `diff --cached` (and lets `commit` return at once)
		// instead of hanging `commit`.
		hangDiff bool
	}{
		{"step 2s, add 1.2s, commit hangs", 2 * time.Second, 1200, false},
		{"step 5s, add 4s, commit hangs", 5 * time.Second, 4000, false},
		{"step 2s, add 1.2s, diff hangs", 2 * time.Second, 1200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := scaledDuration(tc.step)
			delay := stepConsumingDelay(tc.delay)
			dir := t.TempDir()
			fakeDir := t.TempDir()
			diffRule := fakecli.Rule{Argv: []string{"-C", dir, "diff", "--cached", "--quiet"}, Code: 1}
			commitRule := fakecli.Rule{Argv: []string{"-C", dir, "commit", "--no-verify", "-m", "wip(job): checkpoint"}}
			if tc.hangDiff {
				diffRule.Delay = 60000
			} else {
				commitRule.Delay = 60000
			}
			rules := []fakecli.Rule{
				{Argv: []string{"-C", dir, "add", "-A"}, Delay: delay},
				diffRule,
				commitRule,
			}
			if _, err := fakecli.Install(t, fakeDir, "git", rules); err != nil {
				t.Fatalf("install the fake git: %v", err)
			}
			g := &GitSync{Env: envFromPairs(t, fakecli.Env(nil, fakeDir)), Dir: dir, Branch: gitSyncBranch, Base: "main", Repo: gitSyncRepo}
			start := time.Now()
			g.Limit = stepDeadlineLimit(start.Add(step))
			committed, err := g.Checkpoint()
			elapsed := time.Since(start)
			t.Logf("checkpoint step %v, add %dms, hang on diff=%v: returned in %v (committed=%v err=%v)", step, delay, tc.hangDiff, elapsed, committed, err)
			// A killed diff counts as a staged diff and the commit then runs on
			// the 1 ms the step has left, so only the hung commit has a fixed
			// result.
			if !tc.hangDiff && (err == nil || committed) {
				t.Errorf("Checkpoint = %v, %v, want the checkpoint error", committed, err)
			}
			if elapsed < time.Duration(delay)*time.Millisecond {
				t.Errorf("returned in %v, before the %dms add: the scenario did not run", elapsed, delay)
			}
			if elapsed > step+scaledDuration(clampSlack) {
				t.Errorf("Checkpoint took %v, want at most the %v step + %v", elapsed, step, scaledDuration(clampSlack))
			}
			calls, err := fakecli.ReadCalls(filepath.Join(fakeDir, "git.calls.jsonl"))
			if want := 3; err != nil || len(calls) < want-1 || !tc.hangDiff && len(calls) != want {
				t.Errorf("fake git calls = %d (%v), want add, diff and (unless diff hung) commit", len(calls), err)
			}
		})
	}
}

// A start record that lost a write never yields an origin: with the first
// write lost and the second kept, the record's first start is the second
// call's, which would shorten the measure and let a broken clamp pass.
func TestAlignedFirstStartRefusesAPartialRecord(t *testing.T) {
	first := time.Unix(0, 1_000)
	second := time.Unix(0, 2_000)
	if got, err := alignedFirstStart(2, []time.Time{first, second}); err != nil || !got.Equal(first) {
		t.Fatalf("a full record = %v, %v; want the first call's start", got, err)
	}
	if _, err := alignedFirstStart(2, []time.Time{second}); err == nil {
		t.Fatal("a record with the first write lost gave an origin; want an error")
	}
	if _, err := alignedFirstStart(1, []time.Time{first, second}); err == nil {
		t.Fatal("a record with more starts than calls gave an origin; want an error")
	}
	if _, err := alignedFirstStart(0, nil); err == nil {
		t.Fatal("no call gave an origin; want an error")
	}
}
