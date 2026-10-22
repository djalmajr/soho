package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchNoWaitWritesPromptAndTaskState(t *testing.T) {
	// JS: "dispatch: --no-wait happy path, task title, prompt text, state files"
	root := t.TempDir()
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{roles, bin, filepath.Join(state, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roleFile := filepath.Join(roles, "implementer.md")
	if err := os.WriteFile(roleFile, []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	briefRaw := "# Brief — dispatch sample\n\nGoal: run it.\n"
	if err := os.WriteFile(brief, []byte(briefRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tp1\tcodex\timplementer\topenai\t0\t" + root + "\tnow\tgpt-5\task\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	}
	if _, err := fakecli.Install(t, bin, "herdr", calls); err != nil {
		t.Fatal(err)
	}
	env := noWaitDispatchEnv(t, bin, state, roles, root)
	// A race-instrumented fake child otherwise pauses for one second on
	// exit; the harness sets only GORACE and leaves the rest of the env as built.
	env["GORACE"] = fakecli.RaceOptions(env.List())
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := cmdDispatch([]string{"worker", brief, "--no-wait"}, ctx, env, root); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(out.String())))
	if err != nil {
		t.Fatalf("output JSON %q: %v", out.String(), err)
	}
	result := value.(*jsonjs.Object)
	if got, _ := result.Get("wait_status"); got != "submitted" {
		t.Fatalf("wait_status=%v", got)
	}
	composedValue, _ := result.Get("composed_prompt")
	composed := composedValue.(string)
	body, err := os.ReadFile(composed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# Role: Implementer\n\n") || !strings.Contains(string(body), "# Brief\n\n"+briefRaw) || !strings.Contains(string(body), "Role body.") {
		t.Fatalf("composed prompt missed source content: %s", body)
	}
	reportValue, _ := result.Get("report")
	report := reportValue.(string)
	if !strings.Contains(string(body), "Write your report as Markdown to `"+report+"`") {
		t.Fatal("composed prompt did not carry the generated report path")
	}
	task, err := os.ReadFile(filepath.Join(state, "ws", "task-worker"))
	if err != nil || string(task) != "implementer: dispatch sample\n" {
		t.Fatalf("task=%q err=%v", task, err)
	}
	if _, err := os.Stat(filepath.Join(state, "ws", "task-report-worker.json")); err != nil {
		t.Fatalf("task report pointer missing: %v", err)
	}
	sidecar := dispatch.DispatchSidecar(composed)
	if _, err := os.Stat(sidecar); err != nil {
		briefs, _ := os.ReadDir(filepath.Join(state, "ws", "briefs"))
		reports, _ := os.ReadDir(filepath.Join(state, "ws", "reports"))
		briefNames, reportNames := make([]string, 0, len(briefs)), make([]string, 0, len(reports))
		for _, entry := range briefs {
			briefNames = append(briefNames, entry.Name())
		}
		for _, entry := range reports {
			reportNames = append(reportNames, entry.Name())
		}
		t.Fatalf("attempt sidecar missing at %s: %v; briefs=%v reports=%v", sidecar, err, briefNames, reportNames)
	}
	if strings.Contains(stderr.String(), "fakecli: no rule") {
		t.Fatalf("fake Herdr call failed: %s", stderr.String())
	}
}

func TestDispatchSidecarSession(t *testing.T) {
	t.Run("the sidecar records the roster started value as the session", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		composed, ok := dispatchOutputField(t, out, "composed_prompt")
		if !ok {
			t.Fatalf("success JSON omitted composed prompt: %s", out)
		}
		value := mustRead(t, dispatch.DispatchSidecar(composed))
		if !strings.Contains(value, `"session":"now"`) {
			t.Fatalf("sidecar missing the roster started session: %s", value)
		}
	})
	t.Run("an empty roster started leaves no session field", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\tcodex\timplementer\topenai\t0\t\t\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		composed, ok := dispatchOutputField(t, out, "composed_prompt")
		if !ok {
			t.Fatalf("success JSON omitted composed prompt: %s", out)
		}
		value := mustRead(t, dispatch.DispatchSidecar(composed))
		if strings.Contains(value, "session") {
			t.Fatalf("sidecar recorded a session for an empty started: %s", value)
		}
	})
}

func TestDispatchUsageErrorsPortedCases(t *testing.T) {
	// JS: "dispatch: usage errors (missing args, unknown option, brief not found, not in roster)"
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stderr string
	}{
		{name: "missing agent", args: nil, code: 1, stderr: "soho: 1: agent\n"},
		{name: "missing brief", args: []string{"worker"}, code: 1, stderr: "soho: 2: brief.md\n"},
		{name: "unknown option", args: []string{"worker", f.brief, "--not-an-option"}, code: 2, stderr: "soho: dispatch: unknown option --not-an-option\n"},
		{name: "missing file", args: []string{"worker", filepath.Join(f.root, "missing.md")}, code: 2, stderr: "soho: brief not found: " + filepath.Join(f.root, "missing.md") + "\n"},
		{name: "unknown agent", args: []string{"other", f.brief}, code: 3, stderr: "soho: agent 'other' is not in this skill's roster (spawn it first, or pass a name you spawned)\n"},
		// DJA-473: options may come before the agent and the brief.
		{name: "option before the positionals", args: []string{"--for", "anthropic", "other", f.brief}, code: 3, stderr: "soho: agent 'other' is not in this skill's roster (spawn it first, or pass a name you spawned)\n"},
		{name: "option between the positionals", args: []string{"other", "--timeout", "90s", f.brief}, code: 3, stderr: "soho: agent 'other' is not in this skill's roster (spawn it first, or pass a name you spawned)\n"},
		{name: "unknown option before the positionals", args: []string{"--not-an-option", "worker", f.brief}, code: 2, stderr: "soho: dispatch: unknown option --not-an-option\n"},
		{name: "a third positional", args: []string{"worker", f.brief, "extra"}, code: 2, stderr: "soho: dispatch: unexpected argument extra (usage: dispatch <agent> <brief.md> [options])\n"},
		{name: "only options", args: []string{"--no-wait"}, code: 1, stderr: "soho: 1: agent\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := f.run(t, tc.args...)
			if code != tc.code || strings.TrimSpace(out) != "" || stderr != tc.stderr {
				t.Fatalf("code=%d out=%q stderr=%q, want code %d, no stdout, stderr=%q", code, out, stderr, tc.code, tc.stderr)
			}
		})
	}
	t.Run("run positional errors keep the bash text unprefixed", func(t *testing.T) { // Mutation captured: routing a bash positional error through platform.Die adds a duplicate herdr-soho prefix.
		for _, tc := range []struct {
			args []string
			want string
		}{{[]string{"run"}, "soho: 1: role\n"}, {[]string{"run", "implementer"}, "soho: 2: brief.md\n"}} {
			oldErr := platform.Stderr
			var stderr bytes.Buffer
			platform.Stderr = &stderr
			code := Run(tc.args, f.env)
			platform.Stderr = oldErr
			if code != 1 || stderr.String() != tc.want {
				t.Fatalf("args=%v code=%d stderr=%q, want %q", tc.args, code, stderr.String(), tc.want)
			}
		}
	})
}

func TestDispatchWaitEmptyExitErrorRecordsFriction(t *testing.T) { // Mutation captured: letting WaitFor's empty DieError pass through without recording it loses the dispatch friction event.
	f := newDispatchArrivalFixture(t, "working", 1, 1, "$CURRENT_PATHS", "0")
	fixedNow := platform.Now
	platform.Now = func() time.Time { return time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { platform.Now = fixedNow })
	oldWait := dispatchWaitFor
	dispatchWaitFor = func([]string, string, *core.Config, platform.Env, float64, bool, string) int {
		panic(&platform.ExitError{Code: 1})
	}
	t.Cleanup(func() { dispatchWaitFor = oldWait })
	code, _, stderr := f.run(t, "worker", f.brief)
	log, err := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
	if err != nil || code != 1 || !strings.HasSuffix(stderr, "soho: \n") || !strings.Contains(string(log), "error(exit 1)\tdispatch\t\n") {
		t.Fatalf("code=%d stderr=%q friction=%q err=%v", code, stderr, log, err)
	}
}

func TestDispatchForAmendPortedCases(t *testing.T) {
	// JS: "dispatch: --for with --amend is accepted and keeps the --for validations"
	h := newTM3bHarness(t, tm3bFullBrief())
	rolesDir := filepath.Dir(h.role)
	if err := os.WriteFile(filepath.Join(rolesDir, "reviewer.md"), []byte("---\nname: Reviewer\nmode: review\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"reviewer\tw0test:p0r\tclaude\treviewer\tanthropic\t0\t" + h.root + "\tnow\tclaude-sonnet-4\ttask\treviewer\t\t\t\t\n" +
		"author\tw0test:p0o\tcodex\timplementer\topenai\t0\t" + h.root + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(h.ws, "reports", "reviewer-previous.md")
	if err := os.MkdirAll(filepath.Dir(previous), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous, []byte("previous report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "last-report-reviewer"), []byte(previous+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("--for with --amend is accepted and proceeds with the amendment", func(t *testing.T) {
		code, out, stderr := h.run(t, "reviewer", h.brief, "--amend", "--for", "author", "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) || !strings.Contains(out, `"amend":true`) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if strings.Contains(stderr, "--for needs a plain dispatch") {
			t.Fatalf("amend with --for still rejected: %s", stderr)
		}
	})
	t.Run("--for with --amend and an unknown spec still exits 2", func(t *testing.T) {
		defer func() {
			v := recover()
			x, ok := v.(*platform.ExitError)
			if !ok || x.Code != 2 || !strings.Contains(x.Msg, "not an agent in the roster") {
				t.Fatalf("panic=%#v", v)
			}
		}()
		h.run(t, "reviewer", h.brief, "--amend", "--for", "nosuchspec", "--no-wait")
	})
	t.Run("--for with --amend on a non-reviewer role still exits 2", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(h.ws, "last-report-author"), []byte(previous+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			v := recover()
			x, ok := v.(*platform.ExitError)
			if !ok || x.Code != 2 || !strings.Contains(x.Msg, "applies to a reviewer dispatch") {
				t.Fatalf("panic=%#v", v)
			}
		}()
		h.run(t, "author", h.brief, "--amend", "--for", "reviewer", "--no-wait")
	})
}

func noWaitDispatchEnv(t *testing.T, bin, state, roles, root string) platform.Env {
	t.Helper()
	return isolatedUserEnv(platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HERDR_ENV": "1", "SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "SOHO_ROLES": roles, "SOHO_SKILL_DIR": testSkillDir(t), "SOHO_PROMPT_CHECK_SECONDS": "0", "SOHO_PROMPT_SETTLE_SECONDS": "0", "SOHO_BRIEF_LINT": "off", "TMPDIR": root})
}
