package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// cursorMCPRealScreen reads the real captured MCP dialog (cursor-agent
// 2026.10.22) shared with the cursorapproval package's fixtures.
func cursorMCPRealScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("cursorapproval", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReviewDJA481ClippedSpawnMCP(t *testing.T) {
	full := "MCP Server Approval Required\n" + strings.Repeat("server description\n", 45) + "[a] Approve all\n"
	lines := strings.Split(strings.TrimSuffix(full, "\n"), "\n")
	limited := strings.Join(lines[len(lines)-40:], "\n") + "\n"
	if !CursorMCPApproval(full) || CursorMCPApproval(limited) {
		t.Fatal("fixture must distinguish full 47-line dialog from isolated choices")
	}
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "reuse"}[reuse], func(t *testing.T) {
			bounded := fakecli.Rule{Argv: grokScreenRead("implementer"), Stdout: limited}
			var f spawnFixture
			if reuse {
				f, _ = cursorReuseFixture(t, full, false, bounded)
			} else {
				rules := append([]fakecli.Rule{bounded}, cursorFreshRules(full, false)...)
				f = newSpawnFixture(t, append(rules, freshSpawnRules()...))
				configureSpawnFixture(t, &f)
			}
			code, out, errText, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
			if code != 7 || !strings.Contains(out, `"status": "blocked_at_startup"`) {
				t.Fatalf("47-line visible dialog exit=%d out=%s stderr=%q calls=%#v", code, out, errText, calls)
			}
			assertCursorSpawnEvidence(t, out, "idle", "mcp-approval")
			assertNoInputIntoTheDialog(t, calls)
			fullRead := false
			for _, c := range calls {
				if strings.Join(c.Argv, "\x00") == strings.Join([]string{"agent", "read", "implementer", "--source", "visible"}, "\x00") {
					fullRead = true
				}
			}
			if !fullRead {
				t.Fatalf("no full visible read: %#v", calls)
			}
		})
	}
}

// cursorMCPWrappedFooter splits the navigation footer after "select," in
// the real tall capture, as the renderer wraps it on narrower panels.
func cursorMCPWrappedFooter(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(cursorMCPRealScreen(t, "mcp-dialog-2026.10.22-tall.txt"), "Use arrow keys to navigate, Enter to select, or press the key shown", "Use arrow keys to navigate, Enter to select,\n  │  or press the key shown")
}

// TestReviewDJA653CursorPartialScreenSpawnNotReady: the clipped or wrapped
// approval screens are inconclusive, not ready: the spawn ends
// blocked_at_startup with the observed exit code and the screen-incomplete
// evidence, and nothing is typed or sent into the pane.
func TestReviewDJA653CursorPartialScreenSpawnNotReady(t *testing.T) {
	short := cursorMCPRealScreen(t, "mcp-dialog-2026.10.22-short.txt")
	for name, screen := range map[string]string{
		"wrapped footer":         cursorMCPWrappedFooter(t),
		"clipped c q and footer": strings.ReplaceAll(short, "▶ [a] Approve all servers", ""),
	} {
		t.Run(name, func(t *testing.T) {
			f := newSpawnFixture(t, append(cursorFreshRules(screen, false), freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			code, out, errText, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
			if code != 7 || !strings.Contains(out, `"status": "blocked_at_startup"`) {
				t.Fatalf("exit=%d out=%s stderr=%q calls=%#v", code, out, errText, calls)
			}
			assertCursorSpawnEvidence(t, out, "idle", "screen-incomplete")
			assertNoInputIntoTheDialog(t, calls)
		})
	}
}

// TestReviewDJA653CursorRealScreenSpawnMCPBlocks: the two real captured
// screens (the tall panel with the warned title and the short panel that
// lost it) block a fresh spawn at startup — exit 7, blocked_at_startup
// with the mcp-approval evidence — and nothing is typed or sent into the
// dialog.
func TestReviewDJA653CursorRealScreenSpawnMCPBlocks(t *testing.T) {
	for name, screen := range map[string]string{
		"tall":  cursorMCPRealScreen(t, "mcp-dialog-2026.10.22-tall.txt"),
		"short": cursorMCPRealScreen(t, "mcp-dialog-2026.10.22-short.txt"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newSpawnFixture(t, append(cursorFreshRules(screen, false), freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			code, out, errText, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
			if code != 7 || !strings.Contains(out, `"status": "blocked_at_startup"`) {
				t.Fatalf("exit=%d out=%s stderr=%q calls=%#v", code, out, errText, calls)
			}
			assertCursorSpawnEvidence(t, out, "idle", "mcp-approval")
			assertNoInputIntoTheDialog(t, calls)
		})
	}
}
