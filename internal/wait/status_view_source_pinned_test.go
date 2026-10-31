package wait

// F2 pinned-root consumer tests: the session dir is renamed and replaced
// by a link to an owned decoy AFTER the source acquisition; the
// inspection's compact marker, collaboration store, protected mirror and
// sidecar/history reads must follow the retained session root, never the
// raw (now link-resolved) state path. The decoys carry different
// metadata that a raw path reader would read; the views must stay at the
// control's. The roster snapshot remains the authority after the
// on-disk roster changes, and the collaboration reads are bounded (a
// capped or exhausted store is collaboration-unavailable, never an
// empty success).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// swapSessionDir renames the fixture's session dir and replaces the old
// path with a link to an owned decoy dir, returning the decoy's path.
// The source acquired before the swap keeps its retained root.
func swapSessionDir(t *testing.T, f *statusViewFixture) (decoy string) {
	t.Helper()
	renamed := f.sd + "-retained"
	if err := os.Rename(f.sd, renamed); err != nil {
		t.Fatalf("cannot rename the session dir: %v", err)
	}
	decoy = filepath.Join(f.state, "decoy")
	if err := os.MkdirAll(filepath.Join(decoy, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, f.sd); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	return decoy
}

// TestStatusViewSourcePinnedRootCompact: a valid compact marker stands
// under the session dir before the swap; after the dir is renamed and
// replaced by a link to a decoy with a corrupted marker, the inspection
// must keep rendering the marker of the retained root (compacting),
// never the decoy's corrupted one (compact-unknown) and never the
// absence it would read as (no-report-yet).
func TestStatusViewSourcePinnedRootCompact(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	}
	f := newSourceViewFixture(t, roster, probe)
	pid, started, ok := core.CompactPendingSelf(f.env)
	if !ok {
		t.Skipf("the process's own identity is not readable: no owner class")
	}
	marker := fmt.Sprintf(`{"version":1,"token":"t1","pane":"p1","brief":"/tmp/brief.md","pid":%d,"started":%q,"created_at":"%s"}`, pid, started, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(core.CompactPendingPath(f.sd, "worker"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	control := f.inspectWith(t, "worker")
	if control.State != "compacting" || control.TerminalState != "" {
		t.Fatalf("control view=%+v, want compacting with no probe", control)
	}
	swapSessionDir(t, &f.statusViewFixture)
	if err := os.WriteFile(core.CompactPendingPath(f.sd, "worker"), []byte("corrupted foreign marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	view := f.inspectWith(t, "worker")
	if view.State != "compacting" || view.TerminalState != "" {
		t.Fatalf("pinned-root view=%+v, want the retained root's compacting (the decoy's corrupted marker must not be read)", view)
	}
}

// TestStatusViewSourcePinnedRootCollaboration: an active collaboration
// stands under the session dir before the swap; after the dir is renamed
// and replaced by a link to a decoy with no collaboration dir, the
// inspection must keep rendering collaborating from the retained root's
// store (the participants verify against the snapshot roster rows), and
// the raw-path store reader sees the decoy's absence — the two readers
// must disagree, proving the inspection follows the retained root.
func TestStatusViewSourcePinnedRootCollaboration(t *testing.T) {
	collabRoster := func(cwd string) string {
		return "author\tp-author\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tclaude-sonnet-4-5\t\t\t\n" +
			"reviewer\tp-reviewer\tgrok\treviewer\txai\t1\t" + cwd + "\t2026-10-04T12:01:00Z\tgrok-4\t\t\t\n"
	}
	fc := newStatusViewFixtureFull(t, collabRoster, func(cwd string) []fakecli.Rule {
		return append(collabAgentGets(cwd), fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`})
	})
	fc.env["SOHO_WORKER_MESSAGES"] = "policy"
	fc.env["SOHO_SKILL_DIR"] = filepath.Join(fc.state, "skill")
	if err := os.MkdirAll(fc.env["SOHO_SKILL_DIR"], 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fc.env["SOHO_SKILL_DIR"], "config.defaults"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	collabAssignment(t, &fc)
	src, err := core.OpenSessionSource(fc.state, "ws", fc.env["TMPDIR"])
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	defer src.Close()
	view := inspectCollab(t, &fc, src)
	if view.State != "collaborating" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("control view=%+v, want collaborating", view)
	}
	swapSessionDir(t, &fc)
	view = inspectCollab(t, &fc, src)
	if view.State != "collaborating" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("pinned-root view=%+v, want the retained root's collaborating", view)
	}
	// The raw-path store reader follows the link to the decoy: the
	// decoy has no collaboration dir, so the raw reader sees no active
	// collaboration — the inspection must not.
	if got, err := (collaboration.Store{StateDir: fc.sd}).ActiveFor("p-author"); err != nil || got != nil {
		t.Fatalf("the raw store reader = %v, %v; want no collaboration (the raw path follows the link to the decoy)", got, err)
	}
}

// inspectCollab runs the read-only inspect with the supplied source over
// the fixture's agent author (the collaboration host pane's roster name)
// and asserts the silent no-write invariants.
func inspectCollab(t *testing.T, f *statusViewFixture, src *core.SessionSource) AgentStatusView {
	t.Helper()
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  f.sd,
		SessionID: "ws",
		Agent:     "author",
		Cwd:       f.cwd,
		Config:    f.ctx,
		Env:       f.env,
		Services:  backend.New(f.env),
		Source:    src,
	})
	if out != "" || errOut != "" {
		t.Fatalf("the inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	return view
}

// TestStatusViewSourcePinnedRootMirror: the pointer names a scratch
// report that is gone; the protected mirror stands under the state
// reports dir before the swap. After the session dir is renamed and
// replaced by a link to a decoy with no mirror, the inspection must
// still resolve the mirror under the retained root (report ready),
// while the raw-path mirror observation follows the link to the decoy
// and finds none.
func TestStatusViewSourcePinnedRootMirror(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{{Argv: []string{"agent", "get", "unused"}}}
	f := newSourceViewFixture(t, roster, probe)
	ghostReport := filepath.Join(f.tmp, "soho", "ws", "reports", "worker-final.md")
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(ghostReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(f.sd, "reports", "worker-final.md")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirror, []byte("final report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	control := f.inspectWith(t, "worker")
	if control.State != "done" || !control.ReportReady {
		t.Fatalf("control view=%+v, want done with the mirror report ready", control)
	}
	swapSessionDir(t, &f.statusViewFixture)
	view := f.inspectWith(t, "worker")
	if view.State != "done" || !view.ReportReady {
		t.Fatalf("pinned-root view=%+v, want the retained root's mirror (done with the report ready)", view)
	}
	// The raw-path mirror observation follows the link to the decoy,
	// which has no mirror: it must not report the mirror.
	if m := protectedMirror(f.sd, ghostReport); m != ghostReport {
		t.Fatalf("raw mirror = %q, want the pointer's own path (the decoy has no mirror)", m)
	}
}

// TestStatusViewSourceRosterSnapshotAuthority: the roster row the source
// acquired stays the authority after the on-disk roster changes: the
// same source keeps rendering the marker state of the acquired row,
// while a freshly acquired source reads the changed roster and renders
// the absent row's unknown-agent.
func TestStatusViewSourceRosterSnapshotAuthority(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	}
	f := newSourceViewFixture(t, roster, probe)
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.not-received"), []byte("dispatch-1 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view := f.inspectWith(t, "worker")
	if view.State != "not-received" || view.TerminalState != "idle" {
		t.Fatalf("control view=%+v, want not-received over idle", view)
	}
	// The on-disk roster drops the worker (the session dir is intact).
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f.inspectWith(t, "worker")
	if view.State != "not-received" || view.TerminalState != "idle" {
		t.Fatalf("snapshot view=%+v, want the acquired roster row's not-received (the on-disk change must not apply)", view)
	}
	// A freshly acquired source reads the changed roster: no row, and the
	// probe finds the agent Herdr knows, so the not-in-roster stands.
	fresh, err := core.OpenSessionSource(f.state, "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	defer fresh.Close()
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
		Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: fresh,
	})
	if out != "" || errOut != "" {
		t.Fatalf("the fresh inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if view.State != "not-in-roster" || view.TerminalState != "" {
		t.Fatalf("fresh-source view=%+v, want the changed roster's not-in-roster", view)
	}
}

// TestStatusViewSourceCollaborationBounds: the collaboration read is
// bounded on the inspection path: an assignment record over the cap and
// a store dir over the entry cap refuse (collaboration-unavailable),
// never an empty success and never a truncated read.
func TestStatusViewSourceCollaborationBounds(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	}
	// (a) The record is longer than the 1 MiB cap.
	f := newSourceViewFixture(t, roster, probe)
	record := filepath.Join(f.sd, "collaboration", "c-abcdef1234567890", "assignment.json")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte(strings.Repeat("a", (1<<20)+4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	view := f.inspectWith(t, "worker")
	if view.State != "collaboration-unavailable" {
		t.Fatalf("oversize-record view=%+v, want collaboration-unavailable", view)
	}
	// (b) The store dir exceeds the 4096-entry cap.
	f2 := newSourceViewFixture(t, roster, probe)
	for i := 0; i < 4097; i++ {
		if err := os.MkdirAll(filepath.Join(f2.sd, "collaboration", fmt.Sprintf("skip-%04d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	view = f2.inspectWith(t, "worker")
	if view.State != "collaboration-unavailable" {
		t.Fatalf("exhausted-dir view=%+v, want collaboration-unavailable", view)
	}
	// (c) Control: a valid store with no member of the pane renders the
	// usual state, not a failure.
	f3 := newSourceViewFixture(t, roster, probe)
	if err := os.MkdirAll(filepath.Join(f3.sd, "collaboration"), 0o700); err != nil {
		t.Fatal(err)
	}
	view = f3.inspectWith(t, "worker")
	if view.State != "no-report-yet" || view.TerminalState != "idle" {
		t.Fatalf("control view=%+v, want no-report-yet over idle", view)
	}
}

// TestStatusViewSourcePinnedRootSidecarHistory: the report observation's
// changed-after-the-report scope (the sidecar's owned files) follows the
// retained root: the report stands in the approved scratch reports dir,
// an unowned file is changed after the report, so the retained scope
// (owned.txt only) renders done; after the state session dir is renamed
// and replaced by a link to a decoy whose sidecar owns nothing, the
// observation with the source must keep the retained root's scope (done),
// while the raw-path reader follows the link to the decoy's sidecar and
// renders the decoy's scope (working: the unowned changed file stands).
func TestStatusViewSourcePinnedRootSidecarHistory(t *testing.T) {
	f := newReportFilesFixture(t, "idle", "implementer", "## Owned files\n- `owned.txt`\n")
	// The report observation with a source observes the report through the
	// approved report roots: move the report into the approved scratch
	// reports dir and point the pointer at it.
	reportsDir := filepath.Join(f.env["TMPDIR"], "soho", "ws", "reports")
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	myReport := filepath.Join(reportsDir, "report.md")
	reportFilesWrite(t, myReport, "report")
	if err := os.Chtimes(myReport, f.at, f.at); err != nil {
		t.Fatal(err)
	}
	reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), myReport+"\n")
	f.change(t, "other.txt", time.Second)
	src, err := core.OpenSessionSource(filepath.Dir(f.sd), "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	defer src.Close()
	svc := backend.New(f.env)
	state, cause, terminal := reportObservation(f.sd, "worker", myReport, f.ctx, svc, f.env, true, src)
	if state != "done" || terminal != "idle" || cause != "" {
		t.Fatalf("control scope: state=%s cause=%q terminal=%q, want done: the unowned changed file must not stand in the retained scope", state, cause, terminal)
	}
	// Rename the session dir and replace it with a link to a decoy whose
	// sidecar owns nothing.
	var sidecarJSON struct {
		Tree string `json:"git_tree"`
		Head string `json:"git_head"`
	}
	if data, err := os.ReadFile(f.sidecar); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(data, &sidecarJSON); err != nil {
		t.Fatal(err)
	}
	renamed := f.sd + "-retained"
	if err := os.Rename(f.sd, renamed); err != nil {
		t.Fatalf("cannot rename the session dir: %v", err)
	}
	decoy := filepath.Join(filepath.Dir(f.sd), "decoy")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, f.sd); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	decoySidecar := filepath.Join(decoy, "briefs", "report.dispatch.json")
	if err := os.MkdirAll(filepath.Dir(decoySidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoySidecar, []byte(`{"git_tree":"`+sidecarJSON.Tree+`","git_head":"`+sidecarJSON.Head+`","role":"implementer"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The decoy's brief carries no Owned section: the raw walk resolves an
	// empty owned scope (every changed file stands).
	if err := os.WriteFile(filepath.Join(decoy, "briefs", "report.brief.md"), []byte("# Brief\nno owned section\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The inspection keeps the retained root's scope (done).
	state, cause, terminal = reportObservation(f.sd, "worker", myReport, f.ctx, svc, f.env, true, src)
	if state != "done" || terminal != "idle" || cause != "" {
		t.Fatalf("pinned-root scope: state=%s cause=%q terminal=%q, want the retained root's done (the decoy's sidecar must not be read)", state, cause, terminal)
	}
	// The raw-path reader follows the link to the decoy's sidecar (no
	// owned scope): the unowned changed file stands, and the files-after
	// decision is the decoy's (working), not the retained root's.
	rawState, rawCause, rawTerminal := reportObservation(f.sd, "worker", myReport, f.ctx, svc, f.env, true, nil)
	if rawState != "working" || rawCause != "report draft: files changed after it" || rawTerminal != "idle" {
		t.Fatalf("raw scope: state=%s cause=%q terminal=%q, want the decoy's working (the raw path follows the link)", rawState, rawCause, rawTerminal)
	}
}
