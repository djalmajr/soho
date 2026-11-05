package wait

// Finding origin/class coverage of settled review reports.
//
// checkFindingMarks publishes the report's finding marks on the done line —
// brief/impl/unknown and the header's findings without a valid mark — and
// warns, once per report, when findings remain unmarked. The wait never
// blocks on any of it and never changes the result.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/reportscan"
)

// checkFindingMarks is the done case's finding-marks coverage: the counts
// go to the line when the header has findings or the report carries marks,
// and the warning fires at most once per report, through the
// finding-marks-warned wait file.
func checkFindingMarks(sd, agent, report, text string, header *reportscan.ReviewHeaderResult, line *jsonjs.Object) {
	if text == "" {
		return // an unreadable report stays exactly as today
	}
	marks := reportscan.FindingMarks(text, header)
	total := 0
	if header != nil {
		total = int(header.Findings)
	}
	counted := marks.Brief + marks.Impl + marks.Unknown
	if total > 0 || counted > 0 {
		line.Set("finding_marks", jsonjs.O("brief", marks.Brief, "impl", marks.Impl, "unknown", marks.Unknown, "unmarked", marks.Unmarked))
	}
	if marks.Unmarked == 0 {
		return
	}
	prev, _ := readWaitFile(sd, agent, "finding-marks-warned")
	if prev == report {
		return
	}
	sendWarning(fmt.Sprintf("report of '%s' has %d finding(s) without an origin and a class", agent, marks.Unmarked))
	// The warning already went out; a marker that cannot persist only lets
	// it repeat on the next poll, never aborting the settlement.
	_ = os.WriteFile(filepath.Join(sd, "wait", agent+".finding-marks-warned"), []byte(report+"\n"), 0o666)
}
