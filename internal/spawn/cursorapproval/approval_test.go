package cursorapproval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/startup"
)

// The real MCP approval dialog (cursor-agent 2026.10.22, captured with no
// key sent): the tall panel keeps the warned title and the short panel
// lost it; both end in the option block and the navigation footer. The
// narrow panel wrapped the footer one character per line. The fixtures are
// the captures without the shell command lines.
const (
	realTallFixture   = "mcp-dialog-2026.10.22-tall.txt"
	realShortFixture  = "mcp-dialog-2026.10.22-short.txt"
	realNarrowFixture = "mcp-dialog-2026.10.22-narrow.txt"
)

func realScreen(t *testing.T, fixture string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// withoutLines drops every line containing any of the needles and keeps
// the rest of the real screen intact.
func withoutLines(screen string, needles ...string) string {
	var out []string
	for _, line := range strings.Split(screen, "\n") {
		keep := true
		for _, needle := range needles {
			if strings.Contains(line, needle) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// markerMoved retargets the "▶ " selection marker from the [a] option to
// the named option, as the arrows do on the real dialog.
func markerMoved(screen, option string) string {
	screen = strings.ReplaceAll(screen, "▶ [a] Approve all servers", "[a] Approve all servers")
	return strings.ReplaceAll(screen, option, "▶ "+option)
}

// wrappedFooterScreen splits the navigation footer after "select," in the
// real tall capture, as the renderer wraps it on narrower panels.
func wrappedFooterScreen(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(realScreen(t, realTallFixture), "Use arrow keys to navigate, Enter to select, or press the key shown", "Use arrow keys to navigate, Enter to select,\n  │  or press the key shown")
}

// clippedWithoutAScreen cuts the real short capture right after the [a]
// option line, keeping [c], [q] and the footer.
func clippedWithoutAScreen(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(realScreen(t, realShortFixture), "▶ [a] Approve all servers", "")
}

// TestRealMCPDialogScreens proves the three captured screens are active and
// keeps the negative/positive table: any other line after the option block
// (composer, report, shell) is a quote, not an active dialog, and an option
// quoted in the middle of prose is not an option line.
func TestRealMCPDialogScreens(t *testing.T) {
	tall := realScreen(t, realTallFixture)
	short := realScreen(t, realShortFixture)
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		// The real screens: tall-visible and tall-recent are the same
		// panel; the 12-line visible panel lost the title.
		{"tall real screen", tall, true},
		{"short real screen without title", short, true},
		// Negatives: all false.
		{"real screen with a composer below the options", tall + "\ncursor ready\n❯ \n", false},
		{"real screen without the footer and without the title", withoutLines(tall, "MCP Server Approval Required", "Use arrow keys to navigate"), false},
		{"footer only, no options", "Use arrow keys to navigate, Enter to select, or press the key shown\n", false},
		{"options quoted in the middle of prose", "the report said the dialog offered [a] Approve all, [c] Continue and [q] Quit\n", false},
		{"[a] Approve all with the title on an unrelated trust dialog screen", "MCP Server Approval Required\n[a] Approve all\n⚠ Workspace Trust Required\nCursor Agent can execute code and access files in this directory.\nDo you trust the contents of this directory?\nC:\\dev\\project\n", false},
		// Positives: all true.
		{"selection moved to [c]", markerMoved(tall, "[c] Continue without approval"), true},
		{"selection moved to [q]", markerMoved(tall, "[q] Quit"), true},
		{"applying after the block", withoutLines(tall, "Use arrow keys to navigate") + "Applying your selection...\n", true},
		{"applying after the block with hourglass", withoutLines(tall, "Use arrow keys to navigate") + "⏳ Applying your selection...\n", true},
		{"applying after the footer", tall + "Applying your selection...\n", true},
		{"applying after the footer with hourglass", tall + "⏳ Applying your selection...\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CursorMCPApproval(tc.screen); got != tc.want {
				t.Fatalf("CursorMCPApproval = %v, want %v; screen:\n%s", got, tc.want, tc.screen)
			}
		})
	}
}

// TestCursorMCPApprovalLegacyShapes guards the pre-existing fixtures: the
// exact title with the options sharing one line, the title above a single
// trailing option, and a composer after the quote supersedes the dialog.
func TestCursorMCPApprovalLegacyShapes(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"shared option line with title", "MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\n", true},
		{"warned title and shared option line", "⚠ MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\n", true},
		{"title above a single trailing option", "MCP Server Approval Required\nproject server list\n[q] Quit\n", true},
		{"composer after the quote", "MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\ncursor ready\n❯ \n", false},
		{"title only", "MCP Server Approval Required\n", false},
		{"empty screen", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CursorMCPApproval(tc.screen); got != tc.want {
				t.Fatalf("CursorMCPApproval = %v, want %v; screen:\n%s", got, tc.want, tc.screen)
			}
		})
	}
}

// TestPartialMCPDialogRecognizesCutOrWrappedScreens proves the narrow real
// capture (one letter per line) is recognized, and keeps the
// true/false table: only the end of the compacted screen counts, and a
// composer or continuation text after the dialog's tail keeps the result
// false.
func TestPartialMCPDialogRecognizesCutOrWrappedScreens(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"narrow real screen", realScreen(t, realNarrowFixture), true},
		{"wrapped footer with title", wrappedFooterScreen(t), true},
		{"wrapped footer without title", withoutLines(wrappedFooterScreen(t), "MCP Server Approval Required"), true},
		{"only [c], [q] and the footer", clippedWithoutAScreen(t), true},
		{"only [q] and the footer", "▶ [q] Quit\nUse arrow keys to navigate, Enter to select, or press the key shown\n", true},
		{"[a] and [c] broken without title", "[c] Continue without approval\n[a] Approve all\nservers\n", true},
		{"[q] and Quit on two lines", "▶ [q]\nQuit\n", true},
		{"only shown after the key", "Use arrow keys to navigate, Enter to select, or press\nthe key\nshown\n", true},
		{"applying at the end", "▶ [c] Continue without approval\n[q] Quit\nApplying your selection...\n", true},
		{"applying at the end with hourglass", "▶ [c] Continue without approval\n[q] Quit\n⏳ Applying your selection...\n", true},
		{"composer after the dialog", realScreen(t, realTallFixture) + "→ Plan, search, build anything\n", false},
		{"prompt after the dialog", realScreen(t, realTallFixture) + "❯ \n", false},
		{"chat reply quoting the footer and continuing", "The help says use arrow keys, enter to select, or press the key shown, then pick one.\n", false},
		{"ordinary cursor screen", "cursor ready\n❯ booting\n", false},
		{"empty screen", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PartialMCPDialog(tc.screen); got != tc.want {
				t.Fatalf("PartialMCPDialog = %v, want %v; screen:\n%s", got, tc.want, tc.screen)
			}
		})
	}
}

// TestEvaluatePartialMCPDialogRefuses asserts the shared verdict for the
// partial screens: kind cursor refuses with screen-incomplete (or
// mcp-approval where the active dialog is already recognized), other kinds
// keep the observed result, and the trust screens keep theirs.
func TestEvaluatePartialMCPDialogRefuses(t *testing.T) {
	screens := []struct {
		name   string
		screen string
	}{
		{"narrow real screen", realScreen(t, realNarrowFixture)},
		{"wrapped footer with title", wrappedFooterScreen(t)},
		{"wrapped footer without title", withoutLines(wrappedFooterScreen(t), "MCP Server Approval Required")},
		{"clipped [c] [q] and footer", clippedWithoutAScreen(t)},
		{"[q] and the footer", "▶ [q] Quit\nUse arrow keys to navigate, Enter to select, or press the key shown\n"},
		{"[q] and Quit on two lines", "▶ [q]\nQuit\n"},
		{"applying at the end", "▶ [c] Continue without approval\n[q] Quit\nApplying your selection...\n"},
		{"applying at the end with hourglass", "▶ [c] Continue without approval\n[q] Quit\n⏳ Applying your selection...\n"},
	}
	for _, tc := range screens {
		t.Run(tc.name, func(t *testing.T) {
			if !PartialMCPDialog(tc.screen) {
				t.Fatalf("the screen is not partial; screen:\n%s", tc.screen)
			}
			wantReason := startup.ReasonScreenIncomplete
			if CursorMCPApproval(tc.screen) {
				wantReason = "mcp-approval"
			}
			ev := Evaluate("cursor", "idle", tc.screen, true)
			if !ev.Refuses() || ev.Reason != wantReason {
				t.Fatalf("cursor screen verdict = %#v, want refuses with reason %q", ev, wantReason)
			}
			for _, kind := range []string{"claude", "codex"} {
				other := Evaluate(kind, "idle", tc.screen, true)
				if other.Verdict != startup.VerdictObserved || other.Reason != startup.ReasonVisibleOutput || other.Refuses() {
					t.Fatalf("kind %s changed result: %#v", kind, other)
				}
			}
		})
	}
	t.Run("active MCP screen keeps mcp-approval", func(t *testing.T) {
		ev := Evaluate("cursor", "idle", realScreen(t, realTallFixture), true)
		if ev.Verdict != startup.VerdictBlocked || ev.Reason != "mcp-approval" || !ev.Refuses() {
			t.Fatalf("active dialog verdict changed: %#v", ev)
		}
	})
	t.Run("trust screens keep their results", func(t *testing.T) {
		for _, tc := range []struct {
			name, fixture, wantVerdict, wantReason string
		}{
			{"active trust dialog", "cursor-trust.txt", startup.VerdictBlocked, "workspace-trust"},
			{"clipped trust dialog", "cursor-trust-clipped.txt", startup.VerdictUnverified, startup.ReasonScreenIncomplete},
		} {
			b, err := os.ReadFile(filepath.Join("..", "testdata", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			ev := Evaluate("cursor", "idle", string(b), true)
			if ev.Verdict != tc.wantVerdict || ev.Reason != tc.wantReason {
				t.Fatalf("%s verdict = %#v, want %s/%s", tc.name, ev, tc.wantVerdict, tc.wantReason)
			}
		}
	})
}
