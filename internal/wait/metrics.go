// One-line-per-report performance metrics (issue #39, first slice).
//
// recordMetrics appends one JSON line to <state dir>/metrics.jsonl for every
// report that settles, when the config key metrics is on (default off). Both
// the wait command and the dispatch that waits reach it through the wait's
// done case. The line carries only the performance facts — timing, role,
// kind, model, effort, family, the brief's slice type, item counts and the
// review verdicts: never brief text, code, diffs, file paths (not the
// report's own), URLs, credentials or the report body.
package wait

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
	"github.com/djalmajr/soho/internal/taskreport"
)

// The same fence and state-position rules reportscan applies to [partial],
// so the [done] and [skipped] counts agree with it line for line.
var (
	metricsFenceOpen     = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	metricsFenceClose    = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t\\r]*$")
	metricsDoneMarker    = regexp.MustCompile("[`*]*\\[done\\][`*]*")
	metricsSkippedMarker = regexp.MustCompile("[`*]*\\[skipped\\][`*]*")
	metricsAnyDone       = regexp.MustCompile(`\[done\]`)
	metricsAnySkipped    = regexp.MustCompile(`\[skipped\]`)
	metricsStateBefore   = regexp.MustCompile(`^[ \t]*(?:[-*+][ \t]+|[0-9]+\.[ \t]+|#{1,6}[ \t]+|>[ \t]*|\[[ xX]\][ \t]+)?[ \t]*$`)
	metricsCellBefore    = regexp.MustCompile(`\|[ \t]*$`)
	metricsCellAfter     = regexp.MustCompile(`^[ \t]*(?:\||$)`)
	metricsDashBefore    = regexp.MustCompile("[:\\x{2014}\\x{2013}-][`*]*[ \\t]+$")
)

// recordMetrics appends the report's one line to metrics.jsonl when
// metrics=on. One line per report: the metrics-recorded marker works like
// partial-warned, so a second wait on the same report appends nothing. The
// marker is only written after a successful append; a write failure is a
// core.Warn and never changes the wait result.
func recordMetrics(sd, agent, report, text string, header *reportscan.ReviewHeaderResult, partial int, ctx *core.Config, env platform.Env) {
	if core.Cfg(ctx, "metrics", "off", env) != "on" {
		return
	}
	prev, _ := readWaitFile(sd, agent, "metrics-recorded")
	if prev == report {
		return
	}
	f, err := os.OpenFile(filepath.Join(sd, "metrics.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err == nil {
		_, werr := f.WriteString(metricsLine(sd, agent, report, text, header, partial) + "\n")
		cerr := f.Close()
		if werr != nil || cerr != nil {
			if werr != nil {
				err = werr
			} else {
				err = cerr
			}
		} else if err := os.WriteFile(filepath.Join(sd, "wait", agent+".metrics-recorded"), []byte(report+"\n"), 0o666); err != nil {
			sendWarning("metrics: could not mark the report of '" + agent + "' as recorded: " + err.Error())
			return
		}
	}
	if err != nil {
		sendWarning("metrics: could not record the report of '" + agent + "' in metrics.jsonl: " + err.Error())
	}
}

// metricsLine builds the one JSON line for a settled report. Every field is
// optional except ts, agent and report: a value that does not exist (no
// sidecar, no Type line, empty roster column) stays out of the line. The
// report field is the report's file name (filepath.Base), never a path.
func metricsLine(sd, agent, report, text string, header *reportscan.ReviewHeaderResult, partial int) string {
	line := jsonjs.O("ts", platform.Now().UTC().Format(time.RFC3339), "agent", agent, "report", filepath.Base(report))
	roster := strings.Split(core.RosterLine(sd, agent), "\t")
	role := TaskRole(sd, agent)
	if role != "" {
		line.Set("role", role)
	}
	if v := field(roster, 2); v != "" {
		line.Set("kind", v)
	}
	if v := field(roster, 8); v != "" {
		line.Set("model", v)
	}
	if v := field(roster, 14); v != "" {
		line.Set("effort", v)
	}
	if v := field(roster, 4); v != "" {
		line.Set("family", v)
	}
	if v := metricsBriefType(sd, report); v != "" {
		line.Set("type", v)
	}
	if d, ok := reportDurationS(sd, agent, report); ok {
		line.Set("duration_s", d)
	}
	if amendmentReport(sd, agent, report) {
		line.Set("amendment", true)
	}
	session, arrival, sidecarFor := metricsSidecarFields(sd, report)
	if session != "" {
		line.Set("session", session)
	}
	if arrival != "" {
		line.Set("arrival", arrival)
	}
	line.Set("items", jsonjs.O("done", metricsMarkerCount(text, "[done]"), "partial", partial, "skipped", metricsMarkerCount(text, "[skipped]")))
	// The review roles take the review fields with exactly the values the
	// wait line prints (verdict/findings/severity from the header, the
	// effective verdict from the header or a partial item). The "for" field
	// copies the dispatch sidecar's anonymized --for authors, right after
	// verdict_effective: the author names stay out of the line.
	if core.IsReviewRole(role) {
		if header != nil {
			line.Set("verdict", header.Verdict)
			line.Set("findings", header.Findings)
			line.Set("severity", jsonjs.O("P0", header.Severity["P0"], "P1", header.Severity["P1"], "P2", header.Severity["P2"], "P3", header.Severity["P3"]))
		}
		if header != nil || partial > 0 {
			effective := "pass"
			if partial > 0 || header.Verdict == "fail" || header.Severity["P0"]+header.Severity["P1"]+header.Severity["P2"] > 0 {
				effective = "fail"
			}
			line.Set("verdict_effective", effective)
		}
		if len(sidecarFor) > 0 {
			line.Set("for", sidecarFor)
		}
	}
	return jsonjs.Stringify(line)
}

// metricsBriefType reads the slice Type value from the same brief file the
// stats command reads for this report: the composed brief in the state dir
// when it exists, else the .brief.md beside the report. The first `Type:`
// line decides; a value outside the accepted set keeps the field out.
func metricsBriefType(sd, report string) string { return BriefType(composedBriefBody(sd, report)) }

func composedBriefBody(sd, report string) string {
	base := strings.TrimSuffix(filepath.Base(report), ".md")
	file := filepath.Join(filepath.Dir(report), base+".brief.md")
	if _, err := os.Stat(filepath.Join(sd, "briefs", base+".md")); err == nil {
		file = filepath.Join(sd, "briefs", base+".md")
	}
	text, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	return text
}

var parityItem = regexp.MustCompile(`^\s*(?:[-*+]\s+|[0-9]+[.)]\s+|\|)`)
var paritySame = regexp.MustCompile(`\[same\]`)
var parityNA = regexp.MustCompile(`\[n/a\]`)
var parityDiverges = regexp.MustCompile(`\[diverges:([^\]]*)\]`)

func markedReferenceDivergence(raw string) bool {
	match := parityDiverges.FindStringSubmatch(raw)
	return match != nil && strings.TrimSpace(match[1]) != ""
}

func checkReferenceParity(sd, agent, report, text string, line *jsonjs.Object) {
	body := composedBriefBody(sd, report)
	fence := ""
	hasReference := false
	for _, raw := range strings.Split(body, "\n") {
		if fence != "" {
			if close := metricsFenceClose.FindStringSubmatch(raw); close != nil && close[1][0] == fence[0] && len(close[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		if open := metricsFenceOpen.FindStringSubmatch(raw); open != nil {
			fence = open[1]
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(raw), "Reference:") {
			hasReference = true
			break
		}
	}
	if !hasReference {
		return
	}
	same, diverges, na, unmarked := 0, 0, 0, 0
	found, inSection := false, false
	fence = ""
	for _, raw := range strings.Split(text, "\n") {
		if fence != "" {
			if close := metricsFenceClose.FindStringSubmatch(raw); close != nil && close[1][0] == fence[0] && len(close[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		if open := metricsFenceOpen.FindStringSubmatch(raw); open != nil {
			fence = open[1]
			continue
		}
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "## ") {
			inSection = trim == "## Reference parity"
			if inSection {
				found = true
			}
			continue
		}
		if strings.HasPrefix(trim, "# ") {
			inSection = false
		}
		if !inSection || !parityItem.MatchString(raw) {
			continue
		}
		// Table separators and header rows are structure, not parity items.
		if strings.HasPrefix(trim, "|") && (strings.Contains(trim, "---") || strings.EqualFold(strings.Trim(trim, "| "), "item | parity")) {
			continue
		}
		marks := 0
		if paritySame.MatchString(raw) {
			same++
			marks++
		}
		if markedReferenceDivergence(raw) {
			diverges++
			marks++
		}
		if parityNA.MatchString(raw) {
			na++
			marks++
		}
		if marks != 1 {
			unmarked++
		}
	}
	line.Set("reference_parity", jsonjs.O("same", same, "diverges", diverges, "na", na, "unmarked", unmarked))
	if !found {
		sendWarning(fmt.Sprintf("report of '%s' has no Reference parity section", agent))
	} else if unmarked > 0 {
		sendWarning(fmt.Sprintf("report of '%s' has %d parity item(s) without a mark", agent, unmarked))
	}
}

// BriefType shares the settled-metrics Type vocabulary with dispatch.
// The first Type line outside a fenced example decides, including invalid values.
func BriefType(body string) string {
	fence := ""
	for _, raw := range strings.Split(body, "\n") {
		if fence != "" {
			if close := metricsFenceClose.FindStringSubmatch(raw); close != nil && close[1][0] == fence[0] && len(close[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		if open := metricsFenceOpen.FindStringSubmatch(raw); open != nil {
			fence = open[1]
			continue
		}
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Type:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Type:"))
		switch value {
		case "mechanical", "backend", "ui", "docs", "review", "security":
			return value
		}
		return ""
	}
	return ""
}

// metricsSidecarFields reads the dispatch sidecar's session, arrival and
// for array, from the state dir when the dispatch wrote it there, else from
// beside the report. The for array is the --for authors the dispatch
// anonymized (kind/model/effort/family objects, never names) and is copied
// to the review line as "for" (issue #39, part 2).
func metricsSidecarFields(sd, report string) (string, string, []any) {
	base := strings.TrimSuffix(filepath.Base(report), ".md")
	for _, file := range []string{
		filepath.Join(sd, "briefs", base+".dispatch.json"),
		filepath.Join(filepath.Dir(report), base+".dispatch.json"),
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		value, err := jsonjs.Parse(raw)
		obj, _ := value.(*jsonjs.Object)
		if err != nil || obj == nil {
			return "", "", nil
		}
		session, _ := sidecarString(obj, "session")
		arrival, _ := sidecarString(obj, "arrival")
		forEntries, _ := sidecarForArray(obj)
		return session, arrival, forEntries
	}
	return "", "", nil
}

func sidecarForArray(obj *jsonjs.Object) ([]any, bool) {
	value, ok := obj.Get("for")
	if !ok {
		return nil, false
	}
	entries, ok := value.([]any)
	if !ok || len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

func sidecarString(obj *jsonjs.Object, key string) (string, bool) {
	value, ok := obj.Get(key)
	if !ok {
		return "", false
	}
	str, ok := value.(string)
	if !ok || str == "" {
		return "", false
	}
	return str, true
}

// reportDurationS is the dispatch-to-report duration in whole seconds, the
// status command's task_s: from the last-report marker's mtime (the dispatch
// writes it) to the report's mtime, rounded.
func reportDurationS(sd, agent, report string) (float64, bool) {
	start, err := os.Stat(core.LastReportPath(sd, agent))
	if err != nil {
		return 0, false
	}
	end := platform.Now()
	if info, err := os.Stat(report); err == nil {
		end = info.ModTime()
	}
	return math.Round(end.Sub(start.ModTime()).Seconds()), true
}

// amendmentReport is true when the task report pointer says this report
// replaced an earlier one: only the dispatch's --amend writes a non-empty
// history with the new report as current.
func amendmentReport(sd, agent, report string) bool {
	pointer := taskreport.ReadTaskReportPointer(sd, agent)
	if pointer == nil {
		return false
	}
	current, _ := pointer.Get("current")
	if current != report {
		return false
	}
	history, _ := pointer.Get("history")
	items, _ := history.([]any)
	return len(items) > 0
}

// metricsMarkerCount counts report lines that state a [done] or [skipped]
// item outside fenced code blocks, the same way reportscan counts [partial]:
// a line counts at most once, and a line that names both [done] and
// [skipped] is ambiguous and counts neither.
func metricsMarkerCount(text, marker string) int {
	if text == "" {
		return 0
	}
	markerRE, anyMarker := metricsDoneMarker, metricsAnyDone
	if marker == "[skipped]" {
		markerRE, anyMarker = metricsSkippedMarker, metricsAnySkipped
	}
	count := 0
	inFence, fenceChar, fenceLen := false, byte(0), 0
	for _, line := range strings.Split(text, "\n") {
		if open := metricsFenceOpen.FindStringSubmatch(line); !inFence && open != nil {
			inFence, fenceChar, fenceLen = true, open[1][0], len(open[1])
			continue
		}
		if inFence {
			if close := metricsFenceClose.FindStringSubmatch(line); close != nil && close[1][0] == fenceChar && len(close[1]) >= fenceLen {
				inFence = false
			}
			continue
		}
		line = metricsLower(line)
		if !anyMarker.MatchString(line) || (metricsAnyDone.MatchString(line) && metricsAnySkipped.MatchString(line)) {
			continue
		}
		for _, loc := range markerRE.FindAllStringIndex(line, -1) {
			before, after := line[:loc[0]], line[loc[1]:]
			if metricsStateBefore.MatchString(before) || (metricsCellBefore.MatchString(before) && metricsCellAfter.MatchString(after)) || metricsDashBefore.MatchString(before) {
				count++
				break
			}
		}
	}
	return count
}

func metricsLower(value string) string {
	bytes := []byte(value)
	for i, b := range bytes {
		if b >= 'A' && b <= 'Z' {
			bytes[i] = b + ('a' - 'A')
		}
	}
	return string(bytes)
}
