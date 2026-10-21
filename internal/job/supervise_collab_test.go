package job

// The collaboration terminal step (supervise_collab.go) on every terminal
// path: the ask of the job's active collaborations, the shared deadline
// wait, the evidence and the partial/finished/unavailable outcomes, the
// persist-before-terminate order, the crash and restart story, the no-accept
// bound, and the honest totals.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
)

// errCollabTestCrash is the scripted crash marker of the settlement crash
// test.
var errCollabTestCrash = errors.New("simulated crash inside the collaboration settlement")

// collabWorkspace is the job-workspace state dir the collaboration step
// reads (the fixture records workspace "w1" on the job): the store's root
// plus the workspace id, two levels above the job dir.
func collabWorkspace(t *testing.T, jobDir string) string {
	t.Helper()
	return filepath.Join(filepath.Dir(filepath.Dir(jobDir)), "w1")
}

// collabSup wires the nsSup fixture to job workspace "w1": the workspace id
// is recorded on the state and a fake herdr adapter stands in for the
// workspace close; it returns the fake so the tests can assert the close.
func collabSup(t *testing.T, store *Store, sup *Supervisor) *fakeHerdr {
	t.Helper()
	if _, err := store.Record("job-1", func(st *State) { st.WorkspaceID = "w1" }); err != nil {
		t.Fatal(err)
	}
	// The supervisor binds the collaborations it owns on the job's
	// trusted root: the recorded worktree directory, the root the fixture
	// records use.
	sup.ProjectRoot = collabJobDir(t, store)
	herdr := &fakeHerdr{}
	sup.Herdr = herdr
	return herdr
}

// requireNoActiveCollabs asserts the no-orphan bound (i): every
// collaboration record of the job workspace is finished or absent — none
// stands active.
func requireNoActiveCollabs(t *testing.T, ws string) {
	t.Helper()
	cstore := collaboration.Store{StateDir: ws}
	active, unreadable, err := cstore.ListActive()
	if err != nil {
		t.Fatalf("list the job collaborations: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active collaborations after the terminal path: %+v", active)
	}
	if len(unreadable) != 0 {
		t.Fatalf("unreadable collaborations after the terminal path: %+v", unreadable)
	}
}

// requireWorkspaceClosed asserts the no-orphan bounds (iv) and (iii):
// the fake herdr closed exactly the recorded workspace and the job's
// copies and processes were gc'd once.
func requireWorkspaceClosed(t *testing.T, herdr *fakeHerdr, team *fakeTeam) {
	t.Helper()
	if len(herdr.CloseIDs) != 1 || herdr.CloseIDs[0] != "w1" {
		t.Fatalf("close calls = %v, want the job workspace once", herdr.CloseIDs)
	}
	if team.gcCalls != 1 {
		t.Fatalf("gc calls = %d, want 1", team.gcCalls)
	}
}

// collabJobDir is the job's recorded worktree: the trusted root the
// fixtures bind the collaboration records to (a plain directory in the
// fixture: not a git repository, so the root resolution is the directory
// itself).
func collabJobDir(t *testing.T, store *Store) string {
	t.Helper()
	return supState(t, store).Dir
}

// writeStopReportInWs writes a participant's stop report inside the job
// workspace's reports directory — the only location the terminal step
// accepts for the participant reports — and records the participant's
// last-report marker pointing at it; it returns the path and its hash.
func writeStopReportInWs(t *testing.T, ws, name, body string) (string, string) {
	t.Helper()
	dir := filepath.Join(ws, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"-stop.md")
	raw := []byte(body)
	if err := os.WriteFile(path, raw, 0o666); err != nil {
		t.Fatal(err)
	}
	writeLastReport(t, ws, name, path)
	return path, collaboration.Hash(raw)
}

// readAskedFile returns the ids of the job's colaboracoes-asked.json (nil
// when the file is missing).
func readAskedFile(t *testing.T, jobDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(jobDir, "colaboracoes-asked.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the asked file: %v", err)
	}
	var file struct {
		Asked []string `json:"asked"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("unmarshal the asked file: %v", err)
	}
	return file.Asked
}

// makeCollab creates one collaboration in the store directory ws with the
// record's workspace and root (in the fixtures the job's trusted root: the
// recorded worktree directory; a foreign root or workspace makes the
// record not the job's) and the given participants and panes, and returns
// the store and the assigned id. The author's cwd holds the collaboration
// inputs, so a valid revision can be computed from the record.
func makeCollab(t *testing.T, ws, root, workspace, author, reviewer, authorPane, reviewerPane string) (collaboration.Store, string) {
	t.Helper()
	cstore := collaboration.Store{StateDir: ws}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "code.go"), []byte("package main\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	created, err := cstore.Create(collaboration.Assignment{
		Workspace: workspace,
		Root:      root,
		BriefHash: collaboration.Hash([]byte("the job brief")),
		Paths:     []string{"code.go"},
		Author: collaboration.Participant{
			Name: author, Pane: authorPane, Family: "openai",
			Started: "2026-10-09T10:00:00Z", Session: "s-" + author, Cwd: cwd,
		},
		Reviewer: collaboration.Participant{
			Name: reviewer, Pane: reviewerPane, Family: "anthropic",
			Started: "2026-10-09T10:00:00Z", Session: "s-" + reviewer, Cwd: cwd,
		},
	})
	if err != nil {
		t.Fatalf("create collaboration for %s: %v", author, err)
	}
	return cstore, created.ID
}

func collabRoster(names ...string) []rosterRow {
	rows := make([]rosterRow, 0, len(names))
	for _, n := range names {
		rows = append(rows, rosterRow{Name: n, Role: "worker"})
	}
	return rows
}

// writeCollabRecord lays one raw assignment.json under the workspace store
// directory without the store's write path: the record is exactly the
// given bytes (a torn or hostile shape the store refuses to read).
func writeCollabRecord(t *testing.T, ws, id, body string) {
	t.Helper()
	dir := filepath.Join(ws, "collaboration", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assignment.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeCollabReport writes a collaboration report file and returns its path
// and hash.
func writeCollabReport(t *testing.T, name, body string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+"-collab-report.md")
	raw := []byte(body)
	if err := os.WriteFile(path, raw, 0o666); err != nil {
		t.Fatal(err)
	}
	return path, collaboration.Hash(raw)
}

// writeCollabReportInWs writes a collaboration report inside the job
// workspace's reports directory — the location the terminal step's
// bounded evidence read accepts, so the fresh sha256 of the event's
// evidence stands — and returns the path and its hash.
func writeCollabReportInWs(t *testing.T, ws, name, body string) (string, string) {
	t.Helper()
	dir := filepath.Join(ws, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"-collab-report.md")
	raw := []byte(body)
	if err := os.WriteFile(path, raw, 0o666); err != nil {
		t.Fatal(err)
	}
	return path, collaboration.Hash(raw)
}

// writeLastReport records the participant's last-report marker (the current
// task report for the stop amendment) in the job workspace state dir.
func writeLastReport(t *testing.T, ws, name, target string) {
	t.Helper()
	if err := os.MkdirAll(ws, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "last-report-"+name), []byte(target+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
}

// readColabs returns the colaboracoes array of the stored report.json.
func readColabs(t *testing.T, jobDir string) []CollaborationOutcome {
	t.Helper()
	rep, err := readReport(jobDir)
	if err != nil {
		t.Fatalf("read stored report: %v", err)
	}
	return rep.Colaboracoes
}

// findColab returns one outcome by id.
func findColab(t *testing.T, out []CollaborationOutcome, id string) CollaborationOutcome {
	t.Helper()
	for _, o := range out {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("colaboracoes has no outcome for %s: %+v", id, out)
	return CollaborationOutcome{}
}

// collabDisk reads one assignment record from disk.
func collabDisk(t *testing.T, cstore collaboration.Store, id string) collaboration.Assignment {
	t.Helper()
	a, err := cstore.Read(id)
	if err != nil {
		t.Fatalf("read collaboration %s: %v", id, err)
	}
	return a
}

// armCollabForAccept brings a collaboration to awaiting-orchestrator with a
// valid revision and the ready/reviewed round events, so the store's own
// Finalize(accept) passes: the test harness plays the orchestrator's
// in-grace decision with it.
func armCollabForAccept(t *testing.T, cstore collaboration.Store, id, author, reviewer string) {
	t.Helper()
	// The report stands inside the job workspace's reports directory: the
	// location the terminal step's bounded evidence read accepts.
	reportPath, hash := writeCollabReportInWs(t, cstore.StateDir, author, "the final deliverable")
	a, err := cstore.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cstore.Update(id, a.Generation, func(a *collaboration.Assignment) error {
		a.Phase = collaboration.AwaitingOrchestrator
		a.Revision = rev.Fingerprint
		a.Events = append(a.Events,
			collaboration.Event{ID: "e-0001", Type: "ready", Actor: author, Report: reportPath, ReportHash: hash, Delivery: "submitted"},
			collaboration.Event{ID: "e-0002", Type: "reviewed", Actor: reviewer, Report: reportPath, ReportHash: hash, Delivery: "submitted"},
		)
		return nil
	}); err != nil {
		t.Fatalf("arm collaboration %s: %v", id, err)
	}
}

// requireDispatches asserts the exact recorded dispatch sequence by name
// and amend flag.
func requireDispatches(t *testing.T, team *fakeTeam, want []fakeDispatch) {
	t.Helper()
	if !reflect.DeepEqual(team.dispatches, want) {
		t.Fatalf("dispatches = %+v, want %+v", team.dispatches, want)
	}
}

// orchestratorBriefPath is the path the initial brief dispatch carries.
func orchestratorBriefPath(jobDir string) string {
	return filepath.Join(jobDir, "orchestrator-brief.md")
}

// The done path ends one unanswerable collaboration: the participants are
// asked through the stop amendment, the grace waits the full
// defaultTimeoutGrace, the outcome is persisted partial before the
// termination, the collaboration is ended reject, and the job still ends
// done with untouched totals.
func TestSuperviseCollabDoneIntroducesPartial(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if st := supState(t, store); st.Status != StatusDone || !st.WorkspaceClosed {
		t.Fatalf("status=%s closed=%v, want done and closed", st.Status, st.WorkspaceClosed)
	}
	// No wait of its own and no early exit: the grace is the single
	// defaultTimeoutGrace from the done (t=20s), so 12 poll sleeps inside
	// the wait plus the two before it.
	if clock.sleeps != 14 {
		t.Fatalf("sleeps = %d, want 14", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	raw, err := os.ReadFile(stopPath)
	if err != nil {
		t.Fatalf("stop amendment missing on the done path: %v", err)
	}
	if string(raw) != stopAmendBody {
		t.Fatalf("stop amendment body = %q, want the exact contract body", raw)
	}
	if info, err := os.Stat(stopPath); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("stop amendment mode = %v, want 0600", info.Mode().Perm())
	}
	// The brief, then the stop amendment to both participants (amendments).
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	out := findColab(t, readColabs(t, jobDir), id)
	want := CollaborationOutcome{
		ID: id, Phase: collaboration.Finished, Round: 0, Revision: "",
		LastEvent: "none", Outcome: collabOutcomePartial, Verdict: "reject",
		FinalizedBySupervisor: true,
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("outcome = %+v, want %+v", out, want)
	}
	rep, err := readReport(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != StatusDone || rep.Parciais != 0 || rep.Publico.Verdict != "" || len(rep.Publico.Blockers) != 0 {
		t.Fatalf("report totals changed by the partial collaboration: status=%s parciais=%d publico=%+v", rep.Status, rep.Parciais, rep.Publico)
	}
	if rep.Limpeza.Workspace != "closed" {
		t.Fatalf("workspace = %q, want closed", rep.Limpeza.Workspace)
	}
	// The release ran with the collaboration finished.
	if team.releaseCalls != 1 || team.releaseTeamCalls != 1 || team.gcCalls != 1 {
		t.Fatalf("release=%d team=%d gc=%d, want one of each", team.releaseCalls, team.releaseTeamCalls, team.gcCalls)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned", "worker_done", "terminal", "cleanup", "folder_retained", "workspace_closed"}) {
		t.Fatalf("event kinds = %v", got)
	}
}

// Cancel shares the stop deadline: the ask already ran in beginStop, the
// orchestrator's done ends the stop early, and a collaboration the
// orchestrator finalized inside the grace is recorded finished with its
// verdict — the wait ends as soon as every job collaboration is finished,
// not when the deadline passes.
func TestSuperviseCollabCancelEarlyFinishSharedDeadline(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	armCollabForAccept(t, cstore, id, "impl-1", "rev-1")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	// The orchestrator's own in-grace decision, at t=30 (the third sleep).
	clock.onSleep = func() {
		if clock.sleeps != 3 {
			return
		}
		if _, err := cstore.Finalize(id, "accept"); err != nil {
			t.Fatalf("harness finalize: %v", err)
		}
	}
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	// One tick before the done, then the wait that ends early at t=30 once
	// the collaboration is finished: three sleeps, not the full grace.
	if clock.sleeps != 3 {
		t.Fatalf("sleeps = %d, want 3 (the wait ends when all are finished)", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "orch-1", Path: stopPath, Amend: true},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != "finished" || out.Verdict != "accept" || out.Phase != collaboration.Finished {
		t.Fatalf("outcome = %+v, want finished/accept", out)
	}
	if out.FinalizedBySupervisor {
		t.Fatalf("the supervisor claimed a finalize it did not perform: %+v", out)
	}
	if out.LastEvent != "reviewed by rev-1, delivery submitted" || out.Report == "" || out.ReportHash == "" || out.ReportSHA256 == "" {
		t.Fatalf("evidence = %+v, want the last event's report and hashes", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "accept" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/accept", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The wait ends only when every job collaboration is finished: with two
// active, one finishing inside the grace, the wait runs to the shared stop
// deadline and the other is interrupted.
func TestSuperviseCollabWaitEndsOnlyWhenAllFinished(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	ca, idA := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	armCollabForAccept(t, ca, idA, "impl-1", "rev-1")
	cb, idB := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-2", "rev-2", "w1:p4", "w1:p5")
	team.rosterRows = collabRoster("impl-1", "rev-1", "impl-2", "rev-2")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	// Only the first collaboration is finished in the grace (t=20).
	clock.onSleep = func() {
		if clock.sleeps != 2 {
			return
		}
		if _, err := ca.Finalize(idA, "accept"); err != nil {
			t.Fatalf("harness finalize: %v", err)
		}
	}
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	// The wait ran to the shared deadline (t=60): six sleeps, and it ended
	// at the deadline, not when the first finished (t=20).
	if clock.sleeps != 6 {
		t.Fatalf("sleeps = %d, want 6 (the wait ends at the deadline while one is active)", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	// The ask dispatches follow the active list's id order (author then
	// reviewer).
	order := [][2]string{{"impl-1", "rev-1"}, {"impl-2", "rev-2"}}
	if idB < idA {
		order = [][2]string{{"impl-2", "rev-2"}, {"impl-1", "rev-1"}}
	}
	want := []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "orch-1", Path: stopPath, Amend: true},
	}
	for _, p := range order {
		want = append(want, fakeDispatch{Name: p[0], Path: stopPath, Amend: true})
		want = append(want, fakeDispatch{Name: p[1], Path: stopPath, Amend: true})
	}
	requireDispatches(t, team, want)
	outA := findColab(t, readColabs(t, jobDir), idA)
	outB := findColab(t, readColabs(t, jobDir), idB)
	if outA.Outcome != "finished" || outA.Verdict != "accept" || outA.FinalizedBySupervisor {
		t.Fatalf("first outcome = %+v, want finished/accept by the orchestrator", outA)
	}
	if outB.Outcome != collabOutcomePartial || outB.Verdict != "reject" || !outB.FinalizedBySupervisor {
		t.Fatalf("second outcome = %+v, want partial/reject by the supervisor", outB)
	}
	if a := collabDisk(t, cb, idB); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("second collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// Timeout ends the job on the budget deadline: the stop deadline is the
// collaboration deadline, the outcome is partial, and the stop's own count
// is untouched (12 polls to the deadline, no extra wait).
func TestSuperviseCollabTimeoutIntroducesPartial(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	// The job already ran 90s of a 60s budget when this process starts.
	if _, err := store.Record("job-1", func(st *State) {
		st.TimeoutMin = 1
		st.StartedAt = clock.now.Add(-90 * time.Second).UTC().Format(time.RFC3339)
	}); err != nil {
		t.Fatal(err)
	}
	working := make([]fakeStatus, 13)
	for i := range working {
		working[i] = fakeStatus{State: "working"}
	}
	team.statusScript = working
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitTimeout {
		t.Fatalf("exit = %d, want %d", exit, ExitTimeout)
	}
	if st := supState(t, store); st.Status != StatusTimeout {
		t.Fatalf("status = %s, want timeout", st.Status)
	}
	if clock.sleeps != 12 {
		t.Fatalf("sleeps = %d, want 12 (the stop deadline is the collaboration deadline)", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "orch-1", Path: stopPath, Amend: true},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || !out.FinalizedBySupervisor {
		t.Fatalf("outcome = %+v, want partial/reject by the supervisor", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned", "timeout_warning", "terminal", "cleanup", "folder_retained", "workspace_closed"}) {
		t.Fatalf("event kinds = %v", got)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The failed path (the orchestrator ended without a report) ends the
// collaborations with a partial outcome, and the job still ends failed.
func TestSuperviseCollabFailedIntroducesPartial(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	team.statusScript = []fakeStatus{{State: "gone"}, {State: "gone"}, {State: "gone"}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitFailed {
		t.Fatalf("exit = %d, want %d", exit, ExitFailed)
	}
	if st := supState(t, store); st.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", st.Status)
	}
	// Two polls to the failure, then the full grace from the failure.
	if clock.sleeps != 14 {
		t.Fatalf("sleeps = %d, want 14", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || !out.FinalizedBySupervisor {
		t.Fatalf("outcome = %+v, want partial/reject by the supervisor", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The crash story: with two active collaborations, the process crashes
// after the first is settled and before the confirmed-facts rewrite. The
// snapshot persisted before the termination carries both outcomes and their
// evidence; the restart settles the second, re-asks once in its own process,
// and the next restart converges on the same outcomes with no work.
func TestSuperviseCollabCrashSnapshotSurvives(t *testing.T) {
	var friction1 []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction1)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	ca, idA := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	cb, idB := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-2", "rev-2", "w1:p4", "w1:p5")
	// The first collaboration published a ready event (its report and the
	// recorded hash stand as the evidence); both authors wrote their stop
	// report, the participant reports of the outcome, inside the job
	// workspace's reports directory.
	reportA, hashA := writeCollabReportInWs(t, ws, "impl-1", "the ready round")
	stopA, hashStopA := writeStopReportInWs(t, ws, "impl-1", "the stop report")
	stopB, hashStopB := writeStopReportInWs(t, ws, "impl-2", "the stop report")
	a, err := ca.Read(idA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ca.Update(idA, a.Generation, func(a *collaboration.Assignment) error {
		a.Phase = collaboration.Reviewing
		a.Events = append(a.Events, collaboration.Event{ID: "e-0001", Type: "ready", Actor: "impl-1", Report: reportA, ReportHash: hashA, Delivery: "submitted"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	team.rosterRows = collabRoster("impl-1", "rev-1", "impl-2", "rev-2")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	firstID, secondID := idA, idB
	if idB < idA {
		firstID, secondID = idB, idA
	}
	// The crash: after the first settlement, before the confirmed-facts
	// rewrite.
	settleCollaborationHook = func(id string) {
		if id == firstID {
			panic(errCollabTestCrash)
		}
	}
	defer func() { settleCollaborationHook = nil }()
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	crashed := func() (got bool) {
		defer func() {
			if r := recover(); r != nil {
				if r == errCollabTestCrash {
					got = true
					return
				}
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return false
	}()
	if !crashed {
		t.Fatal("the settlement did not crash where scripted")
	}
	// The crash was scripted once: the later processes settle without it.
	settleCollaborationHook = nil

	// The snapshot persisted before the termination: both outcomes stand,
	// with the evidence of the ready event and the participant stop
	// reports, and neither is finalized by the supervisor yet.
	outA := findColab(t, readColabs(t, jobDir), idA)
	outB := findColab(t, readColabs(t, jobDir), idB)
	if outA.Outcome != collabOutcomePartial || outA.Verdict != "reject" || outA.FinalizedBySupervisor {
		t.Fatalf("persisted first outcome = %+v, want partial/reject not finalized", outA)
	}
	if outB.Outcome != collabOutcomePartial || outB.Verdict != "reject" || outB.FinalizedBySupervisor {
		t.Fatalf("persisted second outcome = %+v, want partial/reject not finalized", outB)
	}
	if outA.Phase != collaboration.Reviewing {
		t.Fatalf("persisted phase = %s, want the pre-termination phase", outA.Phase)
	}
	if outA.LastEvent != "ready by impl-1, delivery submitted" || outA.Report != reportA || outA.ReportHash != hashA || outA.ReportSHA256 != hashA {
		t.Fatalf("persisted evidence = %+v, want the last event's report, hash and fresh sha256", outA)
	}
	if len(outA.ParticipantReports) != 1 || outA.ParticipantReports[0].Name != "impl-1" || outA.ParticipantReports[0].Report != stopA || outA.ParticipantReports[0].ReportSHA256 != hashStopA {
		t.Fatalf("persisted participant reports = %+v, want the author's stop report", outA)
	}
	if len(outB.ParticipantReports) != 1 || outB.ParticipantReports[0].Name != "impl-2" || outB.ParticipantReports[0].Report != stopB || outB.ParticipantReports[0].ReportSHA256 != hashStopB {
		t.Fatalf("persisted participant reports = %+v, want the author's stop report", outB)
	}
	if outB.LastEvent != "none" || outB.Report != "" {
		t.Fatalf("second evidence = %+v, want none (no final collaboration event)", outB)
	}
	// On disk: the first was settled before the crash, the second stands
	// active, and no accept happened anywhere.
	first := collabDisk(t, ca, firstID)
	if firstID == idB {
		first = collabDisk(t, cb, firstID)
	}
	if first.Phase != collaboration.Finished || first.Verdict != "reject" {
		t.Fatalf("first collaboration on disk = %s/%s, want finished/reject", first.Phase, first.Verdict)
	}
	second := collabDisk(t, ca, secondID)
	if secondID == idB {
		second = collabDisk(t, cb, secondID)
	}
	if !second.Active() {
		t.Fatalf("second collaboration on disk = %s, want still active", second.Phase)
	}
	if st := supState(t, store); st.Status != StatusCanceled || st.WorkspaceClosed {
		t.Fatalf("state after the crash = %s/closed=%v, want canceled/open", st.Status, st.WorkspaceClosed)
	}
	if len(friction1) != 0 {
		t.Fatalf("friction = %v, want none before the crash", friction1)
	}
	// The crash came before the release and the close: the first process
	// closed nothing.
	if len(herdr.CloseIDs) != 0 {
		t.Fatalf("close calls before the crash = %v, want none", herdr.CloseIDs)
	}
	// The ask ran once in process one: the brief, the orchestrator's stop
	// amendment, and the four participants in the active list's id order.
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	order := [][2]string{{"impl-1", "rev-1"}, {"impl-2", "rev-2"}}
	if idB < idA {
		order = [][2]string{{"impl-2", "rev-2"}, {"impl-1", "rev-1"}}
	}
	want := []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "orch-1", Path: stopPath, Amend: true},
	}
	for _, p := range order {
		want = append(want, fakeDispatch{Name: p[0], Path: stopPath, Amend: true})
		want = append(want, fakeDispatch{Name: p[1], Path: stopPath, Amend: true})
	}
	requireDispatches(t, team, want)

	// Process two: the restart settles the second collaboration, re-asks
	// once (only the second's participants stand active), keeps the first
	// outcome as it stands, and the workspace close fails.
	var friction2 []string
	clock2 := &nsClock{now: clock.now}
	herdr2 := &fakeHerdr{CloseErr: errors.New("close failed")}
	team2 := &fakeTeam{spawnName: "orch-1", rosterRows: team.rosterRows}
	sup2 := &Supervisor{Store: store, ID: "job-1", Ops: team2, Clock: clock2, Poll: 10 * time.Second, Herdr: herdr2, ProjectRoot: collabJobDir(t, store)}
	sup2.Friction = func(message string) { friction2 = append(friction2, message) }
	exit, err := sup2.Run()
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("restart exit = %d, want %d", exit, ExitCanceled)
	}
	// No orchestrator re-dispatch (the job is terminal): only the second
	// collaboration's participants, and the wait ran the fresh process's
	// own defaultTimeoutGrace (the job is no longer stopping).
	p2, p3 := "impl-2", "rev-2"
	if secondID == idA {
		p2, p3 = "impl-1", "rev-1"
	}
	requireDispatches(t, team2, []fakeDispatch{
		{Name: p2, Path: stopPath, Amend: true},
		{Name: p3, Path: stopPath, Amend: true},
	})
	if clock2.sleeps != 12 {
		t.Fatalf("restart sleeps = %d, want 12 (the fresh process's grace)", clock2.sleeps)
	}
	gotA := findColab(t, readColabs(t, jobDir), idA)
	gotB := findColab(t, readColabs(t, jobDir), idB)
	// The persisted outcomes stand: neither is rewritten to finished; the
	// confirmed facts are the final phase. Only the second carries
	// finalized_by_supervisor: the crashed process's flag was never
	// persisted (the crash came before the rewrite), and the restart only
	// confirms the first one.
	if gotA.Outcome != collabOutcomePartial || gotA.Verdict != "reject" || gotA.Phase != collaboration.Finished {
		t.Fatalf("restart first outcome = %+v, want partial/reject finished", gotA)
	}
	if gotB.Outcome != collabOutcomePartial || gotB.Verdict != "reject" || gotB.Phase != collaboration.Finished {
		t.Fatalf("restart second outcome = %+v, want partial/reject finished", gotB)
	}
	if gotA.FinalizedBySupervisor != (secondID == idA) || gotB.FinalizedBySupervisor != (secondID == idB) {
		t.Fatalf("finalized flags = first:%v second:%v, want only the restart settlement", gotA.FinalizedBySupervisor, gotB.FinalizedBySupervisor)
	}
	if len(gotA.ParticipantReports) != 1 || gotA.ParticipantReports[0].Report != stopA {
		t.Fatalf("restart first participant reports = %+v, want the persisted evidence kept", gotA)
	}
	if a := collabDisk(t, ca, idA); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("first collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	if b := collabDisk(t, cb, idB); b.Phase != collaboration.Finished || b.Verdict != "reject" {
		t.Fatalf("second collaboration on disk = %s/%s, want finished/reject", b.Phase, b.Verdict)
	}
	rep, err := readReport(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != StatusCanceled || rep.Limpeza.Workspace != "open" {
		t.Fatalf("restart report = status %s / workspace %s, want canceled/open (the close failed)", rep.Status, rep.Limpeza.Workspace)
	}
	if len(friction2) != 1 || friction2[0] != "close failed" {
		t.Fatalf("restart friction = %v, want the close failure only", friction2)
	}

	// Process three: everything settled and the close still pending — the
	// restart re-finalizes, converges on the identical outcomes, closes the
	// workspace, and does no work: no re-ask, no wait.
	var friction3 []string
	clock3 := &nsClock{now: clock2.now}
	// The failed close of process two left the workspace listed.
	herdr3 := &fakeHerdr{Workspaces: []herdrWorkspace{{ID: "w1", Label: "job-job-1", PaneCount: 1}}}
	team3 := &fakeTeam{spawnName: "orch-1", rosterRows: team.rosterRows}
	sup3 := &Supervisor{Store: store, ID: "job-1", Ops: team3, Clock: clock3, Poll: 10 * time.Second, Herdr: herdr3, ProjectRoot: collabJobDir(t, store)}
	sup3.Friction = func(message string) { friction3 = append(friction3, message) }
	exit, err = sup3.Run()
	if err != nil {
		t.Fatalf("second restart Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("second restart exit = %d, want %d", exit, ExitCanceled)
	}
	if clock3.sleeps != 0 || len(team3.dispatches) != 0 {
		t.Fatalf("converged restart slept %d and dispatched %v, want no wait and no re-ask", clock3.sleeps, team3.dispatches)
	}
	if len(friction3) != 0 {
		t.Fatalf("converged restart friction = %v, want none", friction3)
	}
	finalA := findColab(t, readColabs(t, jobDir), idA)
	finalB := findColab(t, readColabs(t, jobDir), idB)
	if !reflect.DeepEqual(finalA, gotA) || !reflect.DeepEqual(finalB, gotB) {
		t.Fatalf("outcomes changed across the second restart:\n%+v\n%+v\nwant unchanged\n%+v\n%+v", finalA, finalB, gotA, gotB)
	}
	rep, err = readReport(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != StatusCanceled || rep.Limpeza.Workspace != "closed" {
		t.Fatalf("final report = status %s / workspace %s, want canceled/closed", rep.Status, rep.Limpeza.Workspace)
	}
	if st := supState(t, store); !st.WorkspaceClosed {
		t.Fatal("workspace not closed after the converging restart")
	}
	if len(herdr3.CloseIDs) != 1 || herdr3.CloseIDs[0] != "w1" {
		t.Fatalf("close calls = %v, want the job workspace once", herdr3.CloseIDs)
	}
	if team3.gcCalls != 1 {
		t.Fatalf("gc calls = %d, want 1", team3.gcCalls)
	}
	requireNoActiveCollabs(t, ws)
}

// An unreadable collaboration record the job can prove is its own (the
// job's workspace and root still standing in its raw claim) is an
// unavailable outcome with its cause; an unreadable record of unknown or
// foreign ownership is friction only — never an outcome — while the
// readable one is still ended and the release runs.
//
// Catches the mutation "an unreadable record joins the outcomes without
// ownership proof" (the decision-3 revert in endCollaborations): the
// foreign record would then stand an unavailable outcome and its friction
// line would carry the old wording.
func TestSuperviseCollabUnreadableUnavailable(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	// The foreign record: unknown ownership (a torn byte).
	foreignID := "c-00000000000000ff"
	writeCollabRecord(t, ws, foreignID, "{")
	// The owned unreadable record: the job's workspace and root stand in
	// the raw claim; one invalid field (the version) keeps it unreadable.
	ownedID := "c-00000000000001ee"
	writeCollabRecord(t, ws, ownedID, `{"version":2,"id":"`+ownedID+`","workspace":"w1","root":`+quoteJSON(collabJobDir(t, store))+`}`)
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// The wait ran the full grace for the readable collaboration (the
	// unreadable ones are not waited on: they have nothing to finish).
	if clock.sleeps != 13 {
		t.Fatalf("sleeps = %d, want 13", clock.sleeps)
	}
	outcomes := readColabs(t, jobDir)
	if len(outcomes) != 2 {
		t.Fatalf("colaboracoes = %+v, want two outcomes (the owned unreadable and the readable)", outcomes)
	}
	for _, o := range outcomes {
		if o.ID == foreignID {
			t.Fatalf("the foreign unreadable record stands an outcome: %+v", o)
		}
	}
	owned := findColab(t, outcomes, ownedID)
	if owned.Outcome != collabOutcomeUnavailable || owned.Cause == "" || owned.Verdict != "" {
		t.Fatalf("owned unreadable outcome = %+v, want unavailable with a cause and no verdict", owned)
	}
	good := findColab(t, outcomes, id)
	if good.Outcome != collabOutcomePartial || good.Verdict != "reject" || !good.FinalizedBySupervisor {
		t.Fatalf("readable outcome = %+v, want partial/reject by the supervisor", good)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	// The friction lines, exact: the foreign record is named once with
	// the provably-owned wording, and the owned record is named twice
	// (the snapshot and the settle) with the unreadable wording.
	ownedLine := "job: the collaboration " + ownedID + " is unreadable: invalid collaboration record"
	foreignLine := "job: the collaboration " + foreignID + " is unreadable and not provably the job's own: unreadable collaboration " + foreignID + ": unexpected end of JSON input"
	if len(friction) != 3 || friction[0] != foreignLine || friction[1] != ownedLine || friction[2] != ownedLine {
		t.Fatalf("friction = %v, want the three exact lines", friction)
	}
	// The release still ran (the readable one is finished).
	if team.releaseCalls != 1 || team.releaseTeamCalls != 1 || team.gcCalls != 1 {
		t.Fatalf("release=%d team=%d gc=%d, want one of each", team.releaseCalls, team.releaseTeamCalls, team.gcCalls)
	}
	requireWorkspaceClosed(t, herdr, team)
}

// A late final event inside the grace is read at the grace end: the
// outcome's evidence is the fresh record, and the supervisor still ends it
// reject after the snapshot is persisted.
func TestSuperviseCollabLateEventInGrace(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	reportPath, hash := writeCollabReportInWs(t, ws, "impl-1", "the ready round")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	// The participant publishes its ready event at t=30, mid-grace.
	clock.onSleep = func() {
		if clock.sleeps != 3 {
			return
		}
		fresh, err := cstore.Read(id)
		if err != nil {
			t.Fatalf("harness read: %v", err)
		}
		if _, err := cstore.Update(id, fresh.Generation, func(a *collaboration.Assignment) error {
			a.Phase = collaboration.Reviewing
			a.Events = append(a.Events, collaboration.Event{ID: "e-0001", Type: "ready", Actor: "impl-1", Report: reportPath, ReportHash: hash, Delivery: "submitted"})
			return nil
		}); err != nil {
			t.Fatalf("harness event: %v", err)
		}
	}
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || !out.FinalizedBySupervisor {
		t.Fatalf("outcome = %+v, want partial/reject by the supervisor", out)
	}
	if out.LastEvent != "ready by impl-1, delivery submitted" || out.Report != reportPath || out.ReportHash != hash || out.ReportSHA256 != hash {
		t.Fatalf("evidence = %+v, want the late event read at the grace end", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// A concurrent finalize inside the settlement window (the orchestrator
// accepted between the fresh read and the Stop): the Stop refuses the
// finished record, the outcome keeps the persisted partial and the
// pre-termination verdict, and no finalized_by_supervisor stands.
func TestSuperviseCollabConcurrentFinalizeInSettleWindow(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	armCollabForAccept(t, cstore, id, "impl-1", "rev-1")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	// The orchestrator's accept lands in the window the supervisor opens
	// between its fresh read and the Stop write.
	collabSettlePreWriteHook = func(id string) {
		if _, err := cstore.Finalize(id, "accept"); err != nil {
			t.Fatalf("harness finalize in the window: %v", err)
		}
	}
	defer func() { collabSettlePreWriteHook = nil }()
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	// The collaboration stayed active through the grace (the accept lands
	// only in the settlement window), so the wait ran the shared stop
	// deadline: one tick plus five polls.
	if clock.sleeps != 6 {
		t.Fatalf("sleeps = %d, want 6", clock.sleeps)
	}
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || out.Phase != collaboration.Finished {
		t.Fatalf("outcome = %+v, want the persisted partial standing", out)
	}
	if out.FinalizedBySupervisor || out.Cause != "" {
		t.Fatalf("outcome = %+v, want no supervisor claim and no cause", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "accept" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/accept (the orchestrator's)", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The last event's report file may disappear during the grace: the fresh
// hash is empty, the recorded hash stands, and the path goes on.
func TestSuperviseCollabEvidenceUnreadableReport(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	reportPath, hash := writeCollabReportInWs(t, ws, "impl-1", "the ready round")
	a, err := cstore.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cstore.Update(id, a.Generation, func(a *collaboration.Assignment) error {
		a.Events = append(a.Events, collaboration.Event{ID: "e-0001", Type: "ready", Actor: "impl-1", Report: reportPath, ReportHash: hash, Delivery: "submitted"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	orchReport := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: orchReport}}
	// The report file is removed mid-grace (t=20).
	clock.onSleep = func() {
		if clock.sleeps == 2 {
			if err := os.Remove(reportPath); err != nil {
				t.Fatalf("harness remove: %v", err)
			}
		}
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" {
		t.Fatalf("outcome = %+v, want partial/reject", out)
	}
	if out.Report != reportPath || out.ReportHash != hash || out.ReportSHA256 != "" || out.ReportCheck != "rejected" {
		t.Fatalf("evidence = %+v, want the path and the recorded hash with an empty fresh hash and the rejection (the file disappeared)", out)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// A job without collaborations ends exactly as before: no ask, no stop
// amendment file, no colaboracoes key, no extra sleep.
func TestSuperviseCollabNoneNoLatency(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if clock.sleeps != 1 {
		t.Fatalf("sleeps = %d, want 1 (no collaboration wait)", clock.sleeps)
	}
	if len(team.dispatches) != 1 || team.dispatches[0].Name != "orch-1" || team.dispatches[0].Amend {
		t.Fatalf("dispatches = %+v, want the brief only", team.dispatches)
	}
	raw, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"colaboracoes"`)) {
		t.Fatalf("report carries colaboracoes with none to record:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "stop-amend.md")); !os.IsNotExist(err) {
		t.Fatalf("stop amendment file on a job without collaborations: %v", err)
	}
	requireNoActiveCollabs(t, collabWorkspace(t, jobDir))
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// A restarted supervisor over a fully closed job re-finalizes nothing: the
// report stands byte-identical, and no dispatch or close runs.
func TestSuperviseCollabRestartClosedNoReFinalize(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	cstore, id := makeCollab(t, collabWorkspace(t, jobDir), collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	first, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The restart: the workspace is closed, so no finalize at all.
	var friction2 []string
	team2 := &fakeTeam{spawnName: "orch-1"}
	herdr2 := &fakeHerdr{}
	sup2 := &Supervisor{Store: store, ID: "job-1", Ops: team2, Clock: &nsClock{now: clock.now}, Poll: 10 * time.Second, Herdr: herdr2, ProjectRoot: collabJobDir(t, store)}
	sup2.Friction = func(message string) { friction2 = append(friction2, message) }
	exit, err = sup2.Run()
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("restart exit = %d, want 0", exit)
	}
	second, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("restart rewrote the report of a closed job:\n%s\n---\n%s", first, second)
	}
	if len(team2.dispatches) != 0 || len(herdr2.CloseIDs) != 0 {
		t.Fatalf("restart did work on a closed job: dispatches=%v closes=%v", team2.dispatches, herdr2.CloseIDs)
	}
	if len(friction2) != 0 {
		t.Fatalf("restart friction = %v, want none", friction2)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireWorkspaceClosed(t, herdr, team)
}

// The partial outcome keeps the job's totals honest: the job stays done,
// the partial count and the publico block are untouched, and the outcome
// survives the collected transition as it stands.
func TestSuperviseCollabPartialHonestTotals(t *testing.T) {
	var friction []string
	store, jobDir, _, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	rep, err := readReport(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != StatusDone || rep.Parciais != 0 || rep.Publico.Verdict != "" || len(rep.Publico.Blockers) != 0 {
		t.Fatalf("totals changed by the partial collaboration: status=%s parciais=%d publico=%+v", rep.Status, rep.Parciais, rep.Publico)
	}
	before := findColab(t, rep.Colaboracoes, id)
	if before.Outcome != collabOutcomePartial || before.Verdict != "reject" {
		t.Fatalf("outcome = %+v, want partial/reject", before)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	// The collected transition rewrites the report and keeps the outcomes
	// exactly as they stand.
	if _, err := store.Transition("job-1", StatusCollected); err != nil {
		t.Fatal(err)
	}
	if st := supState(t, store); st.Status != StatusCollected {
		t.Fatalf("state status = %s, want collected", st.Status)
	}
	// The terminal outcome stands in the report: the collected transition
	// rewrites the report without touching the status or the outcomes, and
	// the partial outcome stays partial.
	rep, err = readReport(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != StatusDone {
		t.Fatalf("report status = %s, want the terminal outcome standing", rep.Status)
	}
	after := findColab(t, rep.Colaboracoes, id)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("outcome changed across the collected transition:\n%+v\n---\n%+v", before, after)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The ask only reaches the participants that are names in the job's team:
// a collaboration whose participants are partly rostered gets the dispatch
// for the rostered ones only, and it is still ended and recorded.
func TestSuperviseCollabAskOnlyRosteredParticipants(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-9", "rev-9", "w1:p6", "w1:p7")
	// The roster carries only one of the two participants.
	team.rosterRows = collabRoster("impl-9")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if clock.sleeps != 13 {
		t.Fatalf("sleeps = %d, want 13", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "impl-9", Path: stopPath, Amend: true},
	})
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || !out.FinalizedBySupervisor {
		t.Fatalf("outcome = %+v, want partial/reject by the supervisor", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The mid-grace crash and restart: process one dies inside the grace, the
// cancel stays pending, and the restart re-asks once in its own process
// and ends the collaboration finished/reject with a single partial outcome
// and no duplicate terminal or cleanup event.
func TestSuperviseCollabRestartMidGrace(t *testing.T) {
	var friction1 []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction1)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}}
	// The crash inside the grace (the second sleep, t=20): the job is
	// still running and the cancel still pending.
	clock.stopAfter = 2
	clock.stop = errCollabTestCrash
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	crashed := func() (got bool) {
		defer func() {
			if r := recover(); r != nil {
				if r == errCollabTestCrash {
					got = true
					return
				}
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return false
	}()
	if !crashed {
		t.Fatal("the grace did not crash where scripted")
	}
	// The stop amendment and the participant amendments each ran once in
	// process one; nothing else.
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "orch-1", Path: stopPath, Amend: true},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	if st := supState(t, store); st.Status != StatusRunning || st.WorkspaceClosed {
		t.Fatalf("state after the mid-grace crash = %s/closed=%v, want running/open", st.Status, st.WorkspaceClosed)
	}
	if a := collabDisk(t, cstore, id); !a.Active() {
		t.Fatalf("collaboration after the mid-grace crash = %s, want still active", a.Phase)
	}
	if len(herdr.CloseIDs) != 0 {
		t.Fatalf("close calls before the terminal = %v, want none", herdr.CloseIDs)
	}
	if len(friction1) != 0 {
		t.Fatalf("friction = %v, want none before the crash", friction1)
	}

	// Process two: the restart delivers the pending cancel, re-asks once
	// in its own process (no brief, one stop amendment), the orchestrator
	// reports done, and the job ends canceled with the collaboration
	// settled.
	var friction2 []string
	clock2 := &nsClock{now: clock.now}
	herdr2 := &fakeHerdr{}
	team2 := &fakeTeam{spawnName: "orch-1", rosterRows: team.rosterRows}
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team2.statusScript = []fakeStatus{{State: "done", Report: report}}
	sup2 := &Supervisor{Store: store, ID: "job-1", Ops: team2, Clock: clock2, Poll: 10 * time.Second, Herdr: herdr2, ProjectRoot: collabJobDir(t, store)}
	sup2.Friction = func(message string) { friction2 = append(friction2, message) }
	exit, err := sup2.Run()
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("restart exit = %d, want %d", exit, ExitCanceled)
	}
	// The restart re-asked once: the orchestrator's stop amendment and the
	// two participants — and nothing else (the brief is not re-sent).
	requireDispatches(t, team2, []fakeDispatch{
		{Name: "orch-1", Path: stopPath, Amend: true},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr2, team2)
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || !out.FinalizedBySupervisor {
		t.Fatalf("outcome = %+v, want the single partial/reject by the supervisor", out)
	}
	// No duplicate terminal or cleanup event across the crash.
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	terminal, cleanup := 0, 0
	for _, e := range events {
		if e.Tipo == "terminal" {
			terminal++
		}
		if e.Tipo == "cleanup" {
			cleanup++
		}
	}
	if terminal != 1 || cleanup != 1 {
		t.Fatalf("terminal=%d cleanup=%d, want exactly one of each across the crash", terminal, cleanup)
	}
	if len(friction2) != 0 {
		t.Fatalf("restart friction = %v, want none", friction2)
	}
}

// A readable record in the job's own workspace store whose root is not the
// job's trusted root (the recorded worktree resolved with the CLI's root
// contract, never the record's own claim) is not the job's: no dispatch to
// its participants, no Stop/Finalize, no outcome, and the record stands
// untouched — while the owned control in the same run (the job's own
// workspace and root) is asked, recorded and finalized reject. Catches the
// mutation "drop a.Root == s.ProjectRoot from ownsCollaboration": the
// foreign record would be asked, recorded and touched.
func TestSuperviseCollabForeignRootIgnored(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	// The store directory is the job workspace's, but the record's root is
	// another project's directory.
	cf, idF := makeCollab(t, ws, t.TempDir(), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	// The owned control in the same run: the job's own workspace and root,
	// rostered like the foreign record's participants, so a wrong
	// ownership would show in the dispatches as well.
	cc, idC := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-2", "rev-2", "w1:p4", "w1:p5")
	team.rosterRows = collabRoster("impl-1", "rev-1", "impl-2", "rev-2")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// The wait ran the full grace for the owned control only (the foreign
	// record is not waited on: it is not the job's).
	if clock.sleeps != 14 {
		t.Fatalf("sleeps = %d, want 14 (the owned control waits its full grace)", clock.sleeps)
	}
	// No dispatch to the foreign record's participants: the brief and the
	// stop amendment to the owned control only.
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "impl-2", Path: stopPath, Amend: true},
		{Name: "rev-2", Path: stopPath, Amend: true},
	})
	// The report records the owned control only: the foreign record is not
	// in colaboracoes and not named in the raw report.
	raw, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(idF)) {
		t.Fatalf("report names the foreign-root record:\n%s", raw)
	}
	outcomes := readColabs(t, jobDir)
	if len(outcomes) != 1 || outcomes[0].ID != idC {
		t.Fatalf("colaboracoes = %+v, want the single owned control outcome", outcomes)
	}
	if out := outcomes[0]; out.Outcome != collabOutcomePartial || out.Verdict != collabVerdictReject || out.Phase != collaboration.Finished || !out.FinalizedBySupervisor {
		t.Fatalf("control outcome = %+v, want partial/reject finished by the supervisor", out)
	}
	// The asked file (when the ask persists one) names the owned control
	// only, never the foreign record.
	if ids := readAskedFile(t, jobDir); ids != nil {
		for _, got := range ids {
			if got != idC {
				t.Fatalf("asked file names %s, want only the owned control %s", got, idC)
			}
		}
	}
	// The foreign record stands untouched: no Stop, no Finalize; the owned
	// control is ended reject.
	if a := collabDisk(t, cf, idF); a.Phase != collaboration.Preparing || a.Generation != 1 {
		t.Fatalf("foreign record was touched: phase=%s gen=%d, want preparing/gen 1", a.Phase, a.Generation)
	}
	if a := collabDisk(t, cc, idC); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("control on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	// The foreign record stands active on purpose: it is not the job's, so
	// the job's no-orphan bound does not reach it; the untouched check is
	// the bound that applies.
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// A readable record in the job's own workspace store that claims another
// workspace is not the job's, even when its root is the job's: no
// dispatch, no Stop/Finalize, no outcome — while the owned control in the
// same run (the job's own workspace and root) is asked, recorded and
// finalized reject. Catches the mutation "drop a.Workspace ==
// st.WorkspaceID from ownsCollaboration": the foreign record would be
// asked, recorded and touched.
func TestSuperviseCollabForeignWorkspaceIgnored(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	// The store directory is the job workspace's and the root is the
	// job's, but the record claims another workspace.
	cf, idF := makeCollab(t, ws, collabJobDir(t, store), "w2", "impl-1", "rev-1", "w1:p2", "w1:p3")
	// The owned control in the same run: the job's own workspace and root,
	// rostered like the foreign record's participants, so a wrong
	// ownership would show in the dispatches as well.
	cc, idC := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-2", "rev-2", "w1:p4", "w1:p5")
	team.rosterRows = collabRoster("impl-1", "rev-1", "impl-2", "rev-2")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if clock.sleeps != 14 {
		t.Fatalf("sleeps = %d, want 14 (the owned control waits its full grace)", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "impl-2", Path: stopPath, Amend: true},
		{Name: "rev-2", Path: stopPath, Amend: true},
	})
	raw, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(idF)) {
		t.Fatalf("report names the foreign-workspace record:\n%s", raw)
	}
	outcomes := readColabs(t, jobDir)
	if len(outcomes) != 1 || outcomes[0].ID != idC {
		t.Fatalf("colaboracoes = %+v, want the single owned control outcome", outcomes)
	}
	if out := outcomes[0]; out.Outcome != collabOutcomePartial || out.Verdict != collabVerdictReject || out.Phase != collaboration.Finished || !out.FinalizedBySupervisor {
		t.Fatalf("control outcome = %+v, want partial/reject finished by the supervisor", out)
	}
	if ids := readAskedFile(t, jobDir); ids != nil {
		for _, got := range ids {
			if got != idC {
				t.Fatalf("asked file names %s, want only the owned control %s", got, idC)
			}
		}
	}
	if a := collabDisk(t, cf, idF); a.Phase != collaboration.Preparing || a.Generation != 1 {
		t.Fatalf("foreign record was touched: phase=%s gen=%d, want preparing/gen 1", a.Phase, a.Generation)
	}
	if a := collabDisk(t, cc, idC); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("control on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	// The foreign record stands active on purpose: it is not the job's, so
	// the job's no-orphan bound does not reach it; the untouched check is
	// the bound that applies.
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// A collaboration that finishes normally inside the stop grace, before
// finalize enumerates, is carried by the ids asked to finish (recorded at
// the beginStop ask): it stands in report.json as finished with its actual
// verdict and evidence, though it is no longer active at the enumeration.
func TestSuperviseCollabFinishBeforeFinalizeEnumerates(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	armCollabForAccept(t, cstore, id, "impl-1", "rev-1")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "done", Report: report}}
	// The orchestrator's accept lands inside the stop grace (t=10),
	// before the terminal enumeration.
	clock.onSleep = func() {
		if clock.sleeps != 1 {
			return
		}
		if _, err := cstore.Finalize(id, "accept"); err != nil {
			t.Fatalf("harness finalize: %v", err)
		}
	}
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	// The ask, the done, then the enumeration: no wait (nothing stands
	// active).
	if clock.sleeps != 2 {
		t.Fatalf("sleeps = %d, want 2", clock.sleeps)
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team, []fakeDispatch{
		{Name: "orch-1", Path: orchestratorBriefPath(jobDir)},
		{Name: "orch-1", Path: stopPath, Amend: true},
		{Name: "impl-1", Path: stopPath, Amend: true},
		{Name: "rev-1", Path: stopPath, Amend: true},
	})
	// The asked ids stand persisted in the state (the single source of
	// truth) before the enumeration.
	if st := supState(t, store); !reflect.DeepEqual(st.CollabIDs, []string{id}) {
		t.Fatalf("persisted collab_ids = %v, want %v", st.CollabIDs, []string{id})
	}
	// The finished id is read from the persisted ids and stands finished
	// with its actual verdict and the last event's evidence.
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != "finished" || out.Verdict != "accept" || out.Phase != collaboration.Finished {
		t.Fatalf("outcome = %+v, want finished/accept", out)
	}
	if out.FinalizedBySupervisor {
		t.Fatalf("the supervisor claimed a finalize it did not perform: %+v", out)
	}
	if out.LastEvent != "reviewed by rev-1, delivery submitted" || out.Report == "" || out.ReportHash == "" || out.ReportSHA256 == "" {
		t.Fatalf("evidence = %+v, want the last event's report and hashes", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "accept" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/accept", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The same finish-before-enumeration across a mid-grace restart: process
// one asks (the ids are persisted in the state) and dies inside the grace
// after the collaboration finished; the restart re-asks (nothing stands
// active: the file stands, no participant amendment) and the finished id
// is read from the persisted state ids.
func TestSuperviseCollabRestartMidGraceFinishBeforeEnumerate(t *testing.T) {
	var friction1 []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction1)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	armCollabForAccept(t, cstore, id, "impl-1", "rev-1")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}}
	// The accept lands inside the stop grace (t=10); the process crashes
	// inside the grace (t=20), before any enumeration.
	clock.onSleep = func() {
		if clock.sleeps != 1 {
			return
		}
		if _, err := cstore.Finalize(id, "accept"); err != nil {
			t.Fatalf("harness finalize: %v", err)
		}
	}
	clock.stopAfter = 2
	clock.stop = errCollabTestCrash
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	crashed := func() (got bool) {
		defer func() {
			if r := recover(); r != nil {
				if r == errCollabTestCrash {
					got = true
					return
				}
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return false
	}()
	if !crashed {
		t.Fatal("the grace did not crash where scripted")
	}
	// After the crash: the job is still running, the collaboration is
	// finished/accept, and the persisted state ids carry the id.
	if st := supState(t, store); st.Status != StatusRunning || st.WorkspaceClosed {
		t.Fatalf("state after the crash = %s/closed=%v, want running/open", st.Status, st.WorkspaceClosed)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "accept" {
		t.Fatalf("collaboration after the crash = %s/%s, want finished/accept", a.Phase, a.Verdict)
	}
	if st := supState(t, store); !reflect.DeepEqual(st.CollabIDs, []string{id}) {
		t.Fatalf("persisted collab_ids after the crash = %v, want %v", st.CollabIDs, []string{id})
	}
	if len(herdr.CloseIDs) != 0 {
		t.Fatalf("close calls before the terminal = %v, want none", herdr.CloseIDs)
	}
	if len(friction1) != 0 {
		t.Fatalf("friction = %v, want none before the crash", friction1)
	}

	// Process two: the restart asks (nothing stands active anymore: no
	// participant amendment, the file stands), the orchestrator reports
	// done, and the finished id is read from the persisted state ids.
	var friction2 []string
	clock2 := &nsClock{now: clock.now}
	herdr2 := &fakeHerdr{}
	team2 := &fakeTeam{spawnName: "orch-1", rosterRows: team.rosterRows}
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team2.statusScript = []fakeStatus{{State: "done", Report: report}}
	sup2 := &Supervisor{Store: store, ID: "job-1", Ops: team2, Clock: clock2, Poll: 10 * time.Second, Herdr: herdr2, ProjectRoot: collabJobDir(t, store)}
	sup2.Friction = func(message string) { friction2 = append(friction2, message) }
	exit, err := sup2.Run()
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("restart exit = %d, want %d", exit, ExitCanceled)
	}
	// The restart's ask ran once: the orchestrator's stop amendment only —
	// nothing stands active, so no participant amendment.
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team2, []fakeDispatch{
		{Name: "orch-1", Path: stopPath, Amend: true},
	})
	if clock2.sleeps != 0 {
		t.Fatalf("restart sleeps = %d, want 0 (nothing stands active)", clock2.sleeps)
	}
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != "finished" || out.Verdict != "accept" || out.Phase != collaboration.Finished {
		t.Fatalf("outcome = %+v, want finished/accept", out)
	}
	if out.FinalizedBySupervisor {
		t.Fatalf("the supervisor claimed a finalize it did not perform: %+v", out)
	}
	if out.LastEvent != "reviewed by rev-1, delivery submitted" || out.Report == "" || out.ReportSHA256 == "" {
		t.Fatalf("evidence = %+v, want the last event's report and hashes", out)
	}
	if a := collabDisk(t, cstore, id); a.Phase != collaboration.Finished || a.Verdict != "accept" {
		t.Fatalf("collaboration on disk = %s/%s, want finished/accept", a.Phase, a.Verdict)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr2, team2)
	if len(friction2) != 0 {
		t.Fatalf("restart friction = %v, want none", friction2)
	}
}

// A directory-level enumeration failure after a crashed snapshot is an
// error, not "no collaborations": the persisted snapshot stands (neither
// id disappears from report.json) and no release, cleanup, or close runs in
// that pass; a later pass with the listing restored settles the remaining
// id and closes.
func TestSuperviseCollabEnumerationFailureKeepsSnapshot(t *testing.T) {
	var friction1 []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction1)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	root := collabJobDir(t, store)
	ca, idA := makeCollab(t, ws, root, "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	cb, idB := makeCollab(t, ws, root, "w1", "impl-2", "rev-2", "w1:p4", "w1:p5")
	team.rosterRows = collabRoster("impl-1", "rev-1", "impl-2", "rev-2")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "done", Report: report}}
	firstID, secondID := idA, idB
	if idB < idA {
		firstID, secondID = idB, idA
	}
	// The crash: after the first settlement, before the confirmed-facts
	// rewrite.
	settleCollaborationHook = func(id string) {
		if id == firstID {
			panic(errCollabTestCrash)
		}
	}
	defer func() { settleCollaborationHook = nil }()
	crashed := func() (got bool) {
		defer func() {
			if r := recover(); r != nil {
				if r == errCollabTestCrash {
					got = true
					return
				}
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return false
	}()
	if !crashed {
		t.Fatal("the settlement did not crash where scripted")
	}
	// The crash was scripted once for process one: the later processes
	// settle without it (the first outcome's settle already happened on
	// disk before the crash, and process three confirms it without a
	// supervisor claim).
	settleCollaborationHook = nil
	// The snapshot persisted before the termination carries both ids
	// (partial, not finalized); on disk the first is settled, the second
	// stands active.
	if out := findColab(t, readColabs(t, jobDir), firstID); out.Outcome != collabOutcomePartial || out.Verdict != "reject" || out.FinalizedBySupervisor {
		t.Fatalf("persisted first outcome = %+v, want partial/reject not finalized", out)
	}
	if out := findColab(t, readColabs(t, jobDir), secondID); out.Outcome != collabOutcomePartial || out.Verdict != "reject" || out.FinalizedBySupervisor {
		t.Fatalf("persisted second outcome = %+v, want partial/reject not finalized", out)
	}
	first := collabDisk(t, ca, firstID)
	if firstID == idB {
		first = collabDisk(t, cb, firstID)
	}
	if first.Phase != collaboration.Finished || first.Verdict != "reject" {
		t.Fatalf("first collaboration on disk = %s/%s, want finished/reject", first.Phase, first.Verdict)
	}
	second := collabDisk(t, ca, secondID)
	if secondID == idB {
		second = collabDisk(t, cb, secondID)
	}
	if !second.Active() {
		t.Fatalf("second collaboration on disk = %s, want still active", second.Phase)
	}
	if st := supState(t, store); st.Status != StatusDone || st.WorkspaceClosed {
		t.Fatalf("state after the crash = %s/closed=%v, want done/open", st.Status, st.WorkspaceClosed)
	}
	if team.releaseCalls != 0 || len(herdr.CloseIDs) != 0 {
		t.Fatalf("release/close before the crash = %d/%v, want none", team.releaseCalls, herdr.CloseIDs)
	}
	if len(friction1) != 0 {
		t.Fatalf("friction = %v, want none before the crash", friction1)
	}

	// Process two: the directory-level listing fails (the collaboration
	// directory stands as a regular file). The persisted snapshot and the
	// asked ids stand; no report rewrite, no release, no cleanup, no close.
	collabDir := filepath.Join(ws, "collaboration")
	if err := os.Rename(collabDir, filepath.Join(ws, "collaboration.hidden")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collabDir, []byte("blocked"), 0o666); err != nil {
		t.Fatal(err)
	}
	var friction2 []string
	clock2 := &nsClock{now: clock.now}
	herdr2 := &fakeHerdr{}
	team2 := &fakeTeam{spawnName: "orch-1", rosterRows: team.rosterRows}
	sup2 := &Supervisor{Store: store, ID: "job-1", Ops: team2, Clock: clock2, Poll: 10 * time.Second, Herdr: herdr2, ProjectRoot: collabJobDir(t, store)}
	sup2.Friction = func(message string) { friction2 = append(friction2, message) }
	exit, err := sup2.Run()
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("restart exit = %d, want 0", exit)
	}
	// Neither id disappeared from report.json.
	if out := findColab(t, readColabs(t, jobDir), firstID); out.Outcome != collabOutcomePartial || out.Verdict != "reject" || out.FinalizedBySupervisor {
		t.Fatalf("enumeration-failure pass dropped the first id: %+v", out)
	}
	if out := findColab(t, readColabs(t, jobDir), secondID); out.Outcome != collabOutcomePartial || out.Verdict != "reject" || out.FinalizedBySupervisor {
		t.Fatalf("enumeration-failure pass dropped the second id: %+v", out)
	}
	// Nothing destructive ran in the failed pass.
	if team2.releaseCalls != 0 || team2.releaseTeamCalls != 0 || team2.gcCalls != 0 {
		t.Fatalf("release/team/gc in the failed pass = %d/%d/%d, want none", team2.releaseCalls, team2.releaseTeamCalls, team2.gcCalls)
	}
	if len(herdr2.CloseIDs) != 0 {
		t.Fatalf("close calls in the failed pass = %v, want none", herdr2.CloseIDs)
	}
	if st := supState(t, store); st.WorkspaceClosed {
		t.Fatal("the workspace closed in the failed pass")
	}
	if events, err := readEvents(jobDir); err != nil {
		t.Fatal(err)
	} else if countTipe(events, "cleanup") != 0 {
		t.Fatal("the cleanup event landed in the failed pass")
	}
	if len(friction2) != 1 {
		t.Fatalf("failed-pass friction = %v, want the single enumeration line", friction2)
	}

	// Process three: the listing is restored; the remaining id is settled
	// and the workspace closes.
	if err := os.Rename(collabDir, filepath.Join(ws, "collaboration.blocked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(ws, "collaboration.hidden"), collabDir); err != nil {
		t.Fatal(err)
	}
	var friction3 []string
	clock3 := &nsClock{now: clock2.now}
	herdr3 := &fakeHerdr{}
	team3 := &fakeTeam{spawnName: "orch-1", rosterRows: team.rosterRows}
	sup3 := &Supervisor{Store: store, ID: "job-1", Ops: team3, Clock: clock3, Poll: 10 * time.Second, Herdr: herdr3, ProjectRoot: collabJobDir(t, store)}
	sup3.Friction = func(message string) { friction3 = append(friction3, message) }
	exit, err = sup3.Run()
	if err != nil {
		t.Fatalf("second restart Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("second restart exit = %d, want 0", exit)
	}
	// The restored pass waited its own fresh grace for the remaining id.
	if clock3.sleeps != 12 {
		t.Fatalf("restored-pass sleeps = %d, want 12 (the fresh process's grace)", clock3.sleeps)
	}
	// The ask re-ran for the remaining collaboration's participants only.
	p2, p3 := "impl-2", "rev-2"
	if secondID == idA {
		p2, p3 = "impl-1", "rev-1"
	}
	stopPath := filepath.Join(jobDir, "stop-amend.md")
	requireDispatches(t, team3, []fakeDispatch{
		{Name: p2, Path: stopPath, Amend: true},
		{Name: p3, Path: stopPath, Amend: true},
	})
	// Both ids stand: the persisted first outcome confirmed to the final
	// phase (no supervisor claim: its settle crashed before the rewrite),
	// the second settled by this pass.
	gotA := findColab(t, readColabs(t, jobDir), idA)
	gotB := findColab(t, readColabs(t, jobDir), idB)
	gotFirst, gotSecond := gotA, gotB
	if secondID == idA {
		gotFirst, gotSecond = gotB, gotA
	}
	if gotFirst.Outcome != collabOutcomePartial || gotFirst.Verdict != "reject" || gotFirst.Phase != collaboration.Finished || gotFirst.FinalizedBySupervisor {
		t.Fatalf("restored first outcome = %+v, want partial/reject finished without a supervisor claim", gotFirst)
	}
	if gotSecond.Outcome != collabOutcomePartial || gotSecond.Verdict != "reject" || gotSecond.Phase != collaboration.Finished || !gotSecond.FinalizedBySupervisor {
		t.Fatalf("restored second outcome = %+v, want partial/reject finished by the supervisor", gotSecond)
	}
	if a := collabDisk(t, ca, idA); a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("first collaboration on disk = %s/%s, want finished/reject", a.Phase, a.Verdict)
	}
	if b := collabDisk(t, cb, idB); b.Phase != collaboration.Finished || b.Verdict != "reject" {
		t.Fatalf("second collaboration on disk = %s/%s, want finished/reject", b.Phase, b.Verdict)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if countTipe(events, "cleanup") != 1 {
		t.Fatalf("cleanup events = %d, want exactly one across the passes", countTipe(events, "cleanup"))
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr3, team3)
	if len(friction3) != 0 {
		t.Fatalf("restored-pass friction = %v, want none", friction3)
	}
}

// The retry of a conflicted supervisor finalize is the supervisor's own
// finalize: when the first call fails with a generation conflict and the
// retry succeeds, the outcome stays partial (the class recorded before
// the termination) with phase finished, finalized_by_supervisor and no
// cause. The first attempt injects a genuine Store.Update generation conflict:
// an external actor advances the record after a stale read. The retry uses
// Store.Finalize without a hook. The test catches the mutation "remove
// finalized = true from the retry branch": without it the successful
// retry would not claim the finalize.
func TestSuperviseCollabFinalizeConflictRetryIsSupervisorFinalized(t *testing.T) {
	var friction []string
	store, jobDir, _, team, sup := nsSup(t, 10*time.Second, &friction)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	cstore, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	// Pre-escalate the collaboration so the settlement skips its own Stop
	// and runs straight to the supervisor finalize window.
	if _, err := cstore.Stop(id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	gen := collabDisk(t, cstore, id).Generation
	team.rosterRows = collabRoster("impl-1", "rev-1")
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	// Do not hold state.lock while waiting for the supervisor: a descheduled
	// actor could outlive both lock-acquisition budgets. Instead interleave a
	// real external update inside the stale-read/write window synchronously.
	attempts := 0
	t.Cleanup(func() { collabFinalizeFirstAttemptHook = nil })
	collabFinalizeFirstAttemptHook = func(gotID string) error {
		attempts++
		if gotID != id {
			t.Fatalf("finalize id = %q, want %q", gotID, id)
		}
		stale := collabDisk(t, cstore, id)
		if _, err := cstore.Stop(id); err != nil {
			t.Fatalf("external update: %v", err)
		}
		// The external generation has advanced; the stale write must fail
		// before applying its change, just as Store.Finalize's Update does.
		_, err := cstore.Update(id, stale.Generation, func(*collaboration.Assignment) error {
			t.Fatal("stale finalize change ran despite the generation conflict")
			return nil
		})
		if err == nil || err.Error() != "collaboration generation changed; inspect status before retrying" {
			t.Fatalf("first finalize conflict = %v", err)
		}
		return err
	}
	if queued, err := store.RequestCancel("job-1", 60); !queued || err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitCanceled {
		t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
	}
	if attempts != 1 {
		t.Fatalf("injected first attempts = %d, want 1", attempts)
	}
	// The external bump and the retried supervisor finalize each advanced
	// the generation once: the record stands two generations past the
	// pre-escalated one.
	if a := collabDisk(t, cstore, id); a.Generation != gen+2 || a.Phase != collaboration.Finished || a.Verdict != "reject" {
		t.Fatalf("collaboration on disk = %s/%s gen %d, want finished/reject gen %d", a.Phase, a.Verdict, a.Generation, gen+2)
	}
	out := findColab(t, readColabs(t, jobDir), id)
	if out.Outcome != collabOutcomePartial || out.Verdict != "reject" || out.Phase != collaboration.Finished {
		t.Fatalf("outcome = %+v, want the persisted partial with phase finished and verdict reject", out)
	}
	if !out.FinalizedBySupervisor {
		t.Fatalf("the successful retry finalize is the supervisor's own: %+v", out)
	}
	if out.Cause != "" {
		t.Fatalf("the stale conflict cause stands after the successful retry: %+v", out)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// A participant's last-report marker is participant-controlled: a target
// outside the job workspace's reports directory is recorded with a cause
// and its path is never published (no path, no hash, not even in the raw
// report); a legitimate target inside the directory stands with its fresh
// hash.
func TestSuperviseCollabHostileParticipantReportRejected(t *testing.T) {
	// The hostile target: the marker (job state) points at a report
	// outside the job workspace's reports directory.
	var friction1 []string
	store, jobDir, _, team, sup := nsSup(t, 10*time.Second, &friction1)
	herdr := collabSup(t, store, sup)
	ws := collabWorkspace(t, jobDir)
	_, id := makeCollab(t, ws, collabJobDir(t, store), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team.rosterRows = collabRoster("impl-1", "rev-1")
	hostile, _ := writeCollabReport(t, "impl-1", "hostile")
	writeLastReport(t, ws, "impl-1", hostile)
	report := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	out := findColab(t, readColabs(t, jobDir), id)
	if len(out.ParticipantReports) != 1 {
		t.Fatalf("participant reports = %+v, want the single rejected impl entry", out.ParticipantReports)
	}
	impl := out.ParticipantReports[0]
	if impl.Name != "impl-1" || impl.Report != "" || impl.ReportSHA256 != "" || impl.ReportCheck != "rejected" {
		t.Fatalf("hostile participant report = %+v, want the rejection without the path or hash", impl)
	}
	raw, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("hostile")) {
		t.Fatalf("the rejected path stands in report.json:\n%s", raw)
	}
	if len(friction1) != 0 {
		t.Fatalf("friction = %v, want none (the rejection is recorded, not friction)", friction1)
	}
	requireNoActiveCollabs(t, ws)
	requireWorkspaceClosed(t, herdr, team)

	// The legitimate target inside the job workspace's reports directory:
	// it stands with its fresh hash.
	var friction2 []string
	store2, jobDir2, _, team2, sup2 := nsSup(t, 10*time.Second, &friction2)
	herdr2 := collabSup(t, store2, sup2)
	ws2 := collabWorkspace(t, jobDir2)
	_, id2 := makeCollab(t, ws2, collabJobDir(t, store2), "w1", "impl-1", "rev-1", "w1:p2", "w1:p3")
	team2.rosterRows = collabRoster("impl-1", "rev-1")
	legit, _ := writeStopReportInWs(t, ws2, "impl-1", "legit")
	report2 := writeOrchestratorReport(t, "# report\n\n- [done] item\n")
	team2.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report2}}
	exit, err = sup2.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	out2 := findColab(t, readColabs(t, jobDir2), id2)
	if len(out2.ParticipantReports) != 1 || out2.ParticipantReports[0].Name != "impl-1" || out2.ParticipantReports[0].Report != legit || out2.ParticipantReports[0].ReportSHA256 == "" || out2.ParticipantReports[0].ReportCheck != "" {
		t.Fatalf("legit participant report = %+v, want the path with the fresh hash and no rejection", out2.ParticipantReports)
	}
	requireNoActiveCollabs(t, ws2)
	requireWorkspaceClosed(t, herdr2, team2)
	if len(friction2) != 0 {
		t.Fatalf("friction = %v, want none", friction2)
	}
}

// quoteJSON is s as a JSON string literal: a hand-written record must
// escape a path (a Windows path holds backslashes).
func quoteJSON(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// TestSuperviseCollabCollectsReportFromTempSohoRoutingDir: the supervisor's
// evidence collection accepts a report recorded at the routing reports
// directory of the job workspace, os.TempDir()/soho/<workspace>/reports
// (the location the workers outside the job worktree publish their reports
// to), with the fresh sha256 of the file; the retired
// os.TempDir()/herdr-soho/<workspace>/reports is refused with the same
// outside-roots rejection.
func TestSuperviseCollabCollectsReportFromTempSohoRoutingDir(t *testing.T) {
	// A workspace id unique per process and time, so the real temp routing
	// dir never aliases another test's.
	ws := fmt.Sprintf("w-c2a-%d-%d", os.Getpid(), time.Now().UnixNano())
	// A unique state dir so the store root and the routing dir never alias
	// the same path.
	stateDir := t.TempDir()
	body := "# report\n\n- [done] item\n"
	routing := writeEvidenceTarget(t,
		filepath.Join(os.TempDir(), "soho", ws, "reports", "report.md"), body)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), "soho", ws)) })
	stale := writeEvidenceTarget(t,
		filepath.Join(os.TempDir(), "herdr-soho", ws, "reports", "report.md"), "stale\n")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), "herdr-soho", ws)) })
	sup := &Supervisor{}
	st := State{WorkspaceID: ws, Dir: stateDir}
	a := collaboration.Assignment{
		ID: "c1",
		Events: []collaboration.Event{{
			Type: "final", Actor: "impl-1", Report: routing, ReportHash: "x", Delivery: "d1",
		}},
	}
	out := sup.collaborationEvidence(a, st, stateDir, collabOutcomeFinished, "accept")
	if out.ReportCheck != "" || out.ReportSHA256 == "" {
		t.Fatalf("routing report = %+v, want it accepted with a fresh hash", out)
	}
	want := sha256.Sum256([]byte(body))
	if out.ReportSHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("sha256 = %s, want %s", out.ReportSHA256, hex.EncodeToString(want[:]))
	}
	// The retired herdr-soho routing dir is refused.
	aStale := collaboration.Assignment{
		ID: "c2",
		Events: []collaboration.Event{{
			Type: "final", Actor: "impl-1", Report: stale, ReportHash: "x", Delivery: "d1",
		}},
	}
	outStale := sup.collaborationEvidence(aStale, st, stateDir, collabOutcomeFinished, "accept")
	if outStale.ReportCheck != "rejected" || outStale.ReportSHA256 != "" {
		t.Fatalf("retired routing report = %+v, want the rejection and no fresh hash", outStale)
	}
}
