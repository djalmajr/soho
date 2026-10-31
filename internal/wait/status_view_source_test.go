package wait

// Consumer tests for the safe source-based inspection (the acquired
// core.SessionSource supplied to InspectAgentStatus) and the
// terminal-only orchestrator inspection (InspectTerminalStatus): the
// regular/absent positive controls, the poisoned metadata refusals,
// the scratch/protected-mirror report authority, both fake transports,
// and the no-duplicate-get / no-caller-discovery parity.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// sourceViewFixture is the statusViewFixture plus one safely acquired
// source over the fixture's state base and session ws (scratch base:
// the fixture's pinned TMPDIR).
type sourceViewFixture struct {
	statusViewFixture
	src *core.SessionSource
}

// newSourceViewFixture builds the fixture and acquires the source for
// the session ws; an acquisition error is a test failure.
func newSourceViewFixture(t *testing.T, rosterRows string, rules []fakecli.Rule) sourceViewFixture {
	t.Helper()
	f := newStatusViewFixture(t, rosterRows, rules)
	src, err := core.OpenSessionSource(f.state, "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	t.Cleanup(func() { src.Close() })
	return sourceViewFixture{statusViewFixture: f, src: src}
}

// inspectWith runs the read-only inspect with the acquired source and
// asserts the fixture invariants: no print, no friction write, no env
// mutation.
func (f sourceViewFixture) inspectWith(t *testing.T, agent string) AgentStatusView {
	t.Helper()
	envBefore := copyEnv(f.env)
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  f.sd,
		SessionID: "ws",
		Agent:     agent,
		Cwd:       f.cwd,
		Config:    f.ctx,
		Env:       f.env,
		Services:  backend.New(f.env),
		Source:    f.src,
	})
	if out != "" || errOut != "" {
		t.Fatalf("the source inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if friction, err := os.ReadFile(f.friction); err != nil || string(friction) != "configured line\n" {
		t.Fatalf("the source inspect wrote the configured friction file: %q %v", friction, err)
	}
	assertEnvUnmutated(t, envBefore, f.env)
	return view
}

// TestStatusViewSourceReportDone: the final report under the state
// reports dir renders done with the report observed and no probe,
// exactly as the command renders it, through the acquired source.
func TestStatusViewSourceReportDone(t *testing.T) {
	f := newSourceViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
		[]fakecli.Rule{{Argv: []string{"agent", "get", "unused"}}})
	report := writeFinalReport(t, f.statusViewFixture, "worker")
	before := f.fixtureBefore(t)
	view := f.inspectWith(t, "worker")
	if view.State != "done" || view.TerminalState != "" || !view.ReportReady {
		t.Fatalf("view=%+v, want done / no terminal probe / report ready", view)
	}
	assertNoSessionDiscovery(t, f.callLog(t))
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	code, out, errOut := f.runStatus(t, []string{"worker"})
	if code != 0 || errOut != "" || !strings.HasPrefix(out, "worker\tdone\t"+report+"\t") || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus code=%d out=%q stderr=%q, want the done line", code, out, errOut)
	}
}

// TestStatusViewSourceWorking: a working pane renders working through
// the source with the exact same transport sequence as the sourceless
// inspect — one probe get, no duplicate get, no discovery — and the
// command renders the same line.
func TestStatusViewSourceWorking(t *testing.T) {
	f := newSourceViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"},
	})
	before := f.fixtureBefore(t)
	view := f.inspectWith(t, "worker")
	if view.State != "working" || view.TerminalState != "working" || view.ReportReady {
		t.Fatalf("view=%+v, want working / working / no report", view)
	}
	wantCalls(t, f.callLog(t),
		"agent\x00get\x00worker",
		"agent\x00list",
		"agent\x00read\x00worker\x00--source\x00visible",
	)
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	// The sourceless inspect spends the identical sequence.
	before2 := f.fixtureBefore(t)
	plain := f.inspect(t, "ws", "worker")
	if plain.State != view.State || plain.TerminalState != view.TerminalState {
		t.Fatalf("sourceless=%+v source=%+v, want the same view", plain, view)
	}
	assertTreeUnchanged(t, "inspect", before2, f.fixtureBefore(t))
	code, out, _ := f.runStatus(t, []string{"worker"})
	if code != 0 || out != "worker\tworking\t\t-\t-\n" || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus code=%d out=%q, want the working line", code, out)
	}
}

// TestStatusViewSourceAbsentSession: an acquired source whose session
// dir is absent is a valid empty source: the agent is unknown-agent
// with no probe state, nothing is created and the base tree is
// untouched.
func TestStatusViewSourceAbsentSession(t *testing.T) {
	f := newSourceViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"server_down","message":"herdr server unreachable"},"id":"cli:agent:get"}`},
	})
	before := f.fixtureBefore(t)
	ghostSrc, err := core.OpenSessionSource(f.state, "ghost", f.env["TMPDIR"])
	if err != nil {
		t.Fatalf("the absent session was an error: %v", err)
	}
	defer ghostSrc.Close()
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  f.sd,
		SessionID: "ghost",
		Agent:     "worker",
		Cwd:       f.cwd,
		Config:    f.ctx,
		Env:       f.env,
		Services:  backend.New(f.env),
		Source:    ghostSrc,
	})
	if out != "" || errOut != "" {
		t.Fatalf("the absent-session inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if view.State != "unknown-agent" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("absent-session view=%+v, want unknown-agent with no probe state", view)
	}
	wantCalls(t, f.callLog(t), "agent\x00get\x00worker")
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
}

// TestStatusViewSourcePoisonedPointer: an oversize, a linked or an
// outside-root last-report pointer poisons the required report
// evidence: the inspection outcome is unknown, never a fabricated
// settled report — no probe and no report-ready. The regular absent
// report keeps the normal absence.
func TestStatusViewSourcePoisonedPointer(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"},
	}
	// (a) The pointer is longer than the 64 KiB metadata cap.
	f := newSourceViewFixture(t, roster, probe)
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(strings.Repeat("x", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	view := f.inspectWith(t, "worker")
	if view.State != "unknown" || view.ReportReady {
		t.Fatalf("oversize-pointer view=%+v, want unknown with no report ready", view)
	}
	// The poisoned evidence settles before any probe.
	wantCalls(t, f.callLog(t))

	// (b) The pointer is a link to a report outside the approved roots.
	f2 := newSourceViewFixture(t, roster, probe)
	outside := t.TempDir()
	outsideReport := filepath.Join(outside, "worker-final.md")
	if err := os.WriteFile(outsideReport, []byte("final\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideReport, filepath.Join(f2.sd, "last-report-worker")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	view = f2.inspectWith(t, "worker")
	if view.State != "unknown" || view.ReportReady {
		t.Fatalf("linked-pointer view=%+v, want unknown with no report ready", view)
	}
	wantCalls(t, f2.callLog(t))

	// (c) The pointer names a path outside the approved report roots.
	f3 := newSourceViewFixture(t, roster, probe)
	if err := os.WriteFile(filepath.Join(f3.sd, "last-report-worker"), []byte(outsideReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f3.inspectWith(t, "worker")
	if view.State != "unknown" || view.ReportReady {
		t.Fatalf("outside-pointer view=%+v, want unknown with no report ready", view)
	}
	wantCalls(t, f3.callLog(t))

	// Control: the pointer names a regular state report that is absent —
	// the existing normal absence, the usual state.
	f4 := newSourceViewFixture(t, roster, probe)
	absentReport := filepath.Join(f4.sd, "reports", "worker-final.md")
	if err := os.WriteFile(filepath.Join(f4.sd, "last-report-worker"), []byte(absentReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f4.inspectWith(t, "worker")
	if view.State != "working" || view.TerminalState != "working" || view.ReportReady {
		t.Fatalf("absent-report view=%+v, want the usual working state", view)
	}
}

// TestStatusViewSourceRosterRefused: an oversize roster and a
// duplicate-identity roster are acquisition errors — the session-level
// refusal the later producer treats as candidate-invalid — never an
// empty-success fallback.
func TestStatusViewSourceRosterRefused(t *testing.T) {
	base := t.TempDir()
	sd := filepath.Join(base, "s1")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	roster := "w\tp1\tclaude\timplementer\t" + strings.Repeat("x", 64<<10)
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := core.OpenSessionSource(base, "s1", ""); err == nil {
		t.Fatalf("the oversize roster was accepted")
	}
	roster = "dup\tp1\tclaude\timplementer\n" +
		"dup\tp1\tcodex\treviewer\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := core.OpenSessionSource(base, "s1", ""); err == nil {
		t.Fatalf("the duplicate-identity roster was accepted")
	}
}

// TestStatusViewSourcePoisonedMarker: an oversize or a linked
// not-received marker reads as absent on the inspection path — the
// agent keeps its usual state — while the ordinary command reader keeps
// its behavior on the linked marker.
// TestStatusViewSourcePoisonedMarker: the required delivery markers
// (not-received, queued) that exist but cannot be read as safe metadata
// (an oversize, a linked, a nonregular or a FIFO marker) yield the
// conservative unknown state, never the absence they would otherwise
// read as — the not-received decision cannot fire on unreadable
// evidence, and neither can a fabricated settled state. A regular
// marker with an unchanged sequence renders not-received on both
// paths, and the command path keeps its ordinary reader on a linked
// marker (its behavior is unchanged).
func TestStatusViewSourcePoisonedMarker(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	}
	// (a) The marker is longer than the 64 KiB metadata cap: the
	// conservative unknown, never the absence.
	f := newSourceViewFixture(t, roster, probe)
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.not-received"), []byte(strings.Repeat("m", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	view := f.inspectWith(t, "worker")
	if view.State != "unknown" || view.TerminalState != "idle" {
		t.Fatalf("oversize-marker view=%+v, want unknown over idle", view)
	}
	// (b) The marker is a link to the valid marker content: the
	// inspection refuses the linked entry (unknown), while the command
	// path keeps its ordinary reader (the linked content renders
	// not-received).
	f2 := newSourceViewFixture(t, roster, probe)
	outside := t.TempDir()
	outsideMarker := filepath.Join(outside, "worker.not-received")
	if err := os.WriteFile(outsideMarker, []byte("dispatch-1 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideMarker, filepath.Join(f2.sd, "wait", "worker.not-received")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	view = f2.inspectWith(t, "worker")
	if view.State != "unknown" || view.TerminalState != "idle" {
		t.Fatalf("linked-marker view=%+v, want unknown over idle", view)
	}
	code, out, _ := f2.runStatus(t, []string{"worker"})
	if code != 15 || out != "worker\tnot-received\t\t-\t-\n" {
		t.Fatalf("cmdstatus code=%d out=%q, want the command's not-received line with rc 15 on the linked marker", code, out)
	}
	// (c) The marker is a directory (nonregular): the same conservative
	// unknown.
	f3 := newSourceViewFixture(t, roster, probe)
	if err := os.MkdirAll(filepath.Join(f3.sd, "wait", "worker.not-received"), 0o700); err != nil {
		t.Fatal(err)
	}
	view = f3.inspectWith(t, "worker")
	if view.State != "unknown" || view.TerminalState != "idle" {
		t.Fatalf("nonregular-marker view=%+v, want unknown over idle", view)
	}
	// (d) The required queued marker, oversize: the same conservative
	// unknown (the queued prompt decision cannot fire on unreadable
	// evidence).
	f4 := newSourceViewFixture(t, roster, probe)
	if err := os.WriteFile(filepath.Join(f4.sd, "wait", "worker.queued"), []byte(strings.Repeat("q", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f4.inspectWith(t, "worker")
	if view.State != "unknown" || view.TerminalState != "idle" {
		t.Fatalf("oversize-queued view=%+v, want unknown over idle", view)
	}
	// (e) Control: the regular marker with an unchanged sequence renders
	// not-received on both paths.
	f5 := newSourceViewFixture(t, roster, probe)
	if err := os.WriteFile(filepath.Join(f5.sd, "wait", "worker.not-received"), []byte("dispatch-1 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f5.inspectWith(t, "worker")
	if view.State != "not-received" || view.TerminalState != "idle" {
		t.Fatalf("control view=%+v, want not-received over idle", view)
	}
	code, out, _ = f5.runStatus(t, []string{"worker"})
	if code != 15 || out != "worker\tnot-received\t\t-\t-\n" {
		t.Fatalf("cmdstatus code=%d out=%q, want the not-received line with rc 15", code, out)
	}
}

// TestStatusViewSourceScratchReportAuthority: the approved scratch
// report and the protected mirror settle the done report authority
// without a probe; a linked scratch report poisons the evidence.
func TestStatusViewSourceScratchReportAuthority(t *testing.T) {
	roster := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	probe := []fakecli.Rule{{Argv: []string{"agent", "get", "unused"}}}
	// (a) The scratch report stands: the pointer names it, the final
	// marker settles it and no probe runs.
	f := newSourceViewFixture(t, roster, probe)
	scratchReport := filepath.Join(f.tmp, "soho", "ws", "reports", "worker-final.md")
	if err := os.MkdirAll(filepath.Dir(scratchReport), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scratchReport, []byte("final report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(scratchReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(scratchReport)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.report-working-final"), []byte(reportIdentity(scratchReport, info)), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	view := f.inspectWith(t, "worker")
	if view.State != "done" || view.TerminalState != "" || !view.ReportReady {
		t.Fatalf("scratch view=%+v, want done / no probe / report ready", view)
	}
	wantCalls(t, f.callLog(t))
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	// Parity: the command renders the same done line.
	code, out, _ := f.runStatus(t, []string{"worker"})
	if code != 0 || !strings.HasPrefix(out, "worker\tdone\t"+scratchReport+"\t") {
		t.Fatalf("cmdstatus code=%d out=%q, want the scratch done line", code, out)
	}
	// (b) The scratch report is gone and the protected mirror stands:
	// the report resolves to the mirror under the state reports dir.
	f2 := newSourceViewFixture(t, roster, probe)
	ghostReport := filepath.Join(f2.tmp, "soho", "ws", "reports", "worker-final.md")
	if err := os.WriteFile(filepath.Join(f2.sd, "last-report-worker"), []byte(ghostReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(f2.sd, "reports", "worker-final.md")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirror, []byte("final report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info2, err := os.Stat(mirror)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f2.sd, "wait", "worker.report-working-final"), []byte(reportIdentity(mirror, info2)), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f2.inspectWith(t, "worker")
	if view.State != "done" || view.TerminalState != "" || !view.ReportReady {
		t.Fatalf("mirror view=%+v, want done / no probe / report ready", view)
	}
	wantCalls(t, f2.callLog(t))
	// (c) The scratch report is a link: the evidence is unsafe, the
	// outcome is unknown, never a fabricated settled report.
	f3 := newSourceViewFixture(t, roster, probe)
	outside := t.TempDir()
	outsideReport := filepath.Join(outside, "worker-final.md")
	if err := os.WriteFile(outsideReport, []byte("final\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratchLink := filepath.Join(f3.tmp, "soho", "ws", "reports", "worker-final.md")
	if err := os.MkdirAll(filepath.Dir(scratchLink), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideReport, scratchLink); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f3.sd, "last-report-worker"), []byte(scratchLink+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view = f3.inspectWith(t, "worker")
	if view.State != "unknown" || view.ReportReady {
		t.Fatalf("linked-scratch view=%+v, want unknown with no report ready", view)
	}
	wantCalls(t, f3.callLog(t))
}

// TestStatusViewSourceStaleCaller: the caller's own workspace context
// is stale or foreign; the acquired source confines the inspection to
// the known session ws — the decoy session's roster, markers and
// reports are never read, and the collaboration verifies against the
// known session instead of discovering the caller's.
func TestStatusViewSourceStaleCaller(t *testing.T) {
	// (a) The agent exists only in the decoy session's roster: the
	// inspection of ws does not find it, and the decoy's bytes are
	// untouched.
	f := newSourceViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "decoy"}, Code: 1, Stderr: `{"error":{"code":"server_down","message":"herdr server unreachable"},"id":"cli:agent:get"}`},
	})
	f.env["HERDR_WORKSPACE_ID"] = "other"
	decoySd := filepath.Join(f.state, "other")
	if err := os.MkdirAll(filepath.Join(decoySd, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoySd, "agents.tsv"), []byte("decoy\tp9\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoySd, "wait", "decoy.not-received"), []byte("dispatch-1 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	view := f.inspectWith(t, "decoy")
	if view.State != "unknown-agent" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("decoy view=%+v, want unknown-agent (the decoy session is not the known session)", view)
	}
	assertNoSessionDiscovery(t, f.callLog(t))
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))

	// (b) The active collaboration of the known session verifies against
	// it despite the stale caller context, without any discovery call.
	collabRoster := func(cwd string) string {
		return "author\tp-author\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tclaude-sonnet-4-5\t\t\t\n" +
			"reviewer\tp-reviewer\tgrok\treviewer\topenai\t1\t" + cwd + "\t2026-10-04T12:01:00Z\tgrok-4\t\t\t\n"
	}
	fc := newStatusViewFixtureFull(t, collabRoster, func(cwd string) []fakecli.Rule {
		return collabAgentGets(cwd)
	})
	fc.env["HERDR_WORKSPACE_ID"] = "other"
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
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  fc.sd,
		SessionID: "ws",
		Agent:     "author",
		Cwd:       fc.cwd,
		Config:    fc.ctx,
		Env:       fc.env,
		Services:  backend.New(fc.env),
		Source:    src,
	})
	if out != "" || errOut != "" {
		t.Fatalf("the stale-caller inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if view.State != "collaborating" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("stale-caller collaboration view=%+v, want collaborating", view)
	}
	wantCalls(t, fc.callLog(t),
		"agent\x00get\x00author",
		"agent\x00get\x00reviewer",
	)
}

// TestStatusViewSourceHc: the acquired source works through the hc
// transport: the working probe spends the exact hc sequence — no
// herdr, no caller get, no session-scoped ls — and the tree is
// untouched.
func TestStatusViewSourceHc(t *testing.T) {
	f := newHcStatusViewFixture(t,
		func(cwd string) string {
			return "worker\tt7\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tmodel-x\t\t\t\n"
		},
		func(cwd string) []fakecli.Rule {
			return []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"t7","name":"worker","state":"live","cwd":` + jsonString(t, cwd) + `,"status":{"state":"working"}}`},
				{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`},
				{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: `{"text":"Busy on the task 30%\n"}`},
			}
		},
		"t9",
	)
	src, err := core.OpenSessionSource(f.state, "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	t.Cleanup(func() { src.Close() })
	before := f.fixtureBefore(t)
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  f.sd,
		SessionID: "ws",
		Agent:     "worker",
		Cwd:       f.cwd,
		Config:    f.ctx,
		Env:       f.env,
		Services:  backend.New(f.env),
		Source:    src,
	})
	if out != "" || errOut != "" {
		t.Fatalf("the hc source inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if view.State != "working" || view.TerminalState != "working" || view.ReportReady {
		t.Fatalf("hc source view=%+v, want working over terminal working", view)
	}
	wantCalls(t, f.hcCallLog(t),
		"get\x00worker\x00--json",
		"ls\x00--json",
		"read\x00worker\x00--lines\x000\x00--json",
	)
	f.assertNoHerdrCall(t)
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
}

// silentTerminal runs the terminal-only inspect with the platform
// writers swapped and returns the view plus whatever (must be nothing)
// was printed.
func silentTerminal(t *testing.T, req TerminalStatusRequest) (TerminalStatusView, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	view := InspectTerminalStatus(req)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return view, out.String(), errOut.String()
}

// terminalFixture is the terminal-only fixture: the worker's terminal
// probe plus the fixture surfaces a terminal-only inspect must not
// touch.
func terminalFixture(t *testing.T, rules []fakecli.Rule) (statusViewFixture, TerminalStatusRequest) {
	t.Helper()
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", rules)
	return f, TerminalStatusRequest{
		Terminal: "worker",
		Kind:     "claude",
		Config:   f.ctx,
		Env:      f.env,
		Services: backend.New(f.env),
	}
}

// TestInspectTerminalStatusControls: done stays done, idle stays idle,
// working stays working; each spends exactly the resolver's reads — the
// done and idle probes take the resolver's quota/provider screen reads
// (the branches key on the probe word, before the no-report-yet
// remap), and none of them spends a roster, report, delivery or
// discovery read.
func TestInspectTerminalStatusControls(t *testing.T) {
	cases := []struct {
		name, state string
		working     bool
	}{
		{"done", "done", false},
		{"idle", "idle", false},
		{"working", "working", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + tc.state + `"}}}`},
			}
			want := []string{"agent\x00get\x00worker"}
			if tc.working {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"})
				want = append(want, "agent\x00read\x00worker\x00--source\x00visible")
			} else {
				// The resolver's quota/provider screen reads, spent on the
				// same probes and matching its exact order.
				rules = append(rules,
					fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "plain screen\n"},
					fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "plain screen\n"},
				)
				want = append(want,
					"agent\x00read\x00worker\x00--source\x00visible\x00--lines\x0020",
					"agent\x00read\x00worker\x00--source\x00recent-unwrapped\x00--lines\x0040",
				)
			}
			f, req := terminalFixture(t, rules)
			before := f.fixtureBefore(t)
			view, out, errOut := silentTerminal(t, req)
			if out != "" || errOut != "" {
				t.Fatalf("the terminal-only inspect printed: stdout=%q stderr=%q", out, errOut)
			}
			if view.State != tc.state || view.TerminalState != tc.state {
				t.Fatalf("view=%+v, want %s over %s", view, tc.state, tc.state)
			}
			wantCalls(t, f.callLog(t), want...)
			assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
		})
	}
}

// TestInspectTerminalStatusQuestionApproval: a blocked terminal on a
// question dialog classifies question; the approval dialog keeps
// blocked, exactly the worker resolver's order.
func TestInspectTerminalStatusQuestionApproval(t *testing.T) {
	const questionScreen = "Select an option (1-2):\n\u276f 1. Use the local cache\n  2. Fetch remote\n\n\u2191\u2193 to navigate \u00b7 Enter to select"
	const approvalScreen = "Do you want to proceed?\n\u276f 1. Yes\n  2. No\n\n\u2191\u2193 to navigate \u00b7 Enter to select"
	t.Run("question", func(t *testing.T) {
		f, req := terminalFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: questionScreen},
		})
		view, _, _ := silentTerminal(t, req)
		if view.State != "question" || view.TerminalState != "blocked" {
			t.Fatalf("view=%+v, want question over blocked", view)
		}
		wantCalls(t, f.callLog(t),
			"agent\x00get\x00worker",
			"agent\x00read\x00worker\x00--source\x00visible\x00--lines\x0040",
		)
	})
	t.Run("approval", func(t *testing.T) {
		f, req := terminalFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: approvalScreen},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: approvalScreen},
		})
		view, _, _ := silentTerminal(t, req)
		if view.State != "blocked" || view.TerminalState != "blocked" {
			t.Fatalf("view=%+v, want blocked over blocked", view)
		}
		wantCalls(t, f.callLog(t),
			"agent\x00get\x00worker",
			"agent\x00read\x00worker\x00--source\x00visible\x00--lines\x0040",
			"agent\x00read\x00worker\x00--source\x00recent-unwrapped\x00--lines\x0040",
		)
	})
}

// TestInspectTerminalStatusQuotaProvider: a quota stop classifies
// quota; a non-working terminal with a provider error classifies the
// provider status, exactly the worker resolver's words.
func TestInspectTerminalStatusQuotaProvider(t *testing.T) {
	t.Run("quota", func(t *testing.T) {
		f, req := terminalFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"error"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "You've hit your usage limit\nResets at 9:00 AM\n"},
		})
		view, _, _ := silentTerminal(t, req)
		if view.State != "quota" || view.TerminalState != "error" {
			t.Fatalf("view=%+v, want quota over error", view)
		}
		wantCalls(t, f.callLog(t),
			"agent\x00get\x00worker",
			"agent\x00read\x00worker\x00--source\x00visible\x00--lines\x0020",
		)
	})
	t.Run("provider", func(t *testing.T) {
		f, req := terminalFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "plain screen\n"},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: Connection error.\n"},
		})
		view, _, _ := silentTerminal(t, req)
		if view.State != "provider-error" || view.TerminalState != "idle" {
			t.Fatalf("view=%+v, want provider-error over idle", view)
		}
		wantCalls(t, f.callLog(t),
			"agent\x00get\x00worker",
			"agent\x00read\x00worker\x00--source\x00visible\x00--lines\x0020",
			"agent\x00read\x00worker\x00--source\x00recent-unwrapped\x00--lines\x0040",
		)
	})
}

// TestInspectTerminalStatusConservative: gone, unavailable and unknown
// probes stay conservative — the effective state is the probe's own
// word — with only the probe's read and nothing else.
func TestInspectTerminalStatusConservative(t *testing.T) {
	cases := []struct {
		name string
		rule fakecli.Rule
	}{
		{"gone", fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"gone"}}}`}},
		{"unavailable", fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"server_down","message":"herdr server unreachable"},"id":"cli:agent:get"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, req := terminalFixture(t, []fakecli.Rule{tc.rule})
			before := f.fixtureBefore(t)
			view, _, _ := silentTerminal(t, req)
			if view.State != tc.name || view.TerminalState != tc.name {
				t.Fatalf("view=%+v, want %s over %s", view, tc.name, tc.name)
			}
			wantCalls(t, f.callLog(t), "agent\x00get\x00worker")
			assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
		})
	}
}
