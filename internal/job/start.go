package job

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// start.go is the non-dry-run `job start`: it probes the Herdr server,
// prepares the checkout and worktree (slice 5), records the job, creates
// the job workspace, writes the team session file, launches the supervisor
// in the workspace's first pane, and moves the job to running. It stops at
// the first failure; a failure after the job is recorded ends in
// Store.Fail, which writes the failed state, the events and the report.
// This slice only launches `job supervise`; the supervisor itself is the
// next slice.

// afterWorkspaceCreate is a test hook: it runs after Herdr.Create succeeds
// and before the workspace id is recorded. Tests point it at a panic to
// simulate a crash in that window and restore it with t.Cleanup.
var afterWorkspaceCreate func()

// StartRequest is the validated input for one non-dry-run job start. The
// CLI already validated the id, the repo and the base regexes and the
// machine label; Prepare and Store re-validate what they own.
type StartRequest struct {
	ID         string
	Org        string
	Repo       string
	Base       string
	Mode       string
	Brief      []byte
	TimeoutMin int
	Team       Team
}

// Starter composes the non-dry-run job start. Every step runs against the
// injected Preparer, herdrWorkspaces and herdrPanes, so tests substitute
// fakes for the herdr CLI.
type Starter struct {
	Env         platform.Env
	Machine     Machine
	Preparer    Preparer
	Herdr       herdrWorkspaces
	Panes       herdrPanes
	SelfExe     string
	StateRoot   func(checkout string) string
	Friction    func(string)
	RawFriction func(string)
}

// NewStarter wires the production starter: the machine's Preparer with the
// default production clone, and one herdr CLI for both the workspace and
// the pane calls. The raw friction sink carries raw subprocess output
// (the unknown create envelope) to the friction log only; a nil sink drops
// it.
func NewStarter(env platform.Env, machine Machine, selfExe string, stateRoot func(string) string, friction func(string), rawFriction func(string)) Starter {
	herdr := newHerdrCLI(env, friction)
	herdr.RawFriction = rawFriction
	return Starter{
		Env:         env,
		Machine:     machine,
		Preparer:    Preparer{Machine: machine, Env: env},
		Herdr:       herdr,
		Panes:       herdr,
		SelfExe:     selfExe,
		StateRoot:   stateRoot,
		Friction:    friction,
		RawFriction: rawFriction,
	}
}

// Start runs the non-dry-run job start and stops at the first failure. The
// returned store is nil when nothing was written; from store.Start on it
// is always returned so the caller can print the job's state line. The
// job's start.lock is held for the whole start: a starter that crashes
// releases it with the process, and a later duplicate start or cancel finds
// it free and recovers the job.
func (s Starter) Start(req StartRequest) (State, *Store, error) {
	// 1. The server must be reachable before anything is written or run;
	// the reachability query is the only subprocess of this step.
	if !s.Herdr.ServerReachable() {
		return State{}, nil, herdrExit("job: no reachable Herdr server")
	}

	// 2. A missing checkout is prepared (and cloned) now, before the job
	// directory is created: the job directory lives in the checkout.
	checkout := filepath.Join(s.Machine.ReposRoot, req.Org, req.Repo)
	preparedIt := false
	var prepared Prepared
	if _, err := os.Stat(checkout); os.IsNotExist(err) {
		var err error
		prepared, err = s.Preparer.Prepare(prepareRequest(req))
		if err != nil {
			// Nothing is written: the job directory cannot exist yet.
			return State{}, nil, err
		}
		preparedIt = true
	}

	// 3. The start lock, held until Start returns (defer, panics included):
	// a held lock is another live starter, which owns the job; the OS
	// releases it when this process dies, which is the crash signal the
	// recovery reads.
	jobDir, err := Dir(s.StateRoot(checkout), req.ID)
	if err != nil {
		return State{}, nil, err
	}
	if err = os.MkdirAll(jobDir, 0o700); err != nil {
		return State{}, nil, err
	}
	startLock, err := tryStartLock(jobDir)
	if err != nil {
		if !errors.Is(err, errStartLockHeld) {
			return State{}, nil, err
		}
		// A live starter owns the job. The supplied brief is checked the
		// way Store.Start checks it: it parses first, and a parse error
		// is returned as is before the snapshot.
		doc, err := parseBrief(req.Brief, req.ID)
		if err != nil {
			return State{}, nil, err
		}
		dupStore := Open(s.StateRoot(checkout))
		snap, snapErr := dupStore.Snapshot(req.ID)
		if snapErr != nil {
			// The live starter has not published the state yet: the job
			// is being created now, and there is no state to return.
			return State{}, nil, snapErr
		}
		st := snap.State
		if st.BriefSHA256 != doc.Hash {
			// The id was reused with a different canonical brief: the
			// conflict Store.Start returns, with the job unchanged.
			return State{}, nil, &ExitError{Code: ExitBriefConflict, Msg: "job: id reused with a different brief"}
		}
		// Same canonical brief: return the live state as a duplicate.
		// No other step runs.
		st.DuplicateOf = req.ID
		return st, dupStore, nil
	}
	defer closeStartLock(startLock)

	// 4. Record the job. A duplicate is returned as is; no other step
	// runs.
	store := Open(s.StateRoot(checkout))
	st, err := store.Start(req.ID, req.Brief)
	if err != nil {
		return st, store, err
	}
	if st.DuplicateOf != "" {
		// A duplicate still accepted or preparing was left by a starter
		// that died: the lock was free, so no live starter owns the job —
		// recover it (the lock is held) and return the recovered state
		// with the duplicate marker.
		if st.Status == StatusAccepted || st.Status == StatusPreparing {
			if st, _, err = recoverStartLocked(store, req.ID, s.Herdr, s.Friction); err != nil {
				return st, store, err
			}
			st.DuplicateOf = req.ID
		}
		return st, store, nil
	}

	// 5. The job is being prepared.
	if _, err = store.Transition(req.ID, StatusPreparing); err != nil {
		return State{}, store, s.failStored(store, req.ID, "", err)
	}
	if _, err = store.Append(req.ID, EventIn{Tipo: "preparing", Resumo: "preparing checkout and worktree"}); err != nil {
		return State{}, store, s.failStored(store, req.ID, "", err)
	}

	// 6. Prepare the checkout and worktree (already done in step 2).
	if !preparedIt {
		prepared, err = s.Preparer.Prepare(prepareRequest(req))
		if err != nil {
			_, _ = store.Fail(req.ID, err.Error(), prepareExitCode(err))
			return State{}, store, err
		}
	}

	// 7. Record the run facts; StartedAt comes from the store clock. This
	// slice computes no deadline: the timeout budget is enforced by the
	// supervisor slice. IntervalStart seeds the budget interval anchor
	// (the active interval); BlockedTimeoutMin and FinishingTimeoutMin
	// record the machine's blocked and finishing budgets for the job's
	// life, like TimeoutMin, and AutoCleanup and HumanInputMin record the
	// job's automatic close settings (autoCloseSettings, resolved from the
	// job checkout's configuration now that the checkout exists), in this
	// same atomic write, once and never rewritten.
	autoCleanup, humanInputMin, err := autoCloseSettings(s.Env, checkout)
	if err != nil {
		_, _ = store.Fail(req.ID, err.Error(), ExitUsage)
		return State{}, store, err
	}
	startedAt := store.clock().Now()
	if _, err = store.Record(req.ID, func(st *State) {
		st.Checkout = prepared.Checkout
		st.Dir = prepared.Dir
		st.Branch = prepared.Branch
		st.Base = prepared.Base
		st.BaseSHA = prepared.BaseSHA
		st.Modo = runMode(req.Mode)
		st.TimeoutMin = req.TimeoutMin
		st.StartedAt = formatTS(startedAt)
		// The budget anchor keeps the full resolution (RFC3339Nano): a
		// whole-second anchor would shift a restarted interval by up to
		// 999 ms.
		st.IntervalStart = startedAt.Format(time.RFC3339Nano)
		st.BlockedTimeoutMin = s.Machine.BlockedTimeoutMin
		st.FinishingTimeoutMin = s.Machine.FinishingTimeoutMin
		st.AutoCleanup = autoCleanup
		st.HumanInputMin = humanInputMin
		st.Equipe = &Equipe{Fonte: req.Team.Source, OverrideBrief: overrideBrief(req.Team.OverrideKeys)}
	}); err != nil {
		return State{}, store, s.failStored(store, req.ID, "", err)
	}

	// 8. Record the create intent before the create: the label the job
	// workspace gets and the ids that already had it. A start that crashes
	// after the create recovers the workspace from this record (closing it
	// only when its identity is proven); a crash before it has nothing to
	// close. A list error fails the job like a create failure, and the
	// create is not called.
	rows, err := s.Herdr.List()
	if err != nil {
		_, _ = store.Fail(req.ID, "job: herdr workspace list failed", ExitHerdr)
		return State{}, store, err
	}
	label := "job-" + req.ID
	var preexisting []string
	for _, row := range rows {
		if row.Label == label {
			preexisting = append(preexisting, row.ID)
		}
	}
	sort.Strings(preexisting)
	if _, err = store.Record(req.ID, func(st *State) {
		st.WorkspaceLabel = label
		st.WorkspacePreexisting = preexisting
	}); err != nil {
		return State{}, store, s.failStored(store, req.ID, "", err)
	}

	// 9. Create the job workspace in the worktree. The root pane, where
	// the supervisor runs, gets the user-configuration locators of this
	// start (locator.go), so the supervisor selects the same user
	// configuration and finds the job in this store.
	locators, err := supervisorLocatorEnv(platform.Current(), s.Env, s.Machine.reposRootHome, absLocator)
	if err != nil {
		_, _ = store.Fail(req.ID, err.Error(), ExitUsage)
		return State{}, store, err
	}
	workspaceID, rootPaneID, err := s.Herdr.Create(prepared.Dir, label, locators)
	if err != nil {
		_, _ = store.Fail(req.ID, err.Error(), herdrExitCode(err))
		return State{}, store, err
	}
	// The id is about to be joined into the session file path and passed
	// to `herdr workspace close`: it must be one safe path segment.
	// Otherwise nothing is recorded or written, the workspace is left
	// open (friction says so), and the job fails with the fixed message.
	// The raw id goes only to the raw friction sink, sanitized and cut.
	if !validWorkspaceID(workspaceID) {
		if s.RawFriction != nil {
			s.RawFriction("job: herdr workspace create returned an invalid workspace id: " + cutFriction(core.FrictionSafe(workspaceID)))
		}
		if s.Friction != nil {
			// The id is invalid, so the line names the workspace by the
			// job's label only (leftOpenFriction).
			s.Friction(leftOpenFriction(workspaceID, label, "its id is invalid"))
		}
		_, _ = store.Fail(req.ID, "job: herdr workspace create returned an invalid workspace id", ExitHerdr)
		// The workspace is left open on purpose; the report says so.
		finishStartFailure(store, req.ID, "open", s.Friction)
		return State{}, store, herdrExit("job: herdr workspace create returned an invalid workspace id")
	}
	// A crash in the window between Herdr.Create and the workspace id
	// record below leaves the job preparing with an unrecorded Herdr
	// workspace: the create intent was recorded in step 8, so start
	// recovery (RecoverStart) identifies the workspace by the label,
	// closes it only when its identity is proven, and ends the job
	// failed. The OS releases the held start.lock with the process, which
	// is the crash signal the recovery reads.
	if afterWorkspaceCreate != nil {
		afterWorkspaceCreate()
	}
	if _, err = store.Record(req.ID, func(st *State) {
		st.WorkspaceID = workspaceID
		st.RootPane = rootPaneID
	}); err != nil {
		return State{}, store, s.failStored(store, req.ID, workspaceID, err)
	}

	// 10. Write the team into the workspace session file.
	if err = writeSessionConf(filepath.Join(s.StateRoot(checkout), workspaceID, "session.conf"), req.Team.Pairs); err != nil {
		workspace := "open"
		if s.closeWorkspace(store, req.ID, workspaceID) {
			workspace = "closed"
		}
		_, _ = store.Fail(req.ID, "job: cannot write the team session", ExitUsage)
		finishStartFailure(store, req.ID, workspace, s.Friction)
		return State{}, store, errUsage("job: cannot write the team session")
	}

	// 11. Launch the supervisor in the workspace's first pane, after
	// issuing the private launch record it must confirm (launch.go). The
	// supervisor gets --repo so it locates the job in this checkout only,
	// without scanning the other checkouts under job_repos_root
	// (DJA-348). A failed pane run closes the workspace as before only
	// once the launch is fenced: a supervisor that confirmed anyway, or a
	// launch that could not be revoked, leaves the workspace open.
	clock := store.clock()
	launch, err := issueLaunch(store, clock, req.ID, workspaceID, rootPaneID)
	if err != nil {
		return State{}, store, s.failStored(store, req.ID, workspaceID, err)
	}
	if err = s.Panes.Run(rootPaneID, []string{s.SelfExe, "job", "supervise", "--id", req.ID, "--repo", req.Org + "/" + req.Repo}); err != nil {
		workspace := "open"
		confirmed, revokeErr := revokeLaunch(store, clock, launch)
		switch {
		case revokeErr != nil:
			s.frictionLaunchOpen(workspaceID, label, "the supervisor launch could not be revoked")
		case confirmed:
			s.frictionLaunchOpen(workspaceID, label, "the job supervisor confirmed its start")
		case s.closeWorkspace(store, req.ID, workspaceID):
			workspace = "closed"
		}
		_, _ = store.Fail(req.ID, err.Error(), herdrExitCode(err))
		finishStartFailure(store, req.ID, workspace, s.Friction)
		return State{}, store, err
	}

	// 12. The supervisor confirms that it started for this job, in this
	// store, from the root pane; the pane-run acknowledgement alone is not
	// enough. The starter's decision below is the revoke's critical
	// section under launch.lock: a confirmation that took the lock before
	// the revoke is accepted even after the nominal wait, none after it.
	// Without the acceptance the job fails as a start failure and the
	// workspace is left open: if the revoke cannot run, a late
	// confirmation remains possible, and whatever runs in its root pane
	// then (a slow or failed supervisor, a shell) is not proven to be
	// this start's and inert, so it is not closed under it.
	accepted, err := awaitLaunch(store, clock, launch, defaultLaunchWait, defaultLaunchPoll)
	if !accepted {
		const motivo = "job: the supervisor did not confirm its start"
		if err != nil {
			s.frictionLaunchOpen(workspaceID, label, "the supervisor launch could not be revoked")
		} else {
			s.frictionLaunchOpen(workspaceID, label, "the supervisor did not confirm its start")
		}
		_, _ = store.Fail(req.ID, motivo, ExitHerdr)
		finishStartFailure(store, req.ID, "open", s.Friction)
		return State{}, store, herdrExit(motivo)
	}

	// 13. The job is running. A confirmed supervisor is running in the
	// root pane, so the workspace is never closed under it: a failed
	// transition fails the job and records the open workspace; the
	// supervisor sees the failed job and closes the workspace itself.
	st, err = store.Transition(req.ID, StatusRunning)
	if err != nil {
		s.frictionLaunchOpen(workspaceID, label, "the job supervisor confirmed its start")
		if _, failErr := store.Fail(req.ID, "job: cannot record the job state", ExitUsage); failErr != nil && s.Friction != nil {
			s.Friction("job: cannot record the job failure: " + failErr.Error())
		}
		finishStartFailure(store, req.ID, "open", s.Friction)
		return State{}, store, err
	}
	return st, store, nil
}

// prepareRequest maps the start request to the preparation input. The CLI
// already validated the machine label; Prepare re-validates with an absent
// maquina, which it accepts.
func prepareRequest(req StartRequest) PrepareRequest {
	return PrepareRequest{ID: req.ID, Org: req.Org, Repo: req.Repo, Base: req.Base, Mode: req.Mode}
}

// runMode records the mode as the contract spells it: worktree or
// workspace (an absent mode is worktree).
func runMode(mode string) string {
	if mode == "workspace" {
		return "workspace"
	}
	return "worktree"
}

// The job's automatic close settings defaults: the values used when the
// layered configuration carries no job_auto_cleanup / job_human_input_minutes.
const (
	// DefaultJobAutoCleanup is the default job_auto_cleanup.
	DefaultJobAutoCleanup = "on"
	// DefaultJobHumanInputMinutes is the default job_human_input_minutes.
	DefaultJobHumanInputMinutes = "5"
)

// autoCloseSettings resolves the job's automatic close settings from the
// layered configuration of the job's checkout (core.LoadConfig over the
// checkout: defaults, user, project) plus the SOHO_* environment
// through core.Cfg. The project file of the job's repository wins over
// the user file, which wins over the defaults. An invalid value is exit
// 2 naming the key, the same failure job_finishing_timeout uses.
func autoCloseSettings(env platform.Env, checkout string) (string, int, error) {
	ctx := core.LoadConfig(env, checkout)
	autoCleanup := core.Cfg(&ctx, "job_auto_cleanup", DefaultJobAutoCleanup, env)
	if !core.ConfigValueOk("job_auto_cleanup", autoCleanup, env, "") {
		return "", 0, exitErr(2, "job: invalid job_auto_cleanup '"+autoCleanup+"'")
	}
	raw := core.Cfg(&ctx, "job_human_input_minutes", DefaultJobHumanInputMinutes, env)
	if !core.ConfigValueOk("job_human_input_minutes", raw, env, "") {
		return "", 0, exitErr(2, "job: invalid job_human_input_minutes '"+raw+"'")
	}
	humanInputMin, err := strconv.Atoi(raw)
	if err != nil {
		return "", 0, exitErr(2, "job: invalid job_human_input_minutes '"+raw+"'")
	}
	return autoCleanup, humanInputMin, nil
}

// overrideBrief maps the team's brief override keys to the report form: an
// empty non-nil slice when nil, the same mapping the job start --dry-run
// JSON carries.
func overrideBrief(keys []string) []string {
	if keys == nil {
		return []string{}
	}
	return keys
}

// prepareExitCode is the exit code a preparation failure ends the job
// with: the code the error itself carries, else 22.
func prepareExitCode(err error) int {
	return exitCodeOr(err, ExitPrepare)
}

// herdrExitCode is the exit code a Herdr failure ends the job with: the
// code the error itself carries, else 4.
func herdrExitCode(err error) int {
	return exitCodeOr(err, ExitHerdr)
}

func exitCodeOr(err error, def int) int {
	var exit *ExitError
	if errors.As(err, &exit) && exit != nil {
		return exit.Code
	}
	return def
}

// leftOpenFriction is the friction line of a start failure that left the
// job workspace open: it names the workspace by its id and the job's
// label when the id is one the job can name (validWorkspaceID), and by
// the label alone when it is not — an invalid or unrecorded id never
// reaches the line.
func leftOpenFriction(workspaceID, label, motivo string) string {
	if !validWorkspaceID(workspaceID) {
		return "job: the Herdr workspace " + label + " was left open: " + motivo
	}
	return "job: the Herdr workspace " + workspaceID + " (" + label + ") was left open: " + motivo
}

// frictionLaunchOpen records why a start failure left the job workspace
// open around its supervisor launch (leftOpenFriction).
func (s Starter) frictionLaunchOpen(workspaceID, label, why string) {
	if s.Friction != nil {
		s.Friction(leftOpenFriction(workspaceID, label, why))
	}
}

// failStored ends a recorded job after a storage error (a state, event or
// run-fact write failed): it closes the workspace when one was created and
// fails the job with exit 2, best effort, so every failure after the job is
// recorded still aims at a terminal state with report.json; the cleanup
// event and the report rewrite record the close it made or the open it
// left. The original error is returned.
func (s Starter) failStored(store *Store, id, workspaceID string, err error) error {
	workspace := ""
	if workspaceID != "" {
		if s.closeWorkspace(store, id, workspaceID) {
			workspace = "closed"
		} else {
			workspace = "open"
		}
	}
	if _, failErr := store.Fail(id, "job: cannot record the job state", ExitUsage); failErr != nil && s.Friction != nil {
		s.Friction("job: cannot record the job failure: " + failErr.Error())
	}
	finishStartFailure(store, id, workspace, s.Friction)
	return err
}

// closeWorkspace closes the job workspace after a post-create failure and
// records the successful close on the state; its errors only go to
// friction. It reports whether the close happened and was recorded: a
// close whose WorkspaceClosed record failed is not reported closed, as the
// recovery does, so the report keeps the workspace open.
func (s Starter) closeWorkspace(store *Store, id, workspaceID string) bool {
	if err := s.Herdr.Close(workspaceID); err != nil {
		if s.Friction != nil {
			s.Friction("job: cannot close the job workspace: " + err.Error())
		}
		return false
	}
	if _, err := store.Record(id, func(st *State) { st.WorkspaceClosed = true }); err != nil {
		if s.Friction != nil {
			s.Friction("job: cannot record the job state: " + err.Error())
		}
		return false
	}
	return true
}

// finishStartFailure writes the workspace outcome of a start failure into
// the job log and the report, after the caller's terminal record
// (store.Fail): the cleanup event — the same type and resumo the
// supervisor's finalize appends — only when the log has none, so a retry
// converges, and report.json rewritten so eventos and limpeza.workspace
// are current ("closed" or "open"). An empty workspace is a failure
// without a Herdr workspace (none created, none proven): the log and the
// report stay as the terminal record left them. A write error only goes to
// friction: the terminal record is already durable. The event order, the
// resumo and the report field on every path are pinned by
// TestReportFactsStartCleanup and the start_recover tests.
func finishStartFailure(store *Store, id, workspace string, friction func(string)) {
	if workspace == "" {
		return
	}
	dir, err := Dir(store.Root, id)
	if err != nil {
		if friction != nil {
			friction(err.Error())
		}
		return
	}
	events, err := readEvents(dir)
	if err != nil {
		if friction != nil {
			friction("job: cannot read the job events: " + err.Error())
		}
		return
	}
	for _, event := range events {
		if event.Tipo == "cleanup" {
			return
		}
	}
	if _, err := store.Append(id, EventIn{Tipo: "cleanup", Resumo: "processes released; worktree kept"}); err != nil {
		if friction != nil {
			friction(err.Error())
		}
		return
	}
	if _, err := store.WriteReport(id, ReportFacts{Workspace: workspace}); err != nil && friction != nil {
		friction(err.Error())
	}
}

// writeSessionConf writes the team pairs to path atomically (temp file in
// the same directory + rename, mode 0600), creating the parent directory
// 0700: one key=value line per pair, in order, and nothing else.
func writeSessionConf(path string, pairs []TeamPair) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lines := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		lines = append(lines, pair.Key+"="+pair.Value)
	}
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	return writeAtomic0600(path, []byte(body))
}

// NewStartRecovery wires the production start recovery: one herdr CLI for
// the list and close calls, and the friction sinks for the errors the
// recovery cannot prove through. The returned function recovers one job.
func NewStartRecovery(env platform.Env, friction, rawFriction func(string)) func(store *Store, id string) (State, bool, error) {
	herdr := newHerdrCLI(env, friction)
	herdr.RawFriction = rawFriction
	return func(store *Store, id string) (State, bool, error) {
		return RecoverStart(store, id, herdr, friction, rawFriction)
	}
}

// RecoverStart recovers a job whose start crashed while it was accepted or
// preparing: it try-locks the job's start.lock — a held lock is a live
// starter, which owns the job, so the current state is returned unchanged
// with recovered=false — and runs the recovery under the lock. Any other
// status is unchanged; an unknown or unreadable job is the store's error.
func RecoverStart(store *Store, id string, herdr herdrWorkspaces, friction, rawFriction func(string)) (State, bool, error) {
	dir, err := Dir(store.Root, id)
	if err != nil {
		return State{}, false, err
	}
	lockFile, err := tryStartLock(dir)
	if err != nil {
		if errors.Is(err, errStartLockHeld) {
			snap, err := store.Snapshot(id)
			if err != nil {
				return State{}, false, err
			}
			return snap.State, false, nil
		}
		return State{}, false, err
	}
	defer closeStartLock(lockFile)
	st, recovered, err := recoverStartLocked(store, id, herdr, friction)
	if err != nil {
		return st, false, err
	}
	return st, recovered, nil
}

// recoverStartLocked runs the crash recovery under the start.lock the
// caller holds. Only accepted and preparing are recovered (recovered=true
// on the way): any other status is unchanged; a crash in the start window
// left the job accepted or preparing with no supervisor. The job workspace
// is closed only when its identity is proven: the recorded WorkspaceID, or
// exactly one new row of the recorded WorkspaceLabel that is not in
// WorkspacePreexisting — an unreadable list, more than one new row, or
// any new row with an id the job cannot name (invalid) leaves the identity
// unproven: close nothing and say so to friction. The job ends failed
// ("job: start interrupted", exit 19) and a pending cancel request retires
// with the terminal record. After the terminal record the workspace
// outcome is written to the log and the report: the cleanup event (the
// supervisor's finalize type and resumo, appended only when the log has
// none) and limpeza.workspace closed or open; a failure with no workspace
// gets neither.
func recoverStartLocked(store *Store, id string, herdr herdrWorkspaces, friction func(string)) (State, bool, error) {
	snap, err := store.Snapshot(id)
	if err != nil {
		return State{}, false, err
	}
	st := snap.State
	if st.Status != StatusAccepted && st.Status != StatusPreparing {
		return st, false, nil
	}
	// The private launch record first (launch.go). Without one the start
	// never ran the supervisor command and the recovery below is
	// unchanged. With one, a pending launch is revoked — the revoke's
	// critical section under launch.lock: a confirmation that took the
	// lock before it is accepted even after the nominal wait, none after
	// it — but the root pane may hold a supervisor (confirmed, slow, or
	// one this recovery could not fence when the revoke could not run,
	// for which a late confirmation remains possible): its workspace is
	// not closed under it. A live supervisor sees the failed job and
	// closes the workspace itself.
	launched := fenceLaunch(store, id) != launchNone
	// The job workspace, closed only when its identity is proven.
	// outcome is the report's limpeza.workspace ("closed" or "open");
	// empty is a failure with no Herdr workspace to report (none created,
	// none proven).
	var workspaceID string
	var outcome string
	if launched {
		outcome = "open"
		if friction != nil {
			friction(leftOpenFriction(st.WorkspaceID, st.WorkspaceLabel, "the job supervisor may have started"))
		}
	} else if st.WorkspaceID != "" {
		// Proven by the record.
		workspaceID = st.WorkspaceID
	} else if st.WorkspaceLabel != "" {
		rows, err := herdr.List()
		if err != nil {
			// Fail closed: the list is unreadable, so no identity.
			outcome = "open"
			frictionStartAmbiguous(friction)
		} else {
			var candidates []string
			invalid := false
			for _, row := range rows {
				if row.Label != st.WorkspaceLabel || hasPreexistingID(st.WorkspacePreexisting, row.ID) {
					continue
				}
				if !validWorkspaceID(row.ID) {
					// A new row the job cannot name: the set is
					// unproven, like an ambiguous one.
					invalid = true
					continue
				}
				candidates = append(candidates, row.ID)
			}
			switch {
			case invalid || len(candidates) > 1:
				// Fail closed: the identity is unproven (an invalid id
				// or more than one candidate), so close nothing.
				outcome = "open"
				frictionStartAmbiguous(friction)
			case len(candidates) == 1:
				workspaceID = candidates[0]
				if _, err := store.Record(id, func(s *State) { s.WorkspaceID = workspaceID }); err != nil {
					return State{}, false, err
				}
			}
			// Zero candidates and no invalid row: nothing to close.
		}
	}
	if workspaceID != "" {
		if err := herdr.Close(workspaceID); err != nil {
			if friction != nil {
				friction("job: cannot close the job workspace: " + err.Error())
			}
			outcome = "open"
		} else {
			if _, err := store.Record(id, func(s *State) { s.WorkspaceClosed = true }); err != nil {
				return State{}, false, err
			}
			outcome = "closed"
		}
	}
	// The terminal record, then the cleanup append and the report rewrite
	// so eventos and limpeza are current, then the pending cancel retires
	// with the terminal record: the supervisor's retirement (control.go)
	// is reused, not copied.
	failed, err := store.Fail(id, "job: start interrupted", ExitFailed)
	if err != nil {
		return failed, false, err
	}
	finishStartFailure(store, id, outcome, friction)
	(&Supervisor{Store: store, ID: id, Friction: friction}).retirePendingCancel()
	return failed, true, nil
}

// frictionStartAmbiguous is the recovery's fail-closed friction line: the
// workspace may be left open because its identity could not be
// established.
func frictionStartAmbiguous(friction func(string)) {
	if friction != nil {
		friction("job: the Herdr workspace may be left open: its identity could not be established")
	}
}

// hasPreexistingID reports whether id is in the recorded preexisting list.
func hasPreexistingID(ids []string, id string) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}
