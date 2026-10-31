package wait

// The read-only status view: focused consumer tests for
// InspectAgentStatus and its parity with the exact line CmdStatus
// renders, plus the proof that the inspect path prints nothing,
// writes nothing (fixture tree, friction file, markers) and makes no
// caller or session discovery call.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// statusViewFixture is the read-only view fixture: a workspace state
// dir (roster + wait dir), a fake herdr on PATH with the call log, a
// pinned HOME/USERPROFILE and a pinned TMPDIR (the scratch root), and
// a configured nonempty friction target that the read-only path must
// leave untouched.
type statusViewFixture struct {
	cwd      string
	state    string
	sd       string
	tmp      string
	bin      string
	friction string
	ctx      *core.Config
	env      platform.Env
}

// newStatusViewFixtureRows builds the fixture with a roster whose rows
// may depend on the fixture's project dir.
func newStatusViewFixtureRows(t *testing.T, rosterRows func(cwd string) string, rules []fakecli.Rule) statusViewFixture {
	return newStatusViewFixtureFull(t, rosterRows, func(cwd string) []fakecli.Rule { return rules })
}

// newStatusViewFixtureFull builds the fixture with a roster and a fake
// herdr rule set that may depend on the fixture's project dir.
func newStatusViewFixtureFull(t *testing.T, rosterRows func(cwd string) string, rulesFor func(cwd string) []fakecli.Rule) statusViewFixture {
	t.Helper()
	base := t.TempDir()
	cwd := filepath.Join(base, "cwd")
	state := filepath.Join(base, "state")
	bin := filepath.Join(base, "bin")
	home := filepath.Join(base, "home")
	tmp := filepath.Join(base, "tmp")
	for _, dir := range []string{cwd, filepath.Join(state, "ws", "wait"), bin, home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + rosterRows(cwd)
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	// The configured nonempty friction target: another caller configured
	// it; the read-only inspect must stay silent even though it exists.
	friction := filepath.Join(base, "friction.log")
	if err := os.WriteFile(friction, []byte("configured line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFriction := frictionLogFile
	frictionLogFile = friction
	t.Cleanup(func() { frictionLogFile = oldFriction })
	if _, err := fakecli.InstallWithOptions(t, bin, "herdr", rulesFor(cwd), fakecli.InstallOptions{CaptureEnv: []string{"GORACE"}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":                bin,
		"HERDR_ENV":           "1",
		"SOHO_DIR":            state,
		"HERDR_WORKSPACE_ID":  "ws",
		"HERDR_SOCKET_PATH":   filepath.Join(base, "none.sock"),
		"SOHO_FAKECLI_CONFIG": bin,
		"HOME":                home,
		"USERPROFILE":         home,
		"TMPDIR":              tmp,
		// These manually constructed fake-child environments do not pass
		// through fakecli.Env. Remove only the child's race exit pause,
		// preserving detector options and the parent process environment.
		"GORACE": fakecli.RaceOptions(os.Environ()),
	}
	// The platform's by-pid process read shells out to ps: the fake bin
	// comes first so the fake herdr wins, and the host ps dir is appended
	// so a compact-phase owner can still be classified.
	if ps, err := exec.LookPath("ps"); err == nil {
		env["PATH"] = bin + string(os.PathListSeparator) + filepath.Dir(ps)
	}
	return statusViewFixture{
		cwd:      cwd,
		state:    state,
		sd:       filepath.Join(state, "ws"),
		tmp:      tmp,
		bin:      bin,
		friction: friction,
		ctx:      &core.Config{Entries: map[string]core.ConfigEntry{}},
		env:      env,
	}
}

func newStatusViewFixture(t *testing.T, rosterRows string, rules []fakecli.Rule) statusViewFixture {
	return newStatusViewFixtureRows(t, func(cwd string) string { return rosterRows }, rules)
}

func TestStatusViewFakeChildRaceOptions(t *testing.T) {
	parent, set := os.LookupEnv("GORACE")
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	})
	if view := f.inspect(t, "ws", "worker"); view.State != "no-report-yet" {
		t.Fatalf("view=%+v, want the ordinary idle consumer", view)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil || len(calls) == 0 {
		t.Fatalf("no actual fake subprocess calls: %v", err)
	}
	for _, call := range calls {
		if call.Env["GORACE"] != fakecli.RaceOptions(os.Environ()) {
			t.Fatal("the fake child did not receive the preserved detector options with zero exit pause")
		}
	}
	if after, present := os.LookupEnv("GORACE"); after != parent || present != set {
		t.Fatal("the fixture changed the parent's GORACE")
	}
	t.Log("actual fake child options preserve the detector options, remove only exit delay; parent GORACE unchanged")
}

func (f statusViewFixture) inspect(t *testing.T, session, agent string) AgentStatusView {
	t.Helper()
	envBefore := copyEnv(f.env)
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  f.sd,
		SessionID: session,
		Agent:     agent,
		Cwd:       f.cwd,
		Config:    f.ctx,
		Env:       f.env,
		Services:  backend.New(f.env),
	})
	if out != "" || errOut != "" {
		t.Fatalf("the read-only inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if friction, err := os.ReadFile(f.friction); err != nil || string(friction) != "configured line\n" {
		t.Fatalf("the read-only inspect wrote the configured friction file: %q %v", friction, err)
	}
	assertEnvUnmutated(t, envBefore, f.env)
	return view
}

// silentInspect runs the read-only inspect with the platform writers
// swapped and returns the view plus whatever (must be nothing) was
// printed.
func silentInspect(t *testing.T, req AgentStatusRequest) (AgentStatusView, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	view := InspectAgentStatus(req)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return view, out.String(), errOut.String()
}

// runStatus swaps the platform writers around the real CmdStatus entry
// point and returns the exit code plus the captured output.
func (f statusViewFixture) runStatus(t *testing.T, agents []string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := CmdStatus(agents, f.ctx, f.env, f.cwd)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

// callLog returns the ordered argv of every fake herdr call; a missing
// log file is the empty sequence (no call ever ran).
func (f statusViewFixture) callLog(t *testing.T) []string {
	t.Helper()
	return f.binaryCallLog(t, "herdr")
}

// hcCallLog is the call sequence of the fake hc binary; a missing log file
// is the empty sequence, which also proves the hc path never ran.
func (f statusViewFixture) hcCallLog(t *testing.T) []string {
	t.Helper()
	return f.binaryCallLog(t, "hc")
}

// binaryCallLog reads one fake binary's ordered argv log.
func (f statusViewFixture) binaryCallLog(t *testing.T, binary string) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, binary+".calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range calls {
		out = append(out, strings.Join(c.Argv, "\x00"))
	}
	return out
}

// assertNoHerdrCall fails when the fake herdr sentinel recorded a call.
func (f statusViewFixture) assertNoHerdrCall(t *testing.T) {
	t.Helper()
	if calls := f.callLog(t); len(calls) != 0 {
		t.Fatalf("the fake herdr sentinel recorded calls: %#v", calls)
	}
}

// wantCalls asserts the exact ordered transport call sequence.
func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("calls=%#v, want exactly %#v", got, want)
	}
}

// assertNoSessionDiscovery asserts the inspect made no caller or
// session discovery call: no pane current (the core.WorkspaceID
// channel) and no current-pane lookup of any form.
func assertNoSessionDiscovery(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(call, "\x00pane\x00") || strings.Contains(call, "current") {
			t.Fatalf("the read-only inspect made a session-discovery call: %q (all: %v)", call, calls)
		}
	}
}

// snapEntry is one file's bytes and metadata.
type snapEntry struct {
	size int64
	mod  time.Time
	mode os.FileMode
	data []byte
}

// treeSnapshot captures every file's bytes and metadata under the
// roots. The fake's call log lives under bin and is deliberately not a
// root: it is the call assertion itself.
func treeSnapshot(t *testing.T, roots ...string) map[string]snapEntry {
	t.Helper()
	snap := map[string]snapEntry{}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			snap[path] = snapEntry{size: info.Size(), mod: info.ModTime(), mode: info.Mode(), data: data}
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot of %s: %v", root, err)
		}
	}
	return snap
}

func assertTreeUnchanged(t *testing.T, what string, before, after map[string]snapEntry) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s changed the entry count: before=%d after=%d", what, len(before), len(after))
	}
	for path, e := range after {
		prev, ok := before[path]
		if !ok || prev.size != e.size || !prev.mod.Equal(e.mod) || prev.mode != e.mode || !bytes.Equal(prev.data, e.data) {
			t.Fatalf("%s changed %s: before=%#v after=%#v", what, path, before[path], e)
		}
	}
}

// fixtureBefore snapshots the owned fixture surfaces the inspect must
// not touch: the state tree, the caller's project dir, the scratch
// root and the home dir.
func (f statusViewFixture) fixtureBefore(t *testing.T) map[string]snapEntry {
	t.Helper()
	return treeSnapshot(t, f.state, f.cwd, f.tmp, f.env["HOME"])
}

// renderedState is the state a status line renders: the "status" key of
// a JSON line, else the second TSV column.
func renderedState(t *testing.T, line string) string {
	t.Helper()
	line = strings.TrimSuffix(line, "\n")
	if strings.HasPrefix(line, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line is not JSON: %v (%q)", err, line)
		}
		state, _ := obj["status"].(string)
		return state
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 2 {
		t.Fatalf("line has no state column: %q", line)
	}
	return fields[1]
}

func copyEnv(env platform.Env) platform.Env {
	out := make(platform.Env, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func assertEnvUnmutated(t *testing.T, before, after platform.Env) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("the caller's env gained keys: before=%d after=%d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("the caller's env %s was mutated: %q -> %q", k, v, after[k])
		}
	}
}

// writeFinalReport records a complete final report under the state dir
// plus the report-working-final marker that reportStatus accepts
// without a probe, and returns the report path.
func writeFinalReport(t *testing.T, f statusViewFixture, agent string) string {
	t.Helper()
	report := filepath.Join(f.sd, "reports", agent+"-final.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("final report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-"+agent), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	identity := reportIdentity(report, info)
	if err := os.WriteFile(filepath.Join(f.sd, "wait", agent+".report-working-final"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	return report
}

// TestStatusViewReportDone: a final report renders done with the report
// observed, no probe (the final marker settles it) and the view
// agreeing with the line CmdStatus renders.
func TestStatusViewReportDone(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
		[]fakecli.Rule{{Argv: []string{"agent", "get", "unused"}}})
	report := writeFinalReport(t, f, "worker")
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	if view.State != "done" || view.TerminalState != "" || !view.ReportReady {
		t.Fatalf("view=%+v, want done / no terminal probe / report ready", view)
	}
	assertNoSessionDiscovery(t, f.callLog(t))
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	// Parity: the same line the command renders, no probe, no write.
	before2 := f.fixtureBefore(t)
	code, out, errOut := f.runStatus(t, []string{"worker"})
	if code != 0 || errOut != "" {
		t.Fatalf("cmdstatus code=%d out=%q stderr=%q", code, out, errOut)
	}
	if !strings.HasPrefix(out, "worker\tdone\t"+report+"\t") || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus out=%q, want the done line for %s", out, report)
	}
	wantCalls(t, f.callLog(t))
	assertTreeUnchanged(t, "cmdstatus", before2, f.fixtureBefore(t))
}

// TestStatusViewWorking: a working pane with no report renders working
// with the probe's own state word as the terminal state.
func TestStatusViewWorking(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"},
	})
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	if view.State != "working" || view.TerminalState != "working" || view.ReportReady {
		t.Fatalf("view=%+v, want working / working / no report", view)
	}
	wantCalls(t, f.callLog(t),
		"agent\x00get\x00worker",
		"agent\x00list",
		"agent\x00read\x00worker\x00--source\x00visible",
	)
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	code, out, _ := f.runStatus(t, []string{"worker"})
	if code != 0 || out != "worker\tworking\t\t-\t-\n" || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus code=%d out=%q, want the working line", code, out)
	}
}

// TestStatusViewNoReportDoneVersusIdle: settled done and idle both
// render no-report-yet; the terminal state is what distinguishes the
// settled no-report from the idle one.
func TestStatusViewNoReportDoneVersusIdle(t *testing.T) {
	f := newStatusViewFixture(t, "wdone\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"+
		"widle\tp2\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "wdone"}, Stdout: `{"result":{"agent":{"agent_status":"done"}}}`},
		{Argv: []string{"agent", "get", "widle"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	})
	done := f.inspect(t, "ws", "wdone")
	if done.State != "no-report-yet" || done.TerminalState != "done" || done.ReportReady {
		t.Fatalf("done view=%+v, want no-report-yet with terminal done", done)
	}
	idle := f.inspect(t, "ws", "widle")
	if idle.State != "no-report-yet" || idle.TerminalState != "idle" || idle.ReportReady {
		t.Fatalf("idle view=%+v, want no-report-yet with terminal idle", idle)
	}
	code, out, _ := f.runStatus(t, []string{"wdone", "widle"})
	if code != 0 || out != "wdone\tno-report-yet\t\t-\t-\nwidle\tno-report-yet\t\t-\t-\n" {
		t.Fatalf("cmdstatus code=%d out=%q, want both no-report-yet", code, out)
	}
	lines := strings.Split(out, "\n")
	if renderedState(t, lines[0]) != done.State || renderedState(t, lines[1]) != idle.State {
		t.Fatalf("the rendered states do not match the view: out=%q", out)
	}
}

// TestStatusViewQuestionAndApproval: a blocked pane on a question
// dialog renders question (the override the line carries); an approval
// dialog stays blocked, as the inventory fold expects.
func TestStatusViewQuestionAndApproval(t *testing.T) {
	const questionScreen = "Select an option (1-2):\n\u276f 1. Use the local cache\n  2. Fetch remote\n\n\u2191\u2193 to navigate \u00b7 Enter to select"
	const approvalScreen = "Do you want to proceed?\n\u276f 1. Yes\n  2. No\n\n\u2191\u2193 to navigate \u00b7 Enter to select"
	fq := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: questionScreen},
	})
	view := fq.inspect(t, "ws", "worker")
	if view.State != "question" || view.TerminalState != "blocked" || view.ReportReady {
		t.Fatalf("question view=%+v, want question over terminal blocked", view)
	}
	wantCalls(t, fq.callLog(t),
		"agent\x00get\x00worker",
		"agent\x00list",
		"agent\x00read\x00worker\x00--source\x00visible\x00--lines\x0040",
	)
	code, out, errOut := fq.runStatus(t, []string{"worker"})
	if code != 7 || renderedState(t, out) != "question" || !strings.Contains(out, `"status":"question"`) {
		t.Fatalf("cmdstatus code=%d out=%q stderr=%q, want the question JSON line with rc 7", code, out, errOut)
	}

	fa := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: approvalScreen},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: approvalScreen},
	})
	view = fa.inspect(t, "ws", "worker")
	if view.State != "blocked" || view.TerminalState != "blocked" || view.ReportReady {
		t.Fatalf("approval view=%+v, want blocked over terminal blocked", view)
	}
	code, out, _ = fa.runStatus(t, []string{"worker"})
	if code != 0 || out != "worker\tblocked\t\t-\t-\n" || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus code=%d out=%q, want the plain blocked line", code, out)
	}
}

// TestStatusViewQuotaAndProvider: a quota stop renders quota (and the
// command's friction line is the wrapper's, not the inspect's), and a
// non-working pane with a provider error renders the provider status.
func TestStatusViewQuotaAndProvider(t *testing.T) {
	fq := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"error"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "You've hit your usage limit\nResets at 9:00 AM\n"},
	})
	before := fq.fixtureBefore(t)
	view := fq.inspect(t, "ws", "worker")
	if view.State != "quota" || view.TerminalState != "error" || view.ReportReady {
		t.Fatalf("quota view=%+v, want quota over terminal error", view)
	}
	assertTreeUnchanged(t, "inspect", before, fq.fixtureBefore(t))
	// Parity: the wrapper applies the friction warning the inspect only
	// recorded.
	code, out, errOut := fq.runStatus(t, []string{"worker"})
	if code != 11 || renderedState(t, out) != "quota" || !strings.Contains(out, `"status":"quota"`) {
		t.Fatalf("cmdstatus code=%d out=%q, want the quota JSON line with rc 11", code, out)
	}
	if !strings.Contains(errOut, "soho: warning: quota: agent 'worker'") {
		t.Fatalf("cmdstatus stderr=%q, want the quota friction warning", errOut)
	}
	if friction, err := os.ReadFile(fq.friction); err != nil || !strings.Contains(string(friction), "quota: agent 'worker'") {
		t.Fatalf("cmdstatus left no quota friction line: %q %v", friction, err)
	}

	fp := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "plain screen\n"},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: Connection error.\n"},
	})
	view = fp.inspect(t, "ws", "worker")
	if view.State != "provider-error" || view.TerminalState != "idle" || view.ReportReady {
		t.Fatalf("provider view=%+v, want provider-error over terminal idle", view)
	}
	code, out, _ = fp.runStatus(t, []string{"worker"})
	if code != 14 || renderedState(t, out) != "provider-error" || !strings.Contains(out, `"status":"provider-error"`) {
		t.Fatalf("cmdstatus code=%d out=%q, want the provider-error JSON line with rc 14", code, out)
	}
}

// TestStatusViewNotReceived: the not-received marker with an unchanged
// sequence renders not-received; the probe's state stays the terminal.
func TestStatusViewNotReceived(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	})
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.not-received"), []byte("dispatch-1 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	if view.State != "not-received" || view.TerminalState != "idle" || view.ReportReady {
		t.Fatalf("not-received view=%+v, want not-received over terminal idle", view)
	}
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	code, out, _ := f.runStatus(t, []string{"worker"})
	if code != 15 || out != "worker\tnot-received\t\t-\t-\n" {
		t.Fatalf("cmdstatus code=%d out=%q, want the not-received line with rc 15", code, out)
	}
}

// TestStatusViewCompact: the compact-phase marker outranks everything
// and the inspect leaves the marker's bytes untouched; the command
// renders the same line.
func TestStatusViewCompact(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
		[]fakecli.Rule{{Argv: []string{"agent", "get", "unused"}}})
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("the platform by-pid read needs the host ps")
	}
	started, live := platform.ReadProc(os.Getpid(), f.env)
	if live != platform.ProcRunning || started == "" {
		t.Fatalf("cannot read the test process identity: %q/%v", started, live)
	}
	brief := filepath.Join(f.sd, "briefs", "worker-1.md")
	marker := core.CompactPendingPath(f.sd, "worker")
	raw, err := json.Marshal(map[string]any{
		"version": 1, "token": "probe", "pane": "p1", "brief": brief,
		"pid": os.Getpid(), "started": started, "created_at": "2026-10-04T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	if view.State != "compacting" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("compact view=%+v, want compacting with no probe", view)
	}
	assertNoSessionDiscovery(t, f.callLog(t))
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	code, out, errOut := f.runStatus(t, []string{"worker"})
	want := "worker\tcompacting\t\tbrief was not sent: " + brief + "\t-\t-\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("cmdstatus code=%d out=%q stderr=%q, want %q", code, out, errOut, want)
	}
	if after, err := os.ReadFile(marker); err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("the compact marker was modified: %q %v", after, err)
	}
	wantCalls(t, f.callLog(t))
}

// collabAgentGets builds the two participant probes for one fixture's
// project dir.
func collabAgentGets(cwd string) []fakecli.Rule {
	var rules []fakecli.Rule
	for _, participant := range []struct{ name, pane, kind, session string }{
		{"author", "p-author", "claude", "s-1"},
		{"reviewer", "p-reviewer", "grok", "s-2"},
	} {
		body, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": map[string]any{
			"name": participant.name, "pane_id": participant.pane, "agent": participant.kind,
			"workspace_id": "ws", "cwd": cwd,
			"agent_session": map[string]string{"kind": "pi", "value": participant.session},
		}}})
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", participant.name}, Stdout: string(body)})
	}
	return rules
}

// collabAssignment seeds the valid active collaboration the fixture's
// roster and fake herdr verify: author and reviewer in the fixture's
// own project dir, workspace ws.
func collabAssignment(t *testing.T, f *statusViewFixture) {
	t.Helper()
	cwdResolved, err := filepath.EvalSymlinks(f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collaboration.Store{StateDir: f.sd}.Create(collaboration.Assignment{
		Author: collaboration.Participant{
			Name: "author", Pane: "p-author", Role: "implementer", Kind: "claude",
			Family: "anthropic", Started: "2026-10-04T12:00:00Z", Model: "claude-sonnet-4-5",
			Session: collaboration.Hash([]byte("pi\x00s-1")), Cwd: cwdResolved,
		},
		Reviewer: collaboration.Participant{
			Name: "reviewer", Pane: "p-reviewer", Role: "reviewer", Kind: "grok",
			Family: "xai", Started: "2026-10-04T12:01:00Z", Model: "grok-4",
			Session: collaboration.Hash([]byte("pi\x00s-2")), Cwd: cwdResolved,
		},
		Workspace: "ws",
		Root:      platform.StateProjectRoot(f.env, cwdResolved),
		BriefHash: "0",
		Paths:     []string{cwdResolved},
		MaxRounds: 3,
	})
	if err != nil {
		t.Fatalf("cannot seed the assignment: %v", err)
	}
}

// TestStatusViewCollaboration: an active collaboration renders
// collaborating through the exact resolver, with the explicit session
// supplying the workspace context and no discovery call; a foreign
// session renders collaboration-unavailable instead.
func TestStatusViewCollaboration(t *testing.T) {
	collabRoster := func(cwd string) string {
		return "author\tp-author\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tclaude-sonnet-4-5\t\t\t\n" +
			"reviewer\tp-reviewer\tgrok\treviewer\txai\t1\t" + cwd + "\t2026-10-04T12:01:00Z\tgrok-4\t\t\t\n"
	}
	buildFixture := func(t *testing.T, withPaneCurrent bool) statusViewFixture {
		t.Helper()
		f := newStatusViewFixtureFull(t, collabRoster, func(cwd string) []fakecli.Rule {
			rules := collabAgentGets(cwd)
			if withPaneCurrent {
				rules = append(rules, fakecli.Rule{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"workspace_id":"ws"}}}`})
			}
			return rules
		})
		skill := filepath.Join(f.state, "skill")
		if err := os.MkdirAll(skill, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "config.defaults"), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_SKILL_DIR"] = skill
		f.env["SOHO_WORKER_MESSAGES"] = "policy"
		delete(f.env, "HERDR_WORKSPACE_ID")
		return f
	}

	// No caller Herdr context (no HERDR_WORKSPACE_ID): the explicit
	// session ws supplies the workspace context; the foreign session
	// refuses the verification instead of discovering one.
	f := buildFixture(t, false)
	collabAssignment(t, &f)
	view := f.inspect(t, "ws", "author")
	if view.State != "collaborating" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("collaboration view=%+v, want collaborating with no probe", view)
	}
	wantCalls(t, f.callLog(t),
		"agent\x00get\x00author",
		"agent\x00get\x00reviewer",
	)
	// Parity: the command derives the same context with its one
	// pane current and renders the same state.
	fp := buildFixture(t, true)
	collabAssignment(t, &fp)
	code, out, _ := fp.runStatus(t, []string{"author"})
	if code != 0 || renderedState(t, out) != "collaborating" || !strings.Contains(out, `"status":"collaborating"`) {
		t.Fatalf("cmdstatus code=%d out=%q, want the collaborating line", code, out)
	}
	// The CLI entry resolves the state dir's session and the collaboration
	// derives its workspace context: exactly the two pane currents the
	// command always spent, then the two participant reads.
	wantCalls(t, fp.callLog(t),
		"pane\x00current\x00--current",
		"pane\x00current\x00--current",
		"agent\x00get\x00author",
		"agent\x00get\x00reviewer",
	)

	ff := buildFixture(t, false)
	collabAssignment(t, &ff)
	view = ff.inspect(t, "foreign", "author")
	if view.State != "collaboration-unavailable" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("foreign-session view=%+v, want collaboration-unavailable", view)
	}
	// The workspace mismatch is decided before any participant read.
	wantCalls(t, ff.callLog(t))
}

// TestStatusViewUnavailable: a failed probe renders unavailable with
// the probe's own state word; the command's warning and rc are the
// wrapper's application of the resolver's record.
func TestStatusViewUnavailable(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"server_down","message":"herdr server unreachable"},"id":"cli:agent:get"}`},
	})
	view := f.inspect(t, "ws", "worker")
	if view.State != "unavailable" || view.TerminalState != "unavailable" || view.ReportReady {
		t.Fatalf("unavailable view=%+v, want unavailable over terminal unavailable", view)
	}
	wantCalls(t, f.callLog(t), "agent\x00get\x00worker")
	before := f.fixtureBefore(t)
	code, out, errOut := f.runStatus(t, []string{"worker"})
	if code != 4 || !strings.HasPrefix(out, "worker\tunavailable\t") || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus code=%d out=%q, want the unavailable line with rc 4", code, out)
	}
	if !strings.Contains(errOut, "soho: warning: agent 'worker': herdr agent get failed: ") {
		t.Fatalf("cmdstatus stderr=%q, want the unavailable friction warning", errOut)
	}
	if friction, err := os.ReadFile(f.friction); err != nil || !strings.Contains(string(friction), "agent 'worker': herdr agent get failed") {
		t.Fatalf("cmdstatus left no unavailable friction line: %q %v", friction, err)
	}
	assertTreeUnchanged(t, "cmdstatus", before, f.fixtureBefore(t))
}

// TestStatusViewAbsentStateDir: an explicit state dir that does not
// exist is inspected without being created — the read-only path never
// mkdirs — and the unknown probe renders unknown-agent.
func TestStatusViewAbsentStateDir(t *testing.T) {
	f := newStatusViewFixture(t, "", []fakecli.Rule{{Argv: []string{"agent", "get", "unused"}}})
	absent := filepath.Join(f.state, "missing-ws")
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("the fixture state dir exists: %v", err)
	}
	before := treeSnapshot(t, f.state, f.cwd)
	view, out, errOut := silentInspect(t, AgentStatusRequest{
		StateDir:  absent,
		SessionID: "ws",
		Agent:     "worker",
		Cwd:       f.cwd,
		Config:    f.ctx,
		Env:       f.env,
		Services:  backend.New(f.env),
	})
	if out != "" || errOut != "" {
		t.Fatalf("the read-only inspect printed: stdout=%q stderr=%q", out, errOut)
	}
	if view.State != "unknown-agent" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("absent-state-dir view=%+v, want unknown-agent with no probe state", view)
	}
	wantCalls(t, f.callLog(t), "agent\x00get\x00worker")
	assertNoSessionDiscovery(t, f.callLog(t))
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("the read-only inspect created the state dir: %v", err)
	}
	if friction, err := os.ReadFile(f.friction); err != nil || string(friction) != "configured line\n" {
		t.Fatalf("the read-only inspect wrote the friction file: %q %v", friction, err)
	}
	assertTreeUnchanged(t, "inspect", before, treeSnapshot(t, f.state, f.cwd))
}

// TestStatusViewForeignScratchMirror: a missing scratch report whose
// protected mirror stands is observed only for the explicit session it
// belongs to; the caller's own session (and the command's derivation)
// does not see the foreign mirror, and nothing is written anywhere.
func TestStatusViewForeignScratchMirror(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"},
	})
	// The foreign session's scratch reports dir exists, the report
	// itself is gone; the protected mirror stands under the state dir.
	scratchReports := core.ScratchReportsDir(f.env, "foreign")
	if err := os.MkdirAll(scratchReports, 0o700); err != nil {
		t.Fatal(err)
	}
	missingScratch := filepath.Join(scratchReports, "worker-mirror.md")
	mirror := filepath.Join(f.sd, "reports", "worker-mirror.md")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirror, []byte("mirror\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(missingScratch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(mirror)
	if err != nil {
		t.Fatal(err)
	}
	identity := reportIdentity(mirror, info)
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.report-working-final"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	// The explicit foreign session observes the protected mirror and
	// settles done from it without a probe.
	view := f.inspect(t, "foreign", "worker")
	if view.State != "done" || view.TerminalState != "" || !view.ReportReady {
		t.Fatalf("foreign-session view=%+v, want done from the protected mirror", view)
	}
	wantCalls(t, f.callLog(t))
	// The caller's own session does not see the foreign mirror: the
	// scratch path stays the (missing) report and the worker renders
	// working, exactly as the command with its derived session does.
	same := f.inspect(t, "ws", "worker")
	if same.State != "working" || same.TerminalState != "working" || same.ReportReady {
		t.Fatalf("caller-session view=%+v, want working without the foreign report", same)
	}
	code, out, _ := f.runStatus(t, []string{"worker"})
	if code != 0 || renderedState(t, out) != "working" {
		t.Fatalf("cmdstatus code=%d out=%q, want working (the foreign mirror is invisible)", code, out)
	}
	if _, err := os.Stat(missingScratch); !os.IsNotExist(err) {
		t.Fatalf("the inspect published a scratch report: %v", err)
	}
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
}

// newHcStatusViewFixture builds the transport=hc twin of the fixture: the
// same state dir, friction target and pinned HOME/USERPROFILE/TMPDIR, with
// the fake hc carrying the rules and the fake herdr installed as a
// sentinel (any launch fails the test). callerID is the inherited
// HC_TERMINAL_ID; empty leaves the env without one.
func newHcStatusViewFixture(t *testing.T, rosterRows func(cwd string) string, hcRules func(cwd string) []fakecli.Rule, callerID string) statusViewFixture {
	t.Helper()
	base := t.TempDir()
	cwd := filepath.Join(base, "cwd")
	state := filepath.Join(base, "state")
	bin := filepath.Join(base, "bin")
	home := filepath.Join(base, "home")
	for _, dir := range []string{cwd, filepath.Join(state, "ws", "wait"), bin, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + rosterRows(cwd)
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	// The same configured nonempty friction target the herdr fixture uses:
	// the read-only inspect must stay silent even though it exists.
	friction := filepath.Join(base, "friction.log")
	if err := os.WriteFile(friction, []byte("configured line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFriction := frictionLogFile
	frictionLogFile = friction
	t.Cleanup(func() { frictionLogFile = oldFriction })
	if _, err := fakecli.Install(t, bin, "hc", hcRules(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":                bin,
		"HOME":                home,
		"USERPROFILE":         home,
		"TMPDIR":              base,
		"XDG_CONFIG_HOME":     home,
		"SOHO_DIR":            state,
		"SOHO_FAKECLI_CONFIG": bin,
		"SOHO_TRANSPORT":      "hc",
	}
	if ps, err := exec.LookPath("ps"); err == nil {
		env["PATH"] = bin + string(os.PathListSeparator) + filepath.Dir(ps)
	}
	if callerID != "" {
		env["HC_TERMINAL_ID"] = callerID
	}
	return statusViewFixture{
		cwd:   cwd,
		state: state,
		sd:    filepath.Join(state, "ws"),
		// The snapshot root is the scratch root itself (absent until a
		// write publishes something there): bin is deliberately not a
		// root, the call logs are the call assertions.
		tmp:      filepath.Join(base, "soho"),
		bin:      bin,
		friction: friction,
		ctx:      &core.Config{Entries: map[string]core.ConfigEntry{}},
		env:      env,
	}
}

// hcCollabRows is the collaboration roster on hc addresses t4/t5.
func hcCollabRows(cwd string) string {
	return "author\tt4\tcodex\timplementer\topenai\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tgpt-5.4\t\t\t\n" +
		"reviewer\tt5\tgrok\treviewer\txai\t1\t" + cwd + "\t2026-10-04T12:01:00Z\tgrok-4\t\t\t\n"
}

// hcCollabGets answers the two participant gets by the actual assigned
// names; the record's name, address and cwd match the roster rows.
func hcCollabGets(t *testing.T, cwd string) []fakecli.Rule {
	t.Helper()
	return []fakecli.Rule{
		{Argv: []string{"get", "author", "--json"}, Stdout: `{"address":"t4","name":"author","state":"live","cwd":` + jsonString(t, cwd) + `,"status":{"state":"working"}}`},
		{Argv: []string{"get", "reviewer", "--json"}, Stdout: `{"address":"t5","name":"reviewer","state":"live","cwd":` + jsonString(t, cwd) + `,"status":{"state":"working"}}`},
	}
}

// hcCollabAssignment seeds the valid active collaboration whose
// participants the roster and the fake hc verify: author and reviewer on
// the hc addresses, workspace ws.
func hcCollabAssignment(t *testing.T, f *statusViewFixture) {
	t.Helper()
	cwdResolved, err := filepath.EvalSymlinks(f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collaboration.Store{StateDir: f.sd}.Create(collaboration.Assignment{
		Author: collaboration.Participant{
			Name: "author", Pane: "t4", Role: "implementer", Kind: "codex",
			Family: kinds.AgentFamily("codex", "gpt-5.4"), Started: "2026-10-04T12:00:00Z", Model: "gpt-5.4",
			Session: collaboration.Hash([]byte("hc\x00t4")), Cwd: cwdResolved,
		},
		Reviewer: collaboration.Participant{
			Name: "reviewer", Pane: "t5", Role: "reviewer", Kind: "grok",
			Family: kinds.AgentFamily("grok", "grok-4"), Started: "2026-10-04T12:01:00Z", Model: "grok-4",
			Session: collaboration.Hash([]byte("hc\x00t5")), Cwd: cwdResolved,
		},
		Workspace: "ws",
		Root:      platform.StateProjectRoot(f.env, cwdResolved),
		BriefHash: "0",
		Paths:     []string{cwdResolved},
		MaxRounds: 3,
	})
	if err != nil {
		t.Fatalf("cannot seed the assignment: %v", err)
	}
}

// collabRosterRows is the collaboration roster (p-author/p-reviewer) the
// herdr consumer tests share.
func collabRosterRows(cwd string) string {
	return "author\tp-author\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tclaude-sonnet-4-5\t\t\t\n" +
		"reviewer\tp-reviewer\tgrok\treviewer\txai\t1\t" + cwd + "\t2026-10-04T12:01:00Z\tgrok-4\t\t\t\n"
}

// prepareCollabPolicy gives the fixture the off-policy-free worker_messages
// mode readCollaboration needs: the skill default file and the policy env.
func prepareCollabPolicy(t *testing.T, f *statusViewFixture) {
	t.Helper()
	skill := filepath.Join(f.state, "skill")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "config.defaults"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_SKILL_DIR"] = skill
	f.env["SOHO_WORKER_MESSAGES"] = "policy"
}

// prepareHcPolicy is the policy preparation of the hc fixture (the same
// files and env; the hc path reads no herdr at all).
func prepareHcPolicy(t *testing.T, f *statusViewFixture) {
	prepareCollabPolicy(t, f)
}

// jsonString quotes s as a JSON string: a temp path holds no escapes on
// the platforms the gates run, but the marshal keeps it exact.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestStatusViewConflictingCallerCollaboration: the caller's env carries
// an inherited HERDR_WORKSPACE_ID that conflicts with the known session.
// The read-only inspect uses the request's session for the workspace
// context and renders collaborating; the explicit env is a clone, so the
// caller's env keeps its conflicting value.
func TestStatusViewConflictingCallerCollaboration(t *testing.T) {
	f := newStatusViewFixtureFull(t, collabRosterRows, func(cwd string) []fakecli.Rule { return collabAgentGets(cwd) })
	f.env["HERDR_WORKSPACE_ID"] = "wrong"
	collabAssignment(t, &f)
	prepareCollabPolicy(t, &f)
	envBefore := copyEnv(f.env)
	view := f.inspect(t, "ws", "author")
	if view.State != "collaborating" || view.TerminalState != "" || view.ReportReady {
		t.Fatalf("collaboration view=%+v, want collaborating despite the conflicting caller", view)
	}
	wantCalls(t, f.callLog(t),
		"agent\x00get\x00author",
		"agent\x00get\x00reviewer",
	)
	assertNoSessionDiscovery(t, f.callLog(t))
	if f.env["HERDR_WORKSPACE_ID"] != "wrong" {
		t.Fatalf("the inspect mutated the caller's env: HERDR_WORKSPACE_ID=%q", f.env["HERDR_WORKSPACE_ID"])
	}
	assertEnvUnmutated(t, envBefore, f.env)
}

// TestStatusViewHcCollaboration: under transport=hc the read-only inspect
// verifies the participants by their actual assigned names and trusts
// nothing about the caller — with no HC_TERMINAL_ID at all and with a
// stale foreign one — while the foreign explicit session still refuses
// before any participant read.
func TestStatusViewHcCollaboration(t *testing.T) {
	staleCaller := `{"address":"t9","state":"live","labels":{"soho.session":"sT9"}}`
	build := func(t *testing.T, callerID string) statusViewFixture {
		t.Helper()
		rules := func(cwd string) []fakecli.Rule {
			all := hcCollabGets(t, cwd)
			if callerID != "" {
				all = append(all,
					fakecli.Rule{Argv: []string{"get", "t9", "--json"}, Stdout: staleCaller},
					fakecli.Rule{Argv: []string{"ls", "--label", "soho.session=sT9", "--json"}, Stdout: `{"terminals":[]}`},
				)
			}
			return all
		}
		f := newHcStatusViewFixture(t, func(cwd string) string { return hcCollabRows(cwd) }, rules, callerID)
		prepareHcPolicy(t, &f)
		hcCollabAssignment(t, &f)
		return f
	}
	for name, callerID := range map[string]string{"no caller terminal": "", "stale foreign caller": "t9"} {
		t.Run(name, func(t *testing.T) {
			f := build(t, callerID)
			before := f.fixtureBefore(t)
			view := f.inspect(t, "ws", "author")
			if view.State != "collaborating" || view.TerminalState != "" || view.ReportReady {
				t.Fatalf("hc collaboration view=%+v, want collaborating", view)
			}
			// The participant gets by their actual names, and nothing else:
			// no caller get, no session-scoped ls, no herdr at all.
			wantCalls(t, f.hcCallLog(t),
				"get\x00author\x00--json",
				"get\x00reviewer\x00--json",
			)
			f.assertNoHerdrCall(t)
			assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
		})
	}
	// The foreign explicit session refuses on the known workspace before
	// any participant read, on both caller configurations.
	for name, callerID := range map[string]string{"no caller terminal": "", "stale foreign caller": "t9"} {
		t.Run(name+" foreign mismatch", func(t *testing.T) {
			f := build(t, callerID)
			view := f.inspect(t, "foreign", "author")
			if view.State != "collaboration-unavailable" || view.TerminalState != "" || view.ReportReady {
				t.Fatalf("foreign-session view=%+v, want collaboration-unavailable", view)
			}
			wantCalls(t, f.hcCallLog(t))
			f.assertNoHerdrCall(t)
		})
	}
}

// TestStatusViewHcStalePaneUsesUnfilteredList: the inspect's stale-name
// comparison under hc lists the unfiltered local terminals once — no
// caller session resolution (no get of the inherited HC_TERMINAL_ID, no
// label-scoped ls) and no herdr — and the working probe keeps its state.
func TestStatusViewHcStalePaneUsesUnfilteredList(t *testing.T) {
	staleCaller := `{"address":"t9","state":"live","labels":{"soho.session":"sT9"}}`
	f := newHcStatusViewFixture(t,
		func(cwd string) string {
			return "worker\tt7\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t2026-10-04T12:00:00Z\tmodel-x\t\t\t\n"
		},
		func(cwd string) []fakecli.Rule {
			return []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"t7","name":"worker","state":"live","cwd":` + jsonString(t, cwd) + `,"status":{"state":"working"}}`},
				{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`},
				{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: `{"text":"Busy on the task 30%\n"}`},
				// The stale caller and the label-scoped listing would be
				// recorded if the caller-scoped path ever ran.
				{Argv: []string{"get", "t9", "--json"}, Stdout: staleCaller},
				{Argv: []string{"ls", "--label", "soho.session=sT9", "--json"}, Stdout: `{"terminals":[]}`},
			}
		},
		"t9",
	)
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	if view.State != "working" || view.TerminalState != "working" || view.ReportReady {
		t.Fatalf("hc stale-pane view=%+v, want working over terminal working", view)
	}
	// One unfiltered ls, the probe get and the activity read — in order,
	// nothing else.
	wantCalls(t, f.hcCallLog(t),
		"get\x00worker\x00--json",
		"ls\x00--json",
		"read\x00worker\x00--lines\x000\x00--json",
	)
	f.assertNoHerdrCall(t)
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
}

// TestStatusViewWorkingDraftReportReady: a nonempty current report that is
// still a working draft keeps ReportReady true while the effective state
// stays working, and the inspect (like the command) never mirrors or
// writes anything for the draft.
func TestStatusViewWorkingDraftReportReady(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"},
	})
	report := filepath.Join(f.sd, "reports", "worker-draft.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("draft in progress\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	// F3: the draft branch performs the report probe and the view reports
	// its actual state word; the report is observed and ready.
	if view.State != "working" || view.TerminalState != "working" || !view.ReportReady {
		t.Fatalf("draft view=%+v, want working over terminal working with the report ready", view)
	}
	assertTreeUnchanged(t, "inspect", before, f.fixtureBefore(t))
	// Parity: the command renders the same state for the draft and, like
	// the inspect, writes nothing (no mirror for a draft).
	before2 := f.fixtureBefore(t)
	code, out, _ := f.runStatus(t, []string{"worker"})
	if code != 0 || !strings.HasPrefix(out, "worker\tworking\t"+report+"\t") || renderedState(t, out) != view.State {
		t.Fatalf("cmdstatus code=%d out=%q, want the working draft line", code, out)
	}
	if !strings.Contains(out, "report draft") {
		t.Fatalf("cmdstatus out=%q, want the draft cause", out)
	}
	assertTreeUnchanged(t, "cmdstatus", before2, f.fixtureBefore(t))
}

// F1 (review-2): the inspection's unfiltered listing is nonprinting on both
// transports, and when it fails the stale-pane identity is unverifiable, so
// the view keeps the existing unavailable inspection outcome instead of
// claiming a verified working identity — silently, without the
// implementation capturing or swapping global writers.
func TestStatusViewFailedListSilent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "control"
		rule := fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}
		if fail {
			name = "failure"
			rule.Stdout = "list-body"
			rule.Stderr = "list-error"
			rule.Code = 4
		}
		t.Run(name, func(t *testing.T) {
			f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
				rule,
				{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "busy"},
			})
			view, out, errOut := silentInspect(t, AgentStatusRequest{
				StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
				Config: f.ctx, Env: f.env, Services: backend.New(f.env),
			})
			if fail {
				// The get probe still read working; the failed listing only
				// makes the pane identity unverifiable.
				if view.State != "unavailable" || view.TerminalState != "working" || view.ReportReady {
					t.Fatalf("failure view=%+v, want unavailable over terminal working", view)
				}
			} else {
				if view.State != "working" || view.TerminalState != "working" || view.ReportReady {
					t.Fatalf("positive get state=%+v", view)
				}
			}
			if out != "" || errOut != "" {
				t.Fatalf("readonly leaked stdout=%q stderr=%q", out, errOut)
			}
		})
	}
}

// F3 (review-2): the report branch performs the report probe, and the view
// reports the probe's actual state word (working here); the early final
// marker branch stays unprobed and empty (TestStatusViewReportDone).
func TestStatusViewReportProbeState(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "busy"},
	})
	report := filepath.Join(f.sd, "report.md")
	if err := os.WriteFile(report, []byte("draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	view := f.inspect(t, "ws", "worker")
	wantCalls(t, f.callLog(t),
		"agent\x00get\x00worker",
		"agent\x00read\x00worker\x00--source\x00visible",
	)
	if view.State != "working" || !view.ReportReady {
		t.Fatalf("positive control=%+v", view)
	}
	if view.TerminalState != "working" {
		t.Fatalf("report branch performed agent get but view=%+v, want TerminalState=working", view)
	}
}

// F2 (review-2): a current draft with a custom-role sidecar must not force
// skill materialization or a Die through SkillDir while inspecting the
// report: the read-only role lookup keeps report authority (done) on
// unavailable metadata, and the fixture tree stays unchanged — with and
// without SOHO_NOWRITE.
func TestStatusViewReportRoleReadonly(t *testing.T) {
	for _, c := range []struct {
		name, role string
		nowrite    bool
	}{
		{"control", "implementer", false},
		{"cold-cache-custom-role", "custom-review", false},
		{"nowrite-missing-cache", "custom-review", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
				[]fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
			report := filepath.Join(f.sd, "report.md")
			if err := os.WriteFile(report, []byte("draft\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.nowrite {
				f.env["SOHO_NOWRITE"] = "1"
			}
			f.env["XDG_CACHE_HOME"] = filepath.Join(f.env["HOME"], "cache")
			raw, err := json.Marshal(map[string]any{"git_tree": f.cwd, "git_head": "0123456789012345678901234567890123456789", "role": c.role})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.sd, "report.dispatch.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			before := f.fixtureBefore(t)
			var out, errOut bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			platform.Stdout, platform.Stderr = &out, &errOut
			var view AgentStatusView
			var pan any
			func() {
				defer func() { pan = recover() }()
				view = InspectAgentStatus(AgentStatusRequest{
					StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
					Config: f.ctx, Env: f.env, Services: backend.New(f.env),
				})
			}()
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if pan != nil {
				t.Fatalf("readonly inspection panicked=%#v stdout=%q stderr=%q", pan, out.String(), errOut.String())
			}
			if view.State != "done" || !view.ReportReady {
				t.Fatalf("report control=%+v", view)
			}
			assertTreeUnchanged(t, "readonly report custom-role inspection", before, f.fixtureBefore(t))
		})
	}
}

// F4 (review-2) regular control: a corrupted regular compact marker is a
// silent compact-unknown on the inspection path, with no transport calls
// and the marker untouched.
func TestStatusViewCompactRegularControl(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
		[]fakecli.Rule{{AnyArgs: true}})
	p := core.CompactPendingPath(f.sd, "worker")
	if err := os.WriteFile(p, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.fixtureBefore(t)
	view := f.inspect(t, "ws", "worker")
	if view.State != "compact-unknown" {
		t.Fatalf("control=%+v", view)
	}
	wantCalls(t, f.callLog(t))
	assertTreeUnchanged(t, "invalid regular compact marker", before, f.fixtureBefore(t))
}
