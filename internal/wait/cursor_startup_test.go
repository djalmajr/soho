package wait

// The cursor startup guard of the wait on every platform (issue #59): the
// verified workspace trust dialog returns the existing blocked result (the
// wait prints the dialog and exits 7) with no approval key — not the
// auto-approve default and not the not-received Enter retry — whatever
// herdr classifies the state (native idle or blocked). A launch-only or
// blank screen does not block the wait: the guard acts only on the active
// dialog. The fixtures run the real ProbeAgent with a fake herdr; the
// platform seam (startup.CurrentPlatform) simulates the host platform on
// portable hosts. The dialog screen is the verified native text of
// cursor-agent 2026.10.01.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/startup"
)

// waitTrustScreen is the verified cursor Workspace Trust Required dialog.
func waitTrustScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/cursor-trust.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// waitLaunchOnlyScreen is the historical launch-only screen of the issue.
const waitLaunchOnlyScreen = "PS C:\\Users\\dev\\pinar> cursor-agent --trust --force --approve-mcps --model grok-4.7-high\n" +
	"PS C:\\Users\\dev\\pinar>\n"

// setWaitPlatform points the shared gate at a simulated platform for the
// duration of the test (the production code uses platform.Current).
func setWaitPlatform(t *testing.T, goos string) {
	t.Helper()
	old := startup.CurrentPlatform
	startup.CurrentPlatform = func() string { return goos }
	t.Cleanup(func() { startup.CurrentPlatform = old })
}

// cursorRoster rewrites the fixture roster row to the given kind (the
// guard reads the kind from the roster, like the dispatch and the wait).
func (f *queuedProbeFixture) cursorRoster(t *testing.T, kind string) {
	t.Helper()
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\t" + kind + "\timplementer\t\t\t\t\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatalf("roster: %v", err)
	}
}

// assertNoSendKeys reports when the probe sent a key to the pane: the
// guard must leave the dialog untouched.
func assertNoSendKeys(t *testing.T, f *queuedProbeFixture) {
	t.Helper()
	for _, call := range f.calls() {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "send-keys" {
			t.Fatalf("the probe sent a key into the dialog: %#v", call)
		}
	}
}

func TestWaitCursorWindowsTrustDialogBlockedNoInput(t *testing.T) {
	// The verified dialog with herdr's blocked classification and a
	// pending not-received marker: the probe returns the existing blocked
	// result on the first probe (no deferred confirmation — the screen
	// itself is the proof) and sends no key, even with auto-approve on.
	setWaitPlatform(t, "win32")
	f := newQueuedProbeFixture(t, "blocked", "5", waitTrustScreen(t), map[string]string{"not-received": "1700000000 5"})
	f.cursorRoster(t, "cursor")
	f.env["SOHO_AUTO_APPROVE"] = "on"
	if got := f.probe(""); got != "blocked" {
		t.Fatalf("probe=%q", got)
	}
	assertNoSendKeys(t, f)
}

func TestWaitCursorWindowsIdleTrustDialogBlocked(t *testing.T) {
	// The same dialog with herdr's native idle classification (the verified
	// native probe reported idle and interactive_ready), a pending
	// not-received marker and auto-approve on: the guard is screen-based
	// and still returns the blocked result, no key.
	setWaitPlatform(t, "win32")
	f := newQueuedProbeFixture(t, "idle", "", waitTrustScreen(t), map[string]string{"not-received": "1700000000 -"})
	f.cursorRoster(t, "cursor")
	f.env["SOHO_AUTO_APPROVE"] = "on"
	if got := f.probe(""); got != "blocked" {
		t.Fatalf("probe=%q", got)
	}
	assertNoSendKeys(t, f)
}

func TestWaitCursorWindowsLaunchOnlyDoesNotBlockWait(t *testing.T) {
	// The launch-only screen does not block the wait: the guard acts only
	// on the active dialog, so the ordinary in-progress handling stands
	// (working) and no TUI banner is demanded.
	setWaitPlatform(t, "win32")
	f := newQueuedProbeFixture(t, "working", "5", waitLaunchOnlyScreen, nil)
	f.cursorRoster(t, "cursor")
	if got := f.probe(""); got != "working" {
		t.Fatalf("probe=%q", got)
	}
	assertNoSendKeys(t, f)
}

func TestWaitCursorWindowsEmptyScreenDoesNotBlockWait(t *testing.T) {
	// An empty visible screen does not block the wait either: the blank TUI
	// is unverified for the spawn and the dispatch, but the wait keeps its
	// ordinary handling.
	setWaitPlatform(t, "win32")
	f := newQueuedProbeFixture(t, "working", "5", "", nil)
	f.cursorRoster(t, "cursor")
	if got := f.probe(""); got != "working" {
		t.Fatalf("probe=%q", got)
	}
	assertNoSendKeys(t, f)
}

func TestWaitCursorNonWindowsDialogBlocksNoInput(t *testing.T) {
	// The guard applies on every platform, not only Windows: the same
	// dialog on a cursor session off-Windows takes the guard's blocked
	// result on the first probe and sends no approval key.
	setWaitPlatform(t, "linux")
	f := newQueuedProbeFixture(t, "blocked", "5", waitTrustScreen(t), nil)
	f.cursorRoster(t, "cursor")
	f.env["SOHO_AUTO_APPROVE"] = "on"
	if got := f.probe(""); got != "blocked" {
		t.Fatalf("probe=%q", got)
	}
	assertNoSendKeys(t, f)
}

func TestWaitNonCursorWindowsDialogKeepsFlow(t *testing.T) {
	// Control: the same dialog on a non-cursor session (the fixture's
	// claude row) on Windows keeps the existing blocked flow — the first
	// probe is deferred, no key sent by the probe itself.
	setWaitPlatform(t, "win32")
	f := newQueuedProbeFixture(t, "blocked", "5", waitTrustScreen(t), nil)
	if got := f.probe(""); got != "working" {
		t.Fatalf("first blocked probe=%q", got)
	}
	if _, err := os.Stat(f.marker("blocked")); err != nil {
		t.Fatalf("the blocked marker was not written: %v", err)
	}
}

func TestWaitCursorWindowsClippedTrustNoInput(t *testing.T) {
	b, err := os.ReadFile("testdata/cursor-trust-clipped.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"idle", "blocked"} {
		t.Run(state, func(t *testing.T) {
			setWaitPlatform(t, "win32")
			f := newQueuedProbeFixture(t, state, "5", string(b), map[string]string{"not-received": "1700000000 5"})
			f.cursorRoster(t, "cursor")
			f.env["SOHO_AUTO_APPROVE"] = "on"
			if got := f.probe(""); got != "blocked" {
				t.Fatalf("probe=%q", got)
			}
			assertNoSendKeys(t, f)
		})
	}
}
