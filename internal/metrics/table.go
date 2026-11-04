package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const (
	metricsTableStartMarker = "<!-- soho:metrics-table:start -->"
	metricsTableEndMarker   = "<!-- soho:metrics-table:end -->"
	metricsTableEmpty       = "_No field summaries yet._"
	metricsTableHeader      = "| Role | Type | Kind | Model | Effort | n | Median min | Partial | Precision | Missed | Confidence |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n"
)

// tableLine is one line of a `metrics export` summary (issue #39, part 3).
// Every key is optional; pointers distinguish absence from zero.
type tableLine struct {
	Role          *string            `json:"role"`
	Type          *string            `json:"type"`
	Kind          *string            `json:"kind"`
	Model         *string            `json:"model"`
	Effort        *string            `json:"effort"`
	DurationS     *float64           `json:"duration_s"`
	Items         *tableItems        `json:"items"`
	FindingsReal  *float64           `json:"findings_real"`
	FindingsFalse *float64           `json:"findings_false"`
	Missed        map[string]float64 `json:"missed"`
}

type tableItems struct {
	Done    *float64 `json:"done"`
	Partial *float64 `json:"partial"`
	Skipped *float64 `json:"skipped"`
}

// groupKey is the role × type × kind × model × effort cell identity; an
// absent field is the table's missing marker.
type groupKey struct {
	role, typ, kind, model, effort string
}

// tableGroup accumulates the lines of one cell. Amendment lines count like
// any other line.
type tableGroup struct {
	role, typ, kind, model, effort string
	n                              int
	durations                      []float64
	itemsSeen, itemsPartial        int
	findingsReal, findingsFalse    float64
	missedSum                      float64
}

// CmdTable turns exported summaries into the generated table of
// references/agent-profiles.md (issue #39, part 4). With --write it replaces
// the generated block atomically; otherwise the table goes to stdout. It
// never reads or writes the project state.
func CmdTable(args []string, c CommandContext) int {
	writeTarget, files, ok := parseTableArgs(args)
	if !ok {
		core.DieFriction(usageLine, 2, c.FrictionLog, "metrics")
	}
	lines, malformed := readExportLines(files, c)
	if malformed > 0 {
		_, _ = fmt.Fprintf(platform.Stderr, "metrics table: skipped %d malformed line(s)\n", malformed)
	}
	content, rows := renderTable(collectGroups(lines))
	if writeTarget == "" {
		_, _ = fmt.Fprintln(platform.Stdout, content)
		return 0
	}
	rewritten, err := replaceMetricsTableBlock(writeTarget, content)
	if err != nil {
		core.DieFriction("metrics table: "+writeTarget+": "+err.Error(), 2, c.FrictionLog, "metrics")
	}
	if err := platform.AtomicWrite(writeTarget, rewritten); err != nil {
		core.DieFriction("metrics table: cannot write "+writeTarget+": "+err.Error(), 4, c.FrictionLog, "metrics")
	}
	_, _ = fmt.Fprintf(platform.Stdout, "metrics table: wrote %d row(s) to %s\n", rows, writeTarget)
	return 0
}

// parseTableArgs splits `table [--write <file>] <export.jsonl>...`. Anything
// starting with -- other than --write, a repeated or valueless --write, or no
// file at all is a usage error.
func parseTableArgs(args []string) (writeTarget string, files []string, ok bool) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--write":
			if writeTarget != "" || i+1 >= len(args) {
				return "", nil, false
			}
			writeTarget = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "--") {
				return "", nil, false
			}
			files = append(files, args[i])
		}
	}
	if len(files) == 0 {
		return "", nil, false
	}
	return writeTarget, files, true
}

// readExportLines reads every file, line by line. A missing or unreadable
// file exits 4 with the path; a line that is not a JSON object is counted
// malformed; blank lines are ignored.
func readExportLines(files []string, c CommandContext) (lines []tableLine, malformed int) {
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			core.DieFriction("metrics table: cannot read "+path, 4, c.FrictionLog, "metrics")
		}
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			l, ok := parseExportLine(line)
			if !ok {
				malformed++
				continue
			}
			lines = append(lines, l)
		}
	}
	return lines, malformed
}

// parseExportLine accepts only JSON objects; JSON values that are not
// objects (arrays, scalars, null) and wrong-typed fields are malformed.
func parseExportLine(line string) (tableLine, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &object); err != nil || object == nil {
		return tableLine{}, false
	}
	var l tableLine
	if err := json.Unmarshal([]byte(line), &l); err != nil {
		return tableLine{}, false
	}
	return l, true
}

// fieldOr maps an absent (or empty) field to the table's missing marker.
func fieldOr(v *string) string {
	if v == nil || *v == "" {
		return "-"
	}
	return *v
}

func collectGroups(lines []tableLine) []*tableGroup {
	byKey := make(map[groupKey]*tableGroup)
	var out []*tableGroup
	for _, l := range lines {
		key := groupKey{
			role:   fieldOr(l.Role),
			typ:    fieldOr(l.Type),
			kind:   fieldOr(l.Kind),
			model:  fieldOr(l.Model),
			effort: fieldOr(l.Effort),
		}
		g := byKey[key]
		if g == nil {
			g = &tableGroup{role: key.role, typ: key.typ, kind: key.kind, model: key.model, effort: key.effort}
			byKey[key] = g
			out = append(out, g)
		}
		g.n++
		if l.DurationS != nil {
			g.durations = append(g.durations, *l.DurationS)
		}
		if l.Items != nil {
			g.itemsSeen++
			if l.Items.Partial != nil && *l.Items.Partial > 0 {
				g.itemsPartial++
			}
		}
		if l.FindingsReal != nil {
			g.findingsReal += *l.FindingsReal
		}
		if l.FindingsFalse != nil {
			g.findingsFalse += *l.FindingsFalse
		}
		for _, v := range l.Missed {
			g.missedSum += v
		}
	}
	sortGroups(out)
	return out
}

// sortGroups orders rows by role and type alphabetically, then n descending,
// then kind, model and effort alphabetically, so the output is deterministic
// for the same input.
func sortGroups(groups []*tableGroup) {
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.role != b.role {
			return a.role < b.role
		}
		if a.typ != b.typ {
			return a.typ < b.typ
		}
		if a.n != b.n {
			return a.n > b.n
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.model != b.model {
			return a.model < b.model
		}
		return a.effort < b.effort
	})
}

func renderTable(groups []*tableGroup) (content string, rows int) {
	if len(groups) == 0 {
		return metricsTableEmpty, 0
	}
	var b strings.Builder
	b.WriteString(metricsTableHeader)
	for _, g := range groups {
		_, _ = fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d | %s | %s | %s | %s | %s |\n",
			tableCell(g.role), tableCell(g.typ), tableCell(g.kind), tableCell(g.model), tableCell(g.effort), g.n,
			medianMinutes(g.durations), partialPercent(g.itemsSeen, g.itemsPartial),
			precisionCell(g), missedCell(g), confidenceBand(g.n))
	}
	return strings.TrimSuffix(b.String(), "\n"), len(groups)
}

// tableCell renders a group field for a printed markdown cell: \r and \n
// become spaces (the row must stay one line), backslashes are doubled so a
// \| is unambiguous, and | is escaped. Grouping and ordering keep using the
// original value; only the printed cell changes.
func tableCell(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "|", "\\|")
	return strings.ReplaceAll(strings.ReplaceAll(escaped, "\r", " "), "\n", " ")
}

// medianMinutes is the median of duration_s in minutes, one decimal; "-"
// when the group has no duration_s at all.
func medianMinutes(durations []float64) string {
	if len(durations) == 0 {
		return "-"
	}
	sorted := append([]float64(nil), durations...)
	sort.Float64s(sorted)
	var v float64
	if len(sorted)%2 == 1 {
		v = sorted[len(sorted)/2]
	} else {
		v = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	return fmt.Sprintf("%.1f", v/60)
}

// partialPercent is the rounded integer share of the lines with
// items.partial > 0 among the lines that have items, with a % sign; "-"
// when none do.
func partialPercent(seen, partial int) string {
	if seen == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", int(math.Round(100*float64(partial)/float64(seen))))
}

// precisionCell is only filled for review roles: the share of labeled
// findings that turned out real, as a rounded integer percent with a % sign
// followed by (k), the total labeled findings; "-" without labels or
// outside them.
func precisionCell(g *tableGroup) string {
	if !core.IsReviewRole(g.role) {
		return "-"
	}
	labeled := g.findingsReal + g.findingsFalse
	if labeled == 0 {
		return "-"
	}
	percent := int(math.Round(100 * g.findingsReal / labeled))
	return fmt.Sprintf("%d%% (%s)", percent, formatCount(labeled))
}

// missedCell is only filled for review roles: the sum of every value of
// missed; "-" outside them.
func missedCell(g *tableGroup) string {
	if !core.IsReviewRole(g.role) {
		return "-"
	}
	return formatCount(g.missedSum)
}

// confidenceBand maps the sample size to the confidence the table reports.
func confidenceBand(n int) string {
	switch {
	case n < 5:
		return "low"
	case n < 20:
		return "medium"
	default:
		return "high"
	}
}

// formatCount renders an integral count without a decimal point.
func formatCount(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// replaceMetricsTableBlock rebuilds the file with the table between the
// markers (a blank line before and after it), keeping everything outside the
// block byte for byte. It refuses a file without exactly one start marker,
// without exactly one end marker, or with the end before the start.
func replaceMetricsTableBlock(path, content string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read: " + err.Error())
	}
	lines := strings.Split(string(data), "\n")
	startCount, endCount, start, end := 0, 0, -1, -1
	for i, line := range lines {
		switch strings.TrimSuffix(line, "\r") {
		case metricsTableStartMarker:
			startCount++
			start = i
		case metricsTableEndMarker:
			endCount++
			end = i
		}
	}
	if startCount != 1 || endCount != 1 {
		return "", errors.New("exactly one metrics-table marker pair required")
	}
	if end < start {
		return "", errors.New("metrics-table end marker before the start marker")
	}
	rebuilt := make([]string, 0, len(lines)+2)
	rebuilt = append(rebuilt, lines[:start+1]...)
	rebuilt = append(rebuilt, "", content, "")
	rebuilt = append(rebuilt, lines[end:]...)
	return strings.Join(rebuilt, "\n"), nil
}
