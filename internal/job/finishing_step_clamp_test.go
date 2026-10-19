package job

import (
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
// (0.9 x the step for the 2 s scenarios), so the next command still opens
// inside the step. It is 3 s on Windows and zero on Unix, where the
// durations are unchanged.
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

// watchFirstGhCall polls the fake gh's call log and records when the first
// call shows up: the moment the gh step starts running commands. The
// returned function stops the watcher and returns that time; it fails the
// test when no gh call was ever logged. The watcher also stops with the test.
func watchFirstGhCall(t *testing.T, f *supGitFixture) func() time.Time {
	t.Helper()
	log := filepath.Join(f.job.base.fakeDir, "gh.calls.jsonl")
	stop := make(chan struct{})
	done := make(chan struct{})
	var first time.Time
	go func() {
		defer close(done)
		for {
			// A read that fails (no log yet, a line still being written) is
			// retried at the next tick.
			if calls, err := fakecli.ReadCalls(log); err == nil && len(calls) > 0 {
				first = time.Now()
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	var once sync.Once
	halt := func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
	t.Cleanup(halt)
	return func() time.Time {
		t.Helper()
		halt()
		if first.IsZero() {
			t.Fatalf("no gh call was logged")
		}
		return first
	}
}

// A slow first gh command followed by a hung second one stays inside the gh
// step: `gh pr create` receives the remainder at its own start, not the whole
// step again.
func TestFinishingGhStepBoundsTheSecondCommandByTheRemainder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		step  time.Duration
		delay int
	}{
		{"step 2s, list 1.8s", 2 * time.Second, 1800},
		{"step 5s, list 4s", 5 * time.Second, 4000},
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
			firstGh := watchFirstGhCall(t, f)
			exit, err, elapsed := finishDone(t, f)
			// The bound is the gh step, so it is measured from the first gh
			// call: the final push before it runs real git and takes as long
			// as the runner lets it, and is not part of the step.
			ghElapsed := time.Since(firstGh())
			t.Logf("gh step %v, list %dms, create hangs: terminal in %v, %v after the first gh call", step, delay, elapsed, ghElapsed)
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
		name  string
		step  time.Duration
		delay int
		// hangDiff hangs `diff --cached` (and lets `commit` return at once)
		// instead of hanging `commit`.
		hangDiff bool
	}{
		{"step 2s, add 1.8s, commit hangs", 2 * time.Second, 1800, false},
		{"step 5s, add 4s, commit hangs", 5 * time.Second, 4000, false},
		{"step 2s, add 1.8s, diff hangs", 2 * time.Second, 1800, true},
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
