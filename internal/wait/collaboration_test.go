package wait

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestAnySummaryBeforeActionableCollaboration(t *testing.T) {
	for _, observe := range []bool{false, true} {
		t.Run(strconv.FormatBool(observe), func(t *testing.T) {
			f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.AwaitingOrchestrator, "queued")
			file, err := os.OpenFile(filepath.Join(f.sd, "agents.tsv"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("complete\tp0c\tclaude\timplementer\n"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			f.writeStaleReport(t, "complete")
			report := filepath.Join(f.sd, "reports", "stale-complete.md")
			info, err := os.Stat(report)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(f.sd, "wait"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.sd, "wait", "complete.report-settled"), []byte(reportIdentity(report, info)), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"complete", "author", "--any", "--timeout", "5000"}
			if observe {
				args = append(args, "--observe")
			}
			code, out, stderr := runWaitCommand(t, f, func() int { return CmdWait(args, f.ctx, f.env, f.cwd) })
			// SOH-485: both agents reached a final state in this pass — the
			// done report and the actionable collaboration — so nothing is
			// left pending to name in an unwatched line. The phase still ends
			// the wait with 10.
			if code != 10 || !strings.Contains(out, `"status":"done"`) || !strings.Contains(out, `awaiting-orchestrator`) || strings.Contains(out, `"unwatched"`) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

// collabFixture is the cooperative-completion fixture: a project dir, a
// workspace state dir (roster + wait dir), two roster participants whose
// registration verifies against a fake herdr, and an optional active
// collaboration assignment plus a stale report from the previous task.
// runStatus/runWait exercise the real CmdStatus/CmdWait entry points.
type collabFixture struct {
	cwd   string
	state string
	sd    string
	ctx   *core.Config
	env   platform.Env
	a     collaboration.Assignment // the valid active assignment, if any
}

type agentGetResponse struct {
	Stdout string
	Stderr string
	Code   int
}

type collabFixtureOptions struct {
	// authorGet/reviewerGet override the fake `agent get` answers; the
	// defaults verify both participants cleanly.
	authorGet, reviewerGet agentGetResponse
	// workspaceID is put in the env when non-empty; empty exercises the
	// once-at-entry derivation through `pane current --current`.
	workspaceID string
	// policyMode is the SOHO_WORKER_MESSAGES value; empty omits it
	// (mode off by default).
	policyMode string
	extraRules []fakecli.Rule
}

const (
	collabAuthorPane   = "p-author"
	collabReviewerPane = "p-reviewer"
	collabStarted      = "2026-10-04T12:00:00Z"
)

func defaultAgentGetResponse(name, pane, kind, sessionValue, cwd string) agentGetResponse {
	encoded, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": map[string]any{
		"name": name, "pane_id": pane, "agent": kind, "workspace_id": "ws", "cwd": cwd,
		"agent_session": map[string]string{"kind": "pi", "value": sessionValue},
	}}})
	return agentGetResponse{Stdout: string(encoded)}
}

func newCollabFixture(t *testing.T, options collabFixtureOptions) collabFixture {
	t.Helper()
	base := t.TempDir()
	cwd := filepath.Join(base, "cwd")
	state := filepath.Join(base, "state")
	bin := filepath.Join(base, "bin")
	skill := filepath.Join(base, "skill")
	for _, dir := range []string{cwd, filepath.Join(state, "ws", "wait"), bin, skill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The defaults layer of the config resolution: an empty file keeps the
	// mode coming from the SOHO_WORKER_MESSAGES env var alone.
	if err := os.WriteFile(filepath.Join(skill, "config.defaults"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
		"author\tp-author\tclaude\timplementer\tanthropic\t1\t" + cwd + "\t" + collabStarted + "\tclaude-sonnet-4-5\t\t\t\n" +
		"reviewer\tp-reviewer\tgrok\treviewer\txai\t1\t" + cwd + "\t2026-10-04T12:00:05Z\tgrok-4\t\t\t\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	authorGet := options.authorGet
	if authorGet.Stdout == "" && authorGet.Stderr == "" {
		authorGet = defaultAgentGetResponse("author", collabAuthorPane, "claude", "s-1", cwd)
	}
	reviewerGet := options.reviewerGet
	if reviewerGet.Stdout == "" && reviewerGet.Stderr == "" {
		reviewerGet = defaultAgentGetResponse("reviewer", collabReviewerPane, "grok", "s-2", cwd)
	}
	// A __CWD__ placeholder in an overridden answer resolves to the fixture's
	// project dir, so variants keep a real, evaluable cwd.
	cwdJSON, _ := json.Marshal(cwd)
	authorGet.Stdout = strings.ReplaceAll(authorGet.Stdout, `"__CWD__"`, string(cwdJSON))
	reviewerGet.Stdout = strings.ReplaceAll(reviewerGet.Stdout, `"__CWD__"`, string(cwdJSON))
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "author"}, Stdout: authorGet.Stdout, Stderr: authorGet.Stderr, Code: authorGet.Code},
		{Argv: []string{"agent", "get", "reviewer"}, Stdout: reviewerGet.Stdout, Stderr: reviewerGet.Stderr, Code: reviewerGet.Code},
		{Argv: []string{"agent", "read", "author", "--source", "visible"}, Stdout: "screen line\n"},
		{Argv: []string{"agent", "read", "reviewer", "--source", "visible"}, Stdout: "screen line\n"},
	}
	rules = append(rules, options.extraRules...)
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":                bin,
		"HERDR_ENV":           "1",
		"SOHO_DIR":            state,
		"SOHO_SKILL_DIR":      skill,
		"HERDR_SOCKET_PATH":   filepath.Join(base, "none.sock"),
		"SOHO_FAKECLI_CONFIG": bin,
		"SOHO_WAIT_POLL_MS":   "5",
	}
	if options.workspaceID != "" {
		env["HERDR_WORKSPACE_ID"] = options.workspaceID
	}
	if options.policyMode != "" {
		env["SOHO_WORKER_MESSAGES"] = options.policyMode
	}
	f := collabFixture{
		cwd:   cwd,
		state: state,
		sd:    filepath.Join(state, "ws"),
		ctx:   &core.Config{Entries: map[string]core.ConfigEntry{}},
		env:   env,
	}
	return f
}

// writeStaleReport records a complete report from the previous task so the
// traditional logic would declare done from it.
func (f *collabFixture) writeStaleReport(t *testing.T, agent string) string {
	t.Helper()
	report := filepath.Join(f.sd, "reports", "stale-"+agent+".md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("stale report from the previous task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-"+agent), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return report
}

// seedAssignment registers a valid active collaboration (preparing) whose
// participants verify against the fixture roster and the fake herdr.
func (f *collabFixture) seedAssignment(t *testing.T) collaboration.Assignment {
	t.Helper()
	cwdResolved, err := filepath.EvalSymlinks(f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	store := collaboration.Store{StateDir: f.sd}
	created, err := store.Create(collaboration.Assignment{
		Author: collaboration.Participant{
			Name: "author", Pane: collabAuthorPane, Role: "implementer", Kind: "claude",
			Family: "anthropic", Started: collabStarted, Model: "claude-sonnet-4-5",
			Session: collaboration.Hash([]byte("pi\x00s-1")), Cwd: cwdResolved,
		},
		Reviewer: collaboration.Participant{
			Name: "reviewer", Pane: collabReviewerPane, Role: "reviewer", Kind: "grok",
			Family: "xai", Started: "2026-10-04T12:00:05Z", Model: "grok-4",
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
	f.a = created
	return created
}

// setPhase moves the seeded assignment to phase; delivery, when non-empty,
// is appended as the last event.
func (f *collabFixture) setPhase(t *testing.T, phase, delivery string) {
	t.Helper()
	if f.a.ID == "" {
		t.Fatal("setPhase before seedAssignment")
	}
	store := collaboration.Store{StateDir: f.sd}
	a, err := store.Update(f.a.ID, f.a.Generation, func(a *collaboration.Assignment) error {
		a.Phase = phase
		a.Round = 1
		a.Revision = "rev-1"
		if delivery != "" {
			a.Events = append(a.Events, collaboration.Event{ID: "e-1", Type: "review-ready", Actor: "author", Revision: "rev-1", Round: 1, Delivery: delivery})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot move the assignment to %s: %v", phase, err)
	}
	f.a = a
}

func (f *collabFixture) runStatus(t *testing.T, agents []string) (int, string, string) {
	t.Helper()
	return runWaitCommand(t, *f, func() int { return CmdStatus(agents, f.ctx, f.env, f.cwd) })
}

func (f *collabFixture) runWait(t *testing.T, agents []string, timeoutMs int) (int, string, string) {
	t.Helper()
	argv := append([]string{}, agents...)
	argv = append(argv, "--timeout", strconv.Itoa(timeoutMs))
	return runWaitCommand(t, *f, func() int { return CmdWait(argv, f.ctx, f.env, f.cwd) })
}

func runWaitCommand(t *testing.T, f collabFixture, run func() int) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	out, errOut := &strings.Builder{}, &strings.Builder{}
	platform.Stdout, platform.Stderr = out, errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := run()
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

// herdrCalls returns every fake herdr call in order; a missing log means
// no herdr call happened at all (e.g. the registration failed before the
// identity check ran).
func (f collabFixture) herdrCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.env["PATH"], "herdr.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return []fakecli.Call{}
		}
		t.Fatal(err)
	}
	return calls
}

// assertNoKeysSent proves (by effect, not call counts of reads) that no
// prompt or key was ever sent while the collaboration was active.
func assertNoKeysSent(t *testing.T, f collabFixture) {
	t.Helper()
	for _, c := range f.herdrCalls(t) {
		argv := c.Argv
		sent := len(argv) >= 2 &&
			((argv[0] == "agent" && (argv[1] == "prompt" || argv[1] == "send-keys")) ||
				(argv[0] == "pane" && argv[1] == "send-keys"))
		if sent {
			t.Fatalf("a prompt or key was sent during an active collaboration: %v", argv)
		}
	}
}

func collabCallCount(calls []fakecli.Call, argv []string) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) == len(argv) {
			eq := true
			for i := range argv {
				if c.Argv[i] != argv[i] {
					eq = false
					break
				}
			}
			if eq {
				n++
			}
		}
	}
	return n
}

// parseCollabJSON returns the JSON object line for agent from out.
func parseCollabJSON(t *testing.T, out, agent string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["agent"] == agent {
			return m
		}
	}
	t.Fatalf("no JSON line for %q in stdout %q", agent, out)
	return nil
}

// assertNotDone proves the stale report was not turned into a done state.
func assertNotDone(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, `"status":"done"`) || strings.Contains(out, "\tdone\t") {
		t.Fatalf("the stale report was declared done: %q", out)
	}
}

// activeStatusFixture seeds a collaboration in phase with a stale report for
// author and returns the fixture plus the report path.
func activeStatusFixture(t *testing.T, options collabFixtureOptions, phase, delivery string) (collabFixture, string) {
	f := newCollabFixture(t, options)
	report := f.writeStaleReport(t, "author")
	f.seedAssignment(t)
	if phase != "" {
		f.setPhase(t, phase, delivery)
	}
	return f, report
}

// TestStatusActiveCollaborationPresentsPhase: with a complete stale report
// from the previous task, status never declares done while the assignment is
// active; it presents the assignment, phase, round, revision and delivery,
// and keeps rc 0. The compact-phase marker keeps its higher precedence.
func TestStatusActiveCollaborationPresentsPhase(t *testing.T) {
	for _, tc := range []struct {
		name, phase, delivery string
	}{
		{name: "reviewing with a received delivery", phase: collaboration.Reviewing, delivery: "received"},
		{name: "fixing without events", phase: collaboration.Fixing, delivery: ""},
		{name: "awaiting-orchestrator with a queued delivery", phase: collaboration.AwaitingOrchestrator, delivery: "queued"},
		{name: "preparing (no phase move)", phase: "", delivery: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, report := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, tc.phase, tc.delivery)
			code, out, errOut := f.runStatus(t, []string{"author"})
			if code != 0 {
				t.Fatalf("code=%d out=%q stderr=%q; an active collaboration is a healthy in-progress state", code, out, errOut)
			}
			line := parseCollabJSON(t, out, "author")
			if line["status"] != "collaborating" {
				t.Fatalf("status=%v, want collaborating: %q", line["status"], out)
			}
			if line["assignment"] != f.a.ID {
				t.Fatalf("assignment=%v, want %s", line["assignment"], f.a.ID)
			}
			if line["phase"] != f.a.Phase {
				t.Fatalf("phase=%v, want %s", line["phase"], f.a.Phase)
			}
			if line["round"] != float64(f.a.Round) || line["revision"] != f.a.Revision {
				t.Fatalf("round/revision=%v/%v, want %d/%s", line["round"], line["revision"], f.a.Round, f.a.Revision)
			}
			wantDelivery := "none"
			if tc.delivery != "" {
				wantDelivery = tc.delivery
			}
			if line["delivery"] != wantDelivery {
				t.Fatalf("delivery=%v, want %s", line["delivery"], wantDelivery)
			}
			if line["report"] != report {
				t.Fatalf("report=%v, want the stale report carried as an artifact: %v", line["report"], report)
			}
			assertNotDone(t, out)
		})
	}
}

// TestStatusActiveCollaborationWithoutArguments covers the roster default:
// every participant's line is the collaboration JSON.
func TestStatusActiveCollaborationWithoutArguments(t *testing.T) {
	f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Reviewing, "received")
	code, out, errOut := f.runStatus(t, nil)
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	for _, agent := range []string{"author", "reviewer"} {
		line := parseCollabJSON(t, out, agent)
		if line["status"] != "collaborating" || line["phase"] != collaboration.Reviewing || line["assignment"] != f.a.ID {
			t.Fatalf("%s line=%v, want the active collaboration", agent, line)
		}
	}
	assertNotDone(t, out)
}

// TestWaitActiveCollaborationPendingUntilTimeout: preparing/reviewing/fixing
// stay pending under the existing timeout — the phase is announced once per
// change (not per poll), the stale report never becomes done, and the wait
// ends with the standard timeout code 9. No prompt or key is sent.
func TestWaitActiveCollaborationPendingUntilTimeout(t *testing.T) {
	f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Reviewing, "received")
	code, out, errOut := f.runWait(t, []string{"author"}, 100)
	if code != 9 {
		t.Fatalf("code=%d out=%q stderr=%q; want the existing timeout code 9", code, out, errOut)
	}
	if n := strings.Count(out, `"status":"collaborating"`); n != 1 {
		t.Fatalf("collaborating lines=%d, want the phase announced once: %q", n, out)
	}
	line := parseCollabJSON(t, out, "author")
	if line["phase"] != collaboration.Reviewing || line["delivery"] != "received" {
		t.Fatalf("line=%v, want the active collaboration details", line)
	}
	assertNotDone(t, out)
	if !strings.Contains(out, `"status":"timeout"`) || !strings.Contains(out, `"state":"collaborating"`) {
		t.Fatalf("timeout line must name the collaborating state: %q", out)
	}
	assertNoKeysSent(t, f)
}

// TestWaitAwaitingOrchestratorReturns10 and the escalated twin: actionable
// states make the wait print the collaboration JSON and return 10 at once —
// not 0/done, not the timeout.
func TestWaitCollaborationAwaitingOrchestratorReturns10(t *testing.T) {
	f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.AwaitingOrchestrator, "queued")
	code, out, errOut := f.runWait(t, []string{"author"}, 5000)
	if code != 10 {
		t.Fatalf("code=%d out=%q stderr=%q; want 10 for the actionable state", code, out, errOut)
	}
	line := parseCollabJSON(t, out, "author")
	if line["status"] != "collaborating" || line["phase"] != collaboration.AwaitingOrchestrator || line["delivery"] != "queued" {
		t.Fatalf("line=%v, want the actionable collaboration state", line)
	}
	if strings.Contains(out, `"status":"timeout"`) {
		t.Fatalf("the actionable state must not wait for the timeout: %q", out)
	}
	assertNotDone(t, out)
	assertNoKeysSent(t, f)
}

func TestWaitCollaborationEscalatedReturns10(t *testing.T) {
	f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Escalated, "refused")
	code, out, errOut := f.runWait(t, []string{"reviewer"}, 5000)
	if code != 10 {
		t.Fatalf("code=%d out=%q stderr=%q; want 10 for the escalated state", code, out, errOut)
	}
	line := parseCollabJSON(t, out, "reviewer")
	if line["status"] != "collaborating" || line["phase"] != collaboration.Escalated {
		t.Fatalf("line=%v, want the escalated collaboration state", line)
	}
	assertNotDone(t, out)
	assertNoKeysSent(t, f)
}

// assertUnavailable proves both entry points report the unreadable
// registration as collaboration-unavailable with code 4 and never fall
// through to the stale report's done.
func assertUnavailable(t *testing.T, f collabFixture, cause string) {
	t.Helper()
	code, out, errOut := f.runStatus(t, []string{"author"})
	if code != 4 {
		t.Fatalf("status code=%d out=%q stderr=%q; want 4", code, out, errOut)
	}
	line := parseCollabJSON(t, out, "author")
	if line["status"] != "collaboration-unavailable" {
		t.Fatalf("status=%v, want collaboration-unavailable: %v", line["status"], line)
	}
	if !strings.Contains(line["cause"].(string), cause) {
		t.Fatalf("cause=%v, want it to name %q", line["cause"], cause)
	}
	assertNotDone(t, out)

	code, out, errOut = f.runWait(t, []string{"author"}, 100)
	if code != 4 {
		t.Fatalf("wait code=%d out=%q stderr=%q; want 4", code, out, errOut)
	}
	line = parseCollabJSON(t, out, "author")
	if line["status"] != "collaboration-unavailable" {
		t.Fatalf("wait status=%v, want collaboration-unavailable: %v", line["status"], line)
	}
	if strings.Contains(out, `"status":"timeout"`) {
		t.Fatalf("an unreadable registration must be terminal, not a timeout: %q", out)
	}
	assertNotDone(t, out)
	assertNoKeysSent(t, f)
}

// TestStatusAndWaitUnreadableRegistration: a registration that cannot be
// read (a corrupt record, or an id that no longer matches its record) is a
// visible collaboration-unavailable (code 4) in status and a terminal
// collaboration-unavailable in wait — never the stale report's done.
func TestStatusAndWaitCollaborationUnreadableRegistration(t *testing.T) {
	t.Run("corrupt record", func(t *testing.T) {
		f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Reviewing, "received")
		path := filepath.Join(f.sd, "collaboration", f.a.ID, "assignment.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append([]byte("{"), raw...), 0o600); err != nil {
			t.Fatal(err)
		}
		assertUnavailable(t, f, "unreadable collaboration")
	})
	t.Run("id mismatch", func(t *testing.T) {
		f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Reviewing, "received")
		path := filepath.Join(f.sd, "collaboration", f.a.ID, "assignment.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		newID := "c-ffffffffffffffff"
		if f.a.ID == newID {
			newID = "c-0000000000000000"
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(raw), f.a.ID, newID, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		assertUnavailable(t, f, "collaboration ID does not match its record")
	})
}

// TestStatusAndWaitPolicyOffOrUnreadable: with an active assignment, an off
// or unreadable worker_messages policy is a visible collaboration-disabled
// (code 4) — no completion is declared.
func TestStatusAndWaitCollaborationPolicyOffOrUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name, mode, cause string
	}{
		{name: "policy off", mode: "off", cause: "worker_messages is off"},
		{name: "unreadable mode", mode: "sometimes", cause: "invalid mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: tc.mode}, collaboration.Reviewing, "received")
			code, out, errOut := f.runStatus(t, []string{"author"})
			if code != 4 {
				t.Fatalf("status code=%d out=%q stderr=%q; want 4", code, out, errOut)
			}
			line := parseCollabJSON(t, out, "author")
			if line["status"] != "collaboration-disabled" || !strings.Contains(line["cause"].(string), tc.cause) {
				t.Fatalf("line=%v, want collaboration-disabled naming %q", line, tc.cause)
			}
			assertNotDone(t, out)

			code, out, errOut = f.runWait(t, []string{"author"}, 100)
			if code != 4 {
				t.Fatalf("wait code=%d out=%q stderr=%q; want 4", code, out, errOut)
			}
			line = parseCollabJSON(t, out, "author")
			if line["status"] != "collaboration-disabled" {
				t.Fatalf("wait status=%v, want collaboration-disabled: %v", line["status"], line)
			}
			assertNotDone(t, out)
		})
	}
}

// TestStatusAndWaitReusedPaneOrSession: a pane whose live agent no longer
// matches the registration (closed or reused pane) and a session that was
// replaced both fail the identity check: collaboration-unavailable (code 4),
// never done from the stale report.
func TestStatusAndWaitCollaborationReusedPaneOrSession(t *testing.T) {
	gone := agentGetResponse{Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"agent target author not found"},"id":"cli:agent:get"`}
	for _, tc := range []struct {
		name  string
		opts  collabFixtureOptions
		cause string
	}{
		{
			name:  "pane closed or reused",
			opts:  collabFixtureOptions{workspaceID: "ws", policyMode: "policy", authorGet: gone},
			cause: "cannot verify live collaboration participant author",
		},
		{
			name: "session replaced",
			opts: collabFixtureOptions{
				workspaceID: "ws",
				policyMode:  "policy",
				authorGet:   defaultAgentGetResponse("author", collabAuthorPane, "claude", "s-1-other", "__CWD__"),
			},
			cause: "collaboration participant identity changed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := activeStatusFixture(t, tc.opts, collaboration.Reviewing, "received")
			assertUnavailable(t, f, tc.cause)
		})
	}
}

// TestFinalizedCollaborationFallsBackToTraditional: once finalize ends the
// assignment, ActiveFor finds nothing and the traditional contract stands —
// the complete report is done in status and wait returns 0. A roster agent
// without any collaboration keeps the traditional state too.
func TestFinalizedCollaborationFallsBackToTraditional(t *testing.T) {
	f, report := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Finished, "received")
	code, out, errOut := f.runStatus(t, []string{"author"})
	if code != 0 {
		t.Fatalf("status code=%d out=%q stderr=%q; a finished assignment is transparent", code, out, errOut)
	}
	if !strings.HasPrefix(out, "author\tdone\t"+report) {
		t.Fatalf("out=%q, want the traditional done line", out)
	}

	code, out, errOut = f.runWait(t, []string{"author"}, 500)
	if code != 0 {
		t.Fatalf("wait code=%d out=%q stderr=%q; want the traditional done rc 0", code, out, errOut)
	}
	line := parseCollabJSON(t, out, "author")
	if line["status"] != "done" || line["report"] != report {
		t.Fatalf("line=%v, want the traditional done JSON", line)
	}

	t.Run("without any collaboration", func(t *testing.T) {
		f := newCollabFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"})
		report := f.writeStaleReport(t, "author")
		code, out, errOut := f.runStatus(t, []string{"author"})
		if code != 0 || !strings.HasPrefix(out, "author\tdone\t"+report) {
			t.Fatalf("code=%d out=%q stderr=%q; without collaboration the report stays done", code, out, errOut)
		}
	})
}

// TestNowriteStatusActiveCollaboration: under SOHO_NOWRITE=1 the
// active-collaboration read prints the line and changes nothing in the
// project or the state tree (paths, sizes and mtimes).
func TestNowriteStatusActiveCollaboration(t *testing.T) {
	f, _ := activeStatusFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"}, collaboration.Reviewing, "received")
	f.env["SOHO_NOWRITE"] = "1"

	type entry struct {
		size int64
		mod  time.Time
	}
	snapshot := func() map[string]entry {
		snap := map[string]entry{}
		for _, root := range []string{f.cwd, f.state} {
			err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				snap[path] = entry{size: info.Size(), mod: info.ModTime()}
				return nil
			})
			if err != nil {
				t.Fatalf("NOWRITE snapshot of %s: %v", root, err)
			}
		}
		return snap
	}
	before := snapshot()
	code, out, errOut := f.runStatus(t, []string{"author"})
	after := snapshot()
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	line := parseCollabJSON(t, out, "author")
	if line["status"] != "collaborating" || line["phase"] != collaboration.Reviewing {
		t.Fatalf("line=%v, want the active collaboration", line)
	}
	if len(before) != len(after) {
		t.Fatalf("NOWRITE changed the entry count: before=%d after=%d", len(before), len(after))
	}
	for path, e := range after {
		prev, ok := before[path]
		if !ok || prev != e {
			t.Fatalf("NOWRITE changed %s: before=%v after=%v", path, before[path], e)
		}
	}
}

// TestStatusDerivesWorkspaceOnce: when the caller's env does not carry
// HERDR_WORKSPACE_ID, the id is derived once at the entry for the
// collaboration helpers (a clone is passed; the caller's env is never
// mutated). The CLI entry itself derives it for the state dir, so the
// whole status run spends exactly one extra `pane current` call, no matter
// how many agents the run covers.
func TestStatusCollaborationDerivesWorkspaceOnce(t *testing.T) {
	f, _ := activeStatusFixture(t, collabFixtureOptions{
		workspaceID: "",
		policyMode:  "policy",
		extraRules: []fakecli.Rule{
			{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"workspace_id":"ws"}}}`},
		},
	}, collaboration.Reviewing, "received")
	code, out, errOut := f.runStatus(t, []string{"author", "reviewer"})
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	for _, agent := range []string{"author", "reviewer"} {
		line := parseCollabJSON(t, out, agent)
		if line["status"] != "collaborating" {
			t.Fatalf("%s line=%v, want the active collaboration via the derived workspace", agent, line)
		}
	}
	if n := collabCallCount(f.herdrCalls(t), []string{"pane", "current", "--current"}); n != 2 {
		t.Fatalf("pane current calls=%d, want exactly 2 (the CLI entry plus the one collaboration derivation): %d", n, n)
	}
	if f.env["HERDR_WORKSPACE_ID"] != "" {
		t.Fatalf("the caller's env was mutated: HERDR_WORKSPACE_ID=%q", f.env["HERDR_WORKSPACE_ID"])
	}
}

// TestCollaborationViewForPaneKeepsMetadata: the read-only view exported
// for collect names the collaboration even when its state is a failure —
// an off policy or a failed identity check — as long as the valid
// assignment was read; ok is false only when no active collaboration hosts
// the pane.
func TestCollaborationViewForPaneKeepsMetadata(t *testing.T) {
	t.Run("active collaboration", func(t *testing.T) {
		f := newCollabFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"})
		a := f.seedAssignment(t)
		view, ok := CollaborationViewForPane(f.sd, collabAuthorPane, f.ctx, func() platform.Env { return f.env })
		if !ok {
			t.Fatal("ok=false, want the active collaboration")
		}
		if view.State != "collaborating" || view.Code != 0 || view.Assignment == nil || view.Assignment.ID != a.ID {
			t.Fatalf("view=%+v, want the active assignment %s", view, a.ID)
		}
		if view.Delivery != "none" {
			t.Fatalf("delivery=%q, want none before any event", view.Delivery)
		}
	})
	t.Run("policy off keeps the assignment", func(t *testing.T) {
		f := newCollabFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "off"})
		a := f.seedAssignment(t)
		view, ok := CollaborationViewForPane(f.sd, collabAuthorPane, f.ctx, func() platform.Env { return f.env })
		if !ok {
			t.Fatal("ok=false, want the collaboration the pane hosts")
		}
		if view.State != "collaboration-disabled" || view.Code != 4 || view.Cause != "worker_messages is off" {
			t.Fatalf("view=%+v", view)
		}
		if view.Assignment == nil || view.Assignment.ID != a.ID {
			t.Fatalf("the valid assignment was not kept: %+v", view.Assignment)
		}
	})
	t.Run("failed identity keeps the assignment", func(t *testing.T) {
		f := newCollabFixture(t, collabFixtureOptions{
			workspaceID: "ws",
			policyMode:  "policy",
			reviewerGet: agentGetResponse{Code: 1, Stderr: "boom"},
		})
		a := f.seedAssignment(t)
		view, ok := CollaborationViewForPane(f.sd, collabAuthorPane, f.ctx, func() platform.Env { return f.env })
		if !ok {
			t.Fatal("ok=false, want the collaboration the pane hosts")
		}
		if view.State != "collaboration-unavailable" || view.Code != 4 || !strings.Contains(view.Cause, "reviewer") {
			t.Fatalf("view=%+v", view)
		}
		if view.Assignment == nil || view.Assignment.ID != a.ID {
			t.Fatalf("the valid assignment was not kept: %+v", view.Assignment)
		}
	})
	t.Run("no active collaboration", func(t *testing.T) {
		f := newCollabFixture(t, collabFixtureOptions{workspaceID: "ws", policyMode: "policy"})
		if _, ok := CollaborationViewForPane(f.sd, collabAuthorPane, f.ctx, func() platform.Env { return f.env }); ok {
			t.Fatal("ok=true without an active collaboration")
		}
	})
}
