package job

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
)

// supervise.go is the job supervisor: the process job start launches in the
// first pane of the job's Herdr workspace. It makes sure the job
// orchestrator has a lane, spawns it in the job worktree, dispatches the
// job's brief to it once, and then watches it until its report lands or it
// stops, moving the job through running, blocked and finishing to a
// terminal outcome with the contract's events and report.json. It composes
// the CLI's own session, spawn, dispatch, status and release commands
// behind teamOps. Git sync, control delivery, the job budget, the wake hook
// and the final release are later slices; their extension points are the
// tick comment and the terminal-return TODO.

// startWait is how long the supervisor waits for the job to become running.
const startWait = 60 * time.Second

// defaultSupervisorPoll is the watch poll when Supervisor.Poll is zero.
const defaultSupervisorPoll = 5 * time.Second

// supervisorExit maps a terminal outcome to the contract exit code, as
// jobOutcomeExit in internal/cli does: the CLI imports the job package, so
// the mapping lives here and the CLI keeps its own for display.
func supervisorExit(status string) int {
	switch status {
	case StatusDone:
		return 0
	case StatusBlocked:
		return ExitBlocked
	case StatusTimeout:
		return ExitTimeout
	case StatusFailed:
		return ExitFailed
	case StatusCanceled:
		return ExitCanceled
	default:
		return 0
	}
}

// The terminal procedure (the finishing phase) has its own budget and one
// bound per step inside it; the zero finishingLimits are these defaults.
const (
	defaultFinishingBudget     = 10 * time.Minute
	defaultFinishingGitStep    = 5 * time.Minute
	defaultFinishingGHStep     = 2 * time.Minute
	defaultFinishingCollabStep = defaultTimeoutGrace
)

// finishingLimits are the bounds of the terminal procedure: the whole
// budget and the final git sync, draft pull request and collaboration
// steps inside it. A zero field is its default; tests set small values.
type finishingLimits struct {
	Budget time.Duration
	Git    time.Duration
	GH     time.Duration
	Collab time.Duration
}

func (l finishingLimits) git() time.Duration    { return limitOr(l.Git, defaultFinishingGitStep) }
func (l finishingLimits) gh() time.Duration     { return limitOr(l.GH, defaultFinishingGHStep) }
func (l finishingLimits) collab() time.Duration { return limitOr(l.Collab, defaultFinishingCollabStep) }

func limitOr(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// finishingBudgetMotivo is the motivo of a done job whose final push or
// draft pull request was cut by the terminal procedure's budget.
const finishingBudgetMotivo = "tempo de finalização esgotado"

// Supervisor watches one job. The loop sleeps through Clock.Sleep, never
// time.Sleep directly; a zero Poll is 5 s.
type Supervisor struct {
	Store    *Store
	ID       string
	Ops      teamOps
	Clock    Clock
	Poll     time.Duration
	Friction func(string)

	// FinishingLimits bound the terminal procedure; the zero value is the
	// defaults.
	FinishingLimits finishingLimits

	// Wake: the machine's job wake hook; nil or an empty Cmd disables it
	// (no worker, no cursor).
	Wake *WakeHook

	// PrivateLaunch: this supervisor was started by job start with the
	// private launch context (set by the CLI); only then does waitRunning
	// refuse a running or blocked state written inside a failure
	// (Store.Fail's walk).
	PrivateLaunch bool
	// actorEnv is the supervisor's own environment, without the private
	// names: the environment of every agent pane of the job workspace
	// (actor_session.go). Set with PrivateLaunch.
	actorEnv platform.Env
	// launchSel is the starter's user configuration selection of the
	// private launch (set with PrivateLaunch).
	launchSel core.UserSelection

	// Git sync and release: the git sync of the job branch — nil in
	// workspace mode, which runs no git at all — and the Herdr adapter
	// that closes the job workspace last.
	Git   *GitSync
	Herdr herdrWorkspaces

	// ProjectRoot is the trusted project root the supervisor binds the
	// collaborations it owns on (supervise_collab.go): the same root
	// resolution the CLI uses for collaborations, never derived from an
	// assignment record; an empty root owns nothing.
	ProjectRoot string

	// Git sync bookkeeping (pushNow, the immediate push ask of job
	// checkpoint, is declared with the control fields): the ticks since the
	// last sync, the last synced head, the announced
	// commit shas, the first commit's title, and this process's draft
	// pull request.
	lastPushTick int
	lastHead     string
	announced    map[string]bool
	prTitle      string
	lastPR       *PullRequest

	// Tick bookkeeping: consecutive bad observations (the gone-family
	// states, status errors) and the unknown states already reported to
	// friction. tickCutByCancel marks the tick the pending cancel cut one
	// of its slow calls (the orchestrator's status read, the periodic git
	// sync): the cut counted no failure (no friction, no error streak, no
	// refused-push block), and the watch loop starts the next tick without
	// sleeping the poll interval, so its deliverControl sees the cancel and
	// starts the stop at once.
	goneStreak      int
	errStreak       int
	frictioned      map[string]bool
	tickCutByCancel bool

	// Wake bookkeeping: the queue and its worker (nil when the hook is
	// disabled) and the highest event seq queued in this process
	// (initialized from the cursor).
	wakeQueue  chan Event
	wakeDone   chan struct{}
	wakeQueued int

	// Control delivery and the job budget (supervise_control.go): pushNow is
	// set here and read and cleared by the git sync slice; stopping is "" or
	// the kind the stop path ends with (canceled, timeout, or the blocked
	// ceiling — which ends through the existing timeout outcome) and
	// stopDeadline its Clock.Monotonic deadline; warnedActive and
	// warnedBlocked guard the single timeout_warning and blocked_warning;
	// intervalBase is the virtual Monotonic base of the current budget
	// interval it counts from, captured once on the first budget check of
	// this process (ensureIntervalBase) and re-anchored at every settled
	// blocked-boundary crossing (settleAndTransition).
	pushNow         bool
	stopping        string
	stopDeadline    int64
	warnedActive    bool
	warnedBlocked   bool
	intervalBase    int64
	intervalBaseSet bool

	// collabAsked guards the single ask of the job's active collaborations
	// in this process (supervise_collab.go): a restarted supervisor
	// re-asks once in its own process, the same accepted bound as the stop
	// amendment.
	collabAsked bool

	// collabEnded guards the single collaboration capture of this
	// process's terminal path: finishOutcome and fail capture and
	// persist the outcomes before the terminal transition, and finalize
	// then settles the persisted outcomes only; a fresh process over a
	// terminal job (a restart) has not captured and runs the capture in
	// finalize instead.
	collabEnded bool

	// collabUnenumerated records that this process's capture could not
	// enumerate the job's collaborations by the deadline: finalize then
	// refuses the settlement, the release, the cleanup and the workspace
	// close exactly as a fresh process's failed capture does, and a later
	// supervisor pass retries the capture.
	collabUnenumerated bool

	// The terminal procedure's budget (finishing_started_at): finBase is
	// the virtual Monotonic base the budget counts from, captured once per
	// process from the persisted anchor (ensureFinishingBase); stepDeadline
	// and stepName are the Monotonic deadline and the name of the git or
	// pull request step now running (0 when none), which the GitSync
	// timeouts are clamped to; askDeadline bounds the collaboration stop
	// request while the terminal path's capture runs (0 outside it).
	// finBudget is the budget recorded with the job at start
	// (finishing_timeout_min), read with the anchor by
	// ensureFinishingBase; 0 when the state predates the field.
	finBase      int64
	finBaseSet   bool
	finBudget    time.Duration
	stepDeadline int64
	stepName     string
	askDeadline  int64
}

// NewSupervisor wires the production supervisor over a recorded job: the
// job worktree from state.json is the working directory of every team
// command, the clock is the store's, the git sync watches the job branch
// (workspace mode has none), and the Herdr adapter owns the workspace
// close.
func NewSupervisor(store *Store, id string, env platform.Env, selfExe string, friction func(string)) (*Supervisor, error) {
	snap, err := store.Snapshot(id)
	if err != nil {
		return nil, err
	}
	if snap.State.Dir == "" {
		return nil, errUsage("job: the job has no worktree directory")
	}
	s := &Supervisor{
		Store:       store,
		ID:          id,
		Ops:         selfCLI{Exe: selfExe, Dir: snap.State.Dir, Env: env},
		Clock:       store.clock(),
		Friction:    friction,
		Herdr:       newHerdrCLI(env, friction),
		ProjectRoot: platform.StateProjectRoot(env, snap.State.Dir),
	}
	if snap.State.Modo != "workspace" && snap.State.Branch != "" {
		// A branch job without a recorded branch has no push refspec, so
		// it is not synced; workspace mode runs no git at all. The draft
		// pull request's repo comes from brief.json; a missing or
		// unreadable brief leaves it empty and the pr step errors on use.
		var repo string
		if dir, err := store.readDir(id); err == nil {
			if fields, err := readBriefFields(dir); err == nil {
				repo = jsonString(fields, "repo")
			}
		}
		s.Git = &GitSync{Env: env, Dir: snap.State.Dir, Branch: snap.State.Branch, Base: snap.State.Base, Repo: repo}
	}
	return s, nil
}

func (s *Supervisor) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return defaultSupervisorPoll
}

func (s *Supervisor) clockSleep(d time.Duration) {
	if s.Clock != nil {
		s.Clock.Sleep(d)
		return
	}
	systemClock{}.Sleep(d)
}

// monoToMs converts a monotonic nanosecond difference into the whole
// milliseconds the budget counters persist: the single conversion point
// between the clock's nanosecond Monotonic readings and the persisted
// millisecond counts, so nothing compares or stores the raw nanoseconds.
func monoToMs(nano int64) int64 {
	return nano / int64(time.Millisecond)
}

// ensureIntervalBase captures the process's virtual Monotonic base for the
// current budget interval, once per process: the first reading minus the
// wall elapsed since the persisted anchor (IntervalStart, or StartedAt on
// a state without it), clamped at zero so a future anchor starts the
// interval at zero. The wall clock is compared only here; from then on the
// in-flight interval is counted on the monotonic clock only, so a backwards
// Now never shortens or extends it.
//
// Legacy rule: a blocked state without an IntervalStart is a job started
// before the anchor field existed, whose blocked interval's start is
// unknown — the in-flight interval is counted as zero and the anchor is
// re-anchored to now (persisted), losing at most the interval's elapsed
// tail and never double-counting. A blocked state with a recorded anchor
// always trusts it: every blocked-boundary crossing settles and re-anchors
// in the same state write (settleAndTransition), so a crash at a crossing
// leaves the complete pre-crossing state (old status, old counters, old
// anchor) and never a blocked status with a stale job-start anchor.
//
// The anchor is written with RFC3339Nano (sub-second resolution) and read
// back with the same layout; whole-second anchors written before the
// nanosecond precision (or by formatTS callers) parse unchanged.
func (s *Supervisor) ensureIntervalBase(st State) error {
	if s.intervalBaseSet {
		return nil
	}
	if st.Status == StatusBlocked && st.IntervalStart == "" {
		// Legacy state: the blocked interval's start is unrecorded: re-anchor
		// to now and count the in-flight interval as zero.
		s.intervalBase = s.Clock.Monotonic()
		s.intervalBaseSet = true
		return s.recordIntervalAnchor(st)
	}
	anchor := st.IntervalStart
	if anchor == "" {
		anchor = st.StartedAt
	}
	s.intervalBase = s.Clock.Monotonic()
	if anchor != "" {
		if start, err := time.Parse(time.RFC3339Nano, anchor); err == nil {
			if elapsed := s.Clock.Now().Sub(start); elapsed > 0 {
				s.intervalBase -= int64(elapsed)
			}
		}
	}
	s.intervalBaseSet = true
	return nil
}

// recordIntervalAnchor persists the interval anchor at the current wall
// time: the re-anchor record the legacy rule leaves behind, so the next
// process re-derives its virtual base from the same anchor. A no-op when
// the anchor already carries it. The anchor is written with RFC3339Nano
// (sub-second resolution): a whole-second anchor would shift a restarted
// in-flight interval by up to 999 ms.
func (s *Supervisor) recordIntervalAnchor(st State) error {
	now := s.Clock.Now().Format(time.RFC3339Nano)
	if st.IntervalStart == now {
		return nil
	}
	_, err := s.Store.Record(s.ID, func(st *State) { st.IntervalStart = now })
	return err
}

// settleAndTransition settles the finished budget interval — the whole
// milliseconds of the virtual Monotonic elapsed since the process's
// interval base, clamped at zero, into the counter of the finished
// interval's kind (blocked when finishedBlocked, active otherwise) and the
// re-anchor at the crossing — and moves the job along the crossing edge in
// one locked state write (TransitionSettle): the status change and the
// settle land together, so a crash at the crossing leaves either the
// pre-crossing state (the finished interval still in flight, of its old
// kind) or the post-crossing state (settled and re-anchored) — never the
// window the separate Transition + settle writes left, which a restart
// would misread as an in-flight interval of the new status's kind. The
// process's virtual base moves to the crossing instant.
func (s *Supervisor) settleAndTransition(st State, to string, finishedBlocked bool) error {
	return s.settleAndTransitionWith(st, to, finishedBlocked, nil)
}

// settleAndTransitionWith is settleAndTransition with extra facts (update,
// may be nil) recorded in the same locked write as the crossing.
func (s *Supervisor) settleAndTransitionWith(st State, to string, finishedBlocked bool, update func(*State)) error {
	if err := s.ensureIntervalBase(st); err != nil {
		return err
	}
	before := s.Clock.Monotonic()
	elapsed := before - s.intervalBase
	if elapsed < 0 {
		elapsed = 0
	}
	ms := monoToMs(int64(elapsed))
	if _, err := s.Store.TransitionSettle(s.ID, to, func(st *State) {
		if finishedBlocked {
			st.BudgetBlockedMs += ms
		} else {
			st.BudgetActiveMs += ms
		}
		st.IntervalStart = s.Clock.Now().Format(time.RFC3339Nano)
		if update != nil {
			update(st)
		}
	}); err != nil {
		return err
	}
	s.intervalBase = before
	s.intervalBaseSet = true
	return nil
}

// collabDeadline is the shared deadline of the collaboration wait on a
// terminal path: the stop deadline when a stop is running in this process
// (cancel and timeout keep the stop's original grace, never restarted or
// extended), one defaultTimeoutGrace from now for done and failed and for
// a fresh process over a terminal job.
func (s *Supervisor) collabDeadline() int64 {
	if s.stopping != "" {
		return s.stopDeadline
	}
	return s.Clock.Monotonic() + int64(defaultTimeoutGrace.Nanoseconds())
}

// persistCollabOutcomes merges the captured outcomes into the persisted
// ones (the no-late-erasure rule) and records the merge into the state,
// the single source of the report's colaboracoes. An empty merge is a
// no-op that rewrites nothing.
func (s *Supervisor) persistCollabOutcomes(outcomes []CollaborationOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}
	_, err := s.Store.Record(s.ID, func(st *State) {
		st.Colaboracoes = mergeCollabOutcomes(st.Colaboracoes, outcomes)
	})
	return err
}

// Run drives the job to a terminal outcome. The exit code follows the
// outcome (done 0, failed 19, canceled 21, timeout 9, blocked 7); err is
// non-nil only for unexpected store failures, which exit with the
// conservative failed code.
func (s *Supervisor) Run() (int, error) {
	// The wake worker runs for the whole process and drains on every
	// return, so waking events behind the cursor are run even when the
	// supervisor returns before the watch loop (an already-ended job, a
	// failed start wait). A panic is a crash: it does not drain, and the
	// cursor reruns the events on the next start.
	s.startWake()
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
		s.finishWakes()
	}()
	// The terminal intent (persisted before the terminal transition on
	// every terminal path): a crash left the outcome behind without the
	// transition, and this process converges on it instead of re-deriving
	// the outcome. A job already terminal keeps today's path (finalize
	// again; settle the persisted outcomes; never re-ask or rewrite a
	// persisted outcome).
	if snap, err := s.Store.Snapshot(s.ID); err == nil {
		intent := snap.State.TerminalIntent
		if intent != nil && !isTerminalOutcome(snap.State.Status) &&
			snap.State.Status != StatusCollected && snap.State.Status != StatusClosed {
			if intent.Status == StatusFailed {
				if err := s.fail(intent.Motivo, intent.Exit); err != nil {
					return ExitFailed, err
				}
				return intent.Exit, nil
			}
			if snap.State.Status == StatusRunning || snap.State.Status == StatusBlocked {
				if _, err := s.Store.TransitionSettle(s.ID, StatusFinishing, s.anchorFinishing); err != nil {
					s.frictionf("job: the supervisor could not enter finishing: " + err.Error())
					return ExitFailed, err
				}
			}
			code, _, err := s.finishOutcome(intent.Status, intent.Motivo, intent.Exit)
			if err != nil {
				return ExitFailed, err
			}
			return code, nil
		}
		// The pending-decision path entered finishing with its motivo and
		// no intent, and a crash came before the crossing to blocked:
		// complete it, then watch the blocked job as usual.
		if intent == nil && snap.State.Status == StatusFinishing &&
			snap.State.Motivo != nil && *snap.State.Motivo == decisionsPendingMotivo {
			if _, _, err := s.blockOnPendingDecisions(snap.State, snap.State.Orchestrator); err != nil {
				return ExitFailed, err
			}
		}
	}
	code, done, err := s.waitRunning()
	if err != nil {
		return ExitFailed, err
	}
	if done {
		// A job that already ended may not have finished its release (a
		// crash before the workspace close): finalize again; it skips the
		// steps already done. A job without a recorded workspace has no
		// release to finish. A workspace whose close is already recorded
		// but whose log still lacks the cleanup event finalizes again as
		// well: the first pass's release failed and the close never
		// depended on the release, so the cleanup stayed pending behind a
		// closed workspace — finalize re-evaluates the release (a release
		// of an agent that no longer exists counts as done), appends the
		// pending cleanup and skips the close the flag already records.
		if snap, snapErr := s.Store.Snapshot(s.ID); snapErr == nil && isTerminalOutcome(snap.State.Status) {
			finalizePass := snap.State.WorkspaceID != "" && !snap.State.WorkspaceClosed
			if snap.State.WorkspaceID != "" && snap.State.WorkspaceClosed {
				// The log is the marker of the pending cleanup; an
				// unreadable log or directory retries the pass (finalize
				// re-reads, and the log check is its own append gate).
				cleanupDone := false
				if dir, dirErr := s.Store.readDir(s.ID); dirErr == nil {
					if events, evErr := readEvents(dir); evErr == nil {
						for _, event := range events {
							if event.Tipo == "cleanup" {
								cleanupDone = true
								break
							}
						}
					}
				}
				finalizePass = !cleanupDone
			}
			if finalizePass {
				s.finishWakes()
				s.finalize(snap.State.Status)
			}
			// A crash between the terminal record and the retirement of the
			// cancel leaves it pending on the terminal job: retire it
			// idempotently and send nothing.
			s.retirePendingCancel()
		}
		return code, nil
	}
	// The job lane (and its max_workers bump) is set once, before the spawn
	// intent is recorded; a restarted supervisor (the intent is recorded, or
	// the orchestrator is) skips it, so the bump is never applied twice.
	if snap, err := s.Store.Snapshot(s.ID); err != nil {
		return ExitFailed, err
	} else if snap.State.Orchestrator == "" && snap.State.OrchestratorSpawnAt == "" {
		if err := s.Ops.EnsureJobLane(); err != nil {
			if err := s.fail("job: cannot ensure the job lane", ExitHerdr); err != nil {
				return ExitFailed, err
			}
			return ExitHerdr, nil
		}
	}
	// The workspace's own session file carries the job team and state
	// root for the agents (actor_session.go): after the job lane, before
	// the orchestrator, again on every restart. A pin the job needs and
	// cannot make fails the job before any agent runs without it.
	if err := s.pinActorSession(); err != nil {
		if err := s.fail("job: cannot pin the job workspace session", ExitUsage); err != nil {
			return ExitFailed, err
		}
		return ExitUsage, nil
	}
	name, done, err := s.ensureOrchestrator()
	if err != nil {
		return ExitFailed, err
	}
	if done {
		return ExitHerdr, nil
	}
	code, done, err = s.ensureDispatched(name)
	if err != nil {
		return ExitFailed, err
	}
	if done {
		return code, nil
	}
	for {
		s.tickCutByCancel = false
		code, done, err := s.tick()
		if err != nil {
			return ExitFailed, err
		}
		if done {
			return code, nil
		}
		if s.tickCutByCancel {
			// The pending cancel cut this tick's slow call: the next tick
			// starts at once (no poll sleep) so its deliverControl sees the
			// cancel and starts the stop.
			continue
		}
		s.clockSleep(s.poll())
	}
}

// waitRunning waits for the running transition of job start. It reads the
// state every poll for at most 60 s of polls: a running or a blocked job
// proceeds at once (a blocked job is alive — the orchestrator is waiting
// on a question, and the watch loop keeps enforcing the budgets and the
// blocked ceiling while it stays blocked), a terminal state returns its
// outcome code at once, and a job still not running after the bound fails
// with exit 19. The bound counts sleep iterations, not wall-clock
// differences, so a moving clock cannot end the wait early. Only a
// privately scoped supervisor (PrivateLaunch) refuses a running or
// blocked snapshot written inside a failure: the store's failure walk
// appends the failure event and saves running and finishing before
// failed, so a snapshot read in that window is a failure in flight, not
// the start's running, and the wait keeps polling; every other
// supervisor accepts the snapshot at once, exactly as before.
func (s *Supervisor) waitRunning() (int, bool, error) {
	poll := s.poll()
	maxSleeps := int(startWait / poll)
	if maxSleeps < 1 {
		maxSleeps = 1
	}
	for i := 0; ; i++ {
		snap, err := s.Store.Snapshot(s.ID)
		if err == nil {
			if snap.State.Status == StatusRunning || snap.State.Status == StatusBlocked {
				// A failure in flight is not the start's running: the store's
				// failure walk appends the failure event and saves running and
				// finishing before failed, so a snapshot read in that window is
				// refused below and the wait keeps polling. The refusal applies
				// only to this supervisor's own private scoped execution
				// (PrivateLaunch): a supervisor without the private context
				// accepts exactly as before.
				if !s.PrivateLaunch {
					return 0, false, nil
				}
				dir, dirErr := s.Store.readDir(s.ID)
				accepted := dirErr == nil
				if dirErr == nil {
					// A failed directory or events read, or a log that already
					// holds a failure event, is refused: keep polling like a
					// not-yet-running state.
					events, eventsErr := readEvents(dir)
					accepted = eventsErr == nil && !hasTipo(events, "failure")
				}
				if accepted {
					return 0, false, nil
				}
				// Refused: fall through to the sleep and keep polling.
			}
			if isTerminalOutcome(snap.State.Status) {
				return supervisorExit(snap.State.Status), true, nil
			}
		}
		if i >= maxSleeps {
			break
		}
		s.clockSleep(poll)
	}
	if err := s.fail("job: supervisor started before the job was running", ExitFailed); err != nil {
		return ExitFailed, true, err
	}
	return ExitFailed, true, nil
}

// ensureOrchestrator records the job orchestrator and appends its
// worker_spawned event exactly once. A fresh job records the spawn intent
// (OrchestratorSpawnAt) before spawning: a crash between the spawn and the
// Orchestrator record leaves the intent without the name, and the
// restarted supervisor recovers the orchestrator it already spawned —
// adopting it when its identity is proven, spawning when it is proven that
// none exists, and failing the job closed without spawning otherwise. A
// crash between the Orchestrator record and the event append leaves the
// name recorded with no event: the event is appended once, keyed on the
// name. done is true when the spawn failed or the recovery failed closed
// (the job already failed); err is a store failure.
func (s *Supervisor) ensureOrchestrator() (string, bool, error) {
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		return "", false, err
	}
	if snap.State.Orchestrator != "" {
		// A crash between the record and the append left the name with no
		// worker_spawned: append it once when the spawn intent shows the
		// name came from this job's spawn. A name without the intent (an
		// older record or a recovery-free restart) is untouched.
		if snap.State.OrchestratorSpawnAt != "" {
			if err := s.appendWorkerSpawnedOnce(snap.State.Orchestrator); err != nil {
				return "", false, err
			}
		}
		return snap.State.Orchestrator, false, nil
	}
	if snap.State.OrchestratorSpawnAt != "" {
		// The spawn intent is recorded and the name is not: the spawn ran
		// and a crash lost the record. Recover the orchestrator it left.
		return s.recoverOrchestrator(snap.State)
	}
	// The spawn intent is recorded before the spawn, so the crash window
	// between the spawn and the record still leaves it behind for a
	// restart to recover from.
	spawnAt := formatTS(s.Store.clock().Now())
	if _, err := s.Store.Record(s.ID, func(st *State) { st.OrchestratorSpawnAt = spawnAt }); err != nil {
		return "", false, err
	}
	return s.spawnOrchestrator()
}

// recoverOrchestrator is the restart path of ensureOrchestrator when the
// spawn intent is recorded and the name is not: the crash happened after
// the spawn. The roster gives only the identity (name, role, pane); the
// liveness of each job-orchestrator candidate in the job's own workspace
// comes from the authoritative Status read the supervisor already uses.
// A single live candidate is adopted — recorded and evented exactly as the
// normal path does, without a spawn. With no candidate, a workspace whose
// only pane is the supervisor's own root pane proves the orchestrator
// pane is gone, and the orchestrator is spawned as normal; a gone
// candidate counts as no candidate. Anything else — a status read error,
// more than one candidate, a roster read error, an invalid or missing workspace, a multi-pane
// workspace, a list error, no Herdr adapter — fails the job closed without
// spawning: the supervisor never spawns blind and never releases or closes
// a pane it did not prove is the job's.
func (s *Supervisor) recoverOrchestrator(st State) (string, bool, error) {
	rows, err := s.Ops.Roster()
	if err != nil {
		return s.failOrchestratorRecovery()
	}
	var candidates []rosterRow
	for _, row := range rows {
		if row.Role != "job-orchestrator" {
			continue
		}
		// The candidate's pane must live in the job's own workspace: the
		// text before the first : of the pane, compared to a recorded id
		// that is one safe path segment. The roster STATE column is not
		// read: a spaced tab label before it shifts it.
		workspace, _, ok := strings.Cut(row.Pane, ":")
		if !ok || !validWorkspaceID(st.WorkspaceID) || workspace != st.WorkspaceID {
			continue
		}
		// Liveness is the candidate's own status read: gone is not a
		// candidate; a failed read fails the recovery closed; every other
		// state keeps the row a candidate.
		state, _, err := s.Ops.Status(row.Name)
		if err != nil {
			return s.failOrchestratorRecovery()
		}
		if state == "gone" {
			continue
		}
		candidates = append(candidates, row)
	}
	if len(candidates) == 1 {
		// Adopt the orchestrator the crash left alive: no spawn.
		name := candidates[0].Name
		if _, err := s.Store.Record(s.ID, func(st *State) { st.Orchestrator = name }); err != nil {
			return "", false, err
		}
		if _, err := s.Store.Append(s.ID, EventIn{
			Tipo:   "worker_spawned",
			Resumo: "job orchestrator spawned",
			Refs:   map[string]string{"agente": name, "papel": "job-orchestrator"},
		}); err != nil {
			return "", false, err
		}
		return name, false, nil
	}
	if len(candidates) > 1 {
		// Two live orchestrators in the job workspace: the identity is
		// unproven.
		return s.failOrchestratorRecovery()
	}
	if s.Herdr == nil || !validWorkspaceID(st.WorkspaceID) {
		// No adapter, or a recorded id that is not one safe path segment:
		// the job workspace cannot be identified in the list.
		return s.failOrchestratorRecovery()
	}
	workspaces, err := s.Herdr.List()
	if err != nil {
		return s.failOrchestratorRecovery()
	}
	for _, workspace := range workspaces {
		if workspace.ID != st.WorkspaceID {
			continue
		}
		if workspace.PaneCount != 1 {
			// A pane beyond the supervisor's own root pane may be the
			// crashed orchestrator's: the identity is unproven.
			return s.failOrchestratorRecovery()
		}
		// One pane, the supervisor's own root pane: no orchestrator pane
		// exists, so spawn as normal.
		return s.spawnOrchestrator()
	}
	// The job workspace is missing from the list.
	return s.failOrchestratorRecovery()
}

// failOrchestratorRecovery is the fail-closed of the recovery: the job
// fails with the fixed motivo and the Herdr exit code, without spawning
// and without releasing or closing a pane the supervisor did not prove is
// the job's.
func (s *Supervisor) failOrchestratorRecovery() (string, bool, error) {
	if err := s.fail("job: cannot establish the job orchestrator", ExitHerdr); err != nil {
		return "", true, err
	}
	return "", true, nil
}

// appendWorkerSpawnedOnce appends the worker_spawned event for name when no
// worker_spawned event with that refs.agente exists yet: the crash lost the
// append after the record, and exactly one such event ever lands.
func (s *Supervisor) appendWorkerSpawnedOnce(name string) error {
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		return err
	}
	events, err := readEvents(jobDir)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Tipo == "worker_spawned" && event.Refs["agente"] == name {
			return nil
		}
	}
	_, err = s.Store.Append(s.ID, EventIn{
		Tipo:   "worker_spawned",
		Resumo: "job orchestrator spawned",
		Refs:   map[string]string{"agente": name, "papel": "job-orchestrator"},
	})
	return err
}

// spawnOrchestrator spawns the job orchestrator, records its name, and
// appends the worker_spawned event. A crash between the spawn and the
// record leaves the spawn intent for the recovery; a crash between the
// record and the append leaves the name for the worker_spawned backfill.
// done is true when the spawn failed (the job already failed); err is a
// store failure.
func (s *Supervisor) spawnOrchestrator() (string, bool, error) {
	name, err := s.Ops.SpawnOrchestrator()
	if err != nil {
		// The message stays fixed; the cause goes to the job's friction log.
		var exitErr *ExitError
		if errors.As(err, &exitErr) && exitErr.Detail != "" {
			s.frictionf("job: spawn the job orchestrator failed: " + exitErr.Detail)
		}
		if err := s.fail("job: cannot spawn the job orchestrator", ExitHerdr); err != nil {
			return "", true, err
		}
		return "", true, nil
	}
	if _, err = s.Store.Record(s.ID, func(st *State) { st.Orchestrator = name }); err != nil {
		return "", false, err
	}
	if _, err = s.Store.Append(s.ID, EventIn{
		Tipo:   "worker_spawned",
		Resumo: "job orchestrator spawned",
		Refs:   map[string]string{"agente": name, "papel": "job-orchestrator"},
	}); err != nil {
		return "", false, err
	}
	return name, false, nil
}

// ensureDispatched writes the orchestrator brief (the job brief plus the job
// section) and dispatches it once; a restart with DispatchedAt set never
// dispatches again. The exit code is returned when the dispatch (or the
// missing brief) failed; err is a store failure.
func (s *Supervisor) ensureDispatched(name string) (int, bool, error) {
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		return 0, false, err
	}
	if snap.State.DispatchedAt != "" {
		return 0, false, nil
	}
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		return 0, false, err
	}
	brief, err := os.ReadFile(filepath.Join(jobDir, "brief.md"))
	if err != nil {
		if err := s.fail("job: the job brief is missing", ExitFailed); err != nil {
			return ExitFailed, true, err
		}
		return ExitFailed, true, nil
	}
	brief = append(brief, orchestratorBriefSection(s.ID)...)
	path := filepath.Join(jobDir, "orchestrator-brief.md")
	if err := writeAtomic0600(path, brief); err != nil {
		return 0, false, err
	}
	if err := s.Ops.Dispatch(name, path, false); err != nil {
		if err := s.fail("job: cannot dispatch the job orchestrator", ExitHerdr); err != nil {
			return ExitFailed, true, err
		}
		return ExitHerdr, true, nil
	}
	// DispatchedAt comes from the store clock: RFC 3339 with an explicit
	// offset.
	dispatchedAt := formatTS(s.Store.clock().Now())
	if _, err := s.Store.Record(s.ID, func(st *State) { st.DispatchedAt = dispatchedAt }); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

// orchestratorBriefSection is the section appended to the job brief for the
// job orchestrator, with the job id filled in.
func orchestratorBriefSection(id string) []byte {
	return []byte("\n## Job\n\n- Job id: " + id +
		"\n- Publish decisions, questions, checkpoints and notes with `soho job note --id " + id + " --tipo <type> ...`." +
		"\n- Ask for an immediate push after a commit with `soho job checkpoint --id " + id + "`.\n")
}

// tick runs one watch iteration: the wake queue first (it only enqueues),
// then the dispatcher's control requests (amend, send, checkpoint,
// cancel), then the git sync (a checkpoint request pushes in the same tick;
// a rejected push blocks the job before the orchestrator is read; while a
// stop is running the periodic sync does not run at all), then, while a
// stop is running, the job budget (the stop path's end); otherwise the job
// orchestrator — read once, the blocked-boundary crossing first (a block
// entry's settle lands before the tick's budget check, so the active time
// accrued up to the block is enforced while blocked), then the job budget
// against the post-crossing state, then the done and gone-family outcomes.
// Outside the stop the two slow steps (the periodic git sync and the
// orchestrator's status read) run under the cancel observer: a pending
// cancel that arrives in flight cuts them without counting a failure (no
// friction, no error streak, no refused-push block), and the tick ends so
// the next starts at once, without the poll sleep, and its deliverControl
// starts the stop.
func (s *Supervisor) tick() (int, bool, error) {
	if code, done, err := s.dispatchWakes(); err != nil || done {
		return code, true, err
	}
	if code, done, err := s.deliverControl(); err != nil || done {
		return code, done, err
	}
	if code, done, err := s.syncGit(); err != nil || done {
		return code, done, err
	}
	if s.tickCutByCancel {
		// The cancel cut the sync: the tick ends here, before any status
		// read or transition, and the next tick starts the stop.
		return 0, false, nil
	}
	if s.stopping != "" {
		// A stop is already running: the stop path owns the job (a fresh
		// orchestrator read for the done check, no crossing).
		return s.checkBudget("")
	}
	// No stop running: checkOrchestrator reads the orchestrator once,
	// runs the blocked-boundary crossing first, then the budget check
	// against the post-crossing state (reusing this read for the stop
	// path's done check), then the done and gone-family outcomes.
	return s.checkOrchestrator()
}

// checkOrchestrator maps one status read of the job orchestrator to the
// job's transitions, events and outcome. The blocked-boundary crossing
// (block or unblock) runs first, when the read shows one: its settle lands
// before this tick's budget check, so a block entry's accrued active time
// is counted by that check while the job is blocked. The budget check then
// runs against the post-crossing state, reusing this tick's read for the
// stop path's done check (no second read); a stop that starts this tick
// ends the job on a done read (endStop), so the done and gone-family
// outcomes below are reached only when no stop is running. The gone streak
// counts consecutive gone-family observations and drops on any observation
// that shows the orchestrator alive; the error streak counts consecutive
// failed reads and drops on any read that succeeds. A done state without a
// readable report counts like a gone observation.
func (s *Supervisor) checkOrchestrator() (int, bool, error) {
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.goneStreak = 0
		return s.accumulate()
	}
	st := snap.State
	// The status read runs under the cancel observer (the same watch as the
	// in-flight delivery): a pending cancel that arrives while the read is
	// in flight ends only the read this adapter opened, and the cut is not
	// a failure (no friction, no error streak) — the tick ends without
	// sleeping and the next tick's deliverControl starts the stop.
	statusCtx, stopWatching := s.watchForCancel()
	state, reportPath, err := s.interruptibleOps(statusCtx).Status(st.Orchestrator)
	cut := statusCtx.Err() != nil
	stopWatching()
	if err != nil {
		if cut {
			s.goneStreak = 0
			s.tickCutByCancel = true
			return 0, false, nil
		}
		s.goneStreak = 0
		return s.accumulate()
	}
	s.errStreak = 0
	if aliveOrchestratorState[state] {
		s.goneStreak = 0
	}
	switch state {
	case "working", "no-report-yet", "idle", "compacting":
		// Nothing; a block this function set is lifted by the working
		// state.
		if code, done, err := s.unblockOrchestrator(st); err != nil || done {
			return code, done, err
		}
	case "blocked", "question":
		motivo := "dialog"
		if state == "question" {
			motivo = "question"
		}
		if code, done, err := s.blockOrchestrator(st, st.Orchestrator, motivo); err != nil || done {
			return code, done, err
		}
	case "quota":
		if code, done, err := s.blockOrchestrator(st, st.Orchestrator, "quota"); err != nil || done {
			return code, done, err
		}
	case "provider-error", "capacity":
		if code, done, err := s.blockOrchestrator(st, st.Orchestrator, "provider_error"); err != nil || done {
			return code, done, err
		}
	}
	// The budget check against the post-crossing state; a stop that starts
	// this tick reuses this tick's read (state) for the done check.
	if code, done, err := s.checkBudget(state); err != nil || done {
		return code, done, err
	}
	// No stop is running: the done and gone-family outcomes. The crossing
	// states (handled above) are no-ops here: nothing more this tick.
	switch state {
	case "done":
		return s.onOrchestratorDone(st, st.Orchestrator, reportPath)
	case "gone", "settled-no-report", "not-received", "unavailable":
		s.goneStreak++
		if s.goneStreak >= 3 {
			if err := s.fail("job: the job orchestrator ended without a report", ExitFailed); err != nil {
				return ExitFailed, true, err
			}
			return ExitFailed, true, nil
		}
		return 0, false, nil
	case "working", "no-report-yet", "idle", "compacting", "blocked", "question", "quota", "provider-error", "capacity":
		return 0, false, nil
	default:
		s.frictionUnknown(state)
		return 0, false, nil
	}
}

// accumulate counts a failed observation (a status error or an unreadable
// state): three in a row ends the job as failed.
func (s *Supervisor) accumulate() (int, bool, error) {
	s.errStreak++
	if s.errStreak < 3 {
		return 0, false, nil
	}
	if err := s.fail("job: the job orchestrator ended without a report", ExitFailed); err != nil {
		return ExitFailed, true, err
	}
	return ExitFailed, true, nil
}

// blockOrchestrator blocks a running job for the orchestrator's motivo and
// appends the blocked event; a job that is not running is left to the next
// state. The crossing is one locked write: the transition and the settle
// (the counters and the re-anchor) land together, so the blocked interval
// starts at the crossing and a crash at the crossing never leaves the
// transition without the settle.
func (s *Supervisor) blockOrchestrator(st State, name, motivo string) (int, bool, error) {
	if st.Status != StatusRunning {
		return 0, false, nil
	}
	if err := s.settleAndTransition(st, StatusBlocked, false); err != nil {
		return 0, true, err
	}
	if _, err := s.Store.Record(s.ID, func(st *State) { st.Motivo = &motivo }); err != nil {
		return 0, true, err
	}
	if _, err := s.Store.Append(s.ID, EventIn{
		Tipo: "blocked",
		Refs: map[string]string{"agente": name, "motivo": motivo},
	}); err != nil {
		return 0, true, err
	}
	return 0, false, nil
}

// aliveOrchestratorState are the states that show the orchestrator alive;
// they drop the gone streak. The gone-family states, the done state (a done
// without a readable report must keep accumulating) and the unknown ones
// leave the streak alone.
var aliveOrchestratorState = map[string]bool{
	"working": true, "no-report-yet": true, "idle": true, "compacting": true,
	"blocked": true, "question": true, "quota": true,
	"provider-error": true, "capacity": true,
}

// orchestratorMotivos are the stored motivos this function sets on a block;
// only those are lifted by a working state.
var orchestratorMotivos = map[string]bool{
	"dialog": true, "question": true, "quota": true, "provider_error": true,
}

// unblockOrchestrator lifts a block this function set: the job moves back
// to running, the stored motivo clears, and the unblocked event lands. The
// crossing is one locked write: the transition and the settle (the counters
// and the re-anchor) land together, so the active interval starts at the
// unblock and a crash at the crossing never leaves the transition without
// the settle.
func (s *Supervisor) unblockOrchestrator(st State) (int, bool, error) {
	if st.Status != StatusBlocked || st.Motivo == nil {
		return 0, false, nil
	}
	if !orchestratorMotivos[*st.Motivo] {
		return 0, false, nil
	}
	if err := s.settleAndTransition(st, StatusRunning, true); err != nil {
		return 0, true, err
	}
	if _, err := s.Store.Record(s.ID, func(st *State) { st.Motivo = nil }); err != nil {
		return 0, true, err
	}
	if _, err := s.Store.Append(s.ID, EventIn{Tipo: "unblocked"}); err != nil {
		return 0, true, err
	}
	return 0, false, nil
}

// onOrchestratorDone handles the report landing: the report is copied into
// the job dir, the worker_done event lands, the job moves to finishing, and
// the outcome follows the partial count and the pending decisions.
func (s *Supervisor) onOrchestratorDone(st State, name, reportPath string) (int, bool, error) {
	if st.Status == StatusBlocked && st.Motivo != nil && *st.Motivo == decisionsPendingMotivo {
		// The outcome was already derived: the loop keeps waiting for the
		// decision to be resolved, and derives nothing again.
		return 0, false, nil
	}
	// A regular file only, opened without blocking: a FIFO at the report
	// path reads as no report instead of holding the supervisor.
	raw, err := platform.ReadRegularFile(reportPath)
	if err != nil {
		// A done state without a readable report counts like a gone one.
		s.goneStreak++
		if s.goneStreak >= 3 {
			if err := s.fail("job: the job orchestrator ended without a report", ExitFailed); err != nil {
				return ExitFailed, true, err
			}
			return ExitFailed, true, nil
		}
		return 0, false, nil
	}
	partials := reportscan.PartialCount(string(raw))
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		return 0, true, err
	}
	reportMD := filepath.Join(jobDir, "report.md")
	if err := writeAtomic0600(reportMD, raw); err != nil {
		return 0, true, err
	}
	if _, err := s.Store.Append(s.ID, EventIn{
		Tipo:   "worker_done",
		Resumo: fmt.Sprintf("job orchestrator reported, %d partial", partials),
		Refs:   map[string]string{"agente": name, "report": reportMD},
	}); err != nil {
		return 0, true, err
	}
	// The outcome is derived before the job enters finishing, and the
	// entry records it in the same locked write: a crash can never leave
	// the job finishing without the terminal intent (or, for the pending
	// path, without the motivo the restart completes the crossing from).
	if partials == 0 {
		if err := s.enterFinishing(&TerminalIntent{Status: StatusDone}, nil); err != nil {
			return 0, true, err
		}
		return s.finishOutcome(StatusDone, "", 0)
	}
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		return 0, true, err
	}
	if len(snap.Pending) > 0 {
		// Partial items with an open decision: the job stays alive,
		// blocked, until the decision lands; the loop keeps running. The
		// active interval (finishing counts as active) settles and
		// re-anchors with the crossing, in one locked write.
		motivo := decisionsPendingMotivo
		if err := s.enterFinishing(nil, &motivo); err != nil {
			return 0, true, err
		}
		return s.blockOnPendingDecisions(st, name)
	}
	motivo := "itens parciais"
	if err := s.enterFinishing(&TerminalIntent{Status: StatusFailed, Motivo: motivo, Exit: ExitFailed}, &motivo); err != nil {
		return 0, true, err
	}
	return s.finishOutcome(StatusFailed, motivo, ExitFailed)
}

// decisionsPendingMotivo is the motivo of a job whose orchestrator
// finished with partial items that still have an open decision.
const decisionsPendingMotivo = "decisions pending"

// enterFinishing moves the job to finishing and records the terminal
// intent and the motivo (either may be nil: left unchanged) in the same
// locked state write; a job already finishing records them with one
// Record. Either way the job is never durably finishing without what the
// restart converges on. Entering finishing from blocked is a
// blocked-boundary crossing like any other: the blocked interval settles
// into the blocked clock and the anchor moves to the crossing in the same
// write, so time spent blocked is never charged to the active budget (from
// running, both sides are active and nothing settles, as before).
func (s *Supervisor) enterFinishing(intent *TerminalIntent, motivo *string) error {
	err := s.enterFinishingWrite(intent, motivo)
	if err != nil {
		// The returned error is the store's own (callers match its code);
		// the text and the step go to friction.
		s.frictionf("job: the supervisor could not enter finishing: " + err.Error())
	}
	return err
}

// enterFinishingWrite is the state write of enterFinishing. The finishing
// anchor (the wall ts the terminal procedure's budget counts from) lands
// only with the terminal intent: the provisional entry of the pending
// decision path (no intent, straight back to blocked) must not start the
// budget while the job waits for the decision.
func (s *Supervisor) enterFinishingWrite(intent *TerminalIntent, motivo *string) error {
	update := func(st *State) {
		if intent != nil {
			s.anchorFinishing(st)
			st.TerminalIntent = intent
		}
		if motivo != nil {
			st.Motivo = motivo
		}
	}
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		return err
	}
	if snap.State.Status == StatusFinishing {
		_, err = s.Store.Record(s.ID, update)
		return err
	}
	if snap.State.Status == StatusBlocked {
		return s.settleAndTransitionWith(snap.State, StatusFinishing, true, update)
	}
	_, err = s.Store.TransitionSettle(s.ID, StatusFinishing, update)
	return err
}

// blockOnPendingDecisions completes the pending-decision path from
// finishing: the crossing to blocked (the active interval settles and
// re-anchors in the same write) and the blocked event. A restart over a
// job left finishing with the pending motivo and no terminal intent (a
// crash between the two writes) completes it the same way.
func (s *Supervisor) blockOnPendingDecisions(st State, name string) (int, bool, error) {
	if err := s.settleAndTransition(st, StatusBlocked, false); err != nil {
		return 0, true, err
	}
	if _, err := s.Store.Append(s.ID, EventIn{
		Tipo: "blocked",
		Refs: map[string]string{"agente": name, "motivo": decisionsPendingMotivo},
	}); err != nil {
		return 0, true, err
	}
	return 0, false, nil
}

// finishOutcome commits the terminal outcome in the contract order: the
// terminal intent is persisted first (a crash leaves it for a restart to
// converge on), the final push and draft pull request run while the job is
// still finishing, the collaboration outcomes are captured and persisted
// before the terminal transition (so the partial facts exist before the
// outcome is visible), the terminal event lands at most once, and the
// transition publishes report.json — built from the state's persisted
// outcomes — before the retire, the wake drain and the finalize (the
// settlement, the release, the cleanup event, the workspace close).
func (s *Supervisor) finishOutcome(status, motivo string, exit int) (int, bool, error) {
	// The terminal intent is durable before anything else: a crash between
	// the intent and the terminal transition leaves the outcome for the
	// restart instead of re-deriving it.
	intent := &TerminalIntent{Status: status, Motivo: motivo, Exit: exit}
	if _, err := s.Store.Record(s.ID, func(st *State) {
		s.anchorFinishing(st)
		st.TerminalIntent = intent
	}); err != nil {
		s.frictionf("job: the supervisor could not record the terminal intent: " + err.Error())
		return 0, true, err
	}
	// A terminal event an earlier pass already published is final: its
	// outcome (the recorded intent's) stands even when the deadline is
	// already spent and the sync below is cut. The read is the job's own
	// events, before the finalSync, with the same readDir/readEvents the
	// append below uses, and its error goes to friction and back, like the
	// append's read.
	terminalDir, err := s.Store.readDir(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor could not read the job directory for the terminal event: " + err.Error())
		return 0, true, err
	}
	terminalEvents, err := readEvents(terminalDir)
	if err != nil {
		s.frictionf("job: the supervisor could not read the events for the terminal event: " + err.Error())
		return 0, true, err
	}
	announced := hasTipo(terminalEvents, "terminal")
	// The final push happens while the job is still finishing, so its events
	// precede the terminal event. It runs inside the terminal procedure's
	// budget: a done job whose final push or pull request was cut by it is
	// not done, and the rewritten failed outcome is durable before anything
	// else, so a restart converges on it — unless the terminal event
	// already announced the outcome, which the rewrite may not undo.
	s.ensureFinishingBase()
	if cut := s.finalSync(status); cut && status == StatusDone && !announced {
		status, motivo, exit = StatusFailed, finishingBudgetMotivo, ExitFailed
		intent = &TerminalIntent{Status: status, Motivo: motivo, Exit: exit}
		stored := motivo
		if _, err := s.Store.Record(s.ID, func(st *State) {
			st.TerminalIntent = intent
			st.Motivo = &stored
		}); err != nil {
			s.frictionf("job: the supervisor could not record the terminal intent: " + err.Error())
			return 0, true, err
		}
	}
	// The collaboration capture: the outcomes are merged into the state
	// (the persisted ones stand forever) and persisted before the
	// terminal transition; an enumeration that could not be restored by
	// the deadline still persists every known id, and the friction names
	// it.
	outcomes, capErr := s.endCollaborationsFinishing()
	if capErr != nil {
		s.frictionf("job: the supervisor cannot enumerate the collaborations: " + capErr.Error())
		s.collabUnenumerated = true
	}
	s.collabEnded = true
	if err := s.persistCollabOutcomes(outcomes); err != nil {
		s.frictionf("job: the supervisor could not persist the collaboration outcomes: " + err.Error())
		return 0, true, err
	}
	// The terminal event lands at most once: a crashed earlier pass may
	// have appended it already, and the restart converges without a
	// duplicate.
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor could not read the job directory for the terminal event: " + err.Error())
		return 0, true, err
	}
	events, err := readEvents(jobDir)
	if err != nil {
		s.frictionf("job: the supervisor could not read the events for the terminal event: " + err.Error())
		return 0, true, err
	}
	if !hasTipo(events, "terminal") {
		refs := map[string]string{"exit": strconv.Itoa(exit)}
		if motivo != "" {
			refs["motivo"] = motivo
		}
		if _, err := s.Store.Append(s.ID, EventIn{Tipo: "terminal", Refs: refs}); err != nil {
			s.frictionf("job: the supervisor could not append the terminal event: " + err.Error())
			return 0, true, err
		}
	}
	if err := s.transitionTerminal(status); err != nil {
		// The terminal state was not saved within the budget: the job stays
		// finishing with its terminal intent, and finalize never runs for it.
		return 0, true, err
	}
	// The job is terminal: a cancel still pending (it stays pending through
	// the stop so a restart finds it) has nothing left to deliver.
	s.retirePendingCancel()
	// The terminal events wake the dispatcher too; the worker drains
	// before the release and the workspace close.
	s.finishWakes()
	s.finalize(status)
	return exit, true, nil
}

// clk is the supervisor's clock, the system clock when none is set.
func (s *Supervisor) clk() Clock {
	if s.Clock != nil {
		return s.Clock
	}
	return systemClock{}
}

// anchorFinishing records the terminal procedure's anchor (the wall ts its
// budget counts from) when the state has none: it runs inside the locked
// write that records the terminal intent or enters finishing, and never
// overwrites an anchor that is there, so a restart keeps the deadline.
func (s *Supervisor) anchorFinishing(st *State) {
	if st.FinishingStartedAt == "" {
		st.FinishingStartedAt = s.clk().Now().Format(time.RFC3339Nano)
	}
}

// ensureFinishingBase captures the process's virtual Monotonic base of the
// terminal procedure's budget, once per process, like ensureIntervalBase:
// the first reading minus the wall time elapsed since the persisted anchor,
// clamped at zero so a future anchor counts as no time spent. The wall
// clock is compared only here; from then on the budget runs on the
// monotonic clock only, so a backwards Now never extends it, and a restart
// counts the downtime. A state that holds a terminal intent but no anchor (a
// job finishing before the field existed) is anchored now and the anchor is
// persisted.
func (s *Supervisor) ensureFinishingBase() {
	if s.finBaseSet {
		return
	}
	s.finBaseSet = true
	clock := s.clk()
	s.finBase = clock.Monotonic()
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor could not read the finishing anchor: " + err.Error())
		return
	}
	if min := snap.State.FinishingTimeoutMin; min > 0 {
		s.finBudget = time.Duration(min) * time.Minute
	}
	anchor := snap.State.FinishingStartedAt
	if anchor == "" {
		if _, err := s.Store.Record(s.ID, s.anchorFinishing); err != nil {
			s.frictionf("job: the supervisor could not record the finishing anchor: " + err.Error())
		}
		return
	}
	if start, err := time.Parse(time.RFC3339Nano, anchor); err == nil {
		if elapsed := clock.Now().Sub(start); elapsed > 0 {
			s.finBase -= int64(elapsed)
		}
	}
}

// finishingBudget is the length of the terminal procedure's budget: the
// test seam FinishingLimits.Budget when set, else the value recorded with
// the job at start (read once per process by ensureFinishingBase, never
// from the machine configuration), else the built-in 10 minutes for a state
// that predates the field. Every use of the budget (the deadline, the
// terminal write retries, the step caps) goes through it, after
// ensureFinishingBase.
func (s *Supervisor) finishingBudget() time.Duration {
	if s.FinishingLimits.Budget > 0 {
		return s.FinishingLimits.Budget
	}
	if s.finBudget > 0 {
		return s.finBudget
	}
	return defaultFinishingBudget
}

// finDeadline is the Monotonic deadline of the terminal procedure's budget.
func (s *Supervisor) finDeadline() int64 {
	return s.finBase + int64(s.finishingBudget())
}

// finRemaining is what is left of the terminal procedure's budget.
func (s *Supervisor) finRemaining() time.Duration {
	return time.Duration(s.finDeadline() - s.clk().Monotonic())
}

// beginFinStep starts one bounded step of the terminal procedure: its
// deadline is the step's own bound or the budget's end, whichever comes
// first. A step with no time left is skipped with one friction line naming
// it, and reports false.
func (s *Supervisor) beginFinStep(name string, bound time.Duration) bool {
	now := s.clk().Monotonic()
	deadline := now + int64(bound)
	if end := s.finDeadline(); end < deadline {
		deadline = end
	}
	if deadline <= now {
		s.frictionf("job: finishing budget exhausted before " + name)
		return false
	}
	s.stepDeadline, s.stepName = deadline, name
	return true
}

// stepCut reports whether the step now running reached its deadline (a
// subprocess killed by the clamped timeout counts as the step being cut) and
// names it in friction.
func (s *Supervisor) stepCut() bool {
	if s.stepDeadline == 0 || s.clk().Monotonic() < s.stepDeadline {
		return false
	}
	s.frictionf("job: finishing budget exhausted during " + s.stepName)
	return true
}

// finCap is the GitSync timeout clamp: while a finishing step runs, every
// subprocess timeout is the original default or the step's remaining time,
// whichever is shorter, never below one millisecond (a zero would mean no
// timeout) and rounded up to whole milliseconds so the kill never lands
// before the step's deadline.
func (s *Supervisor) finCap(d time.Duration) time.Duration {
	if s.stepDeadline == 0 {
		return d
	}
	remaining := time.Duration(s.stepDeadline - s.clk().Monotonic())
	if remaining < time.Millisecond {
		remaining = time.Millisecond
	}
	remaining = (remaining + time.Millisecond - 1) / time.Millisecond * time.Millisecond
	if remaining < d {
		return remaining
	}
	return d
}

// finCollabDeadline is the deadline of the collaboration wait of the
// terminal path: the collaboration step's bound from now, or the stop's own
// deadline when a stop is running in this process, and in both cases never
// past the end of the terminal procedure's budget.
func (s *Supervisor) finCollabDeadline() int64 {
	deadline := s.clk().Monotonic() + int64(s.FinishingLimits.collab())
	if s.stopping != "" {
		deadline = s.stopDeadline
	}
	if end := s.finDeadline(); end < deadline {
		deadline = end
	}
	return deadline
}

// endCollaborationsFinishing is the collaboration capture of the terminal
// path inside the terminal procedure's budget: the wait and the stop
// request's commands are bounded by finCollabDeadline.
func (s *Supervisor) endCollaborationsFinishing() ([]CollaborationOutcome, error) {
	s.ensureFinishingBase()
	deadline := s.finCollabDeadline()
	s.askDeadline = deadline
	defer func() { s.askDeadline = 0 }()
	return s.endCollaborations(deadline)
}

// transitionTerminal writes the terminal state (idempotent on a state that
// is already terminal). A failed write is retried at the poll cadence while
// the terminal procedure's budget remains, at least once in total and at
// most one attempt per poll of the budget (a clock that does not advance
// cannot hold it); each distinct error text goes to friction once, and the
// last error is returned unchanged when the budget is spent.
func (s *Supervisor) transitionTerminal(status string) error {
	s.ensureFinishingBase()
	maxRetries := int(s.finishingBudget() / s.poll())
	lastText := ""
	for retries := 0; ; retries++ {
		_, err := s.Store.Transition(s.ID, status)
		if err == nil {
			return nil
		}
		if text := "job: the supervisor could not record the terminal state " + status + ": " + err.Error(); text != lastText {
			s.frictionf(text)
			lastText = text
		}
		if s.finRemaining() <= 0 || retries >= maxRetries {
			return err
		}
		s.clockSleep(s.poll())
	}
}

// frictionUnknown reports an unknown orchestrator state to friction, once
// per state.
func (s *Supervisor) frictionUnknown(state string) {
	if s.frictioned == nil {
		s.frictioned = map[string]bool{}
	}
	if s.frictioned[state] || s.Friction == nil {
		return
	}
	s.frictioned[state] = true
	s.Friction("job: the job orchestrator reports an unknown state '" + state + "'")
}

// fail records the failure and returns its store error: an unexpected
// store failure (the state read, or the crossing write of a blocked exit)
// exits through Run with the conservative failed code instead of being
// retried, and the next supervisor start converges. From blocked the
// failure exit settles the blocked interval and lands the failed terminal
// intent in the same locked write as the blocked -> finishing crossing
// (the contract's atomic crossing): the blocked tail goes to the blocked
// clock and the anchor moves to the crossing, so a hard stop at the write
// leaves either the pre-crossing blocked state (the tail still in flight,
// no intent) or the committed finishing state (settled, re-anchored, with
// the intent) — never the separate Record + Fail writes, which left the
// blocked tail on neither clock. Any other status
// records the intent first, best effort, and the Store.Fail walk runs
// from that status as before.
func (s *Supervisor) fail(motivo string, exit int) error {
	intent := &TerminalIntent{Status: StatusFailed, Motivo: motivo, Exit: exit}
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		// The status is unknown, so the unsettled walk must not run.
		s.frictionf("job: the supervisor could not record the failure: " + err.Error())
		return err
	}
	if snap.State.Status == StatusBlocked {
		// The failure exit from blocked: the settle, the re-anchor and the
		// intent land with the crossing in one write; the Store.Fail walk
		// then runs from finishing.
		if err := s.settleAndTransitionWith(snap.State, StatusFinishing, true, func(st *State) {
			s.anchorFinishing(st)
			st.TerminalIntent = intent
		}); err != nil {
			// The job stays blocked and unsettled: the failure walk refused
			// to run from a state it could not cross, and the restart
			// converges on the still-blocked job.
			s.frictionf("job: the supervisor could not record the failure: " + err.Error())
			return err
		}
	} else {
		// The terminal intent, best effort: on error, friction and continue
		// — the failure still has to be recorded.
		if _, err := s.Store.Record(s.ID, func(st *State) {
			s.anchorFinishing(st)
			st.TerminalIntent = intent
		}); err != nil {
			s.frictionf("job: the supervisor could not record the terminal intent: " + err.Error())
		}
	}
	s.ensureFinishingBase()
	s.finalSync(StatusFailed)
	// The collaboration capture, persisted before the failure walk; an
	// enumeration that could not be restored by the deadline still
	// persists every known id, and the friction names it.
	outcomes, capErr := s.endCollaborationsFinishing()
	if capErr != nil {
		s.frictionf("job: the supervisor cannot enumerate the collaborations: " + capErr.Error())
		s.collabUnenumerated = true
	}
	s.collabEnded = true
	if err := s.persistCollabOutcomes(outcomes); err != nil {
		s.frictionf("job: the supervisor could not persist the collaboration outcomes: " + err.Error())
	}
	if _, err := s.Store.Fail(s.ID, motivo, exit); err != nil {
		// The failed state was not saved: the job stays finishing with its
		// terminal intent, so the pending cancel is not retired, finalize
		// (the cleanup event, the release, the workspace close) never runs
		// for it, and the store's own error is returned unwrapped.
		s.frictionf("job: the supervisor could not record the failure: " + err.Error())
		return err
	}
	// The job is terminal: a pending cancel has nothing left to deliver.
	s.retirePendingCancel()
	// The terminal events wake the dispatcher too; the worker drains
	// before the release and the workspace close.
	s.finishWakes()
	s.finalize(StatusFailed)
	return nil
}
