// Package job stores ephemeral job state, events and reports at
// <stateRoot>/jobs/<id>, outside any workspace directory.
package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	StatusAccepted  = "accepted"
	StatusPreparing = "preparing"
	StatusRunning   = "running"
	StatusBlocked   = "blocked"
	StatusFinishing = "finishing"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusTimeout   = "timeout"
	StatusCanceled  = "canceled"
	StatusCollected = "collected"
	StatusClosed    = "closed"
)

// State is the jobs/<id>/state.json document. The fields from Checkout on
// are run facts recorded by the start slice; they are optional so a job
// without them serializes byte for byte as before.
type State struct {
	Schema            int     `json:"schema"`
	ID                string  `json:"id"`
	Status            string  `json:"status"`
	BriefSHA256       string  `json:"brief_sha256"`
	DecisionsAckedSeq int     `json:"decisions_acked_seq"`
	Motivo            *string `json:"motivo"`
	DuplicateOf       string  `json:"duplicate_of,omitempty"`
	Checkout          string  `json:"checkout,omitempty"`
	Dir               string  `json:"dir,omitempty"`
	Branch            string  `json:"branch,omitempty"`
	Base              string  `json:"base,omitempty"`
	BaseSHA           string  `json:"base_sha,omitempty"`
	Modo              string  `json:"modo,omitempty"`
	// WorkspaceLabel and WorkspacePreexisting are the create intent, recorded
	// before `herdr workspace create`: the label the job workspace gets and
	// the ids of the workspaces that already had it. Start recovery
	// identifies the job workspace by them when a crash left it unrecorded.
	WorkspaceLabel       string   `json:"workspace_label,omitempty"`
	WorkspacePreexisting []string `json:"workspace_preexisting,omitempty"`
	WorkspaceID          string   `json:"workspace_id,omitempty"`
	RootPane             string   `json:"root_pane,omitempty"`
	WorkspaceClosed      bool     `json:"workspace_closed,omitempty"`
	// WorktreeRemoved records that the end-of-job removal (the finalize
	// automatic cleanup) removed the job's own worktree: the report's
	// limpeza.worktree is "removed" when it is set, "kept" otherwise.
	// omitempty, so a state that predates it serializes byte for byte as
	// before.
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	TimeoutMin      int    `json:"timeout_min,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	// IntervalStart is the wall ts of the current budget interval's start.
	// The interval kind needs no field of its own: it is blocked iff the
	// status is blocked (steady state), so the anchor moves only with the
	// settled counters, at every blocked-boundary crossing.
	IntervalStart string `json:"interval_start,omitempty"`
	// BudgetActiveMs and BudgetBlockedMs are the cumulative settled active
	// and blocked time in milliseconds (every status except blocked is
	// active), measured on the monotonic clock; the in-flight interval is
	// settled only at the next boundary crossing, so sub-second intervals
	// never truncate away.
	BudgetActiveMs  int64 `json:"budget_active_ms,omitempty"`
	BudgetBlockedMs int64 `json:"budget_blocked_ms,omitempty"`
	// BlockedTimeoutMin is the job's blocked budget in minutes, recorded
	// at start and immutable for the job's life, like TimeoutMin; 0 on a
	// state that predates it, which checks the machine default at check
	// time.
	BlockedTimeoutMin int `json:"blocked_timeout_min,omitempty"`
	// FinishingTimeoutMin is the budget in minutes of the job's terminal
	// procedure (the finishing phase), recorded at start in the same write
	// as BlockedTimeoutMin and immutable for the job's life; 0 on a state
	// that predates it, which uses the built-in 10 minutes at read time and
	// is never rewritten.
	FinishingTimeoutMin int `json:"finishing_timeout_min,omitempty"`
	// AutoCleanup is the job's job_auto_cleanup setting ("on"/"off"),
	// resolved from the job checkout's configuration at start in the same
	// write as BlockedTimeoutMin and immutable for the job's life; "" on a
	// state that predates it, which AutoCleanupOn reads as "on".
	AutoCleanup string `json:"auto_cleanup,omitempty"`
	// HumanInputMin is the job's job_human_input_minutes setting in
	// minutes (1..1440), resolved at start in the same write as
	// BlockedTimeoutMin and immutable for the job's life; 0 on a state
	// that predates it, which HumanInputWait reads as the built-in 5
	// minutes.
	HumanInputMin int `json:"human_input_min,omitempty"`
	// Equipe is the team resolution the job ran with (the report's
	// equipe field), recorded with the run facts: the source file and the
	// brief override keys, an empty non-nil list when there are none.
	Equipe *Equipe `json:"equipe,omitempty"`
	// OrchestratorSpawnAt is the spawn intent, recorded before the job
	// orchestrator is spawned: a restarted supervisor that finds it without
	// Orchestrator recovers the spawn instead of spawning blindly.
	OrchestratorSpawnAt string `json:"orchestrator_spawn_at,omitempty"`
	Orchestrator        string `json:"orchestrator,omitempty"`
	DispatchedAt        string `json:"dispatched_at,omitempty"`
	// The collaboration terminal facts (supervise_collab.go), persisted
	// before the terminal status becomes visible and never erased: the
	// outcomes one persisted outcome per job collaboration, the owned ids
	// observed when the ask ran (sorted, unique), and the terminal intent
	// the outcome converges to on a restart. All omitempty, so a state
	// without them serializes byte for byte as before.
	Colaboracoes   []CollaborationOutcome `json:"colaboracoes,omitempty"`
	CollabIDs      []string               `json:"collab_ids,omitempty"`
	TerminalIntent *TerminalIntent        `json:"terminal_intent,omitempty"`
	// FinishingStartedAt is the wall ts (RFC3339Nano) the terminal
	// procedure's own budget counts from: set once, in the same locked
	// write that records the terminal intent or enters finishing, and
	// never overwritten, so a restarted supervisor keeps the same
	// deadline. FinishingSynced records that the final git and pull
	// request steps completed without being cut by that budget, so a
	// restart does not run them again. Both are internal and omitempty.
	FinishingStartedAt string `json:"finishing_started_at,omitempty"`
	FinishingSynced    bool   `json:"finishing_synced,omitempty"`
	// FinalPublication is the verified final publication observation of a
	// worktree job (publication.go), saved in the same write as
	// FinishingSynced. A state with only the Boolean (written before it
	// existed) carries no verified publication. Internal and omitempty.
	FinalPublication *PublicationSnapshot `json:"final_publication,omitempty"`
	// CancelRequestedAt and CancelDeadline are the wall ts (RFC3339Nano)
	// the cancel's grace counts from, recorded at the acceptance (the
	// requestControlFlag write of control/cancel.json, in the same locked
	// operation): the acceptance instant and the acceptance plus the
	// requested grace. A restarted supervisor ends the grace on the
	// persisted deadline instead of measuring a fresh one; a state without
	// them (a job accepted by a binary before the fields) or with an
	// unreadable deadline keeps the full-grace-from-process bound, with one
	// friction line. Both are internal and omitempty, so a state without
	// them serializes byte for byte as before.
	CancelRequestedAt string `json:"cancel_requested_at,omitempty"`
	CancelDeadline    string `json:"cancel_deadline,omitempty"`
}

// AutoCleanupOn reports whether the job's automatic close is on: the
// recorded job_auto_cleanup setting, or "on" on a state that predates the
// field.
func (st State) AutoCleanupOn() bool {
	return st.AutoCleanup == "" || st.AutoCleanup == "on"
}

// HumanInputWait is the human input wait the recorded
// job_human_input_minutes setting gives, or the built-in 5 minutes on a
// state that predates the field.
func (st State) HumanInputWait() time.Duration {
	if st.HumanInputMin > 0 {
		return time.Duration(st.HumanInputMin) * time.Minute
	}
	return 5 * time.Minute
}

// TerminalIntent is the terminal outcome a supervisor process committed to
// before it could record it: the status, the stored motivo and the exit
// code. A restarted supervisor converges on it instead of re-deriving the
// outcome, so a crash between the intent and the terminal transition loses
// neither the outcome nor its partial facts.
type TerminalIntent struct {
	Status string `json:"status"`
	Motivo string `json:"motivo,omitempty"`
	Exit   int    `json:"exit"`
}

// Store is the job state root (core.StateRootPath, not a workspace directory).
type Store struct {
	Root  string
	Clock Clock
}

func Open(stateRoot string) *Store {
	return &Store{Root: stateRoot}
}

// Dir is <stateRoot>/jobs/<id>. The id is the contract regex, and "." / ".."
// are refused so the directory cannot leave jobs/.
func Dir(stateRoot, id string) (string, error) {
	if !idPattern.MatchString(id) || id == "." || id == ".." {
		return "", errUsage("job: invalid id")
	}
	jobs := filepath.Join(stateRoot, "jobs")
	dir := filepath.Join(jobs, id)
	if filepath.Base(dir) != id || filepath.Clean(filepath.Dir(dir)) != filepath.Clean(jobs) {
		return "", errUsage("job: invalid id")
	}
	return dir, nil
}

// Start validates the brief, records the job, and returns the current state.
// The same canonical brief hash returns that state with DuplicateOf set.
// A different hash returns exit 20 and does not change the job.
func (s *Store) Start(id string, brief []byte) (State, error) {
	dir, err := Dir(s.Root, id)
	if err != nil {
		return State{}, err
	}
	doc, err := parseBrief(brief, id)
	if err != nil {
		return State{}, err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return State{}, err
	}
	var st State
	err = withLock(dir, func() error {
		existing, readErr := readState(dir)
		if readErr == nil {
			if existing.BriefSHA256 != doc.Hash {
				return &ExitError{Code: ExitBriefConflict, Msg: "job: id reused with a different brief"}
			}
			existing.DuplicateOf = existing.ID
			st = existing
			return nil
		}
		if !isCode(readErr, ExitNotFound) {
			return readErr
		}
		// Interrupted start: a published brief.json survives without
		// state.json. A different hash is a conflict that changes nothing;
		// the same hash resumes the publication instead of overwriting it.
		saved, err := os.ReadFile(filepath.Join(dir, "brief.json"))
		switch {
		case err == nil:
			sum := sha256.Sum256(saved)
			if hex.EncodeToString(sum[:]) != doc.Hash {
				return &ExitError{Code: ExitBriefConflict, Msg: "job: id reused with a different brief"}
			}
		case os.IsNotExist(err):
			if err = writeAtomic0600(filepath.Join(dir, "brief.json"), doc.Canonical); err != nil {
				return err
			}
		default:
			return fmt.Errorf("job: brief unreadable: %w", err)
		}
		if err := writeAtomic0600(filepath.Join(dir, "brief.md"), []byte(doc.Markdown)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, "control"), 0o700); err != nil {
			return err
		}
		events, err := readEvents(dir)
		if err != nil {
			return err
		}
		if !hasTipo(events, "accepted") {
			if _, err = appendEventFn(dir, s.clock().Now(), EventIn{Tipo: "accepted", Resumo: doc.Objetivo}); err != nil {
				return err
			}
		}
		st = State{Schema: 1, ID: id, Status: StatusAccepted, BriefSHA256: doc.Hash}
		return saveState(dir, st)
	})
	return st, err
}

var transitions = map[string]map[string]bool{
	StatusAccepted:  {StatusPreparing: true},
	StatusPreparing: {StatusRunning: true},
	StatusRunning:   {StatusBlocked: true, StatusFinishing: true},
	StatusBlocked:   {StatusRunning: true, StatusFinishing: true},
	StatusFinishing: {
		StatusDone: true, StatusFailed: true, StatusBlocked: true,
		StatusTimeout: true, StatusCanceled: true,
	},
	StatusDone:      {StatusCollected: true},
	StatusFailed:    {StatusCollected: true},
	StatusTimeout:   {StatusCollected: true},
	StatusCanceled:  {StatusCollected: true},
	StatusCollected: {StatusClosed: true},
}

// Transition moves a job along one allowed lifecycle edge. The closed edge
// runs the shared close checks (pending decisions, cleanup audit, report
// refresh); a transition into a terminal outcome publishes report.json in
// the same locked operation, before the terminal state.json.
func (s *Store) Transition(id, to string) (State, error) {
	var st State
	err := s.withExisting(id, func(dir string) error {
		var err error
		st, err = readState(dir)
		if err != nil {
			return err
		}
		if st.Status == to && isTerminalOutcome(to) {
			// Idempotent retry of a transition whose report publish crashed:
			// the state is already the terminal outcome, so republish the
			// report and leave the state unchanged.
			return refreshReport(dir, st)
		}
		if !transitions[st.Status][to] {
			return errUsage("job: transition " + st.Status + " -> " + to + " is not allowed")
		}
		if to == StatusClosed {
			closed, closeErr := s.closeLocked(dir, st, false)
			st = closed
			return closeErr
		}
		// Leaving a terminal outcome republishes the report while the state
		// still carries it, so a missing report is built with the outcome as
		// its status instead of collected.
		if to == StatusCollected && isTerminalOutcome(st.Status) {
			if err = refreshReport(dir, st); err != nil {
				return err
			}
		}
		st.Status = to
		if isTerminalOutcome(to) {
			// The report is published before the terminal state: a reader
			// that sees the outcome in state.json (status, wait) finds
			// report.json already carrying it with the partial facts. A
			// crash between the two leaves the state still finishing, and
			// the retry republishes the report before the state.
			if err = refreshReport(dir, st); err != nil {
				return err
			}
			return saveState(dir, st)
		}
		if err = saveState(dir, st); err != nil {
			return err
		}
		return s.refreshIfWrote(dir, st, true)
	})
	return st, err
}

// TransitionSettle moves a job along one allowed lifecycle edge and applies
// settle to the moved state inside the same locked write: the status change
// and the settle land together, so a crash can never leave the transition
// without the settle (or the settle without the transition) — a window the
// separate Transition + Record writes left, which a restart would misread
// as an in-flight interval of the new status's kind. The blocked-boundary
// crossings use it for the budget interval settle (the counters and the
// re-anchor), so the anchor always names the start of the interval of the
// current status's kind. settle may not change ID, Status, BriefSHA256 or
// DecisionsAckedSeq: the stored values are restored after settle runs, like
// Record. A retry of a terminal-outcome transition republishes the report
// and settles nothing (the settle ran once, with the edge).
func (s *Store) TransitionSettle(id, to string, settle func(*State)) (State, error) {
	var st State
	err := s.withExisting(id, func(dir string) error {
		var err error
		st, err = readState(dir)
		if err != nil {
			return err
		}
		if st.Status == to && isTerminalOutcome(to) {
			// Idempotent retry of a transition whose report publish
			// crashed: the state is already the terminal outcome, so
			// republish the report and leave it unchanged.
			return refreshReport(dir, st)
		}
		if !transitions[st.Status][to] {
			return errUsage("job: transition " + st.Status + " -> " + to + " is not allowed")
		}
		if to == StatusClosed {
			closed, closeErr := s.closeLocked(dir, st, false)
			st = closed
			return closeErr
		}
		if to == StatusCollected && isTerminalOutcome(st.Status) {
			if err = refreshReport(dir, st); err != nil {
				return err
			}
		}
		st.Status = to
		saved := st
		settle(&st)
		st.ID = saved.ID
		st.Status = saved.Status
		st.BriefSHA256 = saved.BriefSHA256
		st.DecisionsAckedSeq = saved.DecisionsAckedSeq
		if isTerminalOutcome(to) {
			// The report before the terminal state, as in Transition.
			if err = refreshReport(dir, st); err != nil {
				return err
			}
			return saveState(dir, st)
		}
		if err = saveState(dir, st); err != nil {
			return err
		}
		return s.refreshIfWrote(dir, st, true)
	})
	return st, err
}

// readDir resolves the job directory for a lock-free read. Reads open only
// existing files: they never create the lock, never open it for writing,
// and work when the job directory and its files are read-only. Writers keep
// withLock exactly for serialization.
func (s *Store) readDir(id string) (string, error) {
	dir, err := Dir(s.Root, id)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(dir, "state.json")); err != nil {
		if os.IsNotExist(err) {
			return "", &ExitError{Code: ExitNotFound, Msg: "job: not found"}
		}
		return "", fmt.Errorf("job: state unreadable: %w", err)
	}
	return dir, nil
}

func (s *Store) withExisting(id string, fn func(dir string) error) error {
	dir, err := Dir(s.Root, id)
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(dir, "state.json")); err != nil {
		if os.IsNotExist(err) {
			return &ExitError{Code: ExitNotFound, Msg: "job: not found"}
		}
		return fmt.Errorf("job: state unreadable: %w", err)
	}
	return withLock(dir, func() error { return fn(dir) })
}

// Snapshot is the lock-free, write-free read projection for the CLI slice:
// the state, the event totals, the pending decision seqs (effective
// watermark, empty not nil) and the stored report (nil when absent).
// Unknown ids are ExitNotFound.
type Snapshot struct {
	State      State
	EventTotal int
	LastSeq    int
	Pending    []int
	Report     *Report
	// dir is the job's state directory the snapshot was read from: it is
	// not a state fact and serializes nowhere; it is only the address of
	// the lock-free reads that take a snapshot as their input
	// (HeldControls).
	dir string
}

func (s *Store) Snapshot(id string) (Snapshot, error) {
	var snap Snapshot
	dir, err := s.readDir(id)
	if err != nil {
		return snap, err
	}
	st, err := readState(dir)
	if err != nil {
		return snap, err
	}
	events, err := readEvents(dir)
	if err != nil {
		return snap, err
	}
	snap.State = st
	snap.dir = dir
	snap.EventTotal = len(events)
	snap.LastSeq = lastSeq(events)
	snap.Pending = pendingDecisions(events, effectiveWatermark(st.DecisionsAckedSeq, events))
	rep, err := readReport(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return snap, err
		}
	} else {
		snap.Report = &rep
	}
	return snap, nil
}

func readState(dir string) (State, error) {
	raw, err := readReplacedFile(filepath.Join(dir, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, &ExitError{Code: ExitNotFound, Msg: "job: not found"}
		}
		return State{}, fmt.Errorf("job: state unreadable: %w", err)
	}
	var st State
	if err = json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("job: state unreadable: %w", err)
	}
	st.DuplicateOf = ""
	return st, nil
}

func saveState(dir string, st State) error {
	st.Schema = 1
	st.DuplicateOf = ""
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeAtomic0600(filepath.Join(dir, "state.json"), raw)
}

// renameAtomic is the last step of an atomic replace; on Windows it admits
// lock-free readers that hold the file with FILE_SHARE_DELETE. Tests point it
// at a failing function to simulate a crash after the temp file is written.
var renameAtomic = replaceFile

func writeAtomic0600(dest string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return err
	}
	if err = file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return err
	}
	if err = renameAtomic(tmp, dest); err != nil {
		cleanup()
		return err
	}
	return os.Chmod(dest, 0o600)
}
