// supervise_collab.go is the job supervisor's end of the job's own worker
// collaborations, run on every terminal path (done, failed, timeout,
// cancel, including restarts): the supervisor asks every active
// collaboration of the job's own workspace to finish — the stop
// amendment, dispatched as an amendment to the participants that are names
// in the job's team — waits on the injected clock on the shared deadline
// (the stop deadline for cancel and timeout, one defaultTimeoutGrace from
// now for done and failed) until every job collaboration is finished, and
// persists one outcome per collaboration with its evidence into
// state.json BEFORE the terminal transition, so the partial facts exist
// before the outcome is visible; the report's colaboracoes and the
// partial fields are derived from that persisted list by every rewrite.
// The still-active ones are ended after the transition (Store.Stop, unless
// escalated, and Store.Finalize reject — one retry on a generation
// conflict each, and never accept) and the confirmed facts are recorded
// back into the state, and the release runs with every readable
// collaboration finished, so no release is refused and no pane or process
// is left behind. The supervisor is the owner of the job-owned assignments
// of the job's own workspace and can only interrupt or reject them: it
// never finalizes accept, never edits or deletes an assignment file, and
// never rewrites a phase by hand. A readable record the job does not own —
// another workspace or another project root than the trusted one — is
// never dispatched to, stopped, finalized, waited on, or recorded, and
// every evidence read is bounded and root-bound, so a hostile report
// target is refused without publishing its path. A partial or unavailable
// outcome is never an accepted success: the job status, the partial count
// and the publico block are decided elsewhere and are never changed by
// this step. A restarted supervisor loads the persisted snapshot first,
// never drops an id it carries, and re-asks once in its own process — the
// same accepted bound as the stop amendment.
package job

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// maxEvidenceBytes bounds every evidence read of the terminal step: a
// report with more bytes is refused, never hashed.
const maxEvidenceBytes = 8 << 20

// errEvidenceOutside is the refusal of a path no allowed root accepts —
// the evidence is outside the allowed directories, and nothing about its
// target (path or content) is published.
var errEvidenceOutside = errors.New("evidence outside the allowed directories")

// The collaboration outcome classes recorded in report.json.
const (
	collabOutcomeFinished    = "finished"
	collabOutcomePartial     = "partial"
	collabOutcomeUnavailable = "unavailable"
)

// collabVerdictReject is the verdict the supervisor plans for every
// collaboration it has to interrupt; the supervisor never accepts.
const collabVerdictReject = "reject"

// ParticipantReport is one participant's current task report for the stop
// amendment, as recorded in the outcome: the last-report target of the job
// workspace state dir and the fresh sha256 of the file (empty when the
// file is missing or unreadable). A participant without a current report
// contributes no entry.
type ParticipantReport struct {
	Name         string `json:"name"`
	Report       string `json:"report"`
	ReportSHA256 string `json:"report_sha256,omitempty"`
	// ReportCheck stands "rejected" when the marker's target is outside
	// the allowed directories (or unreadable): the entry then never
	// carries the target's path or hash.
	ReportCheck string `json:"report_check,omitempty"`
}

// CollaborationOutcome is one collaboration the job ended on a terminal
// path, as recorded in report.json's colaboracoes. The evidence is read
// from the record at the end of the grace: the last event's type, actor
// and delivery, its report path, the hash recorded with the event and the
// fresh sha256 of the file (empty when the file is missing or unreadable),
// and the phase, round and revision of the read. An outcome recorded as
// partial because the supervisor interrupted it stays partial (verdict
// reject) forever; only a collaboration that reached finished by the
// orchestrator's own finalize before the snapshot is finished, with its
// verdict.
type CollaborationOutcome struct {
	ID           string `json:"id"`
	Phase        string `json:"phase"`
	Round        int    `json:"round"`
	Revision     string `json:"revision"`
	LastEvent    string `json:"last_event"`
	Report       string `json:"report"`
	ReportHash   string `json:"report_hash,omitempty"`
	ReportSHA256 string `json:"report_sha256,omitempty"`
	// ReportCheck stands "rejected" when the fresh sha256 of the last
	// event's report could not be computed through the bounded,
	// root-bound read: the recorded path and hash (trusted store data)
	// stand, the fresh hash stays empty.
	ReportCheck string `json:"report_check,omitempty"`
	// ParticipantReports are the participants' current task reports for the
	// stop amendment (their last-report targets in the job workspace state
	// dir), captured in the snapshot before the termination; the partial
	// capture must not discard results the participants wrote without a
	// final collaboration event.
	ParticipantReports    []ParticipantReport `json:"participant_reports,omitempty"`
	Verdict               string              `json:"verdict,omitempty"`
	Outcome               string              `json:"outcome"`
	Cause                 string              `json:"cause,omitempty"`
	FinalizedBySupervisor bool                `json:"finalized_by_supervisor,omitempty"`
}

// settleCollaborationHook is the test seam of the settlement: called once
// per settled collaboration, after its Stop and Finalize and before the
// confirmed-facts report rewrite; a panic there is a crash between the
// settlement and the rewrite.
var settleCollaborationHook func(id string)

// collabListActive is the test seam of the enumeration: the job
// workspace store's ListActive read behind the retry loop of
// endCollaborations, so a directory-level failure can be scripted and
// observed across the shared deadline.
var collabListActive = func(s collaboration.Store) ([]collaboration.Assignment, []collaboration.UnreadableAssignment, error) {
	return s.ListActive()
}

// PartialCollaborations derives the partial facts from the persisted
// outcomes: the sorted unique ids of the outcomes whose outcome is
// partial or unavailable; an outcome finished by the orchestrator's own
// finalize (with any verdict) is never partial. It returns an empty
// non-nil list when none stands, so the report and the status line carry
// the keys with empty values, never null.
func PartialCollaborations(st State) []string {
	ids := map[string]bool{}
	for _, o := range st.Colaboracoes {
		if o.Outcome == collabOutcomePartial || o.Outcome == collabOutcomeUnavailable {
			ids[o.ID] = true
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// unionSorted merges the new ids into the existing list, sorted and
// without duplicates: the persisted list is the union of what the ask
// observed and what an earlier process observed.
func unionSorted(existing, add []string) []string {
	set := map[string]bool{}
	for _, id := range existing {
		set[id] = true
	}
	for _, id := range add {
		set[id] = true
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// mergeCollabOutcomes merges the newly captured outcomes into the
// persisted list (the no-late-erasure rule): an id already persisted
// keeps its persisted outcome and verdict forever — only settle's
// confirmed facts (the phase, the supervisor flag and the cause) may
// touch it later — and a new id is added. The list stays sorted by id
// with no duplicates.
func mergeCollabOutcomes(persisted, fresh []CollaborationOutcome) []CollaborationOutcome {
	byID := map[string]CollaborationOutcome{}
	for _, o := range fresh {
		byID[o.ID] = o
	}
	for _, o := range persisted {
		if f, ok := byID[o.ID]; ok {
			// A persisted id keeps its persisted outcome and verdict
			// forever; only the settle fields — the final phase, the
			// supervisor's confirmed end and the cause — stand from the
			// fresh facts.
			o.Phase = f.Phase
			o.FinalizedBySupervisor = f.FinalizedBySupervisor
			o.Cause = f.Cause
			byID[o.ID] = o
		} else {
			byID[o.ID] = o
		}
	}
	out := make([]CollaborationOutcome, 0, len(byID))
	for _, id := range sortedKeys(byID) {
		out = append(out, byID[id])
	}
	return out
}

func sortedKeys(m map[string]CollaborationOutcome) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// collabSettlePreWriteHook is the test seam of the settlement window:
// called with the collaboration id after the fresh read and right before
// the Stop write; it is where a concurrent generation bump interleaves.
var collabSettlePreWriteHook func(id string)

// collabFinalizeFirstAttemptHook replaces only the first finalize attempt in
// tests. It lets a stale-generation store update interleave deterministically;
// the retry always uses Store.Finalize. A nil hook uses Store.Finalize for both.
var collabFinalizeFirstAttemptHook func(id string) error

// collabStore returns the collaboration store of the job's own workspace
// state dir, or nil when the recorded workspace is missing or not one
// safe path segment: the supervisor only ever acts on that store, and
// every readable record is additionally filtered on the workspace.
func (s *Supervisor) collabStore(st State) *collaboration.Store {
	if !validWorkspaceID(st.WorkspaceID) {
		return nil
	}
	return &collaboration.Store{StateDir: filepath.Join(s.Store.Root, st.WorkspaceID)}
}

// ownsCollaboration reports whether a readable record is one of the
// job's own collaborations: the job's workspace AND the trusted project
// root, never derived from the record's own claim. An empty ProjectRoot
// owns nothing: the record is never dispatched to, stopped, finalized,
// waited on, or recorded.
func (s *Supervisor) ownsCollaboration(st State, a collaboration.Assignment) bool {
	return s.ProjectRoot != "" && a.Workspace == st.WorkspaceID && a.Root == s.ProjectRoot
}

// askCollaborations asks every active collaboration of the job's own
// workspace to finish: it dispatches the stop amendment as an amendment to
// every participant of every active assignment that is a name in the job's
// team (the roster), writing stop-amend.md with the exact contract body
// first when the stop path has not written it yet (the done/failed paths).
// The owned active ids it is about to ask are persisted into the state
// (collab_ids, unioned, sorted, unique) with one Record BEFORE it
// dispatches any stop amendment, so an id that finishes before the
// terminal enumeration still stands in the enumeration. It never
// Store.Stops: the recorded interruption happens after the grace, when
// settleCollaborations finalizes, so the participants can still publish
// their final events. A dispatch failure is a friction line and never
// blocks the terminal path. The ask runs at most once per supervisor
// process (collabAsked), the same accepted bound as the orchestrator's
// stop amendment: a restart re-asks once in its own process.
func (s *Supervisor) askCollaborations() {
	if s.collabAsked {
		return
	}
	s.collabAsked = true
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor cannot read the state to ask the collaborations to finish")
		return
	}
	st := snap.State
	store := s.collabStore(st)
	if store == nil {
		return
	}
	active, _, err := collabListActive(*store)
	if err != nil {
		s.frictionf("job: the supervisor cannot list the job's collaborations to ask them to finish: " + err.Error())
		return
	}
	var mine []collaboration.Assignment
	for _, a := range active {
		if s.ownsCollaboration(st, a) {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		return
	}
	// The owned active ids, durably observed before any dispatch: a crash
	// or a later enumeration (state-first) reads them from the state and
	// never drops an id the ask ran for.
	asked := make([]string, 0, len(mine))
	for _, a := range mine {
		asked = append(asked, a.ID)
	}
	if _, err := s.Store.Record(s.ID, func(st *State) {
		st.CollabIDs = unionSorted(st.CollabIDs, asked)
	}); err != nil {
		s.frictionf("job: the supervisor cannot record the collaborations asked to finish: " + err.Error())
	}
	ops, ok := s.collabOps()
	if !ok {
		s.frictionf("job: finishing budget exhausted before the collaboration stop request")
		return
	}
	rows, err := ops.Roster()
	if err != nil {
		s.frictionf("job: the supervisor cannot read the team roster to ask the collaborations to finish")
		return
	}
	team := make(map[string]bool, len(rows))
	for _, row := range rows {
		team[row.Name] = true
	}
	jobDir, err := s.Store.readDir(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor cannot find the job directory to ask the collaborations to finish")
		return
	}
	path := filepath.Join(jobDir, "stop-amend.md")
	if _, err := os.Stat(path); err != nil {
		// The done/failed path never wrote the stop amendment; write the
		// exact contract body before dispatching it.
		if err := writeAtomic0600(path, []byte(stopAmendBody)); err != nil {
			s.frictionf("job: the supervisor cannot write the stop amendment")
		}
	}
	for _, a := range mine {
		for _, p := range []collaboration.Participant{a.Author, a.Reviewer} {
			if !team[p.Name] {
				continue
			}
			ops, ok := s.collabOps()
			if !ok {
				s.frictionf("job: finishing budget exhausted during the collaboration stop request")
				return
			}
			if err := ops.Dispatch(p.Name, path, true); err != nil {
				s.frictionf("job: the supervisor cannot dispatch the stop amendment to " + p.Name)
			}
		}
	}
}

// collabOps is the team adapter for one command of the collaboration stop
// request: outside the terminal procedure's capture it is the supervisor's
// own; inside it, an adapter whose command timeout is lowered to what is left
// of the collaboration step (one that cannot be bounded is used as is). It
// reports false when no time is left.
func (s *Supervisor) collabOps() (teamOps, bool) {
	if s.askDeadline == 0 {
		return s.Ops, true
	}
	remaining := time.Duration(s.askDeadline - s.clk().Monotonic())
	if remaining <= 0 {
		return nil, false
	}
	if b, ok := s.Ops.(interface{ bounded(time.Duration) teamOps }); ok {
		return b.bounded(remaining), true
	}
	return s.Ops, true
}

// endCollaborations is the common terminal-path collaboration step, called
// from finishOutcome and fail before the terminal transition (the capture)
// and from finalize by a fresh process over a terminal job (the restart
// pass): it reads the persisted state FIRST, enumerates the job's own
// workspace store (a directory-level failure retries each poll on the
// same deadline and, if it still fails, returns every known id with the
// error), asks the still-active ones to finish (the stop path already
// asked in beginStop; the flag makes it a no-op there), waits on the
// injected clock on the shared deadline — the stop deadline for cancel
// and timeout, one defaultTimeoutGrace from now for done and failed,
// computed once by the caller and never restarted per tick — until every
// job collaboration is finished or the deadline passes, and returns one
// outcome per known collaboration with its evidence, sorted by id:
// CollabIDs union the persisted outcomes' ids, the owned active records
// and the unreadable records with provable ownership (an id the job
// already knows, or a bounded raw claim still showing the job's own
// workspace and trusted root; any other unreadable record is friction
// only). It waits only when at least one active job collaboration
// exists; nothing else changes. The caller persists the
// merged outcomes into the state (the single source of the report's
// colaboracoes) before the terminal transition or, on a fresh process,
// before the settlement, so a crash between the persistence and the
// settlement loses no outcome and a finished id observed by the ask
// stands finished with its verdict, never partial.
func (s *Supervisor) endCollaborations(deadline int64) ([]CollaborationOutcome, error) {
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor cannot read the state to end the collaborations")
		return nil, nil
	}
	st := snap.State
	store := s.collabStore(st)
	if store == nil {
		return nil, nil
	}
	// The enumeration, through the seam: a directory-level failure retries
	// each poll on the same deadline; if it still fails at the deadline
	// every known id stands (the persisted ones unchanged, the rest
	// unavailable) and the error is returned for the caller to friction
	// and still persist. A persisted id is never dropped.
	active, unreadable, err := collabListActive(*store)
	for err != nil && s.Clock.Monotonic() < deadline {
		s.clockSleep(s.poll())
		active, unreadable, err = collabListActive(*store)
	}
	if err != nil {
		known := map[string]CollaborationOutcome{}
		for _, o := range st.Colaboracoes {
			known[o.ID] = o
		}
		out := make([]CollaborationOutcome, 0, len(unionSorted(st.CollabIDs, outcomeIDs(st.Colaboracoes))))
		for _, id := range unionSorted(st.CollabIDs, outcomeIDs(st.Colaboracoes)) {
			if o, ok := known[id]; ok {
				out = append(out, o)
				continue
			}
			out = append(out, CollaborationOutcome{
				ID:      id,
				Outcome: collabOutcomeUnavailable,
				Cause:   "collaborations could not be enumerated",
			})
		}
		return out, err
	}
	// Only this job's collaborations: the store dir is the job's own
	// workspace state dir, and every readable record is owned on the
	// recorded workspace AND the trusted project root (ownsCollaboration);
	// a readable record the job does not own is never dispatched to,
	// waited on, stopped, finalized, or recorded.
	activeCount := 0
	ids := map[string]bool{}
	for _, id := range st.CollabIDs {
		ids[id] = true
	}
	for _, a := range active {
		if !s.ownsCollaboration(st, a) {
			continue
		}
		activeCount++
		ids[a.ID] = true
	}
	// The persisted outcomes keep their ids even when the enumeration no
	// longer returns them.
	persisted := map[string]CollaborationOutcome{}
	for _, o := range st.Colaboracoes {
		ids[o.ID] = true
		persisted[o.ID] = o
	}
	// An unreadable record joins the outcomes only with proof of
	// ownership: an id the job already knows (asked or persisted) or a
	// bounded raw claim of the record still showing the job's own
	// workspace and trusted root. An unknown or foreign claim is friction
	// only — never an outcome, never stopped, finalized or waited on, and
	// never partial.
	for _, u := range unreadable {
		if ids[u.ID] {
			continue
		}
		ws, root, err := store.ReadClaim(u.ID)
		if err != nil || s.ProjectRoot == "" || ws != st.WorkspaceID || root != s.ProjectRoot {
			s.frictionf("job: the collaboration " + u.ID + " is unreadable and not provably the job's own: " + u.Cause)
			continue
		}
		ids[u.ID] = true
	}
	if activeCount > 0 {
		// The done/failed ask (a no-op on the stop path, which already
		// asked in beginStop), and the wait on the shared deadline until
		// every job collaboration is finished or the deadline passes; the
		// deadline is fixed once, never restarted per tick.
		s.askCollaborations()
		for {
			allFinished := true
			for _, a := range active {
				if !s.ownsCollaboration(st, a) {
					continue
				}
				fresh, err := store.Read(a.ID)
				if err != nil || fresh.Phase != collaboration.Finished {
					allFinished = false
					break
				}
			}
			if allFinished || s.Clock.Monotonic() >= deadline {
				break
			}
			s.clockSleep(s.poll())
		}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	outcomes := make([]CollaborationOutcome, 0, len(list))
	for _, id := range list {
		if o, ok := persisted[id]; ok {
			// The persisted outcome stands: it is never rewritten to
			// another outcome or verdict; settleCollaborations adds only
			// the confirmed facts.
			outcomes = append(outcomes, o)
			continue
		}
		a, err := store.Read(id)
		if err != nil {
			s.frictionf("job: the collaboration " + id + " is unreadable: " + err.Error())
			outcomes = append(outcomes, CollaborationOutcome{
				ID:      id,
				Outcome: collabOutcomeUnavailable,
				Cause:   err.Error(),
			})
			continue
		}
		if a.Phase == collaboration.Finished {
			// Finished by the orchestrator's own finalize before the
			// snapshot (an id the ask persisted that has since finished,
			// included): finished, with the record's verdict and the
			// last event's evidence; never partial.
			outcomes = append(outcomes, s.collaborationEvidence(a, st, store.StateDir, collabOutcomeFinished, a.Verdict))
			continue
		}
		// Still active when the job ends: the supervisor is about to
		// interrupt it (Stop + Finalize reject) after the snapshot is
		// persisted; the outcome is partial with the planned verdict.
		outcomes = append(outcomes, s.collaborationEvidence(a, st, store.StateDir, collabOutcomePartial, collabVerdictReject))
	}
	return outcomes, nil
}

// outcomeIDs is the ids of the persisted outcomes, in their order.
func outcomeIDs(outcomes []CollaborationOutcome) []string {
	ids := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		ids = append(ids, o.ID)
	}
	return ids
}

// collabEvidenceRoot is one allowed directory as a trusted base plus the
// components the code appends to it: only the base may resolve through
// symlinks (the job workspace state dir, os.TempDir() — itself a
// symlinked path on macOS — or the job worktree); every appended component
// must be a real directory, checked on the handle that is actually opened
// (openEvidenceRoot), so a reports directory that is, or is swapped for, a
// symlink never becomes an allowed root.
type collabEvidenceRoot struct {
	base   string
	suffix string
}

// path is the lexical path of the root: the base with the suffix appended.
func (r collabEvidenceRoot) path() string {
	return filepath.Join(r.base, r.suffix)
}

// collabEvidenceRoots are the allowed directories of one evidence read,
// derived only from trusted job data (never from the marker or event
// being validated): the collaboration store's reports directory, the
// routing reports directory of the job workspace (the location the
// workers outside the job worktree publish their reports to), and the job
// worktree itself when it is an absolute path (a base with no suffix). A
// root that cannot be opened as described in openEvidenceRoot is skipped
// by the read, exactly like a missing root.
func collabEvidenceRoots(st State, stateDir string) []collabEvidenceRoot {
	roots := []collabEvidenceRoot{
		{base: stateDir, suffix: "reports"},
		{base: os.TempDir(), suffix: filepath.Join("soho", st.WorkspaceID, "reports")},
	}
	if filepath.IsAbs(st.Dir) {
		roots = append(roots, collabEvidenceRoot{base: st.Dir})
	}
	return roots
}

// evidenceComponentOpenedHook and evidenceRootOpenedHook are the test
// seams of the root-open windows: the first is called with a component's
// name right after its handle is opened and before the component check,
// the second right after the whole root is opened and before the file is
// opened through it; that is where a concurrent replacement interleaves.
var (
	evidenceComponentOpenedHook func(name string)
	evidenceRootOpenedHook      func()
)

// openEvidenceRoot opens the root as an os.Root bound to a real directory,
// never blocking on the way (openEvidenceBase, openEvidenceComponent: a
// FIFO or any other non-directory at the base or at a component is
// refused at once): the trusted base is opened (its own symlinks resolve),
// then each appended component is opened through the parent's handle,
// which refuses a component that escapes the parent, and it is accepted only
// when the parent's Lstat of its name shows a real directory (not a
// symlink) that is the same file as the handle just opened. A component
// swapped before the open is refused by the escape check or the Lstat; a
// component swapped after the open no longer matches the handle and is
// refused; the check always concerns the handle the read then uses.
func openEvidenceRoot(r collabEvidenceRoot) (*os.Root, error) {
	if r.base == "" {
		return nil, errEvidenceOutside
	}
	cur, err := openEvidenceBase(r.base)
	if err != nil {
		return nil, err
	}
	if r.suffix == "" {
		return cur, nil
	}
	for _, name := range strings.Split(filepath.ToSlash(r.suffix), "/") {
		next, err := openEvidenceComponent(cur, name)
		if err != nil {
			_ = cur.Close()
			return nil, err
		}
		if evidenceComponentOpenedHook != nil {
			evidenceComponentOpenedHook(name)
		}
		link, lerr := cur.Lstat(name)
		opened, oerr := next.Stat(".")
		_ = cur.Close()
		if lerr != nil || oerr != nil || !link.IsDir() || link.Mode()&os.ModeSymlink != 0 || !os.SameFile(link, opened) {
			_ = next.Close()
			return nil, errEvidenceOutside
		}
		cur = next
	}
	return cur, nil
}

// hashEvidence returns the accepted absolute path and the fresh sha256 of
// one evidence report read under the allowed roots, or an error. The path
// must be non-empty and absolute; for the first allowed root where the
// path is local (checked lexically: filepath.Rel and filepath.IsLocal),
// the root is opened bound to a real directory (openEvidenceRoot), the
// file is opened through that handle with the non-blocking read mode (a
// FIFO can never hold the read) — the handle refuses a symlink that
// escapes the root and any absolute symlink, and a relative symlink
// resolving inside the root still stands —
// and the opened file must be a regular file of at most maxEvidenceBytes
// bytes, checked on the opened handle, with the hash computed through a
// limit reader that refuses a longer read. Every refusal — a missing,
// unreadable or symlinked root, a missing file, a symlink escape, a
// non-regular file, or an overlong file — is the same error: the evidence
// is outside the allowed directories, and nothing about the target is
// published.
func hashEvidence(allowedRoots []collabEvidenceRoot, path string) (string, string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", "", errors.New("evidence path is not an absolute path")
	}
	cleaned := filepath.Clean(path)
	for _, root := range allowedRoots {
		if root.base == "" {
			continue
		}
		rel, err := filepath.Rel(root.path(), cleaned)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		r, err := openEvidenceRoot(root)
		if err != nil {
			// The root is missing, unreadable or not a real directory:
			// skip it.
			continue
		}
		if evidenceRootOpenedHook != nil {
			evidenceRootOpenedHook()
		}
		f, err := r.OpenFile(rel, platform.ReadOnlyNonBlocking, 0)
		_ = r.Close()
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxEvidenceBytes {
			_ = f.Close()
			continue
		}
		sum := sha256.New()
		n, err := io.Copy(sum, io.LimitReader(f, maxEvidenceBytes+1))
		if err != nil || n > int64(maxEvidenceBytes) {
			_ = f.Close()
			continue
		}
		_ = f.Close()
		return cleaned, hex.EncodeToString(sum.Sum(nil)), nil
	}
	return "", "", errEvidenceOutside
}

// collaborationEvidence is the outcome fields from one fresh record read:
// the last event's type, actor and delivery (none when the record has no
// event), its report path, the hash recorded with the event and the fresh
// sha256 of the file computed only through the bounded, root-bound
// evidence read (on a refusal the recorded path and hash stand as trusted
// store data, the fresh hash stays empty, and the outcome records the
// rejection), the participants' current task reports for the stop
// amendment (their last-report targets in the workspace state dir
// stateDir, published only when the evidence read accepts them, with the
// fresh sha256 of each file), and the phase, round and revision of the
// read.
func (s *Supervisor) collaborationEvidence(a collaboration.Assignment, st State, stateDir, outcome, verdict string) CollaborationOutcome {
	out := CollaborationOutcome{
		ID:       a.ID,
		Phase:    a.Phase,
		Round:    a.Round,
		Revision: a.Revision,
		Verdict:  verdict,
		Outcome:  outcome,
	}
	roots := collabEvidenceRoots(st, stateDir)
	if len(a.Events) == 0 {
		out.LastEvent = "none"
	}
	if len(a.Events) > 0 {
		e := a.Events[len(a.Events)-1]
		out.LastEvent = e.Type + " by " + e.Actor + ", delivery " + e.Delivery
		out.Report = e.Report
		out.ReportHash = e.ReportHash
		if e.Report != "" {
			// The fresh sha256 only through the bounded read: on a
			// refusal the rejection stands, the fresh hash stays empty.
			if _, sum, err := hashEvidence(roots, e.Report); err == nil {
				out.ReportSHA256 = sum
			} else {
				out.ReportCheck = "rejected"
			}
		}
	}
	// The participants' current task reports, captured in the snapshot
	// before any termination: the last-report marker of the job workspace
	// state dir names the report the participant wrote for its current
	// task (the stop amendment included); the marker's target is
	// participant-controlled, so it is published only when the bounded
	// read accepts it — a missing marker drops the entry, a refused
	// target drops its path and stands with the rejection.
	for _, p := range []collaboration.Participant{a.Author, a.Reviewer} {
		report := core.LastReport(stateDir, p.Name)
		if report == "" {
			continue
		}
		entry := ParticipantReport{Name: p.Name}
		if accepted, sum, err := hashEvidence(roots, report); err == nil {
			entry.Report = accepted
			entry.ReportSHA256 = sum
		} else {
			entry.ReportCheck = "rejected"
		}
		out.ParticipantReports = append(out.ParticipantReports, entry)
	}
	return out
}

// settleCollaborations ends the collaborations the snapshot leaves active,
// after the snapshot is persisted: for each outcome whose record is still
// active in the job's own workspace, Store.Stop (unless the fresh read
// shows escalated already) and then Store.Finalize(reject), one retry on a
// generation conflict each; a successful retry is this process's own
// finalize, a second failure is a friction line and the outcome keeps the
// cause. It never finalizes accept, never edits or
// deletes an assignment file, and never touches a collaboration outside
// the job's workspace and root. It returns the outcomes with the confirmed facts
// added — finalized_by_supervisor when this process's Finalize succeeded
// and the final phase from the confirmed read — and never changes a
// persisted outcome or verdict.
func (s *Supervisor) settleCollaborations(outcomes []CollaborationOutcome) []CollaborationOutcome {
	if len(outcomes) == 0 {
		return outcomes
	}
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil {
		s.frictionf("job: the supervisor cannot read the state to settle the collaborations")
		return outcomes
	}
	st := snap.State
	store := s.collabStore(st)
	if store == nil {
		return outcomes
	}
	for i := range outcomes {
		o := &outcomes[i]
		a, err := store.Read(o.ID)
		if err != nil {
			// The record disappeared or became unreadable: the persisted
			// outcome stands; there is nothing to settle.
			s.frictionf("job: the collaboration " + o.ID + " is unreadable: " + err.Error())
			continue
		}
		if a.Phase == collaboration.Finished {
			// Ended already (by the orchestrator, by this process's
			// earlier step, or by an earlier supervisor process): confirm
			// the final phase only.
			o.Phase = a.Phase
			if settleCollaborationHook != nil {
				settleCollaborationHook(o.ID)
			}
			continue
		}
		if !s.ownsCollaboration(st, a) {
			// Not this job's collaboration (another workspace or another
			// project root): never touched.
			continue
		}
		finalized := false
		cause := ""
		phase := a.Phase
		// The recorded interruption, with one retry on a generation
		// conflict; the fresh read after a conflict re-aims the retry.
		if phase != collaboration.Escalated {
			if collabSettlePreWriteHook != nil {
				collabSettlePreWriteHook(o.ID)
			}
			if _, err := store.Stop(o.ID); err != nil {
				fresh, rerr := store.Read(o.ID)
				if rerr != nil {
					cause, phase = rerr.Error(), ""
				} else {
					phase = fresh.Phase
					if phase != collaboration.Escalated && phase != collaboration.Finished {
						if _, err = store.Stop(o.ID); err != nil {
							cause = err.Error()
						}
					}
				}
			}
		}
		// The state-based rejection, with one retry on a generation
		// conflict; the store refuses it outside awaiting-orchestrator
		// and escalated, and the supervisor only ever asks it to reject.
		if cause == "" && phase != "" && phase != collaboration.Finished {
			var err error
			if collabFinalizeFirstAttemptHook != nil {
				err = collabFinalizeFirstAttemptHook(o.ID)
			} else {
				_, err = store.Finalize(o.ID, collabVerdictReject)
			}
			if err != nil {
				fresh, rerr := store.Read(o.ID)
				if rerr != nil {
					cause, phase = rerr.Error(), ""
				} else {
					phase = fresh.Phase
					if phase != collaboration.Finished {
						if _, err = store.Finalize(o.ID, collabVerdictReject); err != nil {
							cause = err.Error()
						} else {
							// A successful retry is this process's own
							// finalize: the confirmed facts, exactly like
							// the first call.
							finalized = true
							phase = collaboration.Finished
						}
					}
					// A Finished read here means another actor finalized it
					// in the window: the outcome keeps the persisted partial
					// and no finalized_by_supervisor — this process cannot
					// claim an end it did not perform.
				}
			} else {
				finalized = true
				phase = collaboration.Finished
			}
		}
		if cause != "" {
			s.frictionf("job: the collaboration " + o.ID + " was not ended: " + cause)
			o.Cause = cause
		} else if finalized {
			// The confirmed facts: this process ended it, and the cause of
			// an earlier failed settle no longer applies.
			o.FinalizedBySupervisor = true
			o.Cause = ""
		}
		if fresh, err := store.Read(o.ID); err == nil {
			o.Phase = fresh.Phase
		}
		if settleCollaborationHook != nil {
			settleCollaborationHook(o.ID)
		}
	}
	return outcomes
}
