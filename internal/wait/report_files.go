package wait

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

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
	return reportFilesAfterRead(sd, agent, report, env, os.Stat, gittree.Sidecar, gittree.ChangedPaths)
}

// reportFilesAfterReadOnly is reportFilesAfter with the read-only changed
// paths and the inspection's capped sidecar read: the same "changed after
// the report" comparison, but the role gate never materializes the skill
// bundle or Dies and an overlong sidecar refuses instead of being read
// whole, so the inspection's report decision stays silent and nonfatal on
// unavailable role or hostile sidecar metadata.
func reportFilesAfterReadOnly(sd, agent, report string, env platform.Env) []string {
	return reportFilesAfterRead(sd, agent, report, env, os.Stat, gittree.SidecarInspect, gittree.ChangedPathsReadOnly)
}

// reportFilesAfterSource is the inspection's "changed after the report"
// comparison through the source's retained roots: the report metadata,
// the sidecar location and the changed paths are the source's confined
// observations (the D7 caps), never raw state or scratch path reads —
// the changed-path cutoff is the same source report observation the
// report identity uses, so a renamed or replaced raw report path cannot
// move the cutoff, a renamed or replaced state dir does not change the
// decision, and a capped or refused read preserves report authority like
// every other metadata failure.
func reportFilesAfterSource(source *core.SessionSource) func(sd, agent, report string, env platform.Env) []string {
	return func(sd, agent, report string, env platform.Env) []string {
		return reportFilesAfterRead(sd, agent, report, env, source.ObserveReport,
			func(sd, report string) string { return gittree.SidecarInspectSource(source, sd, report) },
			func(sidecar, agent string, env platform.Env) []gittree.ChangedPath {
				return gittree.ChangedPathsInspectSource(source, sidecar, agent, env)
			})
	}
}

// reportFilesAfterRead is the shared "changed after the report" comparison:
// the report metadata source (the ordinary stat on the command and
// read-only paths, the source's confined observation on the inspection
// path), the sidecar location and the changed-paths reader are the only
// decisions that differ between the command and the inspection callers.
func reportFilesAfterRead(sd, agent, report string, env platform.Env, reportStat func(string) (fs.FileInfo, error), sidecarLocate func(sd, report string) string, changedPaths func(sidecar, agent string, env platform.Env) []gittree.ChangedPath) []string {
	info, err := reportStat(report)
	if err != nil {
		return nil
	}
	paths := changedPaths(sidecarLocate(sd, report), agent, env)
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
	state, cause, _ := reportObservation(sd, agent, report, ctx, svc, env, false, nil)
	return state, cause
}

// observedReportInfo is the report's metadata: the ordinary stat on the
// command path (nil source), the confined observation through the
// approved report root on the inspection path (size/mtime/identity only,
// never the report bytes).
func observedReportInfo(req agentStatusRequest, report string) (fs.FileInfo, error) {
	if req.source == nil {
		return os.Stat(report)
	}
	return sourceReportInfo(req.source, report)
}

// sourceReportInfo is the confined metadata of one approved report path
// (the state reports dir or the known scratch reports dir, in any base
// alias); a path that is not an approved one is os.ErrNotExist.
func sourceReportInfo(source *core.SessionSource, report string) (fs.FileInfo, error) {
	return source.ObserveReport(report)
}

// markerForSource is the report observation's marker read: the ordinary
// wait file when no source is supplied, the confined bounded source read
// when the inspection supplies one. poisoned marks a present marker the
// confined read refused (an oversize, linked or nonregular marker): the
// read is ("", false, true), never the absent ("", false, false) the
// unsafe bytes would otherwise collapse to; absent is ("", false, false).
func markerForSource(source *core.SessionSource, sd, agent, name string) (string, bool, bool) {
	if source == nil {
		raw, ok := readWaitFile(sd, agent, name)
		return raw, ok, false
	}
	raw, err := source.ReadMetadata("wait", agent+"."+name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, false
		}
		return "", false, true
	}
	return strings.TrimRight(raw, "\n"), true, false
}

// reportObservation is the shared report observation: the same completion
// evidence as wait, without advancing clocks or writing settlement markers,
// plus the state word the probe actually returned (terminal: "" when the
// branch settled before any probe, working/idle/gone/unavailable when a
// probe ran). readOnly switches the changed-paths decision to the read-only
// reader, so the inspection never materializes the skill bundle or Dies on
// unavailable role metadata; when that metadata is unavailable the
// read-only changed paths are nil and report authority is preserved, as
// the reportFilesAfter contract specifies. source (nil for every command
// caller) confines the report metadata and the marker reads under the
// inspection's opened roots, and a present-invalid required settlement
// marker (report-working-final, report-stable-at, report-settled) is the
// conservative unknown at its own branch — never the absence it would
// otherwise read as — while the independently observed report stays
// report-ready.
func reportObservation(sd, agent, report string, ctx *core.Config, svc transport.Services, env platform.Env, readOnly bool, source *core.SessionSource) (string, string, string) {
	var filesAfter func(sd, agent, report string, env platform.Env) []string
	switch {
	case source != nil:
		filesAfter = reportFilesAfterSource(source)
	case readOnly:
		filesAfter = reportFilesAfterReadOnly
	default:
		filesAfter = reportFilesAfter
	}
	var info fs.FileInfo
	var err error
	if source != nil {
		info, err = sourceReportInfo(source, report)
	} else {
		info, err = os.Stat(report)
	}
	if err != nil {
		return "done", "", ""
	}
	identity := reportIdentity(report, info)
	max := reportWorkingMax(ctx, env)
	final, _, finalPoisoned := markerForSource(source, sd, agent, "report-working-final")
	if finalPoisoned && max != 0 {
		// The required report-working-final marker exists but cannot be
		// read as safe metadata: the settled decision cannot fire on
		// unreadable evidence, and the absence it would otherwise read
		// as is a fabricated continuation — the conservative unknown,
		// never the done or the pane state it would otherwise render.
		// The independently observed report stays report-ready.
		return "unknown", "the report-working-final marker exists but cannot be read as safe metadata", ""
	}
	if final == identity || max == 0 {
		return "done", "", ""
	}
	at, atOK, atPoisoned := reportStableAtFor(source, sd, agent, identity)
	if atPoisoned {
		// The required report-stable-at marker exists but cannot be read
		// as safe metadata: the same conservative unknown at its own
		// branch, never the absent-marker continuation.
		return "unknown", "the report-stable-at marker exists but cannot be read as safe metadata", ""
	}
	if atOK && platform.Now().Unix()-at > max {
		return "done", "", ""
	}
	st := readState(svc, agent)
	switch st.State {
	case "gone", "unavailable":
		return "done", "", st.State
	case "idle", "done":
		settled, _, settledPoisoned := markerForSource(source, sd, agent, "report-settled")
		if settledPoisoned {
			// The required report-settled marker exists but cannot be read
			// as safe metadata on the settling branch: the same
			// conservative unknown, never the settled done it would
			// otherwise read from unreadable evidence.
			return "unknown", "the report-settled marker exists but cannot be read as safe metadata", st.State
		}
		if settled == identity || len(filesAfter(sd, agent, report, env)) == 0 {
			return "done", "", st.State
		}
		return "working", "report draft: files changed after it", st.State
	default:
		return "working", "report draft: pane working", st.State
	}
}

// reportStableAtFor is the report observation's stable-at read through
// the source-aware marker read (nil source: the ordinary wait file);
// poisoned marks a present marker the confined read refused (the stable
// decision cannot fire on unreadable evidence).
func reportStableAtFor(source *core.SessionSource, sd, agent, identity string) (int64, bool, bool) {
	raw, ok, poisoned := markerForSource(source, sd, agent, "report-stable-at")
	if poisoned {
		return 0, false, true
	}
	parts := strings.SplitN(raw, "\t", 2)
	if !ok || len(parts) != 2 || parts[1] != identity || !uintPattern.MatchString(parts[0]) {
		return 0, false, false
	}
	at, err := strconv.ParseInt(parts[0], 10, 64)
	return at, err == nil && at > 0, false
}
