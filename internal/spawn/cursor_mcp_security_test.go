package spawn

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

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
