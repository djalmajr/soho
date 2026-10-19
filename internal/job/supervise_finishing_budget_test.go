package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// supervise_finishing_budget_test.go pins the terminal procedure's own
// budget: a finite budget persisted with the job (so a restart keeps the
// deadline), one bound per step inside it (the final git sync, the draft
// pull request, the collaboration wait), the truthful failed outcome of a
// done job whose final push or pull request was cut, and the retry of the
// terminal write while the budget remains. Real subprocess hangs run on the
// real clock with small limits; every other case runs on the fake clocks.

const budgetMotivoText = "tempo de finalização esgotado"

// failStateWritesRecord is what failEveryStateWrite saw: the failures it
// injected and the monotonic reading of the first and the last.
type failStateWritesRecord struct {
	n           int
	first, last int64
}

// failEveryStateWrite fails every state.json write whose pending content
// satisfies match (a persistent write failure) and records when each
// failure happened on mono.
func failEveryStateWrite(t *testing.T, err error, mono func() int64, match func(pending State) bool) *failStateWritesRecord {
	t.Helper()
	rec := &failStateWritesRecord{}
	previous := renameAtomic
	renameAtomic = func(from, to string) error {
		if filepath.Base(to) == "state.json" {
			var pending State
			if raw, readErr := os.ReadFile(from); readErr == nil && json.Unmarshal(raw, &pending) == nil && match(pending) {
				now := mono()
				if rec.n == 0 {
					rec.first = now
				}
				rec.last = now
				rec.n++
				return err
			}
		}
		return previous(from, to)
	}
	t.Cleanup(func() { renameAtomic = previous })
	return rec
}

// remoteHookName is the hook the fixtures install in the bare remote and
// the name of its fakecli config and call log in the fake directory.
const remoteHookName = "pre-receive"

// installRemoteHook installs a fakecli re-execution as the bare remote's
// pre-receive hook. The fake and its config live in the fake directory (the
// fake finds its config there through the environment, by the base name of
// its argv[0] without a .exe suffix); the hook entry in the remote's hooks
// directory carries the name fakecli.Install gave the binary: `pre-receive`
// on Unix (a symlink to the test binary) and `pre-receive.exe` on Windows (a
// copy): a hook entry that is a real executable under the name Git for
// Windows resolves (as far as the Git source is known to: the bare name,
// then the name plus .exe) is the shape that needs no symlink privilege and
// no extension-less spawn. The Windows hook is a copy, never a hard link of
// the fake: every fakecli binary of the test process is a hard link of one
// prepared image, and Windows refuses to delete any link of an image a
// process still runs, so a held hook that shared it would fail the
// temporary-directory cleanup of every test that ends while it runs. Both
// hook fixtures share this installer; requireRemoteHookCalls proves the hook
// ran, on every platform, so a hook that does not run fails as a fixture
// error instead of as a product result. It returns the hook entry's path.
func installRemoteHook(t *testing.T, f *supGitFixture, rules []fakecli.Rule) string {
	t.Helper()
	target, err := fakecli.Install(t, f.job.base.fakeDir, remoteHookName, rules)
	if err != nil {
		t.Fatalf("install the %s fake: %v", remoteHookName, err)
	}
	hooks := filepath.Join(f.job.base.bare, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooks, filepath.Base(target))
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read the fake for the hook: %v", err)
		}
		if err := os.WriteFile(hook, data, 0o755); err != nil {
			t.Fatalf("copy the fake to the hook: %v", err)
		}
	} else if err := os.Symlink(target, hook); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(hook); err != nil || info.IsDir() || info.Size() == 0 {
		t.Fatalf("fixture error: the hook %s is not a usable file: %v", hook, err)
	}
	return hook
}

// remoteHookCalls returns the calls the installed hook logged; none when it
// never ran (no log).
func remoteHookCalls(t *testing.T, f *supGitFixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.job.base.fakeDir, remoteHookName+".calls.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read the %s calls: %v", remoteHookName, err)
	}
	return calls
}

// requireRemoteHookCalls is the fixtures' self-check: it fails the test as a
// fixture error unless the pre-receive fake logged exactly want calls, so a hook that never ran, or never read its
// config, is not mistaken for a product result. The fake logs a call before
// it applies any rule, and a push cut early may leave the hook starting, so
// the log is polled for a short while before it is judged.
func requireRemoteHookCalls(t *testing.T, f *supGitFixture, want int) {
	t.Helper()
	deadline := time.Now().Add(scaledDuration(5 * time.Second))
	calls := remoteHookCalls(t, f)
	for len(calls) < want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		calls = remoteHookCalls(t, f)
	}
	if len(calls) == want {
		return
	}
	var listed []string
	if entries, err := os.ReadDir(filepath.Join(f.job.base.bare, "hooks")); err == nil {
		for _, entry := range entries {
			if strings.Contains(entry.Name(), remoteHookName) {
				listed = append(listed, entry.Name())
			}
		}
	}
	t.Fatalf("fixture error: the bare remote's %s hook logged %d call(s), want %d: the hook did not run or could not read its fake config, so this test does not exercise the push it claims; hook entries in %s: %v",
		remoteHookName, len(calls), want, filepath.Join(f.job.base.bare, "hooks"), listed)
}

// holdRemotePush makes the bare remote's pre-receive hook (a fakecli
// re-execution) wait for a release file, so a real `git push` hangs; the
// hook is released after hold (or at cleanup). On Windows the cleanup then
// waits until the hook entry can be removed (the released hook has exited,
// or a push the cut left behind can no longer start it) and until the
// fixture tree can be removed: the step cut kills only the git process
// there, and the git processes it started finish on their own, holding
// files of the checkout and the remote for a while. Callers prove the hook
// ran with requireRemoteHookCalls.
func holdRemotePush(t *testing.T, f *supGitFixture, hold time.Duration) {
	t.Helper()
	release := filepath.Join(t.TempDir(), "release")
	hook := installRemoteHook(t, f, []fakecli.Rule{{AnyArgs: true, WaitFile: release}})
	var once sync.Once
	free := func() {
		once.Do(func() {
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Errorf("release the held push: %v", err)
			}
		})
	}
	timer := time.AfterFunc(hold, free)
	t.Cleanup(func() {
		timer.Stop()
		free()
		if runtime.GOOS == "windows" {
			removeReleasedHook(t, hook)
		}
		// On every system the released push may still be writing into
		// the bare remote when the test ends: remove the fixture tree
		// with retries before the testing package's own short cleanup.
		removeHeldFixtureTree(t, f.job.base.root)
	})
}

// removeHeldFixtureTree removes the fixture tree of a held push,
// retrying while the git processes the cut left behind still hold its
// files, and fails the test as a fixture error when the tree is still held
// after releasedHookExitWait. The testing package's own removal retries for
// only a short while.
func removeHeldFixtureTree(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(releasedHookExitWait)
	for {
		err := os.RemoveAll(root)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("fixture error: the held push's fixture tree %s is still in use %v after the release: %v", root, releasedHookExitWait, err)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// releasedHookExitWait bounds how long the cleanup waits for a released
// Windows hook to exit.
const releasedHookExitWait = 30 * time.Second

// removeReleasedHook removes the Windows hook entry of a released hold,
// retrying while a hook process still runs it (Windows refuses to delete a
// running image), and fails the test as a fixture error when the hook is
// still running after releasedHookExitWait.
func removeReleasedHook(t *testing.T, hook string) {
	t.Helper()
	deadline := time.Now().Add(releasedHookExitWait)
	for {
		err := os.Remove(hook)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("fixture error: the released hook %s still runs %v after its release: %v", hook, releasedHookExitWait, err)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// finishingRealClock puts the supervisor of the git fixture on the real
// clock with a short poll and the given limits.
func finishingRealClock(f *supGitFixture, limits finishingLimits) {
	f.sup.Clock = systemClock{}
	f.sup.Poll = 20 * time.Millisecond
	f.sup.FinishingLimits = limits
}

// finishDone runs the done outcome of the git fixture from finishing and
// returns the exit code, the error and the wall time it took.
func finishDone(t *testing.T, f *supGitFixture) (int, error, time.Duration) {
	t.Helper()
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	exit, done, err := f.sup.finishOutcome(StatusDone, "", 0)
	elapsed := time.Since(start)
	if !done {
		t.Errorf("finishOutcome done = false, want the terminal path")
	}
	return exit, err, elapsed
}

// requireBudgetFailed asserts the truthful result of a done job whose
// final push or pull request was cut: failed, exit 19, the motivo in the
// state and in report.json, and a friction line naming the cut step.
func requireBudgetFailed(t *testing.T, f *supGitFixture, exit int, err error, friction string) {
	t.Helper()
	if err != nil {
		t.Errorf("finishOutcome error = %v, want nil", err)
	}
	if exit != ExitFailed {
		t.Errorf("exit = %d, want %d", exit, ExitFailed)
	}
	st := f.state(t)
	if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != budgetMotivoText {
		t.Errorf("state = %s motivo=%v, want failed with %q", st.Status, st.Motivo, budgetMotivoText)
	}
	if st.TerminalIntent == nil || st.TerminalIntent.Status != StatusFailed || st.TerminalIntent.Exit != ExitFailed || st.TerminalIntent.Motivo != budgetMotivoText {
		t.Errorf("terminal intent = %+v, want the rewritten failed intent", st.TerminalIntent)
	}
	rep := f.report(t)
	if rep["status"] != "failed" || rep["motivo"] != budgetMotivoText {
		t.Errorf("report.json status=%v motivo=%v, want failed with %q", rep["status"], rep["motivo"], budgetMotivoText)
	}
	found := false
	for _, line := range *f.friction {
		if strings.Contains(line, friction) {
			found = true
		}
	}
	if !found {
		t.Errorf("friction = %q, want a line naming %q", *f.friction, friction)
	}
}

// A push that hangs far beyond the budget: the terminal path still ends
// within the budget (plus a fixed slack) as failed with the budget motivo.
func TestSuperviseFinishingStalledPushIsBoundedAndFailsTruthfully(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	holdRemotePush(t, f, scaledDuration(8*time.Second))
	finishingRealClock(f, finishingLimits{Budget: scaledDuration(2 * time.Second), Git: scaledDuration(time.Second)})
	exit, err, elapsed := finishDone(t, f)
	t.Logf("stalled push: terminal in %v", elapsed)
	requireRemoteHookCalls(t, f, 1)
	if limit := scaledDuration(2*time.Second + 3*time.Second); elapsed > limit {
		t.Errorf("terminal path took %v, want at most the budget + slack (%v)", elapsed, limit)
	}
	requireBudgetFailed(t, f, exit, err, "the final push")
	if st := f.state(t); st.FinishingSynced {
		t.Errorf("finishing_synced = true after a cut push, want false")
	}
}

// The git step's own bound ends a hung push long before the budget does.
func TestSuperviseFinishingStalledPushIsCutByTheGitStepBeforeTheBudget(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	holdRemotePush(t, f, scaledDuration(8*time.Second))
	finishingRealClock(f, finishingLimits{Budget: scaledDuration(30 * time.Second), Git: scaledDuration(time.Second)})
	exit, err, elapsed := finishDone(t, f)
	t.Logf("stalled push, long budget: terminal in %v", elapsed)
	requireRemoteHookCalls(t, f, 1)
	if limit := scaledDuration(time.Second + 3*time.Second); elapsed > limit {
		t.Errorf("terminal path took %v, want at most the git step + slack (%v)", elapsed, limit)
	}
	requireBudgetFailed(t, f, exit, err, "the final push")
}

// The push succeeds and the gh step hangs: the gh step bound cuts it and
// the result is the same truthful failed.
func TestSuperviseFinishingStalledGhIsBoundedAndFailsTruthfully(t *testing.T) {
	rules := []fakecli.Rule{
		{Argv: ghListArgv(), Stdout: "[]\n", Delay: scaledMS(8000)},
		{Argv: ghCreateArgv("feat: first", supGitPRBody), Stdout: "Creating pull request\n" + gitSyncPRURL + "\n"},
	}
	f := newSupGitFixture(t, rules)
	f.job.commitIn(t, "feat: first")
	// The budget and the git step are far longer than the gh step, so only
	// the gh step's own bound can end the hang.
	finishingRealClock(f, finishingLimits{Budget: scaledDuration(30 * time.Second), Git: scaledDuration(20 * time.Second), GH: scaledDuration(time.Second)})
	exit, err, elapsed := finishDone(t, f)
	t.Logf("stalled gh: terminal in %v", elapsed)
	if limit := scaledDuration(time.Second + 3*time.Second); elapsed > limit {
		t.Errorf("terminal path took %v, want at most the gh step + slack (%v)", elapsed, limit)
	}
	requireBudgetFailed(t, f, exit, err, "the draft pull request")
	if n := countTipes(f.events(t), "push"); n != 1 {
		t.Errorf("push events = %d, want the one push that completed before the gh step", n)
	}
	if st := f.state(t); st.FinishingSynced {
		t.Errorf("finishing_synced = true after a cut gh step, want false")
	}
}

// A canceled job whose final push is cut keeps its outcome: only a done
// intent becomes failed.
func TestSuperviseFinishingBudgetKeepsTheStopOutcome(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	holdRemotePush(t, f, scaledDuration(8*time.Second))
	finishingRealClock(f, finishingLimits{Budget: scaledDuration(2 * time.Second), Git: scaledDuration(time.Second)})
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	exit, done, err := f.sup.finishOutcome(StatusCanceled, "cancelado", ExitCanceled)
	if err != nil || !done || exit != ExitCanceled {
		t.Fatalf("finishOutcome = %d %v %v, want canceled with exit %d", exit, done, err, ExitCanceled)
	}
	requireRemoteHookCalls(t, f, 1)
	if elapsed, limit := time.Since(start), scaledDuration(5*time.Second); elapsed > limit {
		t.Errorf("terminal path took %v, want it bounded by the budget (%v)", elapsed, limit)
	}
	st := f.state(t)
	if st.Status != StatusCanceled || st.Motivo != nil && *st.Motivo == budgetMotivoText {
		t.Errorf("state = %s motivo=%v, want canceled and not the budget motivo", st.Status, st.Motivo)
	}
}

// The collaboration wait of the terminal path ends at the collaboration
// step bound, not at the old fixed grace: the outcome stays done with the
// partial collaboration persisted.
func TestSuperviseFinishingCollaborationWaitIsBoundedByItsStep(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	_, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	sup.FinishingLimits = finishingLimits{Collab: 30 * time.Second}
	if _, err := store.Transition("job-1", StatusFinishing); err != nil {
		t.Fatal(err)
	}
	before := clock.mono
	exit, done, err := sup.finishOutcome(StatusDone, "", 0)
	if err != nil || !done || exit != 0 {
		t.Fatalf("finishOutcome = %d %v %v, want done with exit 0", exit, done, err)
	}
	waited := time.Duration(clock.mono - before)
	if waited > 30*time.Second+10*time.Second {
		t.Errorf("collaboration wait = %v, want at most the 30s step + one poll", waited)
	}
	st := supState(t, store)
	if st.Status != StatusDone || !reflect.DeepEqual(PartialCollaborations(st), []string{id}) {
		t.Errorf("state = %s partial=%v, want done with [%s] partial", st.Status, PartialCollaborations(st), id)
	}
	requireWorkspaceClosed(t, herdr, team)
}

// A restart keeps the deadline: the budget counted from the persisted
// anchor is already spent, so the final sync is skipped (no push), the
// result follows the outcome rule, and the anchor is the original one.
func TestSuperviseFinishingRestartKeepsTheDeadline(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	anchor := f.clock.Now().Add(-11 * time.Minute).Format(time.RFC3339Nano)
	if _, err := f.store.Record(supGitID, func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingStartedAt = anchor
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	sup := f.supRestarted(t)
	exit, err := sup.Run()
	requireBudgetFailed(t, f, exit, err, "the final push")
	if _, err := f.job.base.git(f.job.base.bare, "rev-parse", "--verify", "-q", "refs/heads/"+supGitBranch); err == nil {
		t.Errorf("the job branch reached the remote, want no git sync after the spent budget")
	}
	if n := countTipes(f.events(t), "push"); n != 0 {
		t.Errorf("push events = %d, want none", n)
	}
	if st := f.state(t); st.FinishingStartedAt != anchor {
		t.Errorf("finishing_started_at = %q, want the original anchor %q", st.FinishingStartedAt, anchor)
	}
}

// A restart that finds the final sync recorded as done does not run it
// again, whatever is left of the budget: the done intent ends done.
func TestSuperviseFinishingSyncedRestartDoesNotPushAgain(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	anchor := f.clock.Now().Add(-11 * time.Minute).Format(time.RFC3339Nano)
	if _, err := f.store.Record(supGitID, func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingStartedAt = anchor
		st.FinishingSynced = true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != 0 {
		t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
	}
	if st := f.state(t); st.Status != StatusDone {
		t.Errorf("state = %s, want done", st.Status)
	}
	if _, err := f.job.base.git(f.job.base.bare, "rev-parse", "--verify", "-q", "refs/heads/"+supGitBranch); err == nil {
		t.Errorf("the job branch reached the remote, want no second sync")
	}
	if n := countTipes(f.events(t), "push"); n != 0 {
		t.Errorf("push events = %d, want none", n)
	}
}

// A completed final sync is recorded: finishing_synced and the anchor, in
// the state of a done job.
func TestSuperviseFinishingRecordsTheCompletedSync(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	st := f.state(t)
	if st.Status != StatusDone || !st.FinishingSynced || st.FinishingStartedAt == "" {
		t.Errorf("state = %s synced=%v anchor=%q, want done, synced and anchored", st.Status, st.FinishingSynced, st.FinishingStartedAt)
	}
	if _, err := time.Parse(time.RFC3339Nano, st.FinishingStartedAt); err != nil {
		t.Errorf("finishing_started_at %q is not RFC 3339 nano: %v", st.FinishingStartedAt, err)
	}
}

// A state finishing with an intent and no anchor (written before the
// field existed) is anchored at the restart and the anchor is persisted;
// the budget then counts from it, so the sync still runs.
func TestSuperviseFinishingLegacyStateIsAnchoredAtTheRestart(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	if _, err := f.store.Record(supGitID, func(st *State) { st.TerminalIntent = &TerminalIntent{Status: StatusDone} }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if st := f.state(t); st.FinishingStartedAt != "" {
		t.Fatalf("fixture anchor = %q, want none (a legacy state)", st.FinishingStartedAt)
	}
	want := f.clock.Now().Format(time.RFC3339Nano)
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != 0 {
		t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
	}
	st := f.state(t)
	if st.FinishingStartedAt != want {
		t.Errorf("finishing_started_at = %q, want the restart instant %q", st.FinishingStartedAt, want)
	}
	if st.Status != StatusDone || !st.FinishingSynced {
		t.Errorf("state = %s synced=%v, want done with the sync run from the new anchor", st.Status, st.FinishingSynced)
	}
}

// A restart over a spent budget does not wait a fresh grace for the
// collaborations: nothing is asked or waited on, and the partial facts are
// persisted with the outcome unchanged.
func TestSuperviseFinishingRestartDoesNotWaitAFreshCollaborationGrace(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	_, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	anchor := clock.now.Add(-11 * time.Minute).Format(time.RFC3339Nano)
	if _, err := store.Record("job-1", func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingStartedAt = anchor
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", StatusFinishing); err != nil {
		t.Fatal(err)
	}
	before := clock.mono
	exit, err := sup.Run()
	if err != nil || exit != 0 {
		t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
	}
	if waited := time.Duration(clock.mono - before); waited >= 10*time.Second {
		t.Errorf("restart waited %v for the collaborations, want no wait over a spent budget", waited)
	}
	if len(team.dispatches) != 0 {
		t.Errorf("dispatches = %+v, want none after the budget", team.dispatches)
	}
	st := supState(t, store)
	if st.Status != StatusDone || !reflect.DeepEqual(PartialCollaborations(st), []string{id}) {
		t.Errorf("state = %s partial=%v, want done with [%s] partial", st.Status, PartialCollaborations(st), id)
	}
}

// The terminal write that fails once is retried within the budget: the job
// ends terminal and the injected text is in friction once.
func TestSuperviseFinishingTerminalWriteRetrySucceeds(t *testing.T) {
	var friction []string
	store, jobDir, _, team, herdr, sup := failBlockedSetup(t, &friction)
	report := writeOrchestratorReport(t, "# Report — job\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	injected := errors.New("injected terminal write error")
	fired := failStateWrite(t, injected, func(p State) bool { return p.Status == StatusDone })
	exit, err := sup.Run()
	if err != nil || exit != 0 {
		t.Errorf("Run = %d %v, want done with exit 0 after the retry", exit, err)
	}
	if !*fired {
		t.Fatal("the terminal write error was never injected")
	}
	if st := supState(t, store); st.Status != StatusDone || !st.WorkspaceClosed {
		t.Errorf("state = %s closed=%v, want done and the workspace closed", st.Status, st.WorkspaceClosed)
	}
	want := []string{"job: the supervisor could not record the terminal state done: injected terminal write error"}
	if !reflect.DeepEqual(friction, want) {
		t.Errorf("friction = %q, want the injected text once: %q", friction, want)
	}
	if n := countTipe(mustEvents(t, jobDir), "cleanup"); n != 1 {
		t.Errorf("cleanup events = %d, want 1", n)
	}
	requireWorkspaceClosed(t, herdr, team)
}

// A persistent terminal write failure is retried at the poll cadence until
// the budget is spent and then returned: the job stays finishing with its
// intent and finalize never runs.
func TestSuperviseFinishingTerminalWritePersistentFailureEndsAtTheBudget(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, herdr, sup := failBlockedSetup(t, &friction)
	sup.FinishingLimits = finishingLimits{Budget: 60 * time.Second}
	report := writeOrchestratorReport(t, "# Report — job\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	injected := errors.New("injected persistent terminal write error")
	rec := failEveryStateWrite(t, injected, func() int64 { return clock.mono }, func(p State) bool { return p.Status == StatusDone })
	exit, err := sup.Run()
	if err == nil || !errors.Is(err, injected) {
		t.Errorf("Run error = %v, want the injected error after the budget", err)
	}
	if exit != ExitFailed {
		t.Errorf("exit = %d, want %d", exit, ExitFailed)
	}
	span := time.Duration(rec.last - rec.first)
	if span < 60*time.Second-10*time.Second || span > 60*time.Second+10*time.Second {
		t.Errorf("retried over %v (%d attempts), want the 60s budget +- one poll", span, rec.n)
	}
	st := supState(t, store)
	if st.Status != StatusFinishing || st.TerminalIntent == nil || st.TerminalIntent.Status != StatusDone {
		t.Errorf("state = %s intent=%+v, want finishing with the done intent", st.Status, st.TerminalIntent)
	}
	want := []string{"job: the supervisor could not record the terminal state done: injected persistent terminal write error"}
	if !reflect.DeepEqual(friction, want) {
		t.Errorf("friction = %q, want the one distinct error line %q", friction, want)
	}
	requireNoTerminalFinalize(t, store, jobDir, team, herdr)
}

// A clock that never advances cannot hold the terminal write retry: the
// attempts are also bounded by the budget counted in polls.
func TestSuperviseFinishingTerminalWriteRetryIsFiniteOnAStuckClock(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	rec := failEveryStateWrite(t, errors.New("injected stuck write error"), func() int64 { return 0 }, func(p State) bool { return p.Status == StatusDone })
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	_, done, err := f.sup.finishOutcome(StatusDone, "", 0)
	if err == nil || !done {
		t.Fatalf("finishOutcome = done %v err %v, want the write error", done, err)
	}
	// 10 minutes of 5 s polls: the first attempt and 120 retries.
	if rec.n != 121 {
		t.Errorf("terminal write attempts = %d, want 121 (budget / poll + 1)", rec.n)
	}
}

// Hostile clock: an anchor in the future counts as zero elapsed, so the
// budget runs from the process's own monotonic start and is never
// extended by the anchor's distance.
func TestSuperviseFinishingFutureAnchorClampsToZeroElapsed(t *testing.T) {
	var friction []string
	store, _, clock, _, _, sup := failBlockedSetup(t, &friction)
	sup.FinishingLimits = finishingLimits{Budget: 60 * time.Second}
	if _, err := store.Record("job-1", func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingStartedAt = clock.now.Add(time.Hour).Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", StatusFinishing); err != nil {
		t.Fatal(err)
	}
	rec := failEveryStateWrite(t, errors.New("injected"), func() int64 { return clock.mono }, func(p State) bool { return p.Status == StatusDone })
	if _, err := sup.Run(); err == nil {
		t.Fatal("Run succeeded under a persistent terminal write failure")
	}
	span := time.Duration(rec.last - rec.first)
	if span < 60*time.Second-10*time.Second || span > 60*time.Second+10*time.Second {
		t.Errorf("retried over %v (%d attempts), want the 60s budget from a zero elapsed (+- one poll)", span, rec.n)
	}
}

// Hostile clock: a wall clock that moves backwards on every sleep never
// extends the budget past the monotonic elapsed: the 5 s already spent at
// the process start plus the sleeps add up to the budget.
func TestSuperviseFinishingBackwardsWallClockNeverExtendsTheBudget(t *testing.T) {
	var friction []string
	store, _, clock, _, _, sup := failBlockedSetup(t, &friction)
	sup.FinishingLimits = finishingLimits{Budget: 60 * time.Second}
	clock.rewind = true
	if _, err := store.Record("job-1", func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingStartedAt = clock.now.Add(-5 * time.Second).Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", StatusFinishing); err != nil {
		t.Fatal(err)
	}
	rec := failEveryStateWrite(t, errors.New("injected"), func() int64 { return clock.mono }, func(p State) bool { return p.Status == StatusDone })
	if _, err := sup.Run(); err == nil {
		t.Fatal("Run succeeded under a persistent terminal write failure")
	}
	span := time.Duration(rec.last - rec.first)
	if span < 55*time.Second-10*time.Second || span > 55*time.Second+10*time.Second {
		t.Errorf("retried over %v (%d attempts), want 55s (60s budget minus the 5s already spent) +- one poll", span, rec.n)
	}
}

// Hostile clock, measured on a step that is shorter than the budget's
// default: with a 20 s budget the collaboration wait ends at the budget, not
// at its own 120 s step, whatever the wall clock says about the anchor — an
// anchor in the future counts as zero elapsed, and a wall clock that moves
// backwards on every sleep never extends what the anchor had already spent.
func TestSuperviseFinishingHostileWallClockNeverExtendsTheCollaborationWait(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor time.Duration // offset of the anchor from the supervisor's now
		rewind bool
		want   time.Duration // the budget left when the process starts
	}{
		{"future anchor", time.Hour, false, 20 * time.Second},
		{"backwards wall clock", -5 * time.Second, true, 15 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var friction []string
			store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
			collabSup(t, store, sup)
			ws := collabWorkspace(t, jobDir)
			_, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
			team.rosterRows = collabRoster("impl-1", "rev-1")
			sup.FinishingLimits = finishingLimits{Budget: 20 * time.Second}
			clock.rewind = tc.rewind
			if _, err := store.Record("job-1", func(st *State) {
				st.TerminalIntent = &TerminalIntent{Status: StatusDone}
				st.FinishingStartedAt = clock.now.Add(tc.anchor).Format(time.RFC3339Nano)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Transition("job-1", StatusFinishing); err != nil {
				t.Fatal(err)
			}
			before := clock.mono
			exit, err := sup.Run()
			if err != nil || exit != 0 {
				t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
			}
			waited := time.Duration(clock.mono - before)
			if waited > tc.want+10*time.Second {
				t.Errorf("collaboration wait = %v, want at most the %v of budget left + one poll", waited, tc.want)
			}
			if st := supState(t, store); st.Status != StatusDone || !reflect.DeepEqual(PartialCollaborations(st), []string{id}) {
				t.Errorf("state = %s partial=%v, want done with [%s] partial", st.Status, PartialCollaborations(st), id)
			}
		})
	}
}

// rejectFirstRemotePush makes the bare remote's pre-receive hook (a fakecli
// re-execution) refuse the first push at once and accept every later one, so
// a push fails fast, is not cut by any bound, and a later pass succeeds.
// Callers prove each push reached the hook with requireRemoteHookCalls.
func rejectFirstRemotePush(t *testing.T, f *supGitFixture) {
	t.Helper()
	installRemoteHook(t, f, []fakecli.Rule{
		{AnyArgs: true, Call: 1, Code: 1, Stderr: "rejected by the fixture\n"},
		{AnyArgs: true},
	})
}

// remoteHasBranch reports whether the job branch reached the bare remote.
func remoteHasBranch(f *supGitFixture) bool {
	_, err := f.job.base.git(f.job.base.bare, "rev-parse", "--verify", "-q", "refs/heads/"+supGitBranch)
	return err == nil
}

// pushedEvents counts the push events that carry a completed push.
func pushedEvents(events []Event) int {
	n := 0
	for _, event := range events {
		if event.Tipo == "push" && event.Resumo == "pushed" {
			n++
		}
	}
	return n
}

// A push that the remote refuses at once is not cut by any bound: the job
// keeps today's done, and the final sync is not recorded as complete.
func TestSuperviseFinishingRejectedPushKeepsDoneAndIsNotSynced(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	rejectFirstRemotePush(t, f)
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	requireRemoteHookCalls(t, f, 1)
	st := f.state(t)
	if st.Status != StatusDone || st.Motivo != nil {
		t.Errorf("state = %s motivo=%v, want done without a motivo (a rejected push is not a cut)", st.Status, st.Motivo)
	}
	if st.FinishingSynced {
		t.Errorf("finishing_synced = true after a rejected push, want false")
	}
	if remoteHasBranch(f) {
		t.Errorf("the job branch reached the remote, want the push refused")
	}
}

// A restart over a done intent whose final sync did not complete cleanly (a
// refused push) runs the final sync again, as the baseline did: the push
// lands, the sync is then recorded, and the job ends done.
func TestSuperviseFinishingRestartRunsTheSyncAgainAfterARejectedPush(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	rejectFirstRemotePush(t, f)
	// Process one: the push is refused and the terminal write fails with no
	// budget to retry it, so the job stays finishing with its done intent.
	f.sup.FinishingLimits = finishingLimits{Budget: time.Second}
	fired := failStateWrite(t, errors.New("injected terminal write error"), func(p State) bool { return p.Status == StatusDone })
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if _, done, err := f.sup.finishOutcome(StatusDone, "", 0); err == nil || !done {
		t.Fatalf("process one finishOutcome = done %v err %v, want the terminal write error", done, err)
	}
	if !*fired {
		t.Fatal("the terminal write error was never injected")
	}
	requireRemoteHookCalls(t, f, 1)
	st := f.state(t)
	if st.Status != StatusFinishing || st.TerminalIntent == nil || st.TerminalIntent.Status != StatusDone {
		t.Fatalf("state after process one = %s intent=%+v, want finishing with the done intent", st.Status, st.TerminalIntent)
	}
	if st.FinishingSynced {
		t.Errorf("finishing_synced = true after a refused push, want false")
	}
	if remoteHasBranch(f) {
		t.Fatalf("the job branch reached the remote in process one, want the push refused")
	}
	// Process two: the remote accepts now; the restart pushes again.
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != 0 {
		t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
	}
	requireRemoteHookCalls(t, f, 2)
	if st := f.state(t); st.Status != StatusDone || !st.FinishingSynced {
		t.Errorf("state after the restart = %s synced=%v, want done and synced", st.Status, st.FinishingSynced)
	}
	if !remoteHasBranch(f) {
		t.Errorf("the job branch is not on the remote after the restart, want the final sync run again")
	}
	if n := pushedEvents(f.events(t)); n != 1 {
		t.Errorf("pushed events = %d, want the one push of the restart", n)
	}
}

// A draft pull request step that fails at once (not cut) leaves the sync
// unrecorded and the job done.
func TestSuperviseFinishingGhErrorKeepsDoneAndIsNotSynced(t *testing.T) {
	rules := []fakecli.Rule{{Argv: ghListArgv(), Code: 1, Stderr: "gh failed\n"}}
	f := newSupGitFixture(t, rules)
	f.job.commitIn(t, "feat: first")
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	st := f.state(t)
	if st.Status != StatusDone || st.FinishingSynced {
		t.Errorf("state = %s synced=%v, want done and not synced", st.Status, st.FinishingSynced)
	}
	if !remoteHasBranch(f) {
		t.Errorf("the push did not land, want the gh failure to be the only failed step")
	}
}

// A push that fails on its own command timeout (not the step's clamp) is a
// plain step error: the job keeps done and the sync is not recorded. The
// 2 s command timeout stays below the 20 s step bound while giving the
// remote hook time to start and log on a slow Windows CI; the hook is held
// for 10 s, so it is still waiting when the timeout kills the push.
func TestSuperviseFinishingPushErrorBelowTheStepBoundIsNotSynced(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	holdRemotePush(t, f, 10*time.Second)
	f.sup.Git.NetTimeout = 2 * time.Second
	finishingRealClock(f, finishingLimits{Budget: 30 * time.Second, Git: 20 * time.Second})
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0 (the command timeout is not a cut)", exit, err)
	}
	requireRemoteHookCalls(t, f, 1)
	st := f.state(t)
	if st.Status != StatusDone || st.FinishingSynced {
		t.Errorf("state = %s synced=%v, want done and not synced", st.Status, st.FinishingSynced)
	}
}

// A git read that fails at once (the head, the commit listing) is a plain
// step error too: the job keeps done and the sync is not recorded.
func TestSuperviseFinishingGitReadErrorsAreNotSynced(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(g *GitSync, t *testing.T)
	}{
		{"head", func(g *GitSync, t *testing.T) { g.Dir = filepath.Join(t.TempDir(), "missing") }},
		{"commits", func(g *GitSync, t *testing.T) { g.Base = "no-such-base" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSupGitFixture(t, supGitGhRules)
			f.job.commitIn(t, "feat: first")
			tc.break_(f.sup.Git, t)
			exit, err, _ := finishDone(t, f)
			if err != nil || exit != 0 {
				t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
			}
			if st := f.state(t); st.Status != StatusDone || st.FinishingSynced {
				t.Errorf("state = %s synced=%v, want done and not synced", st.Status, st.FinishingSynced)
			}
		})
	}
}

// A store error returned by the sync pass (the commit event cannot be
// appended) is not a clean sync either.
func TestSuperviseFinishingStoreErrorInTheSyncIsNotSynced(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	previous := appendEventFn
	appendEventFn = func(dir string, now time.Time, in EventIn) (Event, error) {
		if in.Tipo == "commit" {
			return Event{}, errors.New("injected commit append error")
		}
		return previous(dir, now, in)
	}
	t.Cleanup(func() { appendEventFn = previous })
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	if st := f.state(t); st.Status != StatusDone || st.FinishingSynced {
		t.Errorf("state = %s synced=%v, want done and not synced", st.Status, st.FinishingSynced)
	}
}

// The production team adapter lowers its command timeout to the step's
// remaining time and never raises it above its own bound.
func TestSelfCLIBoundedLowersTheCommandTimeout(t *testing.T) {
	base := selfCLI{Exe: "x", Dir: "d", Timeout: 120 * time.Second}
	lowered, ok := base.bounded(5 * time.Second).(selfCLI)
	if !ok || lowered.Timeout != 5*time.Second {
		t.Fatalf("bounded(5s) = %+v, want a 5s timeout", lowered)
	}
	kept, ok := base.bounded(10 * time.Minute).(selfCLI)
	if !ok || kept.Timeout != 120*time.Second {
		t.Fatalf("bounded(10m) = %+v, want the adapter's own 120s", kept)
	}
	zero, ok := selfCLI{Exe: "x", Dir: "d"}.bounded(7 * time.Second).(selfCLI)
	if !ok || zero.Timeout != 7*time.Second {
		t.Fatalf("zero-timeout bounded(7s) = %+v, want 7s", zero)
	}
}

// The finishing budget starts only with the terminal intent: a partial
// report with an open decision enters finishing provisionally and straight
// back to blocked without anchoring the budget, so the eleven minutes the
// job waits for the decision charge the blocked clock only; the amendment
// unblocks and the next report, without partials, ends done on the full
// budget counted from the terminal intent. An anchor at the provisional
// entry would leave the deadline spent by the terminal crossing and cut
// the final sync, rewriting the success to failed.
func TestSuperviseFinishingBudgetStartsAtTheTerminalIntent(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	start := f.clock.Now()
	if _, err := f.store.Note(supGitID, EventIn{Tipo: "decision", Escopo: "global", Resumo: "keep the queue"}); err != nil {
		t.Fatal(err)
	}
	partialReport := writeOrchestratorReport(t, "# Report — job\n\n- [partial] Item 1 — the gate was not run\n")
	fullReport := writeOrchestratorReport(t, "# Report — job\n\n- [done] Item 1\n")
	script := []fakeStatus{{State: "working"}, {State: "working"}, {State: "question"}}
	script = append(script, fakeStatus{State: "done", Report: partialReport})
	for i := 0; i < 132; i++ { // 11 min of blocked wait at the 5 s poll
		script = append(script, fakeStatus{State: "done", Report: partialReport})
	}
	script = append(script, fakeStatus{State: "done", Report: fullReport})
	f.team.statusScript = script
	var provisionalAnchor string
	f.clock.onSleep = func() {
		if f.clock.sleeps == 4 { // after the partial report crossed to finishing and back to blocked
			if st := f.state(t); st.FinishingStartedAt != "" {
				provisionalAnchor = st.FinishingStartedAt
			}
		}
		if f.clock.sleeps == 136 { // the decision is answered and the amendment lands
			if _, err := f.store.Ack(supGitID, 100); err != nil {
				t.Errorf("ack the decision: %v", err)
			}
			if _, err := f.store.RequestAmend(supGitID, []byte("the decision is in: continue\n")); err != nil {
				t.Errorf("queue the amendment: %v", err)
			}
		}
	}
	exit, err := f.sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if waited := f.clock.Now().Sub(start); waited < 11*time.Minute {
		t.Fatalf("the clock advanced %v, want the 11 min of blocked wait to run", waited)
	}
	if provisionalAnchor != "" {
		t.Errorf("the provisional entry anchored the finishing budget at %q, want no anchor before the terminal intent", provisionalAnchor)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0 (the budget counts from the terminal intent, not the provisional entry)", exit)
	}
	st := f.state(t)
	if st.Status != StatusDone || st.Motivo != nil {
		t.Errorf("state = %s motivo=%v, want done without a motivo", st.Status, st.Motivo)
	}
	if st.FinishingStartedAt == "" {
		t.Fatal("finishing_started_at is empty, want the terminal intent's anchor")
	}
	anchor, err := time.Parse(time.RFC3339Nano, st.FinishingStartedAt)
	if err != nil {
		t.Fatalf("finishing_started_at %q does not parse: %v", st.FinishingStartedAt, err)
	}
	if elapsed := f.clock.Now().Sub(anchor); elapsed < 0 || elapsed > 5*time.Second {
		t.Errorf("the budget ran %v from the anchor, want at most one poll (the anchor is the terminal intent, not the provisional entry)", elapsed)
	}
	if !st.FinishingSynced {
		t.Errorf("finishing_synced = false, want the final sync run on the full budget")
	}
	if rep := f.report(t); rep["status"] != "done" {
		t.Errorf("report.json status = %v, want done", rep["status"])
	}
	events := f.events(t)
	if n := countTipes(events, "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want 1", n)
	}
	if n := countTipes(events, "unblocked"); n != 1 {
		t.Errorf("unblocked events = %d, want the amendment's one", n)
	}
	for _, line := range *f.friction {
		if strings.Contains(line, "finishing budget exhausted") {
			t.Errorf("friction = %q, want no cut line: the blocked wait must not spend the finishing budget", line)
		}
	}
}

// A terminal event an earlier pass already published is final: pass one
// pushes (the remote refuses it at once — not a cut), records the terminal
// event with exit 0, and then fails to persist the done state; the restart
// over the spent budget still runs the final sync and gets cut, but must
// not rewrite the announced outcome, so the state, report.json and the
// single terminal event agree on done with exit 0.
func TestSuperviseFinishingRestartKeepsTheAnnouncedOutcome(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	rejectFirstRemotePush(t, f)
	// Pass one: the push is refused and the done state write fails after
	// the terminal event landed, so the job stays finishing with its done
	// intent and the announced outcome.
	f.sup.FinishingLimits = finishingLimits{Budget: time.Second}
	fired := failStateWrite(t, errors.New("injected terminal write error"), func(p State) bool { return p.Status == StatusDone })
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if _, done, err := f.sup.finishOutcome(StatusDone, "", 0); err == nil || !done {
		t.Fatalf("pass one finishOutcome = done %v err %v, want the terminal write error", done, err)
	}
	if !*fired {
		t.Fatal("the terminal write error was never injected")
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Fatalf("terminal events = %d after pass one, want the one announced", n)
	}
	if st := f.state(t); st.Status != StatusFinishing || st.TerminalIntent == nil || st.TerminalIntent.Status != StatusDone {
		t.Fatalf("state after pass one = %s intent=%+v, want finishing with the done intent", st.Status, st.TerminalIntent)
	}
	// Pass two: the clock is past the default budget, so the sync is cut —
	// and the announced outcome stands over the cut.
	f.clock.now = f.clock.now.Add(11 * time.Minute)
	exit, err := f.supRestarted(t).Run()
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if exit != 0 {
		t.Errorf("restart exit = %d, want 0 (the announced done stands over the cut sync)", exit)
	}
	st := f.state(t)
	if st.Status != StatusDone || st.Motivo != nil || st.TerminalIntent == nil || st.TerminalIntent.Status != StatusDone {
		t.Errorf("state = %s motivo=%v intent=%+v, want done without a rewrite to the budget failure", st.Status, st.Motivo, st.TerminalIntent)
	}
	if st.FinishingSynced {
		t.Errorf("finishing_synced = true after a cut sync, want false")
	}
	if remoteHasBranch(f) {
		t.Errorf("the job branch reached the remote, want no push over the spent budget")
	}
	if n := len(remoteHookCalls(t, f)); n != 1 {
		t.Errorf("hook calls = %d, want the one refused push of pass one", n)
	}
	rep := f.report(t)
	// The state keeps no motivo, but the commit never reached the remote:
	// the report says so (push pendente) instead of leaving the motivo empty.
	if rep["status"] != "done" || rep["motivo"] != "push pendente" {
		t.Errorf("report.json status=%v motivo=%v, want done with push pendente", rep["status"], rep["motivo"])
	}
	events := f.events(t)
	if n := countTipes(events, "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want the one announced (no second)", n)
	}
	for _, ev := range events {
		if ev.Tipo == "terminal" && (ev.Refs["exit"] != "0" || ev.Refs["motivo"] != "") {
			t.Errorf("terminal event refs = %v, want exit 0 and no motivo", ev.Refs)
		}
	}
	found := false
	for _, line := range *f.friction {
		if strings.Contains(line, "finishing budget exhausted") && strings.Contains(line, "the final push") {
			found = true
		}
	}
	if !found {
		t.Errorf("friction = %q, want the cut line: the sync ran and was cut, only the rewrite is skipped", *f.friction)
	}
}
