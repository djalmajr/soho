package wait

// Oversized settled reports get one warning per report. The wait never
// refuses or truncates the report; it only tells the orchestrator to keep the
// long log beside the report and cite a short excerpt.

import (
	"os"
	"path/filepath"
)

// checkReportSize is the done case's size check. The marker follows the
// report identity, so another settle of a different report warns again while
// repeated polls of the same report warn once.
func checkReportSize(sd, agent, report string) {
	info, err := os.Stat(report)
	if err != nil {
		return
	}
	warning, ok := ReportSizeWarning(agent, info.Size())
	if !ok {
		return
	}
	prev, _ := readWaitFile(sd, agent, "report-size-warned")
	if prev == report {
		return
	}
	sendWarning(warning.Message)
	if sd != "" {
		// A marker that cannot persist only lets the warning repeat; it
		// never aborts the settlement.
		_ = os.WriteFile(filepath.Join(sd, "wait", agent+".report-size-warned"), []byte(report+"\n"), 0o666)
	}
}
