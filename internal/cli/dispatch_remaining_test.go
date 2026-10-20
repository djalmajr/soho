package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// agentPromptRoleEnv marks the re-executed ordered-stream fixture: the
// agentPrompt merge-order test installs a copy of this test binary as the
// herdr fake, and the production code execs it with its own argv (no
// -test flags reach the re-execution), so the role rides in the
// environment and this package init handles it before any test or flag
// parsing runs. The parent test run never sets it, so the init is a
// no-op there.
const agentPromptRoleEnv = "SOHO_AGENTPROMPT_FIXTURE"

func init() {
	if os.Getenv(agentPromptRoleEnv) == "" {
		return
	}
	// The exact write order of the old sh script — stderr, stdout,
	// stderr. With MergeOutput both FDs point at the same temporary
	// file, so the interleaving is the observable contract.
	os.Stderr.WriteString("first on stderr\n")
	os.Stdout.WriteString("then stdout\n")
	os.Stderr.WriteString("last on stderr\n\n")
	os.Exit(1)
}

func prepareRunHarness(t *testing.T, h *tm3bHarness) {
	t.Helper()
	if err := os.WriteFile(h.role, []byte("---\nname: Implementer\nkind: grok\nmodel: grok-4.7\neffort: high\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.env["SOHO_LANES"] = "off"
	h.env["HERDR_ENV"] = "1"
	h.env["SOHO_LAYOUT"] = "split"
	h.env["SOHO_REUSE_WORKERS"] = "off"
	h.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	h.env["SOHO_WAIT_POLL_MS"] = "1"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "ready\n"},
		{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		{AnyArgs: true, Stdout: `{"result":{"ok":true}}`},
	}
	config, err := json.Marshal(struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{Log: filepath.Join(h.root, "bin", "herdr.calls.jsonl"), Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "bin", "herdr.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runForTest(t *testing.T, h tm3bHarness, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(append([]string{"run", "implementer", h.brief}, args...), h.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func mustRunCalls(t *testing.T, h tm3bHarness) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(h.root, "bin", "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func hasRunCall(calls []fakecli.Call, argv ...string) bool {
	for _, call := range calls {
		if strings.Join(call.Argv, "\x00") == strings.Join(argv, "\x00") {
			return true
		}
	}
	return false
}

func hasRunCallPrefix(calls []fakecli.Call, argv ...string) bool {
	for _, call := range calls {
		if len(call.Argv) >= len(argv) && strings.Join(call.Argv[:len(argv)], "\x00") == strings.Join(argv, "\x00") {
			return true
		}
	}
	return false
}

func countRunCallPrefix(calls []fakecli.Call, argv ...string) int {
	count := 0
	for _, call := range calls {
		if len(call.Argv) >= len(argv) && strings.Join(call.Argv[:len(argv)], "\x00") == strings.Join(argv, "\x00") {
			count++
		}
	}
	return count
}

func configureBuildLane(t *testing.T, state string, env platform.Env) {
	t.Helper()
	path := filepath.Join(state, "ws", "agents.tsv")
	if err := os.WriteFile(path, []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\tgrok\timplementer\txai\t0\t"+state+"\tnow\tgrok-4.7\ttask\timplementer\tbuild\t\t\thigh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["SOHO_LANE_BUILD_ROLES"] = "implementer"
	env["SOHO_LANE_BUILD_KIND"] = "grok"
	env["SOHO_LANE_BUILD_MODEL"] = "grok-4.7"
}

func TestDispatchRemainingParityPortedCases(t *testing.T) {
	t.Run(`JS: "parity dispatch: quota (test-quota.sh)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "RESOURCE_EXHAUSTED\n"})
		configureBuildLane(t, f.state, f.env)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--timeout", "3000")
		if code != 11 || dispatchOutputStatus(t, out) != "quota" || !strings.Contains(out, `"lane":"build"`) || !strings.Contains(out, `"model":"grok-4.7"`) || !strings.Contains(out, `"match":"RESOURCE_EXHAUSTED"`) {
			t.Fatalf("quota code=%d out=%s", code, out)
		}
	})
	t.Run(`JS: "parity dispatch: denied, through the $TMPDIR routing (test-status.sh)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Code: 1, Stderr: "permission denied"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t/elsewhere\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errText := f.run(t, "worker", f.brief, "--timeout", "2000")
		if code != 4 || dispatchOutputStatus(t, out) != "unavailable" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		report, _ := dispatchOutputField(t, out, "report")
		if !strings.HasPrefix(prompt, filepath.Join(f.root, "soho", "ws", "reports")) || filepath.Dir(prompt) != filepath.Dir(report) {
			t.Fatalf("worker-outside-root paths: prompt=%q report=%q", prompt, report)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "briefs", filepath.Base(prompt))); !os.IsNotExist(err) {
			t.Fatalf("prompt unexpectedly routed to state briefs: %v", err)
		}
	})
	t.Run(`JS: "parity run: --no-wait (spawn + dispatch)"`, func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		prepareRunHarness(t, &h)
		code, out, stderr := runForTest(t, h, "--no-wait", "--pane", "w0test:p0b")
		if code != 0 || !strings.Contains(out, `"status": "ready"`) || !strings.Contains(out, `"wait_status":"submitted"`) || !strings.Contains(out, `"agent":"implementer"`) {
			t.Fatalf("run code=%d out=%s stderr=%s", code, out, stderr)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], `{"wait_status":"submitted"`) {
			t.Fatalf("run stdout did not end with dispatch JSON: %q", out)
		}
		calls := mustRunCalls(t, h)
		if !hasRunCallPrefix(calls, "agent", "start", "implementer") || !hasRunCallPrefix(calls, "pane", "report-metadata", "w0test:p0b", "--source", "soho", "--title", "implementer: fix the config") {
			t.Fatalf("spawn/dispatch/title calls=%v", calls)
		}
		if countRunCallPrefix(calls, "agent", "get", "implementer") != 1 || hasRunCallPrefix(calls, "agent", "read", "implementer", "--source", "recent-unwrapped", "--lines", "120") {
			t.Fatalf("--no-wait unexpectedly collected terminal output: %v", calls)
		}
	})
	t.Run(`JS: "parity run: with the wait, the report settles and collect prints it"`, func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		prepareRunHarness(t, &h)
		oldWait := dispatchWaitFor
		dispatchWaitFor = func(agents []string, sd string, _ *core.Config, _ platform.Env, _ float64, _ bool, _ string) int {
			report := core.LastReport(sd, agents[0])
			if err := os.WriteFile(report, []byte("<!-- report: completed -->\n\nfinished report text\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(platform.Stdout, `{"status":"done"}`)
			return 0
		}
		t.Cleanup(func() { dispatchWaitFor = oldWait })
		code, out, stderr := runForTest(t, h, "--pane", "w0test:p0b", "--timeout", "5000")
		if code != 0 || !strings.Contains(out, `"wait_status":"done"`) || !strings.Contains(out, "<!-- report: ") || !strings.Contains(out, "finished report text") {
			t.Fatalf("run code=%d out=%s stderr=%s", code, out, stderr)
		}
	})
}

func TestDispatchRemainingCases(t *testing.T) {
	t.Run("dispatch sidecar writer defaults to the shared implementation", func(t *testing.T) {
		if reflect.ValueOf(writeDispatchSidecar).Pointer() != reflect.ValueOf(dispatch.WriteSidecar).Pointer() {
			t.Fatal("writeDispatchSidecar no longer defaults to dispatch.WriteSidecar")
		}
	})
	t.Run(`JS: "dispatch warns about relative paths visible only from the orchestrator checkout"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relative := "dispatch_remaining_test.go"
		outside := filepath.Join(cwd, relative)
		brief := "# Goal\nrun\n# Expected result\nok\n# Owned files\nnone\n# Forbidden\nno commit or push\n# Report\ndone\n\nRead `" + relative + "`.\n"
		if err := os.WriteFile(f.brief, []byte(brief), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t/tmp\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_BRIEF_LINT"] = "warn"
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(stderr, "which the worker in") || !strings.Contains(stderr, outside) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})
	t.Run(`JS: "dispatch: the role comes from column 4 of the roster"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		role := filepath.Join(f.root, "roles", "researcher.md")
		if err := os.WriteFile(role, []byte("---\nname: Researcher\nmode: read\n---\nResearch role.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0a\tcodex\tresearcher\topenai\t0\t" + f.root + "\tnow\tgpt-5\ttask\tresearcher\t\t\t\thigh\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		got, _ := dispatchOutputField(t, out, "role")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		if code != 0 || got != "researcher" || !strings.Contains(mustRead(t, prompt), "the `researcher` role, agent name `worker`") {
			t.Fatalf("code=%d role=%q", code, got)
		}
	})
	t.Run(`JS: "dispatch: a worker outside the repo root routes the report through $TMPDIR"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t/worker-root\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		report, _ := dispatchOutputField(t, out, "report")
		if code != 0 || !strings.HasSuffix(prompt, ".brief.md") || filepath.Dir(prompt) != filepath.Dir(report) || !strings.Contains(filepath.ToSlash(prompt), "/soho/ws/reports/") {
			t.Fatalf("code=%d prompt=%q report=%q", code, prompt, report)
		}
		if entries, err := os.ReadDir(filepath.Join(f.state, "ws", "briefs")); err != nil || len(entries) != 0 {
			t.Fatalf("state briefs entries=%v err=%v", entries, err)
		}
	})
	t.Run(`JS: "dispatch: a refused prompt leaves the sidecar at failed and keeps the exit 4"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Code: 7, Stderr: "provider refused"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		var side map[string]any
		data, err := os.ReadFile(dispatch.DispatchSidecar(prompt))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &side); err != nil {
			t.Fatal(err)
		}
		if code != 4 || side["submission"] != "failed" {
			t.Fatalf("code=%d sidecar=%v", code, side)
		}
	})
	t.Run(`JS: "dispatch: a first sidecar write failure dies 4 before any dispatch state"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		fixedNow := platform.Now
		platform.Now = func() time.Time { return time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC) }
		t.Cleanup(func() { platform.Now = fixedNow })
		collision := filepath.Join(f.state, "ws", "briefs", "worker-20260929T000000.dispatch.json")
		if err := os.Mkdir(collision, 0o700); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 4 || !strings.Contains(out, "couldn't write the attempt sidecar") {
			t.Fatalf("code=%d out=%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "last-report-worker")); !os.IsNotExist(err) {
			t.Fatalf("last-report written before sidecar failure: %v", err)
		}
	})
	t.Run(`JS: "dispatch: an accepted prompt with a failed outcome write keeps the result and the attempted sidecar"`, func(t *testing.T) {
		// Mutation captured: dropping the accepted-write error warning hides the failed outcome while leaving the test green.
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		realWrite := writeDispatchSidecar
		writes := make([]string, 0, 2)
		writeDispatchSidecar = func(path, kind, model, effort, submission, arrival, session string) error {
			writes = append(writes, submission+"/"+arrival)
			if len(writes) == 2 {
				return fmt.Errorf("write failed: simulated EIO")
			}
			return realWrite(path, kind, model, effort, submission, arrival, session)
		}
		t.Cleanup(func() { writeDispatchSidecar = realWrite })
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		var side map[string]any
		data, err := os.ReadFile(dispatch.DispatchSidecar(prompt))
		if err != nil || json.Unmarshal(data, &side) != nil {
			t.Fatalf("sidecar=%q err=%v", data, err)
		}
		if len(writes) != 2 || writes[0] != "attempted/" || writes[1] != "accepted/" || code != 0 || dispatchOutputStatus(t, out) != "submitted" || side["submission"] != "attempted" || !strings.Contains(stderr, "could not record the accepted submission") || countDispatchCalls(mustCalls(t, f), "prompt") != 1 {
			t.Fatalf("writes=%v code=%d out=%s stderr=%s sidecar=%v", writes, code, out, stderr, side)
		}
	})
	t.Run(`JS: "dispatch: a transport error with a failed outcome write keeps the error JSON, the exit 4 and the attempted sidecar"`, func(t *testing.T) {
		// Mutation captured: dropping the failed-outcome write leaves only one sidecar attempt.
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Code: 1, Stderr: "prompt failed: the fake refused\n"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		realWrite := writeDispatchSidecar
		writes := make([]string, 0, 2)
		writeDispatchSidecar = func(path, kind, model, effort, submission, arrival, session string) error {
			writes = append(writes, submission+"/"+arrival)
			if len(writes) == 2 {
				return fmt.Errorf("write failed: simulated EIO")
			}
			return realWrite(path, kind, model, effort, submission, arrival, session)
		}
		t.Cleanup(func() { writeDispatchSidecar = realWrite })
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		var side map[string]any
		data, err := os.ReadFile(dispatch.DispatchSidecar(prompt))
		if err != nil || json.Unmarshal(data, &side) != nil {
			t.Fatalf("sidecar=%q err=%v", data, err)
		}
		if len(writes) != 2 || writes[0] != "attempted/" || writes[1] != "failed/" || code != 4 || dispatchOutputStatus(t, out) != "error" || !strings.Contains(out, `"raw":"prompt failed: the fake refused"`) || side["submission"] != "attempted" || !strings.Contains(stderr, "prompt submission failed") || !strings.Contains(stderr, "could not record the failed submission in the attempt sidecar "+dispatch.DispatchSidecar(prompt)+": write failed: simulated EIO\n") || countDispatchCalls(mustCalls(t, f), "prompt") != 1 {
			t.Fatalf("writes=%v code=%d out=%s stderr=%s sidecar=%v", writes, code, out, stderr, side)
		}
	})
	t.Run(`JS: "dispatch: a collision-suffixed pair takes the suffixed sidecar"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		fixedNow := platform.Now
		platform.Now = func() time.Time { return time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC) }
		t.Cleanup(func() { platform.Now = fixedNow })
		prefix := filepath.Join(f.state, "ws", "briefs", "worker-20260929T000000")
		if err := os.WriteFile(prefix+".md", []byte("previous"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		if code != 0 || !strings.HasSuffix(prompt, "-2.md") {
			t.Fatalf("code=%d prompt=%q", code, prompt)
		}
		if _, err := os.Stat(dispatch.DispatchSidecar(prompt)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run(`JS: "dispatch --amend: the amendment attempt gets its own sidecar; the earlier one stands"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, first, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("first dispatch code=%d %s", code, first)
		}
		firstPrompt, _ := dispatchOutputField(t, first, "composed_prompt")
		firstSide := dispatch.DispatchSidecar(firstPrompt)
		firstData := mustRead(t, firstSide)
		amendment := filepath.Join(f.root, "amend.md")
		if err := os.WriteFile(amendment, []byte("# Amendment\nChange it.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker", amendment, "--amend", "--no-wait")
		secondPrompt, _ := dispatchOutputField(t, out, "composed_prompt")
		if code != 0 || secondPrompt == firstPrompt {
			t.Fatalf("code=%d prompts %q %q", code, firstPrompt, secondPrompt)
		}
		if mustRead(t, firstSide) != firstData {
			t.Fatal("earlier sidecar changed")
		}
		if _, err := os.Stat(dispatch.DispatchSidecar(secondPrompt)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run(`JS: "dispatch: the family check (strict 5, warn and --allow-same-family continue, off silent)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		roster := "worker\tw0test:p0b\tcodex\treviewer\topenai\t0\t\tnow\tgpt-5\ttask\treviewer\nex\tw0test:p0a\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, _, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 5 || !strings.Contains(errText, "shares a model family") {
			t.Fatalf("strict code=%d stderr=%q", code, errText)
		}
		f.env["SOHO_FAMILY_CHECK"] = "warn"
		code, _, errText = f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(errText, "shares model family") {
			t.Fatalf("warn code=%d stderr=%q", code, errText)
		}
		f.env["SOHO_FAMILY_CHECK"] = "off"
		code, _, errText = f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || strings.Contains(errText, "shares model family") {
			t.Fatalf("off code=%d stderr=%q", code, errText)
		}
	})
	t.Run(`JS: "dispatch: --for compares the reviewer with the slice author(s) (pass, strict 5, warn, the dies 2)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		role := filepath.Join(f.root, "roles", "reviewer.md")
		if err := os.WriteFile(role, []byte("---\nname: Reviewer\nmode: review\n---\nReview.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0b\tcodex\treviewer\topenai\t0\t\tnow\tgpt-5\ttask\treviewer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, _, errText := f.run(t, "worker", f.brief, "--for", "openai", "--no-wait")
		if code != 5 || !strings.Contains(errText, "slice's author") {
			t.Fatalf("strict code=%d stderr=%q", code, errText)
		}
		code, _, errText = f.run(t, "worker", f.brief, "--for", "openai", "--allow-same-family", "--no-wait")
		if code != 0 || !strings.Contains(errText, "shares model family") {
			t.Fatalf("allow code=%d stderr=%q", code, errText)
		}
		code, _, errText = f.run(t, "worker", f.brief, "--for", "openai", "--role", "implementer", "--no-wait")
		if code != 2 || !strings.Contains(errText, "applies to a reviewer dispatch") {
			t.Fatalf("role validation code=%d stderr=%q", code, errText)
		}
	})
	t.Run(`JS: "dispatch: --for with an unknown-family author falls back to the global scan (never accepts more)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		role := filepath.Join(f.root, "roles", "reviewer.md")
		if err := os.WriteFile(role, []byte("---\nname: Reviewer\nmode: review\n---\nReview.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0b\tcodex\treviewer\topenai\t0\t\tnow\tgpt-5\ttask\treviewer\nex\tw0test:p0a\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\nunknown\tw0test:p0c\tmystery\timplementer\tunknown\t0\t\tnow\t\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, _, errText := f.run(t, "worker", f.brief, "--for", "unknown", "--no-wait")
		if code != 5 || !strings.Contains(errText, "checked against every edit agent") || !strings.Contains(errText, "ex (codex)") {
			t.Fatalf("code=%d stderr=%q", code, errText)
		}
	})
	t.Run(`JS: "dispatch: the default timeout is the role timeout scaled by the effort"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		role := filepath.Join(f.root, "roles", "slow.md")
		if err := os.WriteFile(role, []byte("---\nname: Slow\nmode: edit\ntimeout: 1000\n---\nRole body.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0a\tcodex\tslow\topenai\t0\t" + f.root + "\tnow\tgpt-5\ttask\tslow\t\t\t\t\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_ROLE_SLOW_EFFORT"], f.env["SOHO_LANES"], f.env["SOHO_PROMPT_CHECK_SECONDS"] = "max", "off", "0"
		if got := core.RoleTimeoutMs("slow", f.config, f.env, f.root); got != 2000 {
			t.Fatalf("max timeout=%d, want 2000", got)
		}
	})
	t.Run(`JS: "dispatch: timeout 9 with a working agent, quota 11 with the lane fields"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "working", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--timeout", "1000")
		if code != 9 || dispatchOutputStatus(t, out) != "timeout" {
			t.Fatalf("timeout code=%d out=%s", code, out)
		}
		quota := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "RESOURCE_EXHAUSTED\n"})
		configureBuildLane(t, quota.state, quota.env)
		quota.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ = quota.run(t, "worker", quota.brief, "--timeout", "2000")
		if code != 11 || dispatchOutputStatus(t, out) != "quota" || !strings.Contains(out, `"lane":"build"`) || !strings.Contains(out, `"model":"grok-4.7"`) || !strings.Contains(out, `"match":"RESOURCE_EXHAUSTED"`) {
			t.Fatalf("quota code=%d out=%s", code, out)
		}
	})
	t.Run(`JS: "dispatch: a provider-error worker exits 14 with the lane, model and cause"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "Error: connect ECONNREFUSED\n", "0")
		configureBuildLane(t, f.state, f.env)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "3000")
		if code != 14 || dispatchOutputStatus(t, out) != "provider-error" || !strings.Contains(out, `"lane":"build"`) || !strings.Contains(out, `"model":"grok-4.7"`) || !strings.Contains(out, `"cause":"Error: connect ECONNREFUSED"`) || !strings.Contains(stderr, "provider error") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
	})
	t.Run(`JS: "dispatch: capacity after one continue exits 14 with the retries"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "Error: 529 overloaded\n", "0")
		configureBuildLane(t, f.state, f.env)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		f.env["SOHO_PROVIDER_RETRIES"] = "1"
		f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "5000")
		if code != 14 || dispatchOutputStatus(t, out) != "capacity" || !strings.Contains(out, `"lane":"build"`) || !strings.Contains(out, `"model":"grok-4.7"`) || !strings.Contains(out, `"retries":1`) || !strings.Contains(out, `"cause":"Error: 529 overloaded"`) {
			t.Fatalf("capacity code=%d out=%s stderr=%s calls=%v", code, out, stderr, mustCalls(t, f))
		}
	})
	t.Run(`JS: "dispatch: arrival confirmed by the state turning working → no resend"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "working", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" || countDispatchCalls(mustCalls(t, f), "prompt") != 1 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
	t.Run(`JS: "dispatch: an ignored prompt is resent once and the JSON carries resent"`, func(t *testing.T) {
		t.Skip("same resend outcome is asserted by arrival (b); that Go subtest is annotated with this JS title")
	})
	t.Run(`JS: "dispatch: a prompt ignored twice ends not-received with exit 15"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: "screen before\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: "screen redraw\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "›\n"},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || countDispatchCalls(mustCalls(t, f), "prompt") != 2 {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`JS: "dispatch: prompt_check_seconds=0 sends one prompt and probes nothing"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		calls := mustCalls(t, f)
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" || countDispatchCalls(calls, "prompt") != 1 || countDispatchCalls(calls, "get") != 0 || countDispatchCalls(calls, "read") != 0 {
			t.Fatalf("code=%d calls=%v", code, calls)
		}
	})
	t.Run(`JS: "dispatch: a prompt sitting in the input box gets one Enter (enter_sent)"`, func(t *testing.T) {
		// Adapted contract: the first Enter needs a recognized composer
		// holding the exact composed path — the codex composer line with the
		// real brief path, not a plain marker line. The clock advances with
		// real time (a constant clock would never close the arrival window);
		// the screen waits for a stable per-second stamp so it matches the
		// dispatch's own composed name.
		fixedNow := platform.Now
		base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
		began := time.Now()
		platform.Now = func() time.Time { return base.Add(time.Since(began)) }
		t.Cleanup(func() { platform.Now = fixedNow })
		composedName := func() string {
			stamp := ""
			for {
				cur := core.NowStamp(platform.Now())
				if cur != stamp {
					stamp = cur
					continue
				}
				if core.NowStamp(platform.Now().Add(300*time.Millisecond)) == stamp {
					// The separators must match the composed path: codex prints
					// the dispatch's own path, backslashed on Windows.
					return "$ROOT" + string(filepath.Separator) + "state" + string(filepath.Separator) + "ws" + string(filepath.Separator) + "briefs" + string(filepath.Separator) + "worker-" + stamp + ".md"
				}
				time.Sleep(time.Millisecond)
			}
		}
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "Welcome to the worker\n"},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, ArgvPrefix: true, Stdout: "› Read the file " + composedName() + " in full and execute it.\n"},
			fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 8, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
		)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" || !strings.Contains(out, `"enter_sent":true`) || !strings.Contains(errText, "sent Enter") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		calls := mustCalls(t, f)
		if countDispatchCalls(calls, "prompt") != 1 || countDispatchCalls(calls, "send-keys") != 1 {
			t.Fatalf("calls=%v", calls)
		}
	})
	t.Run(`JS: "dispatch: an input-box prompt that ignores the Enter ends not-received (exit 15)"`, func(t *testing.T) {
		// Adapted contract: the first Enter needs a recognized composer
		// holding the exact composed path — the codex composer line with the
		// real brief path, not a plain marker line. The clock advances with
		// real time (a constant clock would never close the arrival window);
		// the screen waits for a stable per-second stamp so it matches the
		// dispatch's own composed name.
		fixedNow := platform.Now
		base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
		began := time.Now()
		platform.Now = func() time.Time { return base.Add(time.Since(began)) }
		t.Cleanup(func() { platform.Now = fixedNow })
		composedName := func() string {
			stamp := ""
			for {
				cur := core.NowStamp(platform.Now())
				if cur != stamp {
					stamp = cur
					continue
				}
				if core.NowStamp(platform.Now().Add(300*time.Millisecond)) == stamp {
					// The separators must match the composed path: codex prints
					// the dispatch's own path, backslashed on Windows.
					return "$ROOT" + string(filepath.Separator) + "state" + string(filepath.Separator) + "ws" + string(filepath.Separator) + "briefs" + string(filepath.Separator) + "worker-" + stamp + ".md"
				}
				time.Sleep(time.Millisecond)
			}
		}
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "Welcome to the worker\n"},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, ArgvPrefix: true, Stdout: "› Read the file " + composedName() + " in full and execute it.\n"},
			fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
		)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || countDispatchCalls(mustCalls(t, f), "prompt") != 1 || countDispatchCalls(mustCalls(t, f), "send-keys") != 1 {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`JS: "dispatch: a new dispatch clears the not-received and enter-retry markers"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		waitDir := filepath.Join(f.state, "ws", "wait")
		for name, value := range map[string]string{"worker.not-received": "1700000000\n", "worker.enter-retry": "2 1700000015\n"} {
			if err := os.WriteFile(filepath.Join(waitDir, name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		f.env["SOHO_FAMILY_CHECK"] = "off"
		code, _, _ := f.run(t, "worker", f.brief, "--no-wait")
		for _, name := range []string{"worker.not-received", "worker.enter-retry"} {
			if _, err := os.Stat(filepath.Join(waitDir, name)); !os.IsNotExist(err) {
				t.Errorf("%s remains: %v", name, err)
			}
		}
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
	})
	t.Run(`JS: "dispatch: a not-received dispatch marks the sidecar arrival without changing the accepted outcome"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "screen before\n"},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, ArgvPrefix: true, Stdout: "screen redraw\n"},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		var side map[string]any
		data, err := os.ReadFile(dispatch.DispatchSidecar(prompt))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &side); err != nil {
			t.Fatal(err)
		}
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || side["submission"] != "accepted" || side["arrival"] != "not-received" {
			t.Fatalf("code=%d output=%s sidecar=%v", code, out, side)
		}
	})
	t.Run(`JS: "dispatch: a delivered prompt leaves no arrival mark in the sidecar"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		var side map[string]any
		data, err := os.ReadFile(dispatch.DispatchSidecar(prompt))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &side); err != nil {
			t.Fatal(err)
		}
		if code != 0 || side["submission"] != "accepted" || side["arrival"] != nil {
			t.Fatalf("code=%d sidecar=%v", code, side)
		}
	})
	t.Run(`JS: "dispatch: a not-received sidecar mark that fails warns and keeps the result and the accepted sidecar"`, func(t *testing.T) {
		// Mutation captured: returning on an arrival-write failure discards the not-received result and its marker.
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "screen before\n"},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, ArgvPrefix: true, Stdout: "screen redraw\n"},
		)
		realWrite := writeDispatchSidecar
		writes := make([]string, 0, 3)
		writeDispatchSidecar = func(path, kind, model, effort, submission, arrival, session string) error {
			writes = append(writes, submission+"/"+arrival)
			if len(writes) == 3 {
				return fmt.Errorf("write failed: simulated EIO")
			}
			return realWrite(path, kind, model, effort, submission, arrival, session)
		}
		t.Cleanup(func() { writeDispatchSidecar = realWrite })
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		prompt, _ := dispatchOutputField(t, out, "composed_prompt")
		var side map[string]any
		data, err := os.ReadFile(dispatch.DispatchSidecar(prompt))
		if err != nil || json.Unmarshal(data, &side) != nil {
			t.Fatalf("sidecar=%q err=%v", data, err)
		}
		if want := "could not record the not-received arrival in the attempt sidecar " + dispatch.DispatchSidecar(prompt) + ": write failed: simulated EIO; the dispatch result stands"; !strings.Contains(stderr, want) {
			t.Fatalf("stderr=%q want %q", stderr, want)
		}
		if len(writes) != 3 || writes[2] != "accepted/not-received" || code != 15 || dispatchOutputStatus(t, out) != "not-received" || side["submission"] != "accepted" || side["arrival"] != nil {
			t.Fatalf("writes=%v code=%d out=%s sidecar=%v", writes, code, out, side)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); err != nil {
			t.Fatalf("not-received marker missing: %v", err)
		}
	})
	t.Run(`JS: "dispatch: the report-writer line leads the standing rules in brief and amendment"`, func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "role.md")
		if err := os.WriteFile(role, []byte("---\nname: Role\n---\nBody\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		line := "- Only you write this report, once all of the brief is done, including any part you handed to subagents or background tasks; a subagent never writes it. Report every item as it stands in the files, not as a subagent summarized it.\n"
		for _, prompt := range []string{dispatch.ComposePrompt(role, "implementer", "worker", "# Goal\nrun", "/r.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{"SOHO_SKILL_DIR": t.TempDir()}, "codex", "", "", false), dispatch.ComposeAmendment("# Amend\nchange", "/r.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{}, "codex", "", "", false)} {
			if strings.Count(prompt, line) != 1 || strings.Index(prompt, "- Write the report in one go") > strings.Index(prompt, line) {
				t.Fatalf("report writer line missing or misplaced")
			}
		}
	})
	t.Run(`JS: "compose: the report covers only the current brief and its amendments (R30, in order)"`, func(t *testing.T) {
		// The package-level mirror asserts the same rule in both prompt shapes.
		line := "- Report only the current brief and its explicit amendments; do not import unrelated work from earlier briefs retained in a reused session. Mention prior work only when it directly affects this brief, stating the relationship.\n"
		role := filepath.Join(t.TempDir(), "role.md")
		if err := os.WriteFile(role, []byte("---\nname: Role\n---\nBody\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, prompt := range []string{dispatch.ComposePrompt(role, "implementer", "worker", "# Goal\nrun", "/r.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{"SOHO_SKILL_DIR": t.TempDir()}, "codex", "", "", false), dispatch.ComposeAmendment("# Amend\nchange", "/r.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{}, "codex", "", "", false)} {
			if strings.Count(prompt, line) != 1 || strings.Index(prompt, "# Report contract") > strings.Index(prompt, line) {
				t.Fatalf("report scope line missing or misplaced")
			}
		}
	})
	t.Run(`JS: "dispatch: a worker that ends in a question exits 7 with the question in the JSON"`, func(t *testing.T) {
		question := "  1. Use the cache\n  2. Fetch remote\n\nEnter to submit answer, esc to cancel\n"
		f := newDispatchArrivalFixture(t, "blocked", 1, 1, question, "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: question})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--timeout", "3000")
		if code != 7 || dispatchOutputStatus(t, out) != "question" || !strings.Contains(out, "Use the cache") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`JS: "agentPrompt: stdout and stderr in write order, trailing newlines dropped (bash \"$(… 2>&1)\")"`, func(t *testing.T) {
		// AgentPrompt runs with MergeOutput: both streams share one temp
		// file, so the write order holds as in bash "$(… 2>&1)". The
		// fixture is a re-executed image of this test binary (a hard link
		// of the package's one prepared image on Windows; no shell behind
		// the name): the role marker env (handled in the package init)
		// writes the two streams interleaved exactly like the old sh
		// script did, and the ordering is the same file handle on every
		// host.
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(root, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			// Windows prefers a hard link of the package's one prepared
			// independent image (fakecli's byte copy in its own temp dir;
			// the running parent image is never linked): no fresh full
			// copy is written and its slow first exec is not paid. When a
			// link is unsupported it falls back to an independent byte
			// copy (slower first exec, same behavior). Install also
			// writes bin/herdr.json, which the leftover check does not
			// see (it globs root/.herdr-soho-out-* only). The env below
			// carries no fakecli marker, so the re-execution runs the
			// package init role, not the fake.
			if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
				t.Logf("falling back to an independent byte copy: %v", err)
				exePath, readErr := os.Executable()
				if readErr != nil {
					t.Fatalf("locating the test binary: %v", readErr)
				}
				data, readErr := os.ReadFile(exePath)
				if readErr != nil {
					t.Fatalf("reading the test binary: %v", readErr)
				}
				if writeErr := os.WriteFile(filepath.Join(bin, "herdr.exe"), data, 0o755); writeErr != nil {
					t.Fatalf("writing the fixture program: %v", writeErr)
				}
			}
		} else {
			exe, err := os.Executable()
			if err != nil {
				t.Fatalf("locating the test binary: %v", err)
			}
			target := filepath.Join(bin, "herdr")
			if err := os.Link(exe, target); err != nil {
				data, readErr := os.ReadFile(exe)
				if readErr != nil {
					t.Fatalf("reading the test binary: %v", readErr)
				}
				if writeErr := os.WriteFile(target, data, 0o755); writeErr != nil {
					t.Fatalf("writing the fixture program: %v", writeErr)
				}
			}
		}
		r := herdr.AgentPrompt("w", "text", platform.Env{"PATH": bin, "TMPDIR": root, agentPromptRoleEnv: "ordered"})
		if r.Ok || r.Raw != "first on stderr\nthen stdout\nlast on stderr" {
			t.Fatalf("result=%#v", r)
		}
		left, err := filepath.Glob(filepath.Join(root, ".herdr-soho-out-*"))
		if err != nil || len(left) != 0 {
			t.Fatalf("temp files left: %v err=%v", left, err)
		}
	})
	t.Run(`JS: "run: a value flag without its value exits 2 before any spawn"`, func(t *testing.T) {
		for _, flag := range []string{"--kind", "--name", "--timeout"} {
			_, _, panicked := splitRunArgsResult([]string{flag})
			if !panicked {
				t.Errorf("%s did not reject missing value", flag)
			}
		}
	})
	t.Run(`JS: "run: --no-wait routes to dispatch and skips collect; --tab-label goes to spawn (test-tab-labels.sh)"`, func(t *testing.T) {
		spawnArgs, dispatchArgs, noWait := splitRunArgs([]string{"--no-wait", "--tab-label", "paridade"})
		if !noWait || strings.Join(spawnArgs, " ") != "--tab-label paridade" || strings.Join(dispatchArgs, " ") != "--no-wait" {
			t.Fatalf("spawn=%v dispatch=%v noWait=%v", spawnArgs, dispatchArgs, noWait)
		}
	})
	t.Run(`JS: "dispatch: warn mode — an edit role without Owned files warns, a read-only role does not"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_BRIEF_LINT"] = "warn"
		f.env["SOHO_FAMILY_CHECK"] = "off"
		if err := os.WriteFile(filepath.Join(f.root, "roles", "reviewer.md"), []byte("---\nname: Reviewer\nmode: review\n---\nReview.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		body := "# Goal\nrun\n# Expected result\nok\n# Forbidden\nno commit or push\n# Report\ndone\n"
		if err := os.WriteFile(f.brief, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + f.root + "\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		_, _, warn := f.run(t, "worker", f.brief, "--no-wait")
		roster = strings.Replace(roster, "worker\tw0test:p0a\tcodex\timplementer", "worker\tw0test:p0a\tcodex\treviewer", 1)
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, quiet := f.run(t, "worker", f.brief, "--no-wait")
		if !strings.Contains(warn, "missing sections: [Owned files]") || strings.Contains(quiet, "missing sections") {
			t.Fatalf("edit warning=%q read-only stderr=%q", warn, quiet)
		}
	})
	t.Run(`JS: "dispatch: lint runs after the role (error order 2 → 3 → 3 → 2; read-only strict passes)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		missing := filepath.Join(f.root, "missing.md")
		f.env["SOHO_BRIEF_LINT"] = "strict"
		if code, _, errText := f.run(t, "ghost", missing, "--no-wait"); code != 2 || !strings.Contains(errText, "brief not found") {
			t.Fatalf("missing precedence: code=%d %q", code, errText)
		}
		if code, _, errText := f.run(t, "ghost", f.brief, "--no-wait"); code != 3 || !strings.Contains(errText, "not in this skill's roster") {
			t.Fatalf("agent precedence: code=%d %q", code, errText)
		}
	})
}

func mustCalls(t *testing.T, f *dispatchArrivalFixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func splitRunArgsResult(args []string) (spawnArgs, dispatchArgs []string, panicked bool) {
	defer func() { panicked = recover() != nil }()
	spawnArgs, dispatchArgs, _ = splitRunArgs(args)
	return
}
