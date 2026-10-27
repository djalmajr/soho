// Package metrics: `metrics export` writes the anonymized summary of the
// lines recorded in <state>/metrics.jsonl (issue #39, part 3).
package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/stats"
)

// exportCopyKeys are the input keys copied into the summary line, in the
// output order. The closed list: any other input key (agent, report, ts, a
// path, a URL, or an unknown key) stays out of the anonymized line. `items`
// and `severity` keep their slot in the order but are projected to their own
// closed keys (exportItemsKeys/exportSeverityKeys), like `for`.
var exportCopyKeys = []string{
	"role", "kind", "model", "effort", "family", "type",
	"duration_s", "amendment", "session", "arrival",
	"items", "verdict", "findings", "severity", "verdict_effective",
}

// exportForKeys are the only keys kept from each object of the input `for`
// array (the authors a review role checked).
var exportForKeys = []string{"kind", "model", "effort", "family"}

// exportItemsKeys are the only keys kept from the input `items` object (the
// done/partial/skipped counters part 1 writes, and env for environment-limited
// items, DJA-340).
var exportItemsKeys = []string{"done", "partial", "skipped", "env"}

// exportSeverityKeys are the only keys kept from the input `severity` object
// (the P0..P3 finding counters).
var exportSeverityKeys = []string{"P0", "P1", "P2", "P3"}

var exportLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// exportLine is one settled report line (a metrics.jsonl line without a
// label) with its parsed ts.
type exportLine struct {
	obj  *jsonjs.Object
	tsAt time.Time // zero when ts is absent or not RFC 3339
}

// exportMark is one mark line (label:"mark") and the fields it carries.
type exportMark struct {
	report    string
	labels    map[string]any     // finding id -> real|false|other
	missed    map[string]float64 // severity -> count
	amendment string
}

// exportArgOptions are the validated flags of `metrics export`.
type exportArgOptions struct {
	since    time.Time
	hasSince bool
	label    string
}

// CmdExport writes the anonymized summary of the recorded lines (issue #39,
// part 3). It is read-only: it scans every <state root>/*/metrics.jsonl (all
// workspaces), applies the mark lines of a report to the settled lines that
// share the report name, and prints one JSON line per settled report to
// stdout, sorted by ts. Without any file it exits 0 with no output.
func CmdExport(args []string, c CommandContext) int {
	opts := parseExportArgs(args, c.FrictionLog)
	stateRoot := core.StateRootPath(c.Config, c.Env, c.Cwd)
	// The standard state refusal, the same as stats and session: from inside
	// the skill the state root would land inside the skill, so nothing runs.
	if skill := core.StateInSkill(c.Config, c.Env, c.Cwd); skill != "" {
		platform.Die(core.StateInSkillMessage("metrics export", stateRoot, skill), 2)
	}
	label := opts.label
	if label == "" {
		label = defaultProjectLabel(stateRoot)
	}
	lines, marks, malformed := scanMetricsLines(stateRoot)
	if opts.hasSince {
		kept := lines[:0]
		for _, line := range lines {
			// A line whose ts cannot be parsed cannot be dated, so it is
			// filtered out when a since bound is in force.
			if !line.tsAt.IsZero() && !line.tsAt.Before(opts.since) {
				kept = append(kept, line)
			}
		}
		lines = kept
	}
	sort.SliceStable(lines, func(i, j int) bool {
		ti, tj := lines[i].tsAt, lines[j].tsAt
		switch {
		case ti.IsZero():
			return !tj.IsZero()
		case tj.IsZero():
			return false
		default:
			return ti.Before(tj)
		}
	})
	for _, line := range lines {
		_, _ = fmt.Fprintln(platform.Stdout, exportSummaryLine(line, marks, label))
	}
	if malformed > 0 {
		_, _ = fmt.Fprintf(platform.Stderr, "metrics export: skipped %d malformed line(s)\n", malformed)
	}
	return 0
}

// parseExportArgs validates the export flags. The same values stats accepts
// for --since, the same missing-value and invalid-date errors (with the
// export command name); --project-label must match the label pattern. Any
// other argument exits 2 with the skeleton usage line. Duplicate flags: the
// last one wins, like stats.
func parseExportArgs(args []string, logFile string) exportArgOptions {
	var opts exportArgOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 >= len(args) {
				core.DieFriction("metrics export: --since expects a date", 2, logFile, "metrics")
			}
			i++
			value := args[i]
			since, ok := stats.ParseSince(value)
			if !ok {
				core.DieFriction("metrics export: --since expects AAAA-MM-DD or an ISO date (got '"+value+"')", 2, logFile, "metrics")
			}
			opts.since, opts.hasSince = since, true
		case "--project-label":
			if i+1 >= len(args) {
				core.DieFriction("metrics export: --project-label expects a label", 2, logFile, "metrics")
			}
			i++
			value := args[i]
			if !exportLabelPattern.MatchString(value) {
				core.DieFriction("metrics export: --project-label expects [a-z0-9][a-z0-9-]{0,31} (got '"+value+"')", 2, logFile, "metrics")
			}
			opts.label = value
		default:
			core.DieFriction(usageLine, 2, logFile, "metrics")
		}
	}
	return opts
}

// defaultProjectLabel derives the stable, name-free project label: "p-" plus
// the first 8 hex chars of the sha256 of the base name of the project root —
// the directory that contains the state root. The same directory name gives
// the same label on every platform.
func defaultProjectLabel(stateRoot string) string {
	base := filepath.Base(filepath.Dir(stateRoot))
	sum := sha256.Sum256([]byte(base))
	return "p-" + hex.EncodeToString(sum[:])[:8]
}

// scanMetricsLines reads every <state root>/*/metrics.jsonl, in sorted path
// order, and classifies each line. It is read-only. A line that is not valid
// JSON, or that is not a JSON object, is counted as malformed (blank lines
// are ignored). A line without a label is a settled report; a label:"mark"
// line with a report name is a mark; any other labeled line is neither and
// stays out of the summary.
func scanMetricsLines(stateRoot string) (lines []exportLine, marks []exportMark, malformed int) {
	paths, err := filepath.Glob(filepath.Join(stateRoot, "*", "metrics.jsonl"))
	if err != nil {
		return nil, nil, 0
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // an unreadable file contributes no lines
		}
		for _, raw := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			value, err := jsonjs.Parse([]byte(raw))
			if err != nil {
				malformed++
				continue
			}
			obj, ok := value.(*jsonjs.Object)
			if !ok {
				malformed++
				continue
			}
			if label, hasLabel := obj.Get("label"); hasLabel {
				if label != "mark" {
					continue
				}
				if mark, ok := markFromLine(obj); ok {
					marks = append(marks, *mark)
				}
				continue
			}
			line := exportLine{obj: obj}
			if ts, ok := obj.Get("ts"); ok {
				if text, isText := ts.(string); isText {
					if parsed, err := time.Parse(time.RFC3339, text); err == nil {
						line.tsAt = parsed
					}
				}
			}
			lines = append(lines, line)
		}
	}
	return lines, marks, malformed
}

// markFromLine reads the mark fields of a label:"mark" line. Only the given
// keys count: report (a mark without it applies to nothing), findings
// (finding id -> real|false), missed (severity -> count) and amendment. A
// field with the wrong type degrades that field only; the line still counts
// as a mark.
func markFromLine(obj *jsonjs.Object) (*exportMark, bool) {
	report, hasReport := obj.Get("report")
	if !hasReport {
		return nil, false
	}
	name, isName := report.(string)
	if !isName || name == "" {
		return nil, false
	}
	mark := &exportMark{report: name}
	if value, ok := obj.Get("findings"); ok {
		if fields, isFields := value.(*jsonjs.Object); isFields {
			mark.labels = make(map[string]any)
			for _, key := range fields.Keys() {
				if item, ok := fields.Get(key); ok {
					mark.labels[key] = item
				}
			}
		}
	}
	if value, ok := obj.Get("missed"); ok {
		if fields, isFields := value.(*jsonjs.Object); isFields {
			mark.missed = make(map[string]float64)
			for _, key := range fields.Keys() {
				if item, ok := fields.Get(key); ok {
					if count, isCount := item.(float64); isCount {
						mark.missed[key] = count
					}
				}
			}
		}
	}
	if value, ok := obj.Get("amendment"); ok {
		if text, isText := value.(string); isText {
			mark.amendment = text
		}
	}
	return mark, true
}

// markMerge is the fold of the marks of one report in file order.
type markMerge struct {
	real, falseCount int
	missed           map[string]float64
	amendment        string
}

// mergeMarks folds the marks of one report: for the same finding the last
// label wins, the missed counts are summed across the marks, and the last
// amendment is the cause. It returns nil when the report has no mark line.
func mergeMarks(line exportLine, marks []exportMark) *markMerge {
	report, ok := line.obj.Get("report")
	if !ok {
		return nil
	}
	name, isName := report.(string)
	if !isName || name == "" {
		return nil
	}
	merge := &markMerge{missed: make(map[string]float64)}
	merged := false
	labels := make(map[string]any)
	for _, mark := range marks {
		if mark.report != name {
			continue
		}
		merged = true
		for id, value := range mark.labels {
			labels[id] = value // the last label of a finding wins
		}
		for severity, count := range mark.missed {
			merge.missed[severity] += count
		}
		if mark.amendment != "" {
			merge.amendment = mark.amendment
		}
	}
	if !merged {
		return nil
	}
	for _, value := range labels {
		switch value {
		case "real":
			merge.real++
		case "false":
			merge.falseCount++
		}
	}
	return merge
}

// exportSummaryLine builds the anonymized JSON line for one settled report:
// the project label, the UTC date of ts, the closed list of input keys
// copied as they are, and the mark-derived counts when the report was marked.
func exportSummaryLine(line exportLine, marks []exportMark, label string) string {
	out := jsonjs.O("project", label)
	if !line.tsAt.IsZero() {
		out.Set("date", line.tsAt.UTC().Format("2006-01-02"))
	}
	for _, key := range exportCopyKeys {
		value, ok := line.obj.Get(key)
		if !ok {
			continue
		}
		// items and severity are projected to their closed keys, like for:
		// a stray key stays out, and a value that is not an object drops
		// the whole field.
		switch key {
		case "items":
			if projected, isObject := projectClosedObject(value, exportItemsKeys); isObject {
				out.Set(key, projected)
			}
		case "severity":
			if projected, isObject := projectClosedObject(value, exportSeverityKeys); isObject {
				out.Set(key, projected)
			}
		default:
			out.Set(key, value)
		}
	}
	if value, ok := line.obj.Get("for"); ok {
		if authors, isAuthors := value.([]any); isAuthors {
			projected := make([]any, 0, len(authors))
			for _, author := range authors {
				if authorObj, isObj := projectClosedObject(author, exportForKeys); isObj {
					projected = append(projected, authorObj)
				}
			}
			out.Set("for", projected)
		}
	}
	if merge := mergeMarks(line, marks); merge != nil {
		out.Set("findings_real", merge.real)
		out.Set("findings_false", merge.falseCount)
		if findings, ok := line.obj.Get("findings"); ok {
			if total, isTotal := findings.(float64); isTotal {
				// More labels than findings are possible (marks on a report
				// with fewer findings); the counter never goes below zero.
				unlabeled := int(total) - merge.real - merge.falseCount
				if unlabeled < 0 {
					unlabeled = 0
				}
				out.Set("findings_unlabeled", unlabeled)
			}
		}
		if len(merge.missed) > 0 {
			missed := jsonjs.O()
			for _, key := range sortedKeys(merge.missed) {
				missed.Set(key, merge.missed[key])
			}
			out.Set("missed", missed)
		}
		if merge.amendment != "" {
			out.Set("amendment_cause", merge.amendment)
		}
	}
	return jsonjs.Stringify(out)
}

// projectClosedObject keeps only the closed keys of an input object, in the
// closed order; ok is false when the value is not an object.
func projectClosedObject(value any, keys []string) (*jsonjs.Object, bool) {
	src, isObj := value.(*jsonjs.Object)
	if !isObj {
		return nil, false
	}
	dst := jsonjs.O()
	for _, key := range keys {
		if item, ok := src.Get(key); ok {
			dst.Set(key, item)
		}
	}
	return dst, true
}

func sortedKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
