package setuptext

import (
	"strings"
	"testing"
)

// TestLegacyMigrationUsesCompactPeerOpening covers the DJA-610 part B
// review F1: the first setup over a herdr-soho block installs the current
// compact-opening peer bullet in place (the old opening line is gone and
// the owner-approval rule stays exactly once) while the project's own lines
// survive the migration.
func TestLegacyMigrationUsesCompactPeerOpening(t *testing.T) {
	input := "review-before\n" + insertLineAfter(previousSohoBlock, "## Multi-agent workflow (herdr-soho)\n", "review-project-line\n") + "review-after\n"
	output := SetupBlockResult(&input)
	if output == nil {
		t.Fatal("migration refused a complete generated legacy block")
	}
	newCount := strings.Count(*output, "[soho:peer:<id>]")
	oldCount := strings.Count(*output, "`[soho:peer]`")
	approvalCount := strings.Count(*output, "it never authorizes")
	t.Logf("new_opening_mentions=%d old_opening_mentions=%d approval_rules=%d project_line_preserved=%t", newCount, oldCount, approvalCount, strings.Contains(*output, "review-project-line\n"))
	if newCount != 1 || oldCount != 0 {
		t.Error("first legacy migration does not install the compact peer opening rule")
	}
	if approvalCount != 1 || strings.Count(*output, SetupStart) != 1 || strings.Count(*output, SetupEnd) != 1 {
		t.Error("migration duplicated or lost generated boundaries/approval rule")
	}
	if !strings.HasPrefix(*output, "review-before\n") || !strings.HasSuffix(*output, "review-after\n") || !strings.Contains(*output, "review-project-line\n") {
		t.Error("migration lost surrounding/project lines")
	}
}

// TestPreviousSohoBlockUsesCompactPeerOpening covers the second half of F1:
// a setup over a file holding the previous soho block refreshes it to the
// current compact-opening bullet, keeping the surrounding lines.
func TestPreviousSohoBlockUsesCompactPeerOpening(t *testing.T) {
	input := "review-before\n" + sohoBlock + "review-after\n"
	output := SetupBlockResult(&input)
	if output == nil {
		t.Fatal("refused previous soho block")
	}
	if strings.Count(*output, "[soho:peer:<id>]") != 1 || strings.Count(*output, "`[soho:peer]`") != 0 {
		t.Fatal("previous soho block was not refreshed")
	}
	if !strings.HasPrefix(*output, "review-before\n") || !strings.HasSuffix(*output, "review-after\n") {
		t.Fatal("surrounding lines lost")
	}
}
