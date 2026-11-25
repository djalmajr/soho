package cli

// The cursor startup preflight and the no-input rechecks of the dispatch
// (issue #59), on every platform: a blocked (the verified workspace trust
// dialog) or unverified (blank, unreadable or launch-only screen) cursor
// start refuses the dispatch with the existing blocked result (exit 7,
// the cause naming the reason) before any prompt, compaction or
// report/sidecar mutation, and nothing is typed. The fixtures run the real
// CLI with a fake herdr; the platform seam (startup.CurrentPlatform)
// simulates the host platform on portable hosts. The dialog screen is the
// verified native text of cursor-agent 2026.10.01 and the launch-only
// screen is the historical one from the issue.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// cursorTrustScreen is the verified cursor Workspace Trust Required dialog.
func cursorTrustScreen(t *testing.T) string {
	t.Helper()
	if packageDirErr != nil {
		t.Fatal(packageDirErr)
	}
	b, err := os.ReadFile(filepath.Join(packageDir, "testdata", "cursor-trust.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cursorLaunchOnlyScreen is the historical launch-only screen of the issue.
const cursorLaunchOnlyScreen = "PS C:\\Users\\dev\\pinar> cursor-agent --trust --force --approve-mcps --model grok-4.7-high\n" +
	"PS C:\\Users\\dev\\pinar>\n"

// cursorTUIScreen is a normal, non-brittle visible UI.
const cursorTUIScreen = "cursor ready\n❯ booting\n"

func TestDispatchCursorMCPApprovalBlocked(t *testing.T) {
	for _, flag := range []string{"", "--resend", "--amend", "--compact"} {
		t.Run("flag="+flag, func(t *testing.T) {
			f := newCursorDispatchFixture(t, "cursor", "MCP Server Approval Required\nproject server list\n[a] Approve all   [c] Continue   [q] Quit\n", "0", false)
			if flag == "--amend" {
				if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte("prior report\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			dispatchAssertRefusal(t, f, "mcp-approval", "idle", "approval of MCP servers", flag)
			calls := dispatchCalls(t, f)
			prompts, keys := 0, 0
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" {
					switch call.Argv[1] {
					case "prompt":
						prompts++
					case "send-keys":
						keys++
					}
				}
			}
			if prompts != 0 || keys != 0 {
				t.Fatalf("agent prompt=%d send-keys=%d; calls=%#v", prompts, keys, calls)
			}
			dispatchAssertNoTaskArtifacts(t, f, flag == "--amend")
		})
	}
}

func TestDispatchCursorMCPApprovalHistoryDelivers(t *testing.T) {
	for _, screen := range []string{
		cursorTUIScreen,
		"MCP Server Approval Required\n",
		"MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\n" + cursorTUIScreen,
		"MCP Server Approval Required\n" + cursorTUIScreen,
		"[a] Approve all   [c] Continue   [q] Quit\n" + cursorTUIScreen,
	} {
		f := newCursorDispatchFixture(t, "cursor", screen, "0", false)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("screen=%q exit=%d stdout=%q stderr=%q", screen, code, out, errText)
		}
		prompts := 0
		for _, call := range dispatchCalls(t, f) {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
				prompts++
			}
		}
		if prompts != 1 {
			t.Fatalf("screen=%q agent prompt=%d, want 1", screen, prompts)
		}
	}
}

func TestDispatchCursorMCPApprovalLandsBeforeInput(t *testing.T) {
	read := []string{"agent", "read", "worker", "--source", "visible"}
	// A safe first read and approval on all subsequent reads exercise
	// the recheck after the task is claimed.
	rules := []fakecli.Rule{
		{Argv: read, ArgvPrefix: true, Call: 1, Stdout: cursorTUIScreen},
		{Argv: read, ArgvPrefix: true, Stdout: "MCP Server Approval Required\n[a] Approve all\n"},
	}
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0", rules...)
	roster := "worker\tw0test:p0a\tcursor\timplementer\tunknown\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	dispatchAssertRefusal(t, f, "mcp-approval", "idle", "approval of MCP servers", "")
	dispatchAssertNoTaskArtifactsButBriefs(t, f)
}

// setDispatchPlatform points the shared gate at a simulated platform for
// the duration of the test (the production code uses platform.Current).
func setDispatchPlatform(t *testing.T, goos string) {
	t.Helper()
	old := startup.CurrentPlatform
	startup.CurrentPlatform = func() string { return goos }
	t.Cleanup(func() { startup.CurrentPlatform = old })
}

// newCursorDispatchFixture is the standard arrival fixture (an idle agent
// before the send) with the worker row pointed at kind, the visible screen
// rule serving screen (or the read failure) and the extra rules appended.
func newCursorDispatchFixture(t *testing.T, kind, screen, settle string, failedRead bool, extra ...fakecli.Rule) *dispatchArrivalFixture {
	t.Helper()
	visible := fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen}
	if failedRead {
		visible.Stdout = ""
		visible.Code = 1
		visible.Stderr = `{"error":{"code":"read_failed","message":"the fake read failed"}}`
	}
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "", settle, append([]fakecli.Rule{visible}, extra...)...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\tw0test:p0a\t" + kind + "\timplementer\t" + kinds.KindFamily(kind) + "\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatalf("roster: %v", err)
	}
	return f
}

// dispatchCalls reads the fake herdr's call log.
func dispatchCalls(t *testing.T, f *dispatchArrivalFixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatalf("the fake call log is unreadable: %v", err)
	}
	return calls
}

// dispatchAssertNoInput reports when the dispatch typed or sent a key to
// the agent: the startup refusal must leave the pane untouched.
func dispatchAssertNoInput(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && (call.Argv[1] == "prompt" || call.Argv[1] == "send-keys") {
			t.Fatalf("the dispatch typed into the dialog: %#v", call)
		}
	}
}

// dispatchAssertNoTaskArtifacts reports when the refusal touched the task
// state: no task-report pointer, no last-report file (unless the test
// pre-created it, for --amend), no composed brief or sidecar.
func dispatchAssertNoTaskArtifacts(t *testing.T, f *dispatchArrivalFixture, allowLastReport bool) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.state, "ws", "task-report-worker.json")); err == nil {
		t.Fatalf("the refusal wrote the task-report pointer")
	}
	if !allowLastReport {
		if _, err := os.Stat(filepath.Join(f.state, "ws", "last-report-worker")); err == nil {
			t.Fatalf("the refusal wrote the last-report")
		}
	}
	briefs, err := os.ReadDir(filepath.Join(f.state, "ws", "briefs"))
	if err != nil {
		t.Fatalf("briefs: %v", err)
	}
	if len(briefs) != 0 {
		t.Fatalf("the refusal wrote brief artifacts: %v", briefs)
	}
}

// dispatchAssertRefusal runs the dispatch under flag and checks the
// existing blocked result (exit 7) with the cause and the shared
// startup_evidence, the precise warning and no input into the pane.
func dispatchAssertRefusal(t *testing.T, f *dispatchArrivalFixture, wantReason, wantState, wantWarning, flag string) {
	t.Helper()
	args := []string{"worker", f.brief, "--no-wait"}
	if flag != "" {
		args = append(args, flag)
	}
	code, out, errText := f.run(t, args...)
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	for _, want := range []string{
		`"wait_status":"blocked"`,
		`"cause":"cursor-startup-` + wantReason + `"`,
		`"report_exists":false`,
		`"startup_evidence":{"state":"` + wantState + `","reason":"` + wantReason + `","source":"visible"}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dispatch JSON lacks %s:\n%s", want, out)
		}
	}
	for _, want := range []string{"startup is not ready", "nothing was typed or sent", wantWarning} {
		if !strings.Contains(errText, want) {
			t.Fatalf("the precise warning lacks %q: %q", want, errText)
		}
	}
	dispatchAssertNoInput(t, dispatchCalls(t, f))
}

func TestDispatchCursorWindowsTrustDialogRefusesPreflight(t *testing.T) {
	// The verified dialog at the preflight: the dispatch ends blocked
	// (exit 7) with the evidence, no prompt and no task artifact.
	setDispatchPlatform(t, "win32")
	f := newCursorDispatchFixture(t, "cursor", cursorTrustScreen(t), "0", false)
	dispatchAssertRefusal(t, f, "workspace-trust", "idle", "cursor workspace trust dialog", "")
	dispatchAssertNoTaskArtifacts(t, f, false)
}

func TestDispatchCursorWindowsLaunchOnlyRefusesPreflight(t *testing.T) {
	// The launch-only screen at the preflight: refused with the launch-only
	// reason — the pane is unverified, not the trust dialog.
	setDispatchPlatform(t, "win32")
	f := newCursorDispatchFixture(t, "cursor", cursorLaunchOnlyScreen, "0", false)
	dispatchAssertRefusal(t, f, "launch-only", "idle", "shell prompt/launch line", "")
	dispatchAssertNoTaskArtifacts(t, f, false)
}

func TestDispatchCursorWindowsEmptyScreenRefusesPreflight(t *testing.T) {
	// A visible read that succeeds with an empty screen at the preflight:
	// the empty-screen reason distinguishes the blank TUI from the
	// unreadable one.
	setDispatchPlatform(t, "win32")
	f := newCursorDispatchFixture(t, "cursor", "", "0", false)
	dispatchAssertRefusal(t, f, "empty-screen", "idle", "visible screen is empty", "")
	dispatchAssertNoTaskArtifacts(t, f, false)
}

func TestDispatchCursorWindowsReadFailedRefusesPreflight(t *testing.T) {
	// A visible read that fails at the preflight: the screen-unavailable
	// reason — the screen could not be read, it is not blank.
	setDispatchPlatform(t, "win32")
	f := newCursorDispatchFixture(t, "cursor", "", "0", true)
	dispatchAssertRefusal(t, f, "screen-unavailable", "idle", "screen could not be read", "")
	dispatchAssertNoTaskArtifacts(t, f, false)
}

func TestDispatchCursorWindowsQueueResendAmendStillRefuse(t *testing.T) {
	// The refusal is independent of --queue, --resend and --amend: the same
	// dialog refuses under each flag, with no task artifact under any of
	// them.
	for _, flag := range []string{"--queue", "--resend", "--amend"} {
		setDispatchPlatform(t, "win32")
		f := newCursorDispatchFixture(t, "cursor", cursorTrustScreen(t), "0", false)
		if flag == "--amend" {
			// The amend needs an earlier last-report: it must survive the
			// refusal untouched.
			if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte("the prior last-report\n"), 0o600); err != nil {
				t.Fatalf("last-report: %v", err)
			}
		}
		dispatchAssertRefusal(t, f, "workspace-trust", "idle", "cursor workspace trust dialog", flag)
		dispatchAssertNoTaskArtifacts(t, f, flag == "--amend")
		if flag == "--amend" {
			data, err := os.ReadFile(filepath.Join(f.state, "ws", "last-report-worker"))
			if err != nil || string(data) != "the prior last-report\n" {
				t.Fatalf("the amend refusal touched the last-report: %q err=%v", data, err)
			}
		}
	}
}

func TestDispatchCursorWindowsNonTargetKindSkipsCheck(t *testing.T) {
	// Control: the same dialog on a non-cursor kind (grok on Windows) skips
	// the gate entirely — the dispatch proceeds to the send and submits,
	// without the evidence field or the refusal warning. The agent works
	// normally after the send (the seq rise is its arrival).
	setDispatchPlatform(t, "win32")
	f := newCursorDispatchFixture(t, "grok", cursorTrustScreen(t), "0", false, workingArrivalRules()...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	if !strings.Contains(out, `"wait_status":"submitted"`) {
		t.Fatalf("dispatch JSON=%q", out)
	}
	if strings.Contains(out, "startup_evidence") || strings.Contains(errText, "startup is not ready") {
		t.Fatalf("the non-cursor dispatch ran the gate: stdout=%q stderr=%q", out, errText)
	}
}

func TestDispatchCursorNonWindowsTrustDialogRefuses(t *testing.T) {
	// The gate applies on every platform: the same cursor dialog off-Windows
	// (linux) is refused at the preflight with the shared evidence — no
	// prompt, no task artifact.
	setDispatchPlatform(t, "linux")
	f := newCursorDispatchFixture(t, "cursor", cursorTrustScreen(t), "0", false)
	dispatchAssertRefusal(t, f, "workspace-trust", "idle", "cursor workspace trust dialog", "")
	dispatchAssertNoTaskArtifacts(t, f, false)
}

// workingArrivalRules is the post-send state of a normal agent: idle (seq
// 1) before the send, working (seq 2) at the arrival probes — the seq rise
// is the arrival proof the check window needs.
func workingArrivalRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
	}
}

func TestDispatchCursorWindowsSafeScreenDelivers(t *testing.T) {
	// The normal path under the gate: the TUI screen is observed, the
	// prompt is sent once and the dispatch is submitted — the preflight and
	// the rechecks spend only their shared reads on the safe screen. The
	// state probes: the preflight, D25 and the recheck see idle (seq 1),
	// the arrival probes see the agent working (seq 2).
	setDispatchPlatform(t, "win32")
	idle1 := `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`
	working2 := `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: idle1},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: idle1},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: idle1},
		{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: working2},
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: working2},
	}
	f := newCursorDispatchFixture(t, "cursor", cursorTUIScreen, "0", false, extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	if !strings.Contains(out, `"wait_status":"submitted"`) {
		t.Fatalf("dispatch JSON=%q", out)
	}
	prompts := 0
	for _, call := range dispatchCalls(t, f) {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("the safe screen sent %d prompts, want 1", prompts)
	}
}

func TestDispatchCursorWindowsSafeScreenBecomesTrustNoInput(t *testing.T) {
	// A safe screen at the preflight that turns into the trust dialog
	// before the actual input (the dialog lands during the settling): the
	// recheck right before the AgentPrompt refuses — no prompt, no key,
	// exit 7 with the cause. This is the recheck, not the preflight: the
	// claim already happened, so the composed brief survives while the
	// task pointer and the last-report are restored to their pre-dispatch
	// absence (the preflight refusal would leave the briefs dir empty).
	setDispatchPlatform(t, "win32")
	idle1 := `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`
	// The visible reads, in call order (Call N matches the Nth call of the
	// same argv, the log counting the current call): the preflight read
	// sees the safe TUI, every later read — including the recheck's —
	// sees the dialog that landed in between.
	readArgv := []string{"agent", "read", "worker", "--source", "visible"}
	rxtra := []fakecli.Rule{
		{Argv: readArgv, Call: 1, ArgvPrefix: true, Stdout: cursorTUIScreen},
		{Argv: readArgv, ArgvPrefix: true, Stdout: cursorTrustScreen(t)},
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: idle1},
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: idle1},
	}
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0", rxtra...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\tw0test:p0a\tcursor\timplementer\txai\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatalf("roster: %v", err)
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	for _, want := range []string{
		`"wait_status":"blocked"`,
		`"cause":"cursor-startup-workspace-trust"`,
		`"startup_evidence":{"state":"idle","reason":"workspace-trust","source":"visible"}`,
		`"report_exists":false`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dispatch JSON lacks %s:\n%s", want, out)
		}
	}
	if !strings.Contains(errText, "nothing was typed or sent") {
		t.Fatalf("the precise warning is missing: %q", errText)
	}
	calls := dispatchCalls(t, f)
	dispatchAssertNoInput(t, calls)
	// The refusal is the recheck's: the claim wrote the composed brief
	// (it survives, named in composed_prompt) while the task pointer and
	// the last-report are restored to their pre-dispatch absence.
	if !strings.Contains(out, `"composed_prompt":"`) || strings.Contains(out, `"composed_prompt":""`) {
		t.Fatalf("the recheck refusal keeps the claimed composed prompt: %s", out)
	}
	briefs, err := os.ReadDir(filepath.Join(f.state, "ws", "briefs"))
	if err != nil || len(briefs) == 0 {
		t.Fatalf("the recheck refusal lost the claimed brief: %v %v", briefs, err)
	}
	if _, err := os.Stat(filepath.Join(f.state, "ws", "task-report-worker.json")); err == nil {
		t.Fatalf("the refusal left the task-report pointer the claim wrote")
	}
	if _, err := os.Stat(filepath.Join(f.state, "ws", "last-report-worker")); err == nil {
		t.Fatalf("the refusal left the last-report the claim wrote")
	}
}

func TestDispatchCursorWindowsSafeScreenBecomesUnverifiedNoInput(t *testing.T) {
	for _, tc := range []struct {
		reason, screen string
		readCode       int
	}{
		{"launch-only", "old shell output\nPS C:\\dev> cursor-agent --model grok-4.7-high\n", 0},
		{"screen-unavailable", "", 1},
		{"empty-screen", "", 0},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			setDispatchPlatform(t, "win32")
			argv := []string{"agent", "read", "worker", "--source", "visible"}
			rules := []fakecli.Rule{
				{Argv: argv, Call: 1, ArgvPrefix: true, Stdout: cursorTUIScreen},
				{Argv: argv, ArgvPrefix: true, Stdout: tc.screen, Code: tc.readCode},
			}
			f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0", rules...)
			roster := "worker\tw0test:p0a\tcursor\timplementer\txai\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"
			if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 7 || !strings.Contains(out, `"cause":"cursor-startup-`+tc.reason+`"`) {
				t.Fatalf("code=%d out=%s err=%s", code, out, errText)
			}
			dispatchAssertNoInput(t, dispatchCalls(t, f))
			for _, name := range []string{"task-report-worker.json", "last-report-worker"} {
				if _, err := os.Stat(filepath.Join(f.state, "ws", name)); err == nil {
					t.Fatalf("refusal retained %s", name)
				}
			}
			if strings.Contains(out, `"composed_prompt":""`) {
				t.Fatal("expected the post-claim refusal")
			}
		})
	}
}

func TestDispatchCursorWindowsClippedTrustRefusesNoInput(t *testing.T) {
	setDispatchPlatform(t, "win32")
	b, err := os.ReadFile(filepath.Join(packageDir, "testdata", "cursor-trust-clipped.txt"))
	if err != nil {
		t.Fatal(err)
	}
	f := newCursorDispatchFixture(t, "cursor", string(b), "0", false)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 || !strings.Contains(out, `"cause":"cursor-startup-screen-incomplete"`) {
		t.Fatalf("code=%d out=%s err=%s", code, out, errText)
	}
	dispatchAssertNoInput(t, dispatchCalls(t, f))
	dispatchAssertNoTaskArtifacts(t, f, false)
}

func TestDispatchAgyLoginAdmission(t *testing.T) {
	const login = "Welcome to the Antigravity CLI. You are currently not signed in.\nSelect login method:\n> 1. Google OAuth\n"
	const ready = "Agy ready\n❯ "
	readVisible := []string{"agent", "read", "worker", "--source", "visible"}
	for _, tc := range []struct {
		name, flag  string
		blockAt     int
		check       bool
		wantPrompts int
	}{
		{"preflight", "", 1, false, 0},
		{"queue", "--queue", 1, false, 0},
		{"resend", "--resend", 1, false, 0},
		{"amend", "--amend", 1, false, 0},
		{"after claim", "--queue", 2, false, 0},
		{"before retry", "", 5, true, 1},
		{"unreadable", "", -1, false, 1},
		{"stale transcript", "", 0, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []fakecli.Rule{}
			if tc.blockAt > 0 {
				rules = append(rules, fakecli.Rule{Argv: readVisible, Call: tc.blockAt, Stdout: login})
			} else if tc.blockAt < 0 {
				rules = append(rules, fakecli.Rule{Argv: readVisible, Code: 1, Stderr: "fixture read failed"})
			}
			rules = append(rules, fakecli.Rule{Argv: readVisible, Stdout: login + ready})
			f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0", rules...)
			sd := filepath.Join(f.state, "ws")
			roster := "worker\tw0test:p0a\tagy\timplementer\tgoogle\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
				t.Fatal(err)
			}
			if !tc.check {
				f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			}
			priorReport := filepath.Join(sd, "reports", "prior.md")
			priorPointer := jsonjs.O("version", 1, "task_report", priorReport, "current", priorReport, "history", []any{}, "pane", "w0test:p0a")
			preserved := map[string]string{
				taskreport.TaskReportPointerPath(sd, "worker"):    jsonjs.Stringify(priorPointer) + "\n",
				filepath.Join(sd, "last-report-worker"):           priorReport + "\n",
				filepath.Join(sd, "wait", "worker.queued"):        "previous queue\n",
				filepath.Join(sd, "wait", "worker.not-received"):  "previous delivery\n",
				filepath.Join(sd, "wait", "worker.submit-failed"): "previous failure\n",
			}
			for path, value := range preserved {
				if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"worker", f.brief, "--no-wait"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			code, out, warnings := f.run(t, args...)
			if tc.blockAt > 0 {
				if code != 7 || !strings.Contains(out, `"wait_status":"blocked"`) || !strings.Contains(out, `"cause":"agy-startup-sign-in-required"`) || !strings.Contains(out, `"startup_evidence":{"state":"idle","reason":"sign-in-required","source":"visible"}`) || !strings.Contains(warnings, "Agy requires Google OAuth sign-in") {
					t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, warnings)
				}
				for path, want := range preserved {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != want {
						t.Fatalf("prior %s = %q, err=%v, want %q", filepath.Base(path), got, err, want)
					}
				}
				if tc.blockAt == 1 {
					entries, err := os.ReadDir(filepath.Join(sd, "briefs"))
					if err != nil || len(entries) != 0 {
						t.Fatalf("initial refusal wrote brief artifacts: %v, %v", entries, err)
					}
				}
			} else if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) || strings.Contains(out, "startup_evidence") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, warnings)
			}
			prompts := 0
			for _, call := range dispatchCalls(t, f) {
				if len(call.Argv) < 2 {
					continue
				}
				switch strings.Join(call.Argv[:2], " ") {
				case "agent prompt":
					prompts++
				case "agent send-keys", "pane send-keys", "pane send-text":
					t.Fatalf("unexpected input: %#v", call)
				}
			}
			if prompts != tc.wantPrompts {
				t.Fatalf("prompt calls=%d, want %d", prompts, tc.wantPrompts)
			}
		})
	}
}
