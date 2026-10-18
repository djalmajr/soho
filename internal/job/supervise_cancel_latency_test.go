package job

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// supervise_cancel_latency_test.go pins, with the test clock, the hard
// bound of the cancel: from the acceptance of job cancel to the durable
// terminal record (state.json in canceled, via finishOutcome), at most the
// grace plus one supervisor poll interval (defaultSupervisorPoll) plus the
// finishing budget (job_finishing_timeout). The grace starts at the
// acceptance and ends on the persisted deadline (a restart never restarts
// it); the stop ends on the first tick at or past the deadline, at most one
// poll after it; the final push and pull request step run inside the
// finishing budget and are cut when the budget is spent, so they add at
// most the budget after the grace. The worst-case subtests run every
// allowed slow step: the stop amendment consumes the grace limit and the
// queue delivery consumes the grace limit (one per subtest), and the final
// push step is cut by the spent finishing budget.
//
// T0 is the acceptance instant (the persisted cancel_requested_at); the
// supervisor's default poll applies (Poll zero), so the bound uses
// defaultSupervisorPoll. The fake clock advances only on Sleep and on the
// scripted cuts, so the wall time when Run returns is the wall time of the
// durable terminal record: nothing after the transition advances the clock.

// cancelLatencyGrace and cancelLatencyBudget are the test values of the
// grace and of the finishing budget (the seam standing in for the
// job_finishing_timeout recorded at job start).
var (
	cancelLatencyGrace  = 60 * time.Second
	cancelLatencyBudget = 2 * time.Minute
)

// workingStatuses returns n scripted working observations.
func workingStatuses(n int) []fakeStatus {
	out := make([]fakeStatus, n)
	for i := range out {
		out[i] = fakeStatus{State: "working"}
	}
	return out
}

// assertTerminalWithinHardBound asserts the hard bound of the cancel: the
// durable terminal record (the clock's wall time when Run returned) is no
// more than the grace, one default supervisor poll interval and the
// finishing budget past the acceptance.
func assertTerminalWithinHardBound(t *testing.T, clock *nsClock, t0 time.Time, grace, budget time.Duration) {
	t.Helper()
	bound := t0.Add(grace + defaultSupervisorPoll + budget)
	if clock.now.After(bound) {
		t.Fatalf("terminal at %s is past the hard bound %s (acceptance %s + grace %s + poll %s + budget %s)", clock.now, bound, t0, grace, defaultSupervisorPoll, budget)
	}
}

// assertCanceledTerminal is the common terminal facts of the latency
// scenarios: the state canceled with its motivo, the single terminal event
// with exit 21, and the single retired cancel under the given name. The
// pending control directory and the friction are asserted by each
// subtest: they differ (a still-pending queue request, the cut line).
func assertCanceledTerminal(t *testing.T, store *Store, jobDir string, retired string) {
	t.Helper()
	if st := supState(t, store); st.Status != StatusCanceled || st.Motivo == nil || *st.Motivo != "cancelado" {
		t.Fatalf("state = %+v, want canceled with the cancel motivo", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := countTipe(events, "terminal"); n != 1 {
		t.Fatalf("terminal events = %d, want 1", n)
	}
	if terminal := eventsLast(t, jobDir); terminal.Refs["exit"] != "21" || terminal.Refs["motivo"] != "cancelado" {
		t.Fatalf("terminal refs = %+v", terminal)
	}
	if got := dirNames(t, filepath.Join(jobDir, "control", "done")); !reflect.DeepEqual(got, []string{retired}) {
		t.Fatalf("retired control = %v, want the single retired cancel %s", got, retired)
	}
}

func TestCancelTerminalWithinTheHardBound(t *testing.T) {
	t.Run("the stop amendment consumes the grace limit", func(t *testing.T) {
		var friction []string
		store, jobDir, clock, team, sup := nsSup(t, 0, &friction)
		sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget}
		// The acceptance at T0 anchors the grace (cancel_requested_at,
		// cancel_deadline) before the supervisor's first tick.
		t0 := clock.now
		monoT0 := clock.mono
		if queued, err := store.RequestCancel("job-1", int(cancelLatencyGrace.Seconds())); !queued || err != nil {
			t.Fatalf("cancel: queued=%v err=%v", queued, err)
		}
		// The stop amendment (the second dispatch; the brief is the first)
		// runs to the grace limit: the scripted cut lands the shared clock
		// exactly on the persisted deadline before it returns.
		team.dispatchCut = func() error {
			if len(team.dispatches) < 2 {
				return nil
			}
			clock.mono = monoT0 + int64(cancelLatencyGrace)
			clock.now = t0.Add(cancelLatencyGrace)
			return errors.New("the stop amendment dispatch was cut")
		}
		team.statusScript = workingStatuses(3)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitCanceled {
			t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
		}
		// The cut landed the clock on the deadline and the stop ended on
		// that same tick: the durable terminal record is exactly at
		// T0 + grace, within the hard bound.
		if !clock.now.Equal(t0.Add(cancelLatencyGrace)) {
			t.Fatalf("terminal at %s, want %s (the cut landed on the deadline and the stop ended there)", clock.now, t0.Add(cancelLatencyGrace))
		}
		assertTerminalWithinHardBound(t, clock, t0, cancelLatencyGrace, cancelLatencyBudget)
		if clock.sleeps != 0 {
			t.Fatalf("sleeps = %d, want 0 (the spent deadline ends the stop on the discovery tick)", clock.sleeps)
		}
		// The stop amendment was the only bounded delivery, limited to the
		// full grace at its start, and its cut wrote exactly one friction
		// line with no delivery_uncertain mark (it is not a queue request).
		if len(team.bounds) != 1 || team.bounds[0] != cancelLatencyGrace {
			t.Fatalf("bounds = %v, want the full grace on the stop amendment", team.bounds)
		}
		if len(team.dispatches) != 2 {
			t.Fatalf("dispatches = %+v, want the brief and the stop amendment", team.dispatches)
		}
		assertStopAmend(t, jobDir)
		if len(friction) != 1 || friction[0] != "job: the supervisor cannot dispatch the stop amendment" {
			t.Fatalf("friction = %v, want exactly the cut line", friction)
		}
		events, err := readEvents(jobDir)
		if err != nil {
			t.Fatal(err)
		}
		if n := countTipe(events, "delivery_uncertain"); n != 0 {
			t.Fatalf("delivery_uncertain = %d, want none (no queue request ran)", n)
		}
		if n := countTipe(events, "push"); n != 0 {
			t.Fatalf("push events = %d, want none (no git on this fixture)", n)
		}
		if got := dirNames(t, filepath.Join(jobDir, "control")); !reflect.DeepEqual(got, []string{"seq"}) {
			t.Fatalf("pending control = %v, want only seq (the cancel retired)", got)
		}
		assertCanceledTerminal(t, store, jobDir, "cancel.json-000001")
	})

	t.Run("the queue delivery consumes the grace limit", func(t *testing.T) {
		var friction []string
		store, jobDir, clock, team, sup := nsSup(t, 0, &friction)
		sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget}
		// A queue request pending before the acceptance.
		if _, err := store.RequestAmend("job-1", []byte("tighten the scope\n")); err != nil {
			t.Fatalf("amend: %v", err)
		}
		t0 := clock.now
		monoT0 := clock.mono
		if queued, err := store.RequestCancel("job-1", int(cancelLatencyGrace.Seconds())); !queued || err != nil {
			t.Fatalf("cancel: queued=%v err=%v", queued, err)
		}
		// The stop amendment (the second dispatch) runs fast and bounded to
		// the full grace; the queue delivery (the third dispatch) runs to
		// the grace limit: the scripted cut lands the shared clock exactly
		// on the persisted deadline before it returns.
		team.dispatchCut = func() error {
			if len(team.dispatches) < 3 {
				return nil
			}
			clock.mono = monoT0 + int64(cancelLatencyGrace)
			clock.now = t0.Add(cancelLatencyGrace)
			return errors.New("the delivery was cut by the grace")
		}
		team.statusScript = workingStatuses(3)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitCanceled {
			t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
		}
		// The cut delivery failed after the deadline passed: the single
		// delivery_uncertain mark, no amend_received, no retry, and the
		// stop ended on that same tick.
		if !clock.now.Equal(t0.Add(cancelLatencyGrace)) {
			t.Fatalf("terminal at %s, want %s (the cut landed on the deadline and the stop ended there)", clock.now, t0.Add(cancelLatencyGrace))
		}
		assertTerminalWithinHardBound(t, clock, t0, cancelLatencyGrace, cancelLatencyBudget)
		events, err := readEvents(jobDir)
		if err != nil {
			t.Fatal(err)
		}
		if n := countTipe(events, "delivery_uncertain"); n != 1 {
			t.Fatalf("delivery_uncertain = %d, want 1 (the cut marks, it does not retry)", n)
		}
		var marked *Event
		for i := range events {
			if events[i].Tipo == "delivery_uncertain" {
				marked = &events[i]
			}
		}
		if marked.Refs["seq_ref"] != "1" {
			t.Fatalf("delivery_uncertain refs = %+v, want seq_ref 1", marked.Refs)
		}
		if n := countTipe(events, "amend_received"); n != 0 {
			t.Fatalf("amend_received = %d, want none (the cut delivery records, it does not deliver)", n)
		}
		// Both deliveries ran bounded to the grace remaining at their
		// start (the full grace), and the request stays pending on the
		// terminal job.
		if len(team.bounds) != 2 || team.bounds[0] != cancelLatencyGrace || team.bounds[1] != cancelLatencyGrace {
			t.Fatalf("bounds = %v, want the full grace on the stop amendment and on the delivery", team.bounds)
		}
		if got := dirNames(t, filepath.Join(jobDir, "control")); !reflect.DeepEqual(got, []string{"amend-000001.md", "seq"}) {
			t.Fatalf("pending control = %v, want the still-pending amend", got)
		}
		assertStopAmend(t, jobDir)
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none (the cut marks, it does not retry)", friction)
		}
		assertCanceledTerminal(t, store, jobDir, "cancel.json-000002")
	})

	t.Run("the final push is cut by the spent finishing budget", func(t *testing.T) {
		var friction []string
		store, jobDir, clock, team, sup := nsSup(t, 0, &friction)
		sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget}
		t0 := clock.now
		// The finishing anchor sits one budget before the acceptance, so
		// the terminal procedure's budget is exactly spent when the stop
		// ends: the worst case in which the final push finds no time left.
		if _, err := store.Record("job-1", func(st *State) {
			st.FinishingStartedAt = t0.Add(-cancelLatencyBudget).Format(time.RFC3339Nano)
		}); err != nil {
			t.Fatal(err)
		}
		// A non-nil git sync so the final push step runs: it is cut by the
		// spent budget before any subprocess, so its Dir is never used.
		sup.Git = &GitSync{Dir: t.TempDir(), Branch: "job/job-1", Base: "main", Repo: "example-org/example-repo"}
		if queued, err := store.RequestCancel("job-1", int(cancelLatencyGrace.Seconds())); !queued || err != nil {
			t.Fatalf("cancel: queued=%v err=%v", queued, err)
		}
		team.statusScript = workingStatuses(15)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitCanceled {
			t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
		}
		// The stop ended on the persisted deadline and the terminal record
		// followed within the same tick: no time past the deadline, no time
		// past the budget.
		if !clock.now.Equal(t0.Add(cancelLatencyGrace)) {
			t.Fatalf("terminal at %s, want %s (the deadline ended the stop and the record followed at once)", clock.now, t0.Add(cancelLatencyGrace))
		}
		assertTerminalWithinHardBound(t, clock, t0, cancelLatencyGrace, cancelLatencyBudget)
		// The cut names the step and the budget: the push never ran past
		// the finishing budget, and a cut of the canceled outcome keeps
		// the canceled outcome (only a done job would be rewritten failed).
		found := false
		for _, line := range friction {
			if line == "job: finishing budget exhausted before the final push" {
				found = true
			}
		}
		if !found {
			t.Fatalf("friction = %v, want the cut line naming the final push", friction)
		}
		if st := supState(t, store); st.Status != StatusCanceled || st.FinishingSynced {
			t.Fatalf("state = %+v, want canceled without a completed final sync", st)
		}
		events, err := readEvents(jobDir)
		if err != nil {
			t.Fatal(err)
		}
		if n := countTipe(events, "push"); n != 0 {
			t.Fatalf("push events = %d, want none (the step was cut before the push)", n)
		}
		if n := countTipe(events, "commit"); n != 0 {
			t.Fatalf("commit events = %d, want none (no step ran)", n)
		}
		if got := dirNames(t, filepath.Join(jobDir, "control")); !reflect.DeepEqual(got, []string{"seq"}) {
			t.Fatalf("pending control = %v, want only seq (the cancel retired)", got)
		}
		assertCanceledTerminal(t, store, jobDir, "cancel.json-000001")
	})

	t.Run("a scenario with no slow step ends one poll at most past the deadline", func(t *testing.T) {
		var friction []string
		store, jobDir, clock, team, sup := nsSup(t, 0, &friction)
		sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget}
		// The acceptance lands one second after the first tick boundary:
		// the supervisor's last tick saw no cancel, so the discovery runs
		// on the next tick (a lag of one second, at most one poll
		// interval), and nothing else is slow (no queue request, no git,
		// a fast stop amendment). T0 is the acceptance.
		var t0 time.Time
		clock.onSleep = func() {
			if clock.sleeps == 1 {
				if queued, err := store.RequestCancel("job-1", int(cancelLatencyGrace.Seconds())); !queued || err != nil {
					t.Fatalf("cancel: queued=%v err=%v", queued, err)
				}
				t0 = clock.now
				// One second after the tick boundary: the next tick is the
				// discovery, one second late.
				clock.mono += int64(time.Second)
				clock.now = clock.now.Add(time.Second)
			}
		}
		team.statusScript = workingStatuses(15)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitCanceled {
			t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
		}
		// The stop ends on the first tick at or past the persisted
		// deadline (T0 + grace, one second off the tick grid): the
		// durable terminal record is exactly T0 + grace + one second —
		// no more than T0 + grace + defaultSupervisorPoll.
		if !clock.now.Equal(t0.Add(cancelLatencyGrace + time.Second)) {
			t.Fatalf("terminal at %s, want %s (the first tick past the deadline)", clock.now, t0.Add(cancelLatencyGrace+time.Second))
		}
		if clock.now.After(t0.Add(cancelLatencyGrace + defaultSupervisorPoll)) {
			t.Fatalf("terminal at %s is past %s (no slow step: at most one poll past the deadline)", clock.now, t0.Add(cancelLatencyGrace+defaultSupervisorPoll))
		}
		// Thirteen 5 s polls from the discovery to the deadline tick.
		if clock.sleeps != 13 {
			t.Fatalf("sleeps = %d, want 13", clock.sleeps)
		}
		// The stop amendment dispatched once, bounded to the grace
		// remaining from the discovery (one second after the acceptance).
		if len(team.bounds) != 1 || team.bounds[0] != cancelLatencyGrace-time.Second {
			t.Fatalf("bounds = %v, want the grace minus the discovery lag on the stop amendment", team.bounds)
		}
		assertStopAmend(t, jobDir)
		events, err := readEvents(jobDir)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"accepted", "worker_spawned", "terminal", "cleanup"}; !reflect.DeepEqual(eventTipes(events), want) {
			t.Fatalf("event types = %v, want %v", eventTipes(events), want)
		}
		if got := dirNames(t, filepath.Join(jobDir, "control")); !reflect.DeepEqual(got, []string{"seq"}) {
			t.Fatalf("pending control = %v, want only seq (the cancel retired)", got)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
		assertCanceledTerminal(t, store, jobDir, "cancel.json-000001")
	})
}

// The in-flight slow steps the pending cancel cuts (this amendment): the
// orchestrator's status read and the periodic git sync run under the cancel
// observer outside the stop, so a cancel that arrives in flight ends only
// the command the adapter (or GitSync) opened — in under 2 s — the tick
// counts no failure (no friction, no status error streak, no refused-push
// block), and the loop starts the next tick without the poll sleep, so its
// deliverControl sees the cancel and starts the stop at once. While a stop
// is running the periodic sync does not run at all: the final commit and
// push are the finishing's, inside its own budget.

// stuckStatusTeam wraps the fake team with the scripted slow orchestrator
// status: its first status read (the watch loop's) blocks on the
// interruptible context until the supervisor's observer cancels it, and
// comes back as the failure the cut is the cause of. Every other read
// passes through to the scripted fake.
type stuckStatusTeam struct {
	*fakeTeam
	// statusCtx is the interruptible context the supervisor handed the
	// in-flight read (nil when the read ran without one).
	statusCtx context.Context
	// stuck makes the next read block until its context is cancelled.
	stuck bool
	// started is closed when the in-flight read blocks.
	started chan struct{}
	// cutAt is when the in-flight read came back (zero when it ran without
	// a context).
	cutAt time.Time
}

func (w *stuckStatusTeam) bounded(d time.Duration) teamOps {
	return &boundFakeTeam{fakeTeam: w.fakeTeam, inner: w, bound: d}
}

func (w *stuckStatusTeam) interruptible(ctx context.Context) teamOps {
	return &stuckStatusCtx{fakeTeam: w.fakeTeam, team: w, ctx: ctx}
}

// Status is the scripted read: the stuck one blocks on the context and
// fails; the rest pass through to the fake.
func (w *stuckStatusTeam) Status(name string) (string, string, error) {
	if w.stuck {
		w.stuck = false
		close(w.started)
		if w.statusCtx != nil {
			<-w.statusCtx.Done()
			w.cutAt = time.Now()
		}
		return "", "", errors.New("the status context was cancelled")
	}
	return w.fakeTeam.Status(name)
}

// stuckStatusCtx records the interruptible context on the way to the
// scripted read, the way the production adapter hands it to the command;
// the embedded fake provides every other operation.
type stuckStatusCtx struct {
	*fakeTeam
	team *stuckStatusTeam
	ctx  context.Context
}

func (w *stuckStatusCtx) Status(name string) (string, string, error) {
	w.team.statusCtx = w.ctx
	return w.team.Status(name)
}

// A cancel that lands while the orchestrator's status read is in flight:
// the read runs under the cancel observer, the observer cancels its context
// the moment the pending cancel appears, the in-flight read the adapter
// opened ends in under 2 s, the tick counts no failure, and the stop starts
// on the next tick without the poll sleep — the terminal record lands at
// most grace + defaultSupervisorPoll past the acceptance.
func TestCancelCutsTheSlowOrchestratorStatus(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 0, &friction)
	sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget}
	watch := &stuckStatusTeam{fakeTeam: team, stuck: true, started: make(chan struct{})}
	sup.Ops = watch
	team.statusScript = workingStatuses(10)
	runDone := make(chan struct {
		exit int
		err  error
	}, 1)
	go func() {
		exit, err := sup.Run()
		runDone <- struct {
			exit int
			err  error
		}{exit, err}
	}()
	// The first watch-loop status read is in flight (blocking on its
	// context): the cancel arrives during the read, with grace 10.
	<-watch.started
	t0 := clock.now
	t0Real := time.Now()
	if queued, err := store.RequestCancel("job-1", 10); !queued || err != nil {
		t.Fatalf("cancel: queued=%v err=%v", queued, err)
	}
	res := <-runDone
	if res.err != nil {
		t.Fatalf("Run: %v", res.err)
	}
	if res.exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d (the cut read must not fail the job)", res.exit, ExitCanceled)
	}
	if watch.cutAt.IsZero() {
		t.Fatalf("the read did not block on the interruptible context")
	}
	// The cut read came back in under 2 s of the acceptance: the observer
	// cancelled the context and the in-flight read ended.
	if elapsed := watch.cutAt.Sub(t0Real); elapsed >= 2*time.Second {
		t.Fatalf("the cut status read took %v after the acceptance, want under 2 s", elapsed)
	}
	// The stop started on the next tick, without the poll sleep: the stop
	// amendment ran bounded to the full grace (a slept tick would have cut
	// the bound by the interval).
	if len(team.bounds) != 1 || team.bounds[0] != 10*time.Second {
		t.Fatalf("bounds = %v, want the full grace on the stop amendment", team.bounds)
	}
	if len(team.dispatches) != 2 {
		t.Fatalf("dispatches = %+v, want the brief and the stop amendment", team.dispatches)
	}
	assertStopAmend(t, jobDir)
	// The terminal record is exactly at the deadline (the stop ended on
	// the first tick at or past it) and within the hard bound.
	if !clock.now.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("terminal at %s, want %s (the stop ended on the deadline)", clock.now, t0.Add(10*time.Second))
	}
	assertTerminalWithinHardBound(t, clock, t0, 10*time.Second, cancelLatencyBudget)
	// The cut read counted no failure: no friction and no status error
	// streak (three failed reads would have failed the job).
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none (the cut read counts no failure)", friction)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := countTipe(events, "delivery_uncertain"); n != 0 {
		t.Fatalf("delivery_uncertain = %d, want none (the cut read is not a queue delivery)", n)
	}
	assertCanceledTerminal(t, store, jobDir, "cancel.json-000001")
}

// supCancelGhRules is the fake gh of the cancel git fixture: the list
// answers an existing draft pull request, so no create runs.
var supCancelGhRules = []fakecli.Rule{{Argv: ghListArgv(), Stdout: supGitPRJSON}}

// supCancelGitFixture is the nsSup-shaped fixture over a real git job
// worktree: the store and the supervisor share the nanosecond test clock
// (the ticks and the stop deadline run on test time), the git sync is the
// real one (a real git push to the local bare remote), and the team is the
// fake (the scripted working orchestrator). The supGitFixture supplies the
// git fixture (the worktree, the bare remote and the hook helpers).
type supCancelGitFixture struct {
	git      *supGitFixture
	store    *Store
	jobDir   string
	clock    *nsClock
	team     *fakeTeam
	friction *[]string
	sup      *Supervisor
}

func newSupCancelGitFixture(t *testing.T, ghRules []fakecli.Rule) *supCancelGitFixture {
	t.Helper()
	git := newSupGitFixture(t, ghRules)
	root := t.TempDir()
	clock := &nsClock{now: time.Date(2026, 10, 9, 10, 0, 0, 0, time.FixedZone("-03:00", -3*3600))}
	store := Open(root)
	store.Clock = clock
	if _, err := store.Start("job-1", []byte(supTestBrief)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("job-1", func(st *State) {
		st.Dir = git.job.dir
		st.Branch = gitSyncBranch
		st.Base = "main"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", StatusRunning); err != nil {
		t.Fatal(err)
	}
	team := &fakeTeam{spawnName: "orch-1"}
	var friction []string
	sup := &Supervisor{
		Store:    store,
		ID:       "job-1",
		Ops:      team,
		Clock:    clock,
		Git:      &GitSync{Env: git.job.base.env, Dir: git.job.dir, Branch: gitSyncBranch, Base: "main", Repo: gitSyncRepo},
		Friction: func(message string) { friction = append(friction, message) },
	}
	return &supCancelGitFixture{
		git: git, store: store, jobDir: filepath.Join(root, "jobs", "job-1"),
		clock: clock, team: team, friction: &friction, sup: sup,
	}
}

// waitHookCalls polls the bare remote's pre-receive call log until at least
// want calls are logged (the push is in flight), failing the test as a
// fixture error when they never arrive. A partial line (the hook logging
// while the poll reads) is a retry, not an error.
func waitHookCalls(t *testing.T, f *supGitFixture, want int) {
	t.Helper()
	log := filepath.Join(f.job.base.fakeDir, remoteHookName+".calls.jsonl")
	deadline := time.Now().Add(scaledDuration(10 * time.Second))
	for {
		if calls, err := fakecli.ReadCalls(log); err == nil && len(calls) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture error: the held push logged fewer than %d pre-receive call(s) before the deadline", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A cancel that lands while the periodic git sync's push is in flight (a
// real push held at the bare remote, the holdRemotePush pattern): the sync
// runs under the cancel observer, the observer cancels the sync's context
// the moment the pending cancel appears, the git push GitSync opened ends
// in under 2 s, no push failure friction and no refused-push block count,
// and the stop starts at once on the next tick. The branch is still
// unpushed, so the finishing's final push runs inside its own budget and is
// cut by its own step bound.
func TestCancelCutsTheStalledPeriodicPush(t *testing.T) {
	f := newSupCancelGitFixture(t, nil)
	f.sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget, Git: scaledDuration(time.Second)}
	f.git.job.commitIn(t, "feat: first")
	holdRemotePush(t, f.git, scaledDuration(8*time.Second))
	f.team.statusScript = workingStatuses(10)
	// The periodic sync is due on the sixth tick (the 30 s cadence on the
	// 5 s default poll): its push is the one the cancel cuts.
	runDone := make(chan struct {
		exit int
		err  error
	}, 1)
	go func() {
		exit, err := f.sup.Run()
		runDone <- struct {
			exit int
			err  error
		}{exit, err}
	}()
	// The held push is in flight (the remote's pre-receive fake logged the
	// call): the cancel arrives during the push, with grace 10.
	waitHookCalls(t, f.git, 1)
	t0Real := time.Now()
	t0 := f.clock.now
	if queued, err := f.store.RequestCancel("job-1", 10); !queued || err != nil {
		t.Fatalf("cancel: queued=%v err=%v", queued, err)
	}
	res := <-runDone
	if res.err != nil {
		t.Fatalf("Run: %v", res.err)
	}
	if res.exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d (the cut push must not fail the job)", res.exit, ExitCanceled)
	}
	// The cut push and the stop came at once: the terminal is well inside
	// the hold (the push is not run to its end) plus the cut slack.
	if elapsed := time.Since(t0Real); elapsed > scaledDuration(5*time.Second) {
		t.Fatalf("the terminal came %v after the acceptance, want the cut push and the stop at once", elapsed)
	}
	// The stop started on the next tick, without the poll sleep: the stop
	// amendment ran bounded to the full grace (a slept tick would have cut
	// the bound by the interval).
	if len(f.team.bounds) != 1 || f.team.bounds[0] != 10*time.Second {
		t.Fatalf("bounds = %v, want the full grace on the stop amendment", f.team.bounds)
	}
	// The cut tick ended at the sync: the only unbounded orchestrator reads
	// are the five ticks before it, and every later read is the stop's,
	// limited to the grace (a read after the cut could end the job done).
	if unbounded := f.team.statusCalls - len(f.team.statusBounds); unbounded != 5 {
		t.Fatalf("unbounded status reads = %d, want 5 (none in the tick the cancel cut)", unbounded)
	}
	// The terminal record is exactly at the deadline and within the hard
	// bound.
	if !f.clock.now.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("terminal at %s, want %s (the stop ended on the deadline)", f.clock.now, t0.Add(10*time.Second))
	}
	assertTerminalWithinHardBound(t, f.clock, t0, 10*time.Second, cancelLatencyBudget)
	events, err := readEvents(f.jobDir)
	if err != nil {
		t.Fatal(err)
	}
	// The cut periodic push counts no failure: no push event (the
	// finishing's final push is cut by its own step bound and records none
	// either), no refused-push block, and the new commit was announced
	// before the push.
	if n := countTipes(events, "push"); n != 0 {
		t.Fatalf("push events = %d, want none (the periodic push was cut, the finishing push by its step bound)", n)
	}
	if n := countTipes(events, "blocked"); n != 0 {
		t.Fatalf("blocked events = %d, want none (no refused-push block)", n)
	}
	if n := countTipes(events, "commit"); n != 1 {
		t.Fatalf("commit events = %d, want the one announced before the push", n)
	}
	// The friction is exactly the finishing's cut final push (its message
	// and the detail line saying it timed out): the periodic cut wrote none.
	if len(*f.friction) != 2 || (*f.friction)[0] != "job: git sync failed: push" || !strings.HasPrefix((*f.friction)[1], "job: git sync push failed: timed out") {
		t.Fatalf("friction = %v, want exactly the finishing's cut final push", *f.friction)
	}
	// Both pushes reached the remote (the cut periodic and the cut
	// finishing): the hook ran twice.
	requireRemoteHookCalls(t, f.git, 2)
	assertCanceledTerminal(t, f.store, f.jobDir, "cancel.json-000001")
}

// While a stop is running the tick runs no periodic git sync: the cadence
// that is due on a stop tick does not push (the final commit and push are
// the finishing's, inside its budget). The cancel is accepted before the
// first stop tick and the cadence is due on every tick (a 30 s poll is one
// tick per cadence): a periodic pass would run on both stop ticks and push
// into the diverged remote ref (a refused push that would block the job),
// which the stop must not do.
func TestNoPeriodicSyncWhileStopping(t *testing.T) {
	f := newSupCancelGitFixture(t, supCancelGhRules)
	f.sup.Poll = 30 * time.Second // one tick per cadence: the sync is due on every tick
	f.sup.FinishingLimits = finishingLimits{Budget: cancelLatencyBudget}
	f.git.job.commitIn(t, "feat: first")
	f.team.statusScript = workingStatuses(10)
	var t0 time.Time
	f.clock.onSleep = func() {
		if f.clock.sleeps == 1 {
			t0 = f.clock.now
			if queued, err := f.store.RequestCancel("job-1", 30); !queued || err != nil {
				t.Fatalf("cancel: queued=%v err=%v", queued, err)
			}
			// The remote job ref diverges from the local head (the pre-cancel
			// push created it): every later push is refused, so a periodic
			// pass on a stop tick would block the job for the refused push.
			sha, err := f.git.job.base.git(f.git.job.base.bare, "rev-parse", "refs/heads/main")
			if err != nil {
				t.Fatalf("rev-parse main: %v", err)
			}
			tree, err := f.git.job.base.git(f.git.job.base.bare, "rev-parse", "main^{tree}")
			if err != nil {
				t.Fatalf("rev-parse main tree: %v", err)
			}
			diverge, err := f.git.job.base.git(f.git.job.base.bare, "commit-tree", strings.TrimSpace(tree), "-p", strings.TrimSpace(sha), "-m", "diverge")
			if err != nil {
				t.Fatalf("commit-tree: %v", err)
			}
			if out, err := f.git.job.base.git(f.git.job.base.bare, "update-ref", "refs/heads/"+gitSyncBranch, strings.TrimSpace(diverge)); err != nil {
				t.Fatalf("update-ref: %s: %v", out, err)
			}
		}
	}
	exit, err := f.sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	// The terminal record is exactly at the deadline and within the hard
	// bound.
	if !f.clock.now.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("terminal at %s, want %s (the stop ended on the deadline)", f.clock.now, t0.Add(30*time.Second))
	}
	assertTerminalWithinHardBound(t, f.clock, t0, 30*time.Second, cancelLatencyBudget)
	events, err := readEvents(f.jobDir)
	if err != nil {
		t.Fatal(err)
	}
	// No periodic sync ran on the stop ticks: a refused push of a periodic
	// pass would have blocked the job and pushed again; the only push is
	// the finishing's final one (refused by the diverged ref).
	if n := countTipes(events, "blocked"); n != 0 {
		t.Fatalf("blocked events = %d, want none (no periodic sync ran on the stop ticks)", n)
	}
	if n := countTipes(events, "push"); n != 2 {
		t.Fatalf("push events = %d, want the pre-cancel push and the finishing's final one", n)
	}
	if n := countTipes(events, "commit"); n != 1 {
		t.Fatalf("commit events = %d, want the one announced by the pre-cancel sync", n)
	}
	if n := countTipes(events, "pr_opened"); n != 1 {
		t.Fatalf("pr_opened events = %d, want the one from the pre-cancel sync", n)
	}
	if len(*f.friction) != 0 {
		t.Fatalf("friction = %v, want none (a refused push counts no failure)", *f.friction)
	}
	assertCanceledTerminal(t, f.store, f.jobDir, "cancel.json-000001")
}
