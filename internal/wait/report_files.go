package wait

import (
	"os"
	"sort"
	"strconv"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func reportWorkingMax(ctx *core.Config, env platform.Env) int64 {
	max, err := strconv.ParseInt(core.Cfg(ctx, "report_working_max", "600", env), 10, 64)
	if err != nil || max < 0 {
		return 600
	}
	return max
}

func reportFilesAfter(sd, agent, report string, env platform.Env) []string {
	info, err := os.Stat(report)
	if err != nil {
		return nil
	}
	paths := gittree.ChangedPaths(gittree.Sidecar(sd, report), agent, env)
	newer := []string{}
	for _, path := range paths {
		if path.ModTime.After(info.ModTime()) {
			newer = append(newer, path.Path)
		}
	}
	sort.Strings(newer)
	return newer
}

// reportStatus reads the same completion evidence as wait, without advancing
// clocks or writing settlement markers. Pane failure preserves report authority.
func reportStatus(sd, agent, report string, ctx *core.Config, svc transport.Services, env platform.Env) (string, string) {
	info, err := os.Stat(report)
	if err != nil {
		return "done", ""
	}
	identity := reportIdentity(report, info)
	max := reportWorkingMax(ctx, env)
	final, _ := readWaitFile(sd, agent, "report-working-final")
	if final == identity || max == 0 {
		return "done", ""
	}
	if at, ok := reportStableAt(sd, agent, identity); ok && platform.Now().Unix()-at > max {
		return "done", ""
	}
	st := readState(svc, agent)
	switch st.State {
	case "gone", "unavailable":
		return "done", ""
	case "idle", "done":
		settled, _ := readWaitFile(sd, agent, "report-settled")
		if settled == identity || len(reportFilesAfter(sd, agent, report, env)) == 0 {
			return "done", ""
		}
		return "working", "report draft: files changed after it"
	default:
		return "working", "report draft: pane working"
	}
}
