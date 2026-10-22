package spawn

// The cursor startup gate on every platform (issue #59): the spawn never
// claims ready while the verified workspace trust dialog is on the visible
// screen, and a blank, unreadable or launch-only final screen is refused
// as unverified — both take the existing blocked path (blocked_at_startup,
// the screen printed, exit 7) with the shared startup_evidence. The
// fixtures run the real CmdSpawn entry point with a fake herdr; the
// platform seam (startup.CurrentPlatform) simulates the host platform on
// portable hosts. The screen texts are the verified native dialog and the
// historical launch-only screen of the issue.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// cursorNativeTrustScreen is the verified cursor Workspace Trust Required
// dialog (cursor-agent 2026.10.01 on Windows, herdr 0.9.1).
func cursorNativeTrustScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/cursor-trust.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cursorHistoricalLaunchScreen is the issue #59 screen: the TUI never
// drew and the pane held only the PowerShell prompt and the launch line.
const cursorHistoricalLaunchScreen = "PS C:\\Users\\dev\\pinar> cursor-agent --trust --force --approve-mcps --model grok-4.7-high\n" +
	"PS C:\\Users\\dev\\pinar>\n"

const cursorIdleState = `{"result":{"agent":{"agent_status":"idle"}}}`

// setStartupPlatform points the shared gate at a simulated platform for
// the duration of the test (the production code uses platform.Current).
func setStartupPlatform(t *testing.T, goos string) {
	t.Helper()
	old := startup.CurrentPlatform
	startup.CurrentPlatform = func() string { return goos }
	t.Cleanup(func() { startup.CurrentPlatform = old })
}

// cursorFreshRules is the fake herdr for a fresh cursor spawn: the agent
// read of the start window and the gate serves `screen` (or the read
// failure), the state probes report idle, and the rest of the spawn's
// calls take the fresh-spawn defaults.
func cursorFreshRules(screen string, failedRead bool) []fakecli.Rule {
	readRule := fakecli.Rule{Argv: grokScreenRead("implementer"), Stdout: screen}
	if failedRead {
		readRule.Stdout = ""
		readRule.Code = 1
		readRule.Stderr = `{"error":{"code":"read_failed","message":"the fake read failed"}}`
	}
	return []fakecli.Rule{
		readRule,
		{Argv: []string{"agent", "get", "implementer"}, Stdout: cursorIdleState},
	}
}

// assertNoInputIntoTheDialog reports when the spawn typed or sent a key to
// the pane: the gate must leave the dialog untouched.
func assertNoInputIntoTheDialog(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("spawn sent keys into the dialog: %#v", got)
	}
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "send-text" {
			t.Fatalf("spawn typed into the dialog: %#v", call)
		}
	}
}

// visibleReadCount counts the pane's visible screen reads in the call log.
func visibleReadCount(calls []fakecli.Call) int {
	n := 0
	want := strings.Join(grokScreenRead("implementer"), "\x00")
	for _, call := range calls {
		if strings.Join(call.Argv, "\x00") == want {
			n++
		}
	}
	return n
}

// assertCursorSpawnEvidence checks the spawn JSON carries the shared
// startup_evidence (state, reason, source=visible) — and nothing else of
// the screen (no raw screen content or command arguments in the field).
func assertCursorSpawnEvidence(t *testing.T, stdout, wantState, wantReason string) {
	t.Helper()
	for _, want := range []string{
		`"startup_evidence"`,
		`"state": "` + wantState + `"`,
		`"reason": "` + wantReason + `"`,
		`"source": "visible"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("spawn JSON lacks %s:\n%s", want, stdout)
		}
	}
}

func TestSpawnCursorWindowsTrustDialogBlocksAtStartup(t *testing.T) {
	// A fresh cursor on Windows showing the verified trust dialog must not
	// be claimed ready: the window stops immediately on the dialog, the
	// JSON is blocked_at_startup with the evidence, the screen is printed
	// and the exit is 7 — nothing is typed or sent.
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append(cursorFreshRules(cursorNativeTrustScreen(t), false), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "workspace-trust")
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if !strings.Contains(stderr, "workspace trust dialog") {
		t.Fatalf("the precise warning is missing: %q", stderr)
	}
	assertNoInputIntoTheDialog(t, calls)
	roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil || !strings.Contains(string(roster), "implementer") {
		t.Fatalf("the blocked worker is not registered: roster=%q err=%v", roster, err)
	}
}

func TestSpawnCursorWindowsLaunchOnlyBlocksAtStartup(t *testing.T) {
	// The historical screen (only the PowerShell prompt and the launch
	// line) is unverified: the window retries the launch-only screen until
	// the five-second window ends (more than one read — the old code ended
	// the window on the first probe), then the start is refused with the
	// launch-only evidence.
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append(cursorFreshRules(cursorHistoricalLaunchScreen, false), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "launch-only")
	if n := visibleReadCount(calls); n < 2 {
		t.Fatalf("the window did not retry the launch-only screen: %d reads", n)
	}
	if !strings.Contains(stderr, "shell prompt/launch line") {
		t.Fatalf("the precise warning is missing: %q", stderr)
	}
	assertNoInputIntoTheDialog(t, calls)
}

func TestSpawnCursorWindowsEmptyScreenBlocksAtStartup(t *testing.T) {
	// A visible read that succeeds with an empty screen (the TUI never
	// drew) is unverified with the empty-screen evidence: the window
	// retries the blank screen until it ends, then the start is refused.
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append(cursorFreshRules("", false), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "empty-screen")
}

func TestSpawnCursorWindowsReadFailedBlocksAtStartup(t *testing.T) {
	// A visible read that fails (the screen is unreadable) is unverified
	// with the screen-unavailable evidence — not the empty-screen one: the
	// fresh read after the window distinguishes the two.
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append(cursorFreshRules("", true), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "screen-unavailable")
}

func TestSpawnCursorWindowsDelayedDrawBecomesReady(t *testing.T) {
	// The TUI draws late: the first visible read of the window is the
	// launch-only screen, the second (one poll later) shows the TUI. The
	// window keeps observing past the first probe and ends ready with the
	// observed evidence — exactly two visible reads prove the retry.
	setStartupPlatform(t, "win32")
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("implementer"), Call: 1, Stdout: cursorHistoricalLaunchScreen},
		{Argv: grokScreenRead("implementer"), Call: 2, Stdout: "cursor ready\n❯ booting\n"},
		{Argv: grokScreenRead("implementer"), Stdout: "cursor ready\n❯ booting\n"},
		{Argv: []string{"agent", "get", "implementer"}, Stdout: cursorIdleState},
	}
	f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "visible-agent-output")
	if n := visibleReadCount(calls); n != 2 {
		t.Fatalf("the window read %d times, want 2 (launch-only, then the drawn TUI)", n)
	}
	if strings.Contains(stderr, "startup is not ready") {
		t.Fatalf("the delayed draw was refused: %q", stderr)
	}
}

func TestSpawnCursorWindowsNormalVisibleUIReady(t *testing.T) {
	// A normal visible UI (no brittle banner demanded) on the first probe
	// is observed and ready, with the evidence recorded — one read only.
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append(cursorFreshRules("cursor ready\n❯ idle\n", false), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "visible-agent-output")
	if n := visibleReadCount(calls); n != 1 {
		t.Fatalf("the window read %d times, want 1 (the drawn TUI ends it)", n)
	}
}

func TestSpawnCursorNonWindowsTrustDialogBlocksAtStartup(t *testing.T) {
	// The gate applies on every platform, not only Windows: the same cursor
	// dialog on macOS (darwin, the platform of the original report) is
	// refused with the same result — blocked_at_startup, the screen
	// printed, exit 7, no input — with the shared evidence.
	setStartupPlatform(t, "darwin")
	f := newSpawnFixture(t, append(cursorFreshRules(cursorNativeTrustScreen(t), false), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "workspace-trust")
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if !strings.Contains(stderr, "workspace trust dialog") {
		t.Fatalf("the precise warning is missing: %q", stderr)
	}
	assertNoInputIntoTheDialog(t, calls)
}

func TestSpawnNonCursorWindowsGateOff(t *testing.T) {
	// Control: a non-cursor kind on Windows keeps its behavior — the same
	// TUI screen is ready with no evidence field.
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append([]fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: "grok ready\n"}}, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if strings.Contains(stdout, "startup_evidence") {
		t.Fatalf("the non-cursor spawn carries the evidence field: %q", stdout)
	}
}

// cursorReuseRow records the default auto capability of a cursor worker in
// the fixture's cwd, so these tests reach the reused-session startup checks.
func cursorReuseRow(name, wdir string) string {
	return strings.Join([]string{name, "p-" + name, "cursor", "implementer", "xai", "1", wdir, "now", "", "auto", "implementer", "", "", "", "xhigh"}, "\t")
}

func cursorReuseFixture(t *testing.T, screen string, failedRead bool) (spawnFixture, []fakecli.Rule) {
	readRule := fakecli.Rule{Argv: grokScreenRead("implementer"), Stdout: screen}
	if failedRead {
		readRule.Stdout = ""
		readRule.Code = 1
		readRule.Stderr = `{"error":{"code":"read_failed","message":"the fake read failed"}}`
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "implementer"}, Stdout: cursorIdleState},
		readRule,
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "50"
	f.roster(t, cursorReuseRow("implementer", f.cwd))
	return f, rules
}

func TestSpawnCursorWindowsReuseTrustDialogBlocks(t *testing.T) {
	// A reused cursor session on Windows showing the verified dialog is
	// not claimed ready: the reuse decision stands (the row matches), but
	// the spawn refuses with the same evidence and result as a fresh start
	// — blocked_at_startup, the screen printed, exit 7, no new pane.
	setStartupPlatform(t, "win32")
	f, _ := cursorReuseFixture(t, cursorNativeTrustScreen(t), false)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "workspace-trust")
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if !strings.Contains(stderr, "workspace trust dialog") {
		t.Fatalf("the precise warning is missing: %q", stderr)
	}
	assertNoStart(t, calls)
	assertNoInputIntoTheDialog(t, calls)
}

func TestSpawnCursorWindowsReuseEmptyScreenBlocks(t *testing.T) {
	// A reused cursor session whose visible screen is empty (the TUI never
	// drew) is refused as unverified with the empty-screen evidence.
	setStartupPlatform(t, "win32")
	f, _ := cursorReuseFixture(t, "", false)
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "empty-screen")
}

func TestSpawnCursorWindowsReuseNormalScreenReady(t *testing.T) {
	// Control: a reused cursor session with a normal visible screen keeps
	// the reuse result (ready, the retained-context warning) unchanged.
	setStartupPlatform(t, "win32")
	f, _ := cursorReuseFixture(t, "cursor ready\n❯ idle\n", false)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertNoStart(t, calls)
}

func TestSpawnCursorNonWindowsReuseDialogBlocksAtStartup(t *testing.T) {
	// The gate applies on every platform: the same dialog on a reused cursor
	// session off-Windows is refused with the same evidence and result as a
	// fresh start — blocked_at_startup, the screen printed, exit 7, no new
	// pane.
	setStartupPlatform(t, "linux")
	f, _ := cursorReuseFixture(t, cursorNativeTrustScreen(t), false)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	assertCursorSpawnEvidence(t, stdout, "idle", "workspace-trust")
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if !strings.Contains(stderr, "workspace trust dialog") {
		t.Fatalf("the precise warning is missing: %q", stderr)
	}
	assertNoStart(t, calls)
	assertNoInputIntoTheDialog(t, calls)
}

func TestSpawnCursorWindowsWrappedLaunchAndNarrowTrust(t *testing.T) {
	for _, tc := range []struct{ name, screen, reason string }{
		{"wrapped launch", "old shell output\nPS C:\\dev> if((Get-Command cu\nrsor-agent.cmd -ErrorAction SilentlyContinue).CommandType -eq 'ExternalScript'){& cursor-agent.cmd '--mode\nl' grok-4.7-high}else{Start-Process -FilePath cursor-agent.cmd -ArgumentList '--model grok-4.7-high' -NoNe\nwWindow -Wait}\n", "launch-only"},
		{"narrow dialog", "│ Do you trust the contents of this │\n│ directory? │\n│ C:\\dev\\project │\n│ ▶ [a] Trust this workspace │\n│ [q] Quit │\n", "workspace-trust"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setStartupPlatform(t, "win32")
			f := newSpawnFixture(t, append(cursorFreshRules(tc.screen, false), freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			f.env["SOHO_WAIT_POLL_MS"] = "50"
			code, out, errText, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
			if code != 7 || !strings.Contains(out, `"status": "blocked_at_startup"`) {
				t.Fatalf("code=%d out=%s err=%s", code, out, errText)
			}
			assertCursorSpawnEvidence(t, out, "idle", tc.reason)
			assertNoInputIntoTheDialog(t, calls)
		})
	}
}

func TestSpawnCursorWindowsClippedTrustRefusesReadiness(t *testing.T) {
	b, err := os.ReadFile("testdata/cursor-trust-clipped.txt")
	if err != nil {
		t.Fatal(err)
	}
	setStartupPlatform(t, "win32")
	f := newSpawnFixture(t, append(cursorFreshRules(string(b), false), freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	code, out, errText, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("code=%d out=%s err=%s", code, out, errText)
	}
	assertCursorSpawnEvidence(t, out, "idle", "screen-incomplete")
	assertNoInputIntoTheDialog(t, calls)
}

func TestSpawnCursorWindowsReuseClippedTrustRefuses(t *testing.T) {
	setStartupPlatform(t, "win32")
	b, err := os.ReadFile("testdata/cursor-trust-clipped.txt")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := cursorReuseFixture(t, string(b), false)
	code, out, errText, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 || !strings.Contains(out, `"reused": true`) {
		t.Fatalf("code=%d out=%s err=%s", code, out, errText)
	}
	assertCursorSpawnEvidence(t, out, "idle", "screen-incomplete")
	assertNoStart(t, calls)
	assertNoInputIntoTheDialog(t, calls)
}
