package wait

// SOH-485: `--any` ends on any final state of a watched agent, not only on
// a done report. The fixtures drive CmdWait through real multi-agent
// passes, including an actionable collaboration, so the wait's exit, its
// per-agent lines and its unwatched summary can be checked together.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	anyFinalTimeout = "2000"
	refusalScreen   = "■ We take extra care with some cybersecurity requests\n"
	quotaScreen     = "Individual quota reached\nError: Connection error.\n"
)

func newAnyFinalFixture(t *testing.T, state, screen string) statusViewFixture {
	t.Helper()
	rows := "settled\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\nworking\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "settled"}, Stdout: fmt.Sprintf(`{"result":{"agent":{"agent_status":%q,"state_change_seq":5}}}`, state)},
		{Argv: []string{"agent", "get", "working"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":5}}}`},
		{Argv: []string{"agent", "read", "settled"}, ArgvPrefix: true, Stdout: screen},
		{Argv: []string{"agent", "read", "working"}, ArgvPrefix: true, Stdout: "still running\n"},
	}
	f := newStatusViewFixture(t, rows, rules)
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	return f
}

// newAnyRankedFixture has two final states in the same pass and no pending
// agent: quota outranks provider-refusal or blocked under WaitRank. Seeding
// the blocked marker makes that peer final on its first sample too.
func newAnyRankedFixture(t *testing.T, blocked bool) statusViewFixture {
	t.Helper()
	otherState, otherScreen := "idle", refusalScreen
	if blocked {
		otherState, otherScreen = "blocked", "waiting for approval\n"
	}
	rows := "settled\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\nother\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "settled"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
		{Argv: []string{"agent", "get", "other"}, Stdout: fmt.Sprintf(`{"result":{"agent":{"agent_status":%q,"state_change_seq":5}}}`, otherState)},
		{Argv: []string{"agent", "read", "settled"}, ArgvPrefix: true, Stdout: quotaScreen},
		{Argv: []string{"agent", "read", "other"}, ArgvPrefix: true, Stdout: otherScreen},
	}
	f := newStatusViewFixture(t, rows, rules)
	if blocked {
		if err := os.WriteFile(filepath.Join(f.sd, "wait", "other.blocked"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	return f
}

// newAnyCollabFixture adds a ready peer and a still-working agent to the
// seeded actionable collaboration. report_working_max=0 makes the peer's
// report final on its first sample, so any line that follows the
// collaboration is evidence that the pass was not cut short.
func newAnyCollabFixture(t *testing.T, phase string) collabFixture {
	t.Helper()
	f, _ := activeStatusFixture(t, collabFixtureOptions{
		workspaceID: "ws",
		policyMode:  "policy",
		extraRules: []fakecli.Rule{
			{Argv: []string{"agent", "get", "complete"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
			{Argv: []string{"agent", "get", "working"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":5}}}`},
			{Argv: []string{"agent", "read", "working"}, ArgvPrefix: true, Stdout: "still running\n"},
		},
	}, phase, "queued")
	f.ctx.Entries["report_working_max"] = core.ConfigEntry{Value: "0"}
	roster, err := os.OpenFile(filepath.Join(f.sd, "agents.tsv"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.WriteString("complete\tp0c\tclaude\timplementer\tanthropic\t\t\t\t\t\t\t\t\nworking\tp0b\tclaude\timplementer\tanthropic\t\t\t\t\t\t\t\t\n"); err != nil {
		t.Fatal(err)
	}
	if err := roster.Close(); err != nil {
		t.Fatal(err)
	}
	report := f.writeStaleReport(t, "complete")
	info, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	// The report's first sample is otherwise held back as unstable by the
	// size-marker debounce; this timer makes it final on the first probe.
	stable := strconv.FormatInt(time.Now().Unix(), 10) + "\t" + reportIdentity(report, info) + "\n"
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "complete.report-stable-at"), []byte(stable), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "task-complete"), []byte("the task\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

// newAnyCollabQuotaFixture pairs an actionable collaboration with a peer
// that settles on quota (11) without writing a report.
func newAnyCollabQuotaFixture(t *testing.T, phase string) collabFixture {
	t.Helper()
	f, _ := activeStatusFixture(t, collabFixtureOptions{
		workspaceID: "ws",
		policyMode:  "policy",
		extraRules: []fakecli.Rule{
			{Argv: []string{"agent", "get", "peer"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
			{Argv: []string{"agent", "read", "peer"}, ArgvPrefix: true, Stdout: quotaScreen},
		},
	}, phase, "queued")
	roster, err := os.OpenFile(filepath.Join(f.sd, "agents.tsv"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.WriteString("peer\tp0q\tclaude\timplementer\tanthropic\t\t\t\t\t\tmodel-x\t\t\t\n"); err != nil {
		t.Fatal(err)
	}
	if err := roster.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func runAnyFinalWait(t *testing.T, f statusViewFixture, argv ...string) (int, string, string) {
	t.Helper()
	return runAnyWait(t, f.cwd, f.ctx, f.env, argv...)
}

func runAnyWait(t *testing.T, cwd string, ctx *core.Config, env platform.Env, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	code := CmdWait(argv, ctx, env, cwd)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

func anyFinalLines(out string) ([]map[string]any, error) {
	lines := []map[string]any{}
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return nil, fmt.Errorf("not a JSON line: %q (%v) in %q", raw, err, out)
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func parseAnyFinalLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	lines, err := anyFinalLines(out)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// checkAnyFinalEnd is the shared acceptance oracle: one summary object with
// exactly the pending names and the exact rearm when a pending agent
// remains, none when every agent settled, one status line per expected
// agent, and the pass-end exit code.
func checkAnyFinalEnd(code int, out, stderr string, wantCode int, wantAgent, wantStatus string, wantUnwatched []string) error {
	if code != wantCode {
		return fmt.Errorf("code=%d stderr=%q out=%q, want exit %d", code, stderr, out, wantCode)
	}
	lines, err := anyFinalLines(out)
	if err != nil {
		return err
	}
	found := false
	summaries := 0
	for _, line := range lines {
		if line["status"] == "timeout" {
			return fmt.Errorf("wait timed out instead of ending on %s: %q", wantStatus, out)
		}
		if line["agent"] == wantAgent && line["status"] == wantStatus {
			found = true
		}
		raw, has := line["unwatched"]
		if !has {
			continue
		}
		summaries++
		got, ok := raw.([]any)
		want := make([]any, 0, len(wantUnwatched))
		for _, name := range wantUnwatched {
			want = append(want, name)
		}
		if !ok || !reflect.DeepEqual(got, want) {
			return fmt.Errorf("summary unwatched=%v, want %v: %q", raw, want, out)
		}
		wantRearm := "soho wait " + strings.Join(wantUnwatched, " ") + " --wake --any --timeout " + anyFinalTimeout
		if rearm, ok := line["rearm"].(string); !ok || rearm != wantRearm {
			return fmt.Errorf("summary rearm=%v, want %q: %q", line["rearm"], wantRearm, out)
		}
	}
	if !found {
		return fmt.Errorf("no %s line for %s: %q (stderr %q)", wantStatus, wantAgent, out, stderr)
	}
	if len(wantUnwatched) > 0 && summaries != 1 {
		return fmt.Errorf("want exactly one summary object for %v, got %d: %q", wantUnwatched, summaries, out)
	}
	if len(wantUnwatched) == 0 && summaries != 0 {
		return fmt.Errorf("want no summary object with every agent settled, got %d: %q", summaries, out)
	}
	return nil
}

func assertAnyFinalEnd(t *testing.T, code int, out, stderr string, wantCode int, wantAgent, wantStatus string, wantUnwatched ...string) {
	t.Helper()
	if err := checkAnyFinalEnd(code, out, stderr, wantCode, wantAgent, wantStatus, wantUnwatched); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestAnyEndsOnProviderRefusalWhileOtherAgentWorks(t *testing.T) {
	f := newAnyFinalFixture(t, "idle", refusalScreen)
	code, out, stderr := runAnyFinalWait(t, f, "settled", "working", "--any", "--timeout", anyFinalTimeout)
	assertAnyFinalEnd(t, code, out, stderr, 14, "settled", "provider-refusal", "working")
}

func TestAnyEndsOnQuotaAndBlockedWhileOtherAgentWorks(t *testing.T) {
	for _, tc := range []struct {
		name, state, screen, status string
		code                        int
	}{
		{"quota", "idle", quotaScreen, "quota", 11},
		{"blocked", "blocked", "waiting for approval\n", "blocked", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAnyFinalFixture(t, tc.state, tc.screen)
			code, out, stderr := runAnyFinalWait(t, f, "settled", "working", "--any", "--timeout", anyFinalTimeout)
			assertAnyFinalEnd(t, code, out, stderr, tc.code, "settled", tc.status, "working")
		})
	}
}

func TestAnyRanksTheSettledAgentsWithoutSummary(t *testing.T) {
	for _, tc := range []struct {
		name, otherStatus string
		blocked           bool
	}{
		{"refusal-then-quota", "provider-refusal", false},
		{"quota-then-refusal", "provider-refusal", false},
		{"blocked-then-quota", "blocked", true},
		{"quota-then-blocked", "blocked", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The name gives the actual argument order: "other" is the
			// refusal/blocked peer and "settled" is the quota agent.
			order := []string{"other", "settled"}
			if strings.HasPrefix(tc.name, "quota-then") {
				order = []string{"settled", "other"}
			}
			f := newAnyRankedFixture(t, tc.blocked)
			argv := append([]string{}, order...)
			argv = append(argv, "--any", "--timeout", anyFinalTimeout)
			code, out, stderr := runAnyFinalWait(t, f, argv...)
			// A first-terminal-code policy would return 14 in the
			// refusal-first case and 7 in the blocked-first case.
			assertAnyFinalEnd(t, code, out, stderr, 11, "settled", "quota")
			assertRankedLines(t, out, map[string]string{"settled": "quota", "other": tc.otherStatus})
		})
	}
}

func assertRankedLines(t *testing.T, out string, want map[string]string) {
	t.Helper()
	lines := parseAnyFinalLines(t, out)
	for agent, status := range want {
		count := 0
		for _, line := range lines {
			if line["agent"] == agent {
				if line["status"] != status {
					t.Fatalf("%s printed %v, want %q: %q", agent, line["status"], status, out)
				}
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s printed %d status lines, want one: %q", agent, count, out)
		}
	}
}

// F1 of round 2: exit 10 from an actionable collaboration outranks a
// peer's nonzero quota exit in both argument orders and both modes.
func TestAnyCollaborationBeatsQuotaPeer(t *testing.T) {
	for _, phase := range []string{string(collaboration.AwaitingOrchestrator), string(collaboration.Escalated)} {
		for _, order := range [][]string{{"author", "peer"}, {"peer", "author"}} {
			for _, observe := range []bool{false, true} {
				phase, order, observe := phase, order, observe
				name := phase + "-" + order[0] + "-first"
				if observe {
					name += "-observe"
				}
				t.Run(name, func(t *testing.T) {
					f := newAnyCollabQuotaFixture(t, phase)
					argv := append([]string{}, order...)
					argv = append(argv, "--any", "--timeout", anyFinalTimeout)
					if observe {
						argv = append(argv, "--observe")
					}
					code, out, stderr := runAnyWait(t, f.cwd, f.ctx, f.env, argv...)
					if err := checkAnyFinalEnd(code, out, stderr, 10, "author", "collaborating", nil); err != nil {
						t.Fatalf("%v", err)
					}
					assertRankedLines(t, out, map[string]string{"author": "collaborating", "peer": "quota"})
				})
			}
		}
	}
}

func TestActionableCollaborationWithoutAnySkipsQuotaPeer(t *testing.T) {
	f := newAnyCollabQuotaFixture(t, string(collaboration.AwaitingOrchestrator))
	code, out, stderr := runAnyWait(t, f.cwd, f.ctx, f.env, "author", "peer", "--timeout", anyFinalTimeout)
	if code != 10 {
		t.Fatalf("code=%d out=%q stderr=%q, want the immediate exit 10", code, out, stderr)
	}
	for _, line := range parseAnyFinalLines(t, out) {
		if line["agent"] != "author" || line["status"] != "collaborating" {
			t.Fatalf("without --any the pass continued past the collaboration: %v", line)
		}
	}
}

func TestAnyObserveEndsOnProviderRefusalWhileOtherAgentWorks(t *testing.T) {
	f := newAnyFinalFixture(t, "idle", refusalScreen)
	code, out, stderr := runAnyFinalWait(t, f, "settled", "working", "--any", "--observe", "--timeout", anyFinalTimeout)
	assertAnyFinalEnd(t, code, out, stderr, 14, "settled", "provider-refusal", "working")
	for _, line := range parseAnyFinalLines(t, out) {
		if line["status"] == "provider-refusal" {
			if observed, has := line["observe"]; has {
				t.Fatalf("a non-done observe line carries observe=%v: %q", observed, out)
			}
		}
	}
}

func TestWithoutAnyStillWaitsForTheWorkingAgent(t *testing.T) {
	f := newAnyFinalFixture(t, "idle", refusalScreen)
	code, out, stderr := runAnyFinalWait(t, f, "settled", "working", "--timeout", "500")
	if code != 9 {
		t.Fatalf("code=%d stderr=%q out=%q, want the timeout exit 9", code, stderr, out)
	}
	foundRefusal, foundTimeout := false, false
	for _, line := range parseAnyFinalLines(t, out) {
		switch line["status"] {
		case "provider-refusal":
			foundRefusal = true
		case "timeout":
			if line["agent"] != "working" {
				t.Fatalf("timeout line names the wrong agent: %v", line)
			}
			foundTimeout = true
		}
		if _, has := line["unwatched"]; has {
			t.Fatalf("without --any the wait published an unwatched line: %q", out)
		}
	}
	if !foundRefusal || !foundTimeout {
		t.Fatalf("want the refusal line and a timeout line for the working agent: %q", out)
	}
}

// F1: with --any an actionable collaboration records 10 but no longer cuts
// the pass short. Both phases, both argument orders and both modes must
// print the ready peer's done line, settle it in the foreground path, and
// name only the working agent in the summary.
func TestAnyActionableCollaborationFinishesPass(t *testing.T) {
	for _, phase := range []string{string(collaboration.AwaitingOrchestrator), string(collaboration.Escalated)} {
		for _, order := range [][]string{{"author", "complete", "working"}, {"complete", "author", "working"}} {
			for _, observe := range []bool{false, true} {
				phase, order, observe := phase, order, observe
				name := phase + "-" + order[0] + "-first"
				if observe {
					name += "-observe"
				}
				t.Run(name, func(t *testing.T) {
					f := newAnyCollabFixture(t, phase)
					argv := append([]string{}, order...)
					argv = append(argv, "--any", "--timeout", anyFinalTimeout)
					if observe {
						argv = append(argv, "--observe")
					}
					code, out, stderr := runAnyWait(t, f.cwd, f.ctx, f.env, argv...)
					if err := checkAnyFinalEnd(code, out, stderr, 10, "author", "collaborating", []string{"working"}); err != nil {
						t.Fatalf("%v", err)
					}
					lines := parseAnyFinalLines(t, out)
					done := false
					for _, line := range lines {
						if line["agent"] == "complete" && line["status"] == "done" {
							done = true
							if observe && line["observe"] != true {
								t.Fatalf("observe mode printed a done line without the observe key: %v", line)
							}
						}
					}
					if !done {
						t.Fatalf("the pass skipped the ready peer's done line: %q", out)
					}
					counts := map[string]int{}
					for _, line := range lines {
						if name, ok := line["agent"].(string); ok {
							counts[name]++
						}
					}
					for _, name := range []string{"author", "complete"} {
						if counts[name] != 1 {
							t.Fatalf("%s printed %d status lines, want one: %q", name, counts[name], out)
						}
					}
					// The pending agent is not printed, but it was sampled in
					// the same pass: its state read is in the fake CLI call log.
					calls, err := fakecli.ReadCalls(filepath.Join(f.env["PATH"], "herdr.calls.jsonl"))
					if err != nil {
						t.Fatal(err)
					}
					polls := 0
					for _, call := range calls {
						if len(call.Argv) == 3 && call.Argv[0] == "agent" && call.Argv[1] == "get" && call.Argv[2] == "working" {
							polls++
						}
					}
					if polls == 0 {
						t.Fatalf("the pass skipped the pending agent's probe: %q", out)
					}
					task, err := os.ReadFile(filepath.Join(f.sd, "task-complete"))
					if err != nil {
						t.Fatal(err)
					}
					wantTask := "the task ✓\n"
					if observe {
						wantTask = "the task\n"
					}
					if string(task) != wantTask {
						t.Fatalf("task after %s: %q, want %q", name, task, wantTask)
					}
				})
			}
		}
	}
}

func TestActionableCollaborationWithoutAnyStillReturnsImmediately(t *testing.T) {
	f := newAnyCollabFixture(t, string(collaboration.AwaitingOrchestrator))
	code, out, stderr := runAnyWait(t, f.cwd, f.ctx, f.env, "author", "complete", "working", "--timeout", anyFinalTimeout)
	if code != 10 {
		t.Fatalf("code=%d out=%q stderr=%q, want the immediate exit 10", code, out, stderr)
	}
	for _, line := range parseAnyFinalLines(t, out) {
		if line["agent"] != "author" {
			t.Fatalf("without --any the pass continued past the collaboration: %v", line)
		}
		if line["status"] != "collaborating" {
			t.Fatalf("unexpected collaboration line: %v", line)
		}
	}
	task, err := os.ReadFile(filepath.Join(f.sd, "task-complete"))
	if err != nil {
		t.Fatal(err)
	}
	if string(task) != "the task\n" {
		t.Fatalf("task after the immediate return: %q", task)
	}
}

// F2: every fixture that expects a summary must fail the shared oracle when
// that object is removed from its captured output.
func TestAnyFinalHelperRejectsMissingSummary(t *testing.T) {
	cases := []struct {
		name                  string
		run                   func(t *testing.T) (int, string, string)
		wantCode              int
		wantAgent, wantStatus string
	}{
		{"provider-refusal", func(t *testing.T) (int, string, string) {
			f := newAnyFinalFixture(t, "idle", refusalScreen)
			return runAnyFinalWait(t, f, "settled", "working", "--any", "--timeout", anyFinalTimeout)
		}, 14, "settled", "provider-refusal"},
		{"quota", func(t *testing.T) (int, string, string) {
			f := newAnyFinalFixture(t, "idle", quotaScreen)
			return runAnyFinalWait(t, f, "settled", "working", "--any", "--timeout", anyFinalTimeout)
		}, 11, "settled", "quota"},
		{"blocked", func(t *testing.T) (int, string, string) {
			f := newAnyFinalFixture(t, "blocked", "waiting for approval\n")
			return runAnyFinalWait(t, f, "settled", "working", "--any", "--timeout", anyFinalTimeout)
		}, 7, "settled", "blocked"},
		{"observe refusal", func(t *testing.T) (int, string, string) {
			f := newAnyFinalFixture(t, "idle", refusalScreen)
			return runAnyFinalWait(t, f, "settled", "working", "--any", "--observe", "--timeout", anyFinalTimeout)
		}, 14, "settled", "provider-refusal"},
		{"collaboration first", func(t *testing.T) (int, string, string) {
			f := newAnyCollabFixture(t, string(collaboration.AwaitingOrchestrator))
			return runAnyWait(t, f.cwd, f.ctx, f.env, "author", "complete", "working", "--any", "--timeout", anyFinalTimeout)
		}, 10, "author", "collaborating"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := tc.run(t)
			if err := checkAnyFinalEnd(code, out, stderr, tc.wantCode, tc.wantAgent, tc.wantStatus, []string{"working"}); err != nil {
				t.Fatalf("the fixture itself failed: %v", err)
			}
			stripped := stripAnySummary(out)
			if stripped == out {
				t.Fatalf("the fixture printed no summary object: %q", out)
			}
			if err := checkAnyFinalEnd(code, stripped, stderr, tc.wantCode, tc.wantAgent, tc.wantStatus, []string{"working"}); err == nil {
				t.Fatalf("the helper accepted output without a summary: %q", stripped)
			} else if !strings.Contains(err.Error(), "summary") {
				t.Fatalf("the helper failed for the wrong reason: %v", err)
			}
		})
	}
}

func stripAnySummary(out string) string {
	lines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"unwatched"`) {
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
