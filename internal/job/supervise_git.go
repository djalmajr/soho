// supervise_git.go is the supervisor's git sync and the terminal path: it
// keeps the job branch pushed (announcing the new commits, pushing them
// without ever forcing, and opening at most one draft pull request on a
// 30 s tick cadence), turns a rejected push into a blocked job that a
// later successful push unblocks, and finishes every terminal outcome the
// same way — checkpoint the leftovers of a canceled or timed-out job, run
// the final push and pull request, publish the complete report facts,
// release the job orchestrator, the team and the registered copies and
// processes, record the cleanup event, and — with the job's automatic
// cleanup on — remove the job's own worktree when the report facts say
// the work is clean and pushed, record the job folder retained, decide
// the close on the workspace's panes through the close watch, close the
// Herdr workspace last, and record the workspace closed. It never
// removes a directory, a branch, or a pull request, and it never
// deletes the job folder.
package job

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// gitSyncCadence is the git watch cadence: 30 s of Poll ticks (6 ticks at
// the 5 s default poll). The cadence counts ticks, not wall-clock
// differences, so a moving clock cannot end or stretch it.
const gitSyncCadence = 30 * time.Second

// syncGit keeps the job branch in sync on every tick: on pushNow or once
// every 30 s of ticks it runs a sync pass. A nil Git (workspace mode)
// runs no git at all. GitSync failures go to friction without a state
// change; store write failures propagate. While a stop is running it runs
// no pass at all: the stop path owns the job, and the final commit and
// push are the finishing's, inside its own budget — a periodic network
// step must not delay the stop.
func (s *Supervisor) syncGit() (int, bool, error) {
	if s.stopping != "" {
		// A stop is already running: the periodic sync does not run during
		// the stop (the final commit and push are the finishing's, inside
		// its budget), so it never delays the stop's end.
		return 0, false, nil
	}
	s.lastPushTick++
	due := s.pushNow || s.lastPushTick >= s.syncTicks()
	s.pushNow = false
	if !due {
		return 0, false, nil
	}
	s.lastPushTick = 0
	return s.syncGitOnce()
}

// syncTicks is the ticks per cadence, clamped to at least one.
func (s *Supervisor) syncTicks() int {
	every := int(gitSyncCadence / s.poll())
	if every < 1 {
		every = 1
	}
	return every
}

// syncGitOnce runs one watch-loop sync pass: the new commit events, the
// push, the unblock of a block this path set, and the draft pull request
// when the remote carries the local head and no pr_opened event exists
// yet. It is the slow step of the tick (a network push of up to the net
// timeout), so it runs under the cancel observer — the same watch as the
// in-flight delivery: the sync's git subprocesses run under the context
// the observer cancels when control/cancel.json appears, and a pending
// cancel that arrives in flight ends only the subprocess GitSync opened.
// A cut pass counts no failure (syncGitPass turns the cut into a clean
// return: no friction, no refused-push block) and marks the tick cut by
// the cancel, so the loop starts the next tick without sleeping the poll
// interval and its deliverControl sees the cancel and starts the stop at
// once. The terminal procedure's final push (finalSync) does not use this
// path: it has its own step bounds and a nil context.
func (s *Supervisor) syncGitOnce() (int, bool, error) {
	if s.Git == nil {
		return 0, false, nil
	}
	ctx, stopWatching := s.watchForCancel()
	defer stopWatching()
	s.Git.Context = ctx
	defer func() { s.Git.Context = nil }()
	code, done, err := s.syncGitPass(nil)
	if ctx.Err() != nil {
		// The pending cancel cut this tick's slow step (or arrived just
		// after the pass ended): no failure was counted, and the loop
		// skips the poll sleep so the next tick starts the stop at once.
		s.tickCutByCancel = true
		return 0, false, nil
	}
	return code, done, err
}

// finSync is what one bounded final sync learned: cut is true when a step
// of the terminal procedure ran out of its bound (a subprocess killed by
// the clamped timeout, or a step skipped for lack of time) and failed is
// true when any step failed for another reason (a git or gh error, a
// refused push, a store error): the sync is recorded as complete only when
// it neither failed nor was cut.
type finSync struct{ cut, failed bool }

// fail marks the final sync as not clean; a nil fin (the watch loop's pass)
// records nothing.
func (f *finSync) fail() {
	if f != nil {
		f.failed = true
	}
}

// cutIfStepEnded marks the final sync cut when the failure just seen is the
// step's bound being reached; a nil fin (the watch loop's pass) is never cut.
func (s *Supervisor) cutIfStepEnded(fin *finSync) {
	if fin != nil && s.stepCut() {
		fin.cut = true
	}
}

// syncGitPass is syncGitOnce's pass with an optional bounded final sync
// (fin, set by finalSync): the git steps run under the step deadline
// finalSync began, and the draft pull request is its own step with its own
// bound. A git step cut by the pending cancel (errCancelled, the watch
// loop's observer) ends the pass cleanly: no friction, no refused-push
// block, no failure — the caller marks the tick cut by the cancel.
func (s *Supervisor) syncGitPass(fin *finSync) (int, bool, error) {
	if s.Git == nil {
		return 0, false, nil
	}
	head, err := s.Git.Head()
	if err != nil {
		if errors.Is(err, errCancelled) {
			return 0, false, nil
		}
		s.frictionGit(err)
		fin.fail()
		s.cutIfStepEnded(fin)
		return 0, false, nil
	}
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		s.frictionf("job: git sync: " + err.Error())
		fin.fail()
		return 0, false, nil
	}
	events, err := readEvents(jobDir)
	if err != nil {
		s.frictionf("job: git sync: " + err.Error())
		fin.fail()
		return 0, false, nil
	}
	// The announced shas seed from the existing commit events, so a
	// restarted supervisor never re-announces a commit.
	if s.announced == nil {
		s.announced = map[string]bool{}
	}
	prKnown := false
	for _, event := range events {
		switch event.Tipo {
		case "commit":
			if sha := event.Refs["sha"]; sha != "" {
				s.announced[sha] = true
			}
		case "pr_opened":
			prKnown = true
		}
	}
	if head != s.lastHead {
		// lastHead is remembered only after the commits are announced: a
		// failed listing leaves it unchanged so the next sync walks again.
		commits, err := s.Git.Commits()
		if err != nil {
			if errors.Is(err, errCancelled) {
				return 0, false, nil
			}
			s.frictionGit(err)
			fin.fail()
			s.cutIfStepEnded(fin)
			return 0, false, nil
		}
		for _, commit := range commits {
			if s.announced[commit.SHA] {
				continue
			}
			resumo, err := cutResumo(commit.Titulo)
			if err != nil {
				return 0, false, err
			}
			if _, err := s.Store.Append(s.ID, EventIn{
				Tipo:   "commit",
				Resumo: resumo,
				Refs:   map[string]string{"sha": commit.SHA},
			}); err != nil {
				return 0, false, err
			}
			s.announced[commit.SHA] = true
		}
		if len(commits) > 0 {
			// The draft pull request's title is the first (oldest)
			// commit of the job branch.
			s.prTitle = commits[0].Titulo
		}
		s.lastHead = head
	}
	result, err := s.Git.Push()
	if err != nil {
		if errors.Is(err, errCancelled) {
			return 0, false, nil
		}
		s.frictionGit(err)
		fin.fail()
		s.cutIfStepEnded(fin)
		return 0, false, nil
	}
	switch {
	case result.Rejected:
		fin.fail()
		if _, err := s.Store.Append(s.ID, EventIn{
			Tipo: "push",
			Refs: map[string]string{"motivo": "push_rejected"},
		}); err != nil {
			return 0, false, err
		}
		if err := s.blockPushRejection(); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	case result.Pushed:
		if _, err := s.Store.Append(s.ID, EventIn{
			Tipo:   "push",
			Resumo: "pushed",
			Refs:   map[string]string{"sha": result.Head},
		}); err != nil {
			return 0, false, err
		}
		s.unblockPushRejection()
	}
	// The remote carries the local head (this push or an earlier one):
	// open the draft pull request when one is not known yet. No commits,
	// no pull request.
	if !prKnown && s.prTitle != "" {
		if fin != nil && !s.beginFinStep("the draft pull request", s.FinishingLimits.gh()) {
			fin.cut = true
			return 0, false, nil
		}
		pr, _, err := s.Git.EnsureDraftPR(s.prTitle, s.prBody())
		if err != nil {
			if errors.Is(err, errCancelled) {
				return 0, false, nil
			}
			s.frictionGit(err)
			fin.fail()
			s.cutIfStepEnded(fin)
			return 0, false, nil
		}
		s.lastPR = &pr
		if _, err := s.Store.Append(s.ID, EventIn{
			Tipo:   "pr_opened",
			Resumo: "draft pull request opened",
			Refs:   map[string]string{"pr": strconv.Itoa(pr.Numero)},
		}); err != nil {
			return 0, false, err
		}
	}
	return 0, false, nil
}

// blockPushRejection blocks a running job for a rejected push: the
// transition, the stored motivo, and the blocked event. A job that is not
// running is left to the next state. The crossing is one locked write:
// the transition and the settle (the counters and the re-anchor) land
// together, so the blocked interval starts at the crossing and a crash at
// the crossing never leaves the transition without the settle.
func (s *Supervisor) blockPushRejection() error {
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		return err
	}
	if snap.State.Status != StatusRunning {
		return nil
	}
	if err := s.settleAndTransition(snap.State, StatusBlocked, false); err != nil {
		return err
	}
	motivo := "push_rejected"
	if _, err := s.Store.Record(s.ID, func(st *State) { st.Motivo = &motivo }); err != nil {
		return err
	}
	_, err = s.Store.Append(s.ID, EventIn{
		Tipo: "blocked",
		Refs: map[string]string{"motivo": "push_rejected"},
	})
	return err
}

// unblockPushRejection lifts a block this path set: a successful push that
// leaves the job blocked for push_rejected moves it back to running,
// clears the stored motivo, and lands the unblocked event. A block with
// any other motivo is never lifted here. The crossing is one locked
// write: the transition and the settle (the counters and the re-anchor)
// land together, so the active interval starts at the unblock and a crash
// at the crossing never leaves the transition without the settle.
func (s *Supervisor) unblockPushRejection() {
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.frictionf("job: git sync: " + err.Error())
		return
	}
	st := snap.State
	if st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != "push_rejected" {
		return
	}
	if err := s.settleAndTransition(st, StatusRunning, true); err != nil {
		s.frictionf("job: git sync: " + err.Error())
		return
	}
	if _, err := s.Store.Record(s.ID, func(st *State) { st.Motivo = nil }); err != nil {
		s.frictionf("job: git sync: " + err.Error())
		return
	}
	if _, err := s.Store.Append(s.ID, EventIn{Tipo: "unblocked"}); err != nil {
		s.frictionf("job: git sync: " + err.Error())
	}
}

// prBody is the draft pull request body: Job: <id> and, when the brief has
// an origem.ref, Origin: <ref> — nothing else: no hosts, paths, machine,
// model, or event text.
func (s *Supervisor) prBody() string {
	lines := []string{"Job: " + s.ID}
	dir, err := s.Store.readDir(s.ID)
	if err == nil {
		if fields, err := readBriefFields(dir); err == nil {
			if origem, ok := fields["origem"].(map[string]any); ok {
				if ref, ok := origem["ref"].(string); ok && ref != "" {
					lines = append(lines, "Origin: "+ref)
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// frictionf reports one line to the friction sink when one is wired.
func (s *Supervisor) frictionf(message string) {
	if s.Friction != nil {
		s.Friction(message)
	}
}

// frictionGit reports a GitSync step error: the line of the message, and,
// when the step kept a diagnostic, a second line with it (the exit and the
// last lines of the subprocess's stderr, credentials already redacted). The
// message, the events and the exit code do not change.
func (s *Supervisor) frictionGit(err error) {
	s.frictionf(err.Error())
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Detail == "" {
		return
	}
	if step, ok := strings.CutPrefix(exitErr.Msg, gitSyncMsgPrefix); ok {
		s.frictionf("job: git sync " + step + " failed: " + exitErr.Detail)
	}
}

// finalSync is the final push, run while the job is still finishing and
// before the terminal event, so the final commit, push and pr_opened
// events precede the terminal event and wake the dispatcher with it. On
// cancel or timeout the leftovers are committed first as
// wip(job): checkpoint.
//
// The final sync runs inside the terminal procedure's budget
// (finishing_started_at): the git steps (the checkpoint, the commit
// announcements and the push) are one bounded step and the draft pull
// request another, each ending at its own bound or at the budget's end,
// whichever comes first, with every subprocess timeout clamped to what is
// left of the step. It reports whether a step was cut (a subprocess killed
// by the clamp, or a step skipped for lack of time); the caller turns a cut
// into the failed outcome of a done job. Only a fully clean sync is recorded
// (finishing_synced), so a restart does not run it again: a step that was cut
// or failed for another reason (a git or gh error, a refused push, a store
// error) leaves the field false, and the restart runs the sync again.
func (s *Supervisor) finalSync(status string) (cut bool) {
	if s.Git == nil {
		return false
	}
	s.ensureFinishingBase()
	if snap, err := s.Store.Snapshot(s.ID); err == nil && snap.State.FinishingSynced {
		return false
	}
	s.Git.Limit = s.finCap
	defer func() {
		s.Git.Limit = nil
		s.stepDeadline, s.stepName = 0, ""
	}()
	if !s.beginFinStep("the final push", s.FinishingLimits.git()) {
		return true
	}
	fin := &finSync{}
	if status == StatusCanceled || status == StatusTimeout {
		if _, err := s.Git.Checkpoint(); err != nil {
			s.frictionGit(err)
			fin.fail()
			s.cutIfStepEnded(fin)
		}
	}
	if fin.cut {
		return true
	}
	// The final sync ignores the watch cadence.
	if _, _, err := s.syncGitPass(fin); err != nil {
		// A store error returned by the pass: the sync is not clean.
		fin.fail()
	}
	if fin.cut {
		return true
	}
	if fin.failed {
		return false
	}
	if _, err := s.Store.Record(s.ID, func(st *State) { st.FinishingSynced = true }); err != nil {
		s.frictionf("job: the supervisor could not record the completed final sync: " + err.Error())
	}
	return false
}

// finalize finishes every terminal outcome the same way, after the
// outcome is recorded and finalSync has pushed: the collaboration
// settlement of the persisted outcomes (a fresh process over a terminal
// job captures them first), the release of the job orchestrator, the
// team, and the registered copies and processes, the cleanup event (the
// release gate and the log check below), and the automatic cleanup and
// the Herdr workspace close last: with the job's automatic cleanup on
// (the job_auto_cleanup setting recorded at start) the pass removes the
// job's own worktree (worktree mode, only on a pass whose release ended
// clean, with the report facts saying the work is clean and pushed; a
// refusal keeps the worktree and appends the needs_attention with its
// motivo, at most one per motivo per job), appends the folder_retained
// event (once per job, before the close decision), decides the close on
// the workspace's panes through the close watch (wait sleeps the poll
// through the clock, close closes, needs_attention with motivo
// human_input keeps the workspace open), closes the workspace, and
// appends the workspace_closed event right after the successful close
// and before the WorkspaceClosed record. The opt-out removes nothing,
// closes nothing, and lands only the folder_retained. It never removes
// a directory or a branch and never deletes the job folder; its
// failures go to friction and the path continues, because everything
// durable is written before the process may end with the workspace.
func (s *Supervisor) finalize(status string) {
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		s.frictionf("job: finalize: " + err.Error())
		return
	}
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.frictionf("job: finalize: " + err.Error())
		return
	}
	events, err := readEvents(jobDir)
	if err != nil {
		s.frictionf("job: finalize: " + err.Error())
		return
	}
	facts := s.reportFacts(snap.State, events, jobDir)
	// The collaboration terminal step (supervise_collab.go): the outcomes
	// of this process's terminal path were persisted into the state
	// before the terminal transition, so they are settled here only; a
	// fresh process over a terminal job (a restart) has not captured and
	// runs the capture first — the state-first enumeration, the ask of the
	// still-active ones (once in its own process) and the wait on the
	// shared deadline. Either way the confirmed facts (the final phase,
	// finalized_by_supervisor and the cause) are recorded back into the
	// state, which is the single source of the report's colaboracoes and
	// partial fields. When the enumeration could not be restored by the
	// deadline the persisted ids stand and the release, the cleanup and
	// the workspace close are refused: the close is what guarantees no
	// orphan pane, and it runs only when the enumeration succeeded.
	var settleSrc []CollaborationOutcome
	if s.collabUnenumerated {
		// This process's capture could not enumerate the collaborations:
		// the known ids are persisted already; the settlement, the
		// release, the cleanup and the workspace close are refused, as on
		// the fresh-process path below.
		return
	}
	if s.collabEnded {
		settleSrc = snap.State.Colaboracoes
	} else {
		outcomes, capErr := s.endCollaborations(s.collabDeadline())
		if capErr != nil {
			s.frictionf("job: the supervisor cannot enumerate the collaborations: " + capErr.Error())
			if err := s.persistCollabOutcomes(outcomes); err != nil {
				s.frictionf("job: finalize: " + err.Error())
			}
			return
		}
		s.collabEnded = true
		if err := s.persistCollabOutcomes(outcomes); err != nil {
			s.frictionf("job: finalize: " + err.Error())
			return
		}
		settleSrc = outcomes
	}
	settled := s.settleCollaborations(settleSrc)
	if len(settled) > 0 {
		if err := s.persistCollabOutcomes(settled); err != nil {
			s.frictionf("job: finalize: " + err.Error())
			return
		}
	}
	if snap.State.WorkspaceID != "" {
		// The release, the cleanup, and the workspace close apply to jobs
		// with a recorded Herdr workspace; a legacy job without one ends
		// as before.
		facts.Workspace = "closed"
		facts.PanesFechados = 0
	}
	if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
		s.frictionf("job: finalize: " + err.Error())
		return
	}

	// Release: the job orchestrator, then the remaining workers, then
	// the job's registered copies and processes. Errors are friction; the
	// path continues, but a failed release in this pass keeps the cleanup
	// event out of this pass (the gate below): the event claims the
	// processes are released, so it lands only on a pass whose release
	// ended clean; a later pass re-evaluates the release and appends it
	// then.
	released := true
	if snap.State.WorkspaceID != "" {
		if snap.State.Orchestrator != "" {
			if err := s.releaseOrchestrator(snap.State.Orchestrator, snap.State.WorkspaceClosed); err != nil {
				s.frictionf(err.Error())
				released = false
			}
		}
		if err := s.Ops.ReleaseTeam(); err != nil {
			s.frictionf(err.Error())
			released = false
		}
		if err := s.Ops.GC(); err != nil {
			s.frictionf(err.Error())
			released = false
		}
	}

	// The cleanup event is checked against the log first: a crashed first
	// attempt may have recorded it already, and the retry converges. The
	// release gate: it is appended only when the release of this pass
	// ended clean, so a failed release keeps the event pending for a
	// later pass (and a pass whose release fails after the event is
	// recorded adds nothing, the log already carries it).
	cleanupDone := false
	for _, event := range events {
		if event.Tipo == "cleanup" {
			cleanupDone = true
			break
		}
	}
	if !cleanupDone && released {
		// With the automatic cleanup on, a job worktree is removed or
		// kept right after this event, and limpeza.worktree and
		// needs_attention say which; the summary does not anticipate it.
		resumo := "processes released; worktree kept"
		if snap.State.AutoCleanupOn() && snap.State.Modo == "worktree" {
			resumo = "processes released"
		}
		if _, err := s.Store.Append(s.ID, EventIn{
			Tipo:   "cleanup",
			Resumo: resumo,
		}); err != nil {
			s.frictionf("job: finalize: " + err.Error())
			return
		}
	}

	// The automatic cleanup and the workspace close are last: the
	// supervisor process may end with the workspace; everything durable
	// is already written. A job without a recorded workspace, without
	// the Herdr adapter, or with the close already recorded ends as
	// before.
	if snap.State.WorkspaceID == "" || s.Herdr == nil || snap.State.WorkspaceClosed {
		return
	}
	if snap.State.AutoCleanupOn() && snap.State.Modo == "worktree" && released {
		// The worktree removal: only when the release of this pass ended
		// clean (the same gate as the cleanup event), in worktree mode,
		// and with the automatic cleanup on, with the report facts
		// finalize already computed. A removal records the state and
		// rewrites the report (limpeza.worktree "removed"); a refusal
		// keeps the worktree, appends the needs_attention with its
		// motivo (at most one per motivo per job, the log deduplicates
		// it), and the close proceeds. The workspace mode never removes.
		if removed, motivo := removeOwnedWorktree(snap.State.Checkout, snap.State.Dir, s.ID, facts); removed {
			if _, err := s.Store.Record(s.ID, func(st *State) { st.WorktreeRemoved = true }); err != nil {
				s.frictionf("job: finalize: " + err.Error())
				return
			}
			if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
				s.frictionf("job: finalize: " + err.Error())
				return
			}
		} else if motivo != "" && !needsAttentionLogged(events, motivo) {
			if !s.appendNeedsAttention("worktree kept: "+motivo, motivo) {
				return
			}
		}
	}
	// The job folder is retained: one folder_retained per job, before
	// the close decision, with the folder's path and its total size in
	// bytes. The opt-out lands it too — and nothing else.
	s.appendFolderRetained(events, jobDir)
	if !snap.State.AutoCleanupOn() {
		// The opt-out: the worktree and the workspace stay; the report
		// says the workspace stays open.
		facts.Workspace = "open"
		if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
			s.frictionf("job: finalize: " + err.Error())
		}
		return
	}
	// A close issued by an earlier pass: the supervisor runs in the job
	// workspace's root pane and ends with the workspace, so the
	// workspace_closed event is appended before the close and the
	// WorkspaceClosed record after it may never land. This pass converges
	// on the workspace list: a workspace no longer listed is recorded
	// closed; one still listed is closed again without a second event,
	// and without a second close watch (the earlier pass already
	// decided); an unreadable list leaves it to a later pass.
	closeIssued := hasTipo(events, "workspace_closed")
	if closeIssued {
		listed, err := s.workspaceListed(snap.State.WorkspaceID)
		if err != nil {
			// The close is not confirmed: the report says open until a
			// later pass can read the list.
			s.frictionf(err.Error())
			facts.Workspace = "open"
			if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
				s.frictionf("job: finalize: " + err.Error())
			}
			return
		}
		if !listed {
			if _, err := s.Store.Record(s.ID, func(st *State) { st.WorkspaceClosed = true }); err != nil {
				s.frictionf("job: finalize: " + err.Error())
			}
			return
		}
	} else if !s.closeWatchMayClose(snap.State, events) {
		// The close decision: the automatic close watch over the
		// workspace's panes (the job's own panes ignored). The watch
		// keeps the workspace open when it asks for human input.
		facts.Workspace = "open"
		if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
			s.frictionf("job: finalize: " + err.Error())
		}
		return
	}
	if !validWorkspaceID(snap.State.WorkspaceID) {
		// The recorded id is not one safe path segment: a close would
		// hand a hostile argument to `herdr workspace close`, so the
		// workspace is left open exactly as a failed close is — the
		// fixed friction line, the report rewritten open, and no
		// WorkspaceClosed record.
		s.frictionf("job: the recorded workspace id is invalid; the workspace was not closed")
		facts.Workspace = "open"
		if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
			s.frictionf("job: finalize: " + err.Error())
		}
		return
	}
	// The workspace_closed event: once per job, right before the close,
	// the last moment this process is sure to be alive — the close ends
	// the root pane this supervisor runs in. A close that then fails
	// appends needs_attention (motivo close_failed, once per job).
	if !closeIssued {
		if _, err := s.Store.Append(s.ID, EventIn{
			Tipo:   "workspace_closed",
			Resumo: "workspace closed",
		}); err != nil {
			s.frictionf("job: finalize: " + err.Error())
			return
		}
	}
	if err := s.Herdr.Close(snap.State.WorkspaceID); err == nil {
		// Recorded so a later supervisor never closes the workspace twice;
		// the process may end with the workspace before this lands, and a
		// later pass then converges on the workspace list (above).
		if _, err := s.Store.Record(s.ID, func(st *State) { st.WorkspaceClosed = true }); err != nil {
			s.frictionf("job: finalize: " + err.Error())
		}
	} else {
		// The close did not happen: the report says the workspace stayed
		// open, a needs_attention names it, and the retry closes it.
		s.frictionf(err.Error())
		if !needsAttentionLogged(events, closeFailedMotivo) {
			s.appendNeedsAttention("workspace close failed", closeFailedMotivo)
		}
		facts.Workspace = "open"
		if _, err := s.Store.WriteReport(s.ID, facts); err != nil {
			s.frictionf("job: finalize: " + err.Error())
		}
	}
}

// appendNeedsAttention appends one needs_attention event with its motivo
// and wakes the dispatcher for it right away: the terminal path stopped
// the wake worker before finalize (finishWakes), so an event appended
// here would otherwise reach the hook only through a later supervisor.
// ok is false when the append failed (the friction line names it).
func (s *Supervisor) appendNeedsAttention(resumo, motivo string) (ok bool) {
	ev, err := s.Store.Append(s.ID, EventIn{
		Tipo:   "needs_attention",
		Resumo: resumo,
		Refs:   map[string]string{"motivo": motivo},
	})
	if err != nil {
		s.frictionf("job: finalize: " + err.Error())
		return false
	}
	s.finalWake(ev)
	return true
}

// finalWake runs the wake hook once, synchronously, for an event the
// terminal path appends after the wake worker was drained and stopped:
// one attempt and no retry schedule, so the workspace close is not held
// for minutes by a failing hook. The persisted cursor is not advanced: a
// later supervisor re-runs the event (at-least-once; the idempotency key
// lets the receiver deduplicate). A disabled hook runs nothing.
func (s *Supervisor) finalWake(ev Event) {
	if s.Wake == nil || strings.TrimSpace(s.Wake.Cmd) == "" {
		return
	}
	hook := *s.Wake
	hook.Delays = []time.Duration{}
	hook.Run(ev)
}

// closeFailedMotivo names the needs_attention of a workspace close that
// failed after the workspace_closed event was appended.
const closeFailedMotivo = "close_failed"

// workspaceListed reports whether the job workspace is still in
// `herdr workspace list`; an unreadable list is an error.
func (s *Supervisor) workspaceListed(workspaceID string) (bool, error) {
	rows, err := s.Herdr.List()
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ID == workspaceID {
			return true, nil
		}
	}
	return false, nil
}

// closeWatchMayClose runs the automatic close watch over the job
// workspace's panes (the Panes method, type-asserted on the Herdr
// client) and reports whether the workspace may be closed this pass. A
// client without the method, a failed pane read, or a failed roster read
// closes as today, with one fixed friction line each. Otherwise the
// job's own panes — the workspace root pane and the rostered workers of
// this workspace — are ignored, and the watch's decisions drive the
// pass: wait sleeps the poll through the supervisor's clock and
// observes again, close closes, and needs_attention (motivo human_input,
// at most one per job, the log deduplicates it) appends the event and
// keeps the workspace open. A restart in the middle of a wait starts the
// observation again, which is conservative.
func (s *Supervisor) closeWatchMayClose(st State, events []Event) bool {
	lister, ok := s.Herdr.(interface {
		Panes(workspaceID string) ([]herdrPane, error)
	})
	if !ok {
		s.frictionf("job: the Herdr client has no pane list; closing the workspace without the close watch")
		return true
	}
	jobPanes, ok := s.jobPanes(st)
	if !ok {
		s.frictionf("job: the roster could not be read; closing the workspace without the close watch")
		return true
	}
	wait := st.HumanInputWait()
	watch := closeWatch{}
	for {
		panes, err := lister.Panes(st.WorkspaceID)
		if err != nil {
			s.frictionf("job: the workspace pane list failed; closing the workspace without the close watch")
			return true
		}
		outcome, next := closeWatchStep(panes, jobPanes, s.clk().Now(), wait, watch)
		watch = next
		switch outcome.Decision {
		case CloseWatchWait:
			s.clockSleep(s.poll())
		case CloseWatchNeedsAttention:
			if !needsAttentionLogged(events, outcome.Motivo) {
				s.appendNeedsAttention("human input needed", outcome.Motivo)
			}
			return false
		default:
			return true
		}
	}
}

// jobPanes is the set of the job's own pane ids the automatic close watch
// ignores: the workspace root pane (the job orchestrator) and the
// rostered workers of this workspace. The roster's pane column is
// workspace:pane; only a row of the job's own workspace adds its pane.
// A roster failure reports false: without the roster the watch cannot
// tell the job's panes from foreign ones.
func (s *Supervisor) jobPanes(st State) (map[string]bool, bool) {
	job := map[string]bool{}
	if st.RootPane != "" {
		job[st.RootPane] = true
	}
	rows, err := s.Ops.Roster()
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		workspace, pane, ok := strings.Cut(row.Pane, ":")
		if !ok || workspace != st.WorkspaceID || pane == "" {
			continue
		}
		job[pane] = true
	}
	return job, true
}

// appendFolderRetained appends the job's folder_retained event once per
// job (the log deduplicates it): the job folder's path and its total size
// in bytes, before the close decision. A size read or an append failure
// goes to friction; the event lands on a later pass and the close never
// waits on it.
func (s *Supervisor) appendFolderRetained(events []Event, jobDir string) {
	if hasTipo(events, "folder_retained") {
		return
	}
	bytes, err := jobFolderBytes(jobDir)
	if err != nil {
		s.frictionf("job: finalize: " + err.Error())
		return
	}
	if _, err := s.Store.Append(s.ID, EventIn{
		Tipo:   "folder_retained",
		Resumo: "job folder retained",
		Refs:   map[string]string{"path": jobDir, "bytes": strconv.FormatInt(bytes, 10)},
	}); err != nil {
		s.frictionf("job: finalize: " + err.Error())
	}
}

// jobFolderBytes is the total size in bytes of every file under dir,
// walking the subdirectories: a regular file counts its size, a symlink
// its own, and a directory nothing. A missing or unreadable entry is an
// error (the caller frictions and defers the event to a later pass).
func jobFolderBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := entry.Type()
		if mode.IsRegular() || mode&fs.ModeSymlink != 0 {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// needsAttentionLogged reports whether the log already carries a
// needs_attention event with the motivo: at most one per motivo per job,
// checked against the log so a retry converges.
func needsAttentionLogged(events []Event, motivo string) bool {
	for _, event := range events {
		if event.Tipo == "needs_attention" && event.Refs["motivo"] == motivo {
			return true
		}
	}
	return false
}

// releaseOrchestrator releases the job orchestrator, counting a release of
// an agent that no longer exists as done — but only on a pass over a job
// whose workspace is already recorded closed: its pane closed with the
// workspace, and the release CLI answers such a name with its "not in the
// roster" refusal (exit 3, the fixed message of the roster check the
// release runs first), so nothing is left to release. The release runs as
// today first; on a failure over a closed workspace, the answer is read
// back from the roster line the refusal consults (the same state dir and
// working dir this adapter runs from): a row for the name means the agent
// still exists and the failure still blocks the cleanup, and an
// unreadable roster blocks it as well. On a pass whose workspace is not
// closed yet, every failure blocks exactly as today.
func (s *Supervisor) releaseOrchestrator(name string, workspaceClosed bool) error {
	err := s.Ops.Release(name)
	if err == nil || !workspaceClosed {
		return err
	}
	rows, rerr := s.Ops.Roster()
	if rerr != nil {
		return err
	}
	for _, row := range rows {
		if row.Name == name {
			return err
		}
	}
	// No roster row for the agent over a closed workspace: the release's
	// "not in the roster" refusal stands — the agent is gone, and the
	// release counts as done.
	return nil
}

// reportFacts is the finalize report facts: the git facts and commits with
// their push state, the draft pull request, the cost, and the logs.
func (s *Supervisor) reportFacts(st State, events []Event, jobDir string) ReportFacts {
	facts := ReportFacts{}
	if s.Git != nil {
		if gitFacts, err := s.Git.Facts(); err != nil {
			s.frictionGit(err)
			facts.GitFactsFailed = true
		} else {
			facts.Clean = gitFacts.Clean
			facts.Head = gitFacts.Head
			facts.HeadRemoto = gitFacts.HeadRemoto
		}
		if commits, err := s.Git.Commits(); err != nil {
			s.frictionGit(err)
		} else {
			// The remote head contains the commits when it is the local
			// head; with the head unknown every commit is pending.
			push := "pendente"
			if facts.Head != "" && facts.Head == facts.HeadRemoto {
				push = "ok"
			}
			for i := range commits {
				commits[i].Push = push
			}
			facts.Commits = commits
		}
	}
	// The draft pull request: the number from the pr_opened event and the
	// URL of this process's EnsureDraftPR when it is the same pull
	// request; without the URL the report carries the number only.
	for _, event := range events {
		if event.Tipo != "pr_opened" {
			continue
		}
		if numero, err := strconv.Atoi(event.Refs["pr"]); err == nil {
			if s.lastPR != nil && s.lastPR.Numero == numero {
				facts.PR = s.lastPR
			} else {
				estado := "draft"
				facts.PR = &PullRequest{Numero: numero, Estado: estado}
			}
		}
		break
	}
	workers := 0
	for _, event := range events {
		if event.Tipo == "worker_spawned" {
			workers++
		}
	}
	fim := s.Store.clock().Now()
	var duracao int
	if started, err := time.Parse(time.RFC3339, st.StartedAt); err == nil {
		duracao = int(fim.Sub(started) / time.Second)
		if duracao < 0 {
			duracao = 0
		}
	}
	facts.Custo = &Custo{
		Inicio:   st.StartedAt,
		Fim:      formatTS(fim),
		DuracaoS: duracao,
		Workers:  workers,
	}
	facts.Logs = &Logs{
		ReportMD: filepath.Join(jobDir, "report.md"),
		StateDir: jobDir,
		Friction: filepath.Join(jobDir, "friction.log"),
	}
	return facts
}
