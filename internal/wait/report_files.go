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
	return reportFilesAfterRead(sd, agent, report, env, gittree.ChangedPaths)
}

// reportFilesAfterReadOnly is reportFilesAfter with the read-only changed
// paths: the same "changed after the report" comparison, but the role gate
// never materializes the skill bundle or Dies, so the inspection's report
// decision stays silent and nonfatal on unavailable role metadata.
func reportFilesAfterReadOnly(sd, agent, report string, env platform.Env) []string {
	return reportFilesAfterRead(sd, agent, report, env, gittree.ChangedPathsReadOnly)
}

// reportFilesAfterRead is the shared "changed after the report" comparison:
// the changed-path reader is the only decision that differs between the
// command and the inspection callers.
func reportFilesAfterRead(sd, agent, report string, env platform.Env, changedPaths func(sidecar, agent string, env platform.Env) []gittree.ChangedPath) []string {
	info, err := os.Stat(report)
	if err != nil {
		return nil
	}
	paths := changedPaths(gittree.Sidecar(sd, report), agent, env)
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
// clocks or writing settlement markers. Pane failure preserves report
// authority. It is the command form of reportObservation: the same
// observation through the ordinary role reader, with the probe's terminal
// state dropped.
func reportStatus(sd, agent, report string, ctx *core.Config, svc transport.Services, env platform.Env) (string, string) {
	state, cause, _ := reportObservation(sd, agent, report, ctx, svc, env, false)
	return state, cause
}

// reportObservation is the shared report observation: the same completion
// evidence as wait, without advancing clocks or writing settlement markers,
// plus the state word the probe actually returned (terminal: "" when the
// branch settled before any probe, working/idle/gone/unavailable when a
// probe ran). readOnly switches the changed-paths decision to the read-only
// reader, so the inspection never materializes the skill bundle or Dies on
// unavailable role metadata; when that metadata is unavailable the
// read-only changed paths are nil and report authority is preserved, as
// the reportFilesAfter contract specifies.
func reportObservation(sd, agent, report string, ctx *core.Config, svc transport.Services, env platform.Env, readOnly bool) (string, string, string) {
	var filesAfter func(sd, agent, report string, env platform.Env) []string = reportFilesAfter
	if readOnly {
		filesAfter = reportFilesAfterReadOnly
	}
	info, err := os.Stat(report)
	if err != nil {
		return "done", "", ""
	}
	identity := reportIdentity(report, info)
	max := reportWorkingMax(ctx, env)
	final, _ := readWaitFile(sd, agent, "report-working-final")
	if final == identity || max == 0 {
		return "done", "", ""
	}
	if at, ok := reportStableAt(sd, agent, identity); ok && platform.Now().Unix()-at > max {
		return "done", "", ""
	}
	st := readState(svc, agent)
	switch st.State {
	case "gone", "unavailable":
		return "done", "", st.State
	case "idle", "done":
		settled, _ := readWaitFile(sd, agent, "report-settled")
		if settled == identity || len(filesAfter(sd, agent, report, env)) == 0 {
			return "done", "", st.State
		}
		return "working", "report draft: files changed after it", st.State
	default:
		return "working", "report draft: pane working", st.State
	}
}
