package startup

import (
	"os"
	"strings"
	"testing"
)

// nativeTrustScreen is the verified cursor Workspace Trust Required dialog
// (cursor-agent 2026.10.01 on Windows, herdr 0.9.1, issue #59 probe).
func nativeTrustScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/cursor-trust.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// historicalLaunchScreen is the screen the issue #59 run reported on
// Windows: the cursor TUI never drew and the pane held only the PowerShell
// prompt and the launch line.
const historicalLaunchScreen = "PS C:\\Users\\dev\\pinar> cursor-agent --trust --force --approve-mcps --model grok-4.7-high\n" +
	"PS C:\\Users\\dev\\pinar>\n"

// grokTrustScreen is grok's first-run trust dialog: it shares the question
// line with the cursor dialog but has different choices.
const grokTrustScreen = "Do you trust the contents of this directory?\n" +
	"  1. Yes, proceed (y)\n" +
	"  2. No, quit (n)\n"

// darwinTrustMcpScreen is the real cursor-agent trust/MCP dialog captured
// on macOS (2026-10-14): it announces the workspace MCP servers, shows a
// POSIX path, and adds the [w] option between the trust choice and the
// Quit choice.
func darwinTrustMcpScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/cursor-trust-mcp-darwin.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCursorTrustMcpDarwinFixtureBlocks(t *testing.T) {
	// The real macOS screen is the verified trust dialog on every
	// platform: Evaluate with goos "darwin" gives the same workspace-trust
	// reason the Windows screen gives, and the [w] "don't enable all MCP
	// servers" option does not prevent recognition.
	ev := Evaluate("cursor", "darwin", "idle", darwinTrustMcpScreen(t), true)
	if ev.Verdict != VerdictBlocked || ev.Reason != ReasonWorkspaceTrust || ev.Source != SourceVisible {
		t.Fatalf("Evaluate(cursor, darwin, fixture)=%+v, want blocked/workspace-trust/visible", ev)
	}
	if !ev.Refuses() {
		t.Fatal("the darwin fixture must refuse the start")
	}
}

func TestStartupGateAppliesToCursorOnEveryPlatform(t *testing.T) {
	for _, tc := range []struct {
		kind, goos string
		want       bool
	}{
		{"cursor", "win32", true},
		{"cursor", "darwin", true},
		{"cursor", "linux", true},
		{"grok", "win32", false},
		{"grok", "darwin", false},
		{"claude", "win32", false},
		{"codex", "win32", false},
		{"", "win32", false},
	} {
		if got := EnabledFor(tc.kind, tc.goos); got != tc.want {
			t.Fatalf("EnabledFor(%q, %q) = %v, want %v", tc.kind, tc.goos, got, tc.want)
		}
	}
}

func TestActiveTrustDialog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		want   bool
	}{
		{"native screen as verified", nativeTrustScreen(t), true},
		{"native screen with CRLF line endings", strings.ReplaceAll(nativeTrustScreen(t), "\n", "\r\n"), true},
		{"native screen with TUI padding", "  ⚠ Workspace Trust Required  \n\tDo you trust the contents of this directory?\n   ▶ [a] Trust this workspace\n    [q] Quit\n", true},
		{"question all upper case", "DO YOU TRUST THE CONTENTS OF THIS DIRECTORY?\n[A] TRUST THIS WORKSPACE\n[Q] QUIT\n", true},
		{"question mixed case and extra spaces", "dO  you\ttrust   the contents of this directory\nTrust this workspace\nQuit\n", true},
		{"old dialog text quoted in a transcript (different question)", "The dialog asked: Do you trust the authors of this repository?\n[A] Trust this workspace\n[Q] Quit\n", false},
		{"question and trust choice, no Quit choice", "Do you trust the contents of this directory?\n[A] Trust this workspace\n", false},
		{"question and Quit choice, no trust choice", "Do you trust the contents of this directory?\n[Q] Quit\n", false},
		{"question only (a transcript quoting the question)", "The cursor CLI asked: Do you trust the contents of this directory?\n", false},
		{"choices only, no question", "[A] Trust this workspace\n[Q] Quit\n", false},
		{"grok trust dialog (shared question, different choices)", grokTrustScreen, false},
		{"macos trust/MCP dialog (posix path, [w] option between trust and quit)", "Do you trust the contents of this directory?\n/private/tmp/cursor-cap-309\n▶ [a] Trust this workspace\n[w] Trust this workspace, but don't enable all MCP servers\n[q] Quit\n", true},
		{"three choice lines between trust and quit is not the dialog", "Do you trust the contents of this directory?\nC:\\dev\\project\n▶ [a] Trust this workspace\n[w] Trust this workspace, but don't enable all MCP servers\n[x] Trust this workspace for this run only\n[y] Trust this workspace for 30 days\n[q] Quit\n", false},
		{"a non-choice line between trust and quit is not the dialog", "Do you trust the contents of this directory?\nC:\\dev\\project\n▶ [a] Trust this workspace\nSome other visible text\n[q] Quit\n", false},
		{"a lone slash line is not a workspace path", "Do you trust the contents of this directory?\n/\n▶ [a] Trust this workspace\n[q] Quit\n", false},
		{"normal cursor TUI with a quit hint", "cursor-2026.10 ready\n❯ /help\n(q) quit\n", false},
		{"empty screen", "", false},
		{"blank screen", "   \n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActiveTrustDialog(tc.screen); got != tc.want {
				t.Fatalf("ActiveTrustDialog(%q) = %v, want %v", tc.screen, got, tc.want)
			}
		})
	}
}

func TestLaunchOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		want   bool
	}{
		{"historical launch screen as reported", historicalLaunchScreen, true},
		{"historical screen with CRLF", strings.ReplaceAll(historicalLaunchScreen, "\n", "\r\n"), true},
		{"bare empty prompt", "PS C:\\Users\\dev\\pinar>\n", true},
		{"padded prompt lines", "  PS /home/u/project>   \nPS /home/u/project>\n", true},
		{"empty screen is not launch-only", "", false},
		{"blank screen is not launch-only", "   \n", false},
		{"normal cursor TUI", "cursor ready\n❯ working on it\n", false},
		{"launch line without a prompt prefix", "cursor-agent --trust --force\n", false},
		{"prompt line plus one TUI line", "PS C:\\dev>\ncursor ready\n", false},
		{"cmd style prompt is not a PowerShell prompt", "C:\\dev> cursor-agent\nC:\\dev>\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LaunchOnly(tc.screen); got != tc.want {
				t.Fatalf("LaunchOnly(%q) = %v, want %v", tc.screen, got, tc.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	for _, tc := range []struct {
		kind, goos, state, screen string
		screenOK                  bool
		wantVerdict, wantReason   string
	}{
		// The gate is off for every non-cursor kind, whatever the platform.
		{"grok", "win32", "idle", nativeTrustScreen(t), true, VerdictObserved, ReasonVisibleOutput},
		// The gate is on for the cursor kind on every platform: the same
		// verified dialog on darwin and linux blocks with the same reason.
		{"cursor", "linux", "idle", nativeTrustScreen(t), true, VerdictBlocked, ReasonWorkspaceTrust},
		{"cursor", "darwin", "idle", "", false, VerdictUnverified, ReasonScreenUnavailable},
		// A state read that failed is unverified before anything else.
		{"cursor", "win32", "", nativeTrustScreen(t), true, VerdictUnverified, ReasonStateUnavailable},
		{"cursor", "win32", "unavailable", nativeTrustScreen(t), true, VerdictUnverified, ReasonStateUnavailable},
		// The verified dialog is blocked, whatever the state reports.
		{"cursor", "win32", "idle", nativeTrustScreen(t), true, VerdictBlocked, ReasonWorkspaceTrust},
		{"cursor", "win32", "blocked", nativeTrustScreen(t), true, VerdictBlocked, ReasonWorkspaceTrust},
		{"cursor", "win32", "idle", strings.ReplaceAll(nativeTrustScreen(t), "\n", "\r\n"), true, VerdictBlocked, ReasonWorkspaceTrust},
		// Blank, unreadable and launch-only screens are unverified.
		{"cursor", "win32", "idle", "", true, VerdictUnverified, ReasonEmptyScreen},
		{"cursor", "win32", "idle", "   \n", true, VerdictUnverified, ReasonEmptyScreen},
		{"cursor", "win32", "idle", "", false, VerdictUnverified, ReasonScreenUnavailable},
		{"cursor", "win32", "idle", historicalLaunchScreen, true, VerdictUnverified, ReasonLaunchOnly},
		{"cursor", "win32", "idle", "PS C:\\dev> cursor-agent\n", true, VerdictUnverified, ReasonLaunchOnly},
		// Anything else visible is observed: no brittle banner is demanded
		// and unknown text is not a proven crash.
		{"cursor", "win32", "idle", "cursor ready\n", true, VerdictObserved, ReasonVisibleOutput},
		{"cursor", "win32", "idle", "some unknown text\n", true, VerdictObserved, ReasonVisibleOutput},
		{"cursor", "win32", "working", "thinking…\n", true, VerdictObserved, ReasonVisibleOutput},
		{"cursor", "win32", "idle", grokTrustScreen, true, VerdictObserved, ReasonVisibleOutput},
	} {
		t.Run(tc.kind+"/"+tc.goos+"/"+tc.state+"/"+tc.wantVerdict+"/"+tc.wantReason, func(t *testing.T) {
			ev := Evaluate(tc.kind, tc.goos, tc.state, tc.screen, tc.screenOK)
			if ev.Verdict != tc.wantVerdict || ev.Reason != tc.wantReason || ev.Source != SourceVisible {
				t.Fatalf("Evaluate(kind=%q goos=%q state=%q screenOK=%v) = %+v, want verdict %q reason %q source %q", tc.kind, tc.goos, tc.state, tc.screenOK, ev, tc.wantVerdict, tc.wantReason, SourceVisible)
			}
			wantRefuse := tc.wantVerdict != VerdictObserved
			if ev.Refuses() != wantRefuse {
				t.Fatalf("Refuses() = %v, want %v (verdict %s)", ev.Refuses(), wantRefuse, tc.wantVerdict)
			}
		})
	}
}

const wrappedCursorLaunchScreen = `Old shell output
PS C:\dev\project> if((Get-Command cu
rsor-agent.cmd -ErrorAction SilentlyContinue).CommandType -eq 'ExternalScript'){& cursor-agent.cmd '--mode
l' grok-4.7-high}else{Start-Process -FilePath cursor-agent.cmd -ArgumentList '--model grok-4.7-high' -NoNe
wWindow -Wait}
`
const narrowCursorTrustScreen = `╭──────────────────────────────────────╮
│ Do you trust the contents of this     │
│ directory?                           │
│ C:\dev\project                        │
│ ▶ [a] Trust this workspace            │
│ [q] Quit                             │
╰──────────────────────────────────────╯
`

func TestStartupWrappedAndStaleScreens(t *testing.T) {
	for _, tc := range []struct{ name, screen, verdict, reason string }{
		{"native wrapped launch with prior shell output", wrappedCursorLaunchScreen, VerdictUnverified, ReasonLaunchOnly},
		{"narrow current dialog", narrowCursorTrustScreen, VerdictBlocked, ReasonWorkspaceTrust},
		{"wrapped navigation hint", narrowCursorTrustScreen + "Use arrow keys to navigate, Enter to select,\nor press the key shown\n", VerdictBlocked, ReasonWorkspaceTrust},
		{"old dialog then composer", nativeTrustScreen(t) + "cursor ready\n❯ Continue reviewing\n", VerdictObserved, ReasonVisibleOutput},
		{"quoted native dialog then composer", "> " + strings.ReplaceAll(nativeTrustScreen(t), "\n", "\n> ") + "\ncursor ready\n❯ Continue reviewing\n", VerdictObserved, ReasonVisibleOutput},
		{"old dialog then shell", nativeTrustScreen(t) + "PS C:\\dev\\project>\n", VerdictUnverified, ReasonLaunchOnly},
		{"wrapped launch followed by TUI", wrappedCursorLaunchScreen + "cursor ready\n❯ Continue\n", VerdictObserved, ReasonVisibleOutput},
		{"direct launch soft wrap", "PS C:\\dev> cursor-agent --mod\nel grok-4.7-high\n", VerdictUnverified, ReasonLaunchOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := Evaluate("cursor", "win32", "idle", tc.screen, true)
			if ev.Verdict != tc.verdict || ev.Reason != tc.reason {
				t.Fatalf("got %+v, want %s/%s", ev, tc.verdict, tc.reason)
			}
		})
	}
}

func TestStartupClippedTrustRemainsUnverified(t *testing.T) {
	b, err := os.ReadFile("testdata/cursor-trust-clipped.txt")
	if err != nil {
		t.Fatal(err)
	}
	screen := string(b)
	if ActiveTrustDialog(screen) {
		t.Fatal("the missing question cannot establish an active dialog")
	}
	ev := Evaluate("cursor", "win32", "idle", screen, true)
	if ev.Verdict != VerdictUnverified || ev.Reason != ReasonScreenIncomplete || !ev.Refuses() {
		t.Fatalf("clipped assessment=%+v", ev)
	}
	if PartialTrustDialog(screen + "cursor ready\n❯ Continue\n") {
		t.Fatal("new composer supersedes the partial dialog")
	}
	if PartialTrustDialog("> " + strings.ReplaceAll(screen, "\n", "\n> ")) {
		t.Fatal("quoted fragment is not current dialog evidence")
	}
}

// TestStartupClippedMacOSTrustWithMCPChoice: a short pane clips the
// question of the macOS dialog while its [a], [w] and [q] choices and the
// navigation hint stay visible. The [w] "don't enable all MCP servers"
// choice between trust and Quit must not hide the clipped dialog.
func TestStartupClippedMacOSTrustWithMCPChoice(t *testing.T) {
	screen := strings.Join([]string{
		"  │  ▶ [a] Trust this workspace                                       │",
		"  │    [w] Trust this workspace, but don't enable all MCP servers     │",
		"  │    [q] Quit                                                       │",
		"  │                                                                   │",
		"  │  Use arrow keys to navigate, Enter to select, or press the key shown │",
		"  ╰───────────────────────────────────────────────────────────────────╯",
	}, "\n")
	if ActiveTrustDialog(screen) {
		t.Fatal("the missing question cannot establish an active dialog")
	}
	if !PartialTrustDialog(screen) {
		t.Fatal("the clipped dialog with the [w] choice must be partial trust evidence")
	}
	for _, goos := range []string{"darwin", "linux", "win32"} {
		ev := Evaluate("cursor", goos, "idle", screen, true)
		if ev.Verdict != VerdictUnverified || ev.Reason != ReasonScreenIncomplete || !ev.Refuses() {
			t.Fatalf("%s clipped assessment=%+v", goos, ev)
		}
	}
	threeExtra := strings.Replace(screen, "    [q] Quit", "    [x] one\n  │    [y] two\n  │    [q] Quit", 1)
	if PartialTrustDialog(threeExtra) {
		t.Fatal("three extra choice lines between trust and Quit are not this dialog")
	}
}
