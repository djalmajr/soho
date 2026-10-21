package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
)

var briefSectionHeadings = map[string][]string{
	"Goal":            {"Goal"},
	"Expected result": {"Expected result", "Acceptance", "Definition of done"},
	"Owned files":     {"Owned files", "Owned", "Scope"},
	"Forbidden":       {"Forbidden", "Non-goals", "Constraints"},
	"Report":          {"Report"},
}

var briefSectionPortugueseHeadings = map[string][]string{
	"Goal":            {"Objetivo", "Meta"},
	"Expected result": {"Resultado esperado", "Critérios de aceitação", "Critérios de aceite", "Pronto quando"},
	"Owned files":     {"Arquivos", "Escopo"},
	"Forbidden":       {"Proibido", "Fora do escopo", "Restrições"},
	"Report":          {"Relatório"},
}

var lintSectionOrder = []string{"Goal", "Expected result", "Owned files", "Forbidden", "Report"}
var lintSectionReasons = map[string]string{
	"Goal":                              "the worker does not know what the slice is for",
	"Expected result":                   "nothing says when the slice is done",
	"Owned files":                       "workers without owned files collide",
	"Forbidden":                         "nothing keeps the worker out of other files",
	"Report":                            "without a report section the worker may never write one",
	"no-git line: say 'no commit/push'": "the worker may commit or push",
}
var noGitLintMarker = "no-git line: say 'no commit/push'"

// `<slot>` counts only inside a path (next to a slash): briefs also use it as a
// label, as in "(`<slot>` = `build`)".
var unfilledPlaceholderBuiltins = []string{"AGENT_NAME", "WORKTREE_PATH", "<slot>"}
var slotInPath = regexp.MustCompile(`/<slot>|<slot>/`)
var missingSectionPattern = regexp.MustCompile(`\[([^\]]+)\]`)
var failureMatrixMarkers = []struct{ marker, reason string }{
	{"crash", "a crash between publish and prune/delete is not covered"},
	{"retry", "a repeated or retried step is not covered"},
	{"clock", "a clock that goes backwards is not covered"},
}
var emptyBacktickRun = regexp.MustCompile("`+")

// BriefLintOptions controls the role-dependent part of the brief contract.
type BriefLintOptions struct {
	ReadOnly        bool
	PathsOnly       bool
	WorkerCwd       string
	OrchestratorCwd string
}

// BriefLintResult is the diagnostic payload shared by lint and dispatch.
type BriefLintResult struct {
	Mode           string
	Warnings       []string
	MissingMessage string
}

// ParseBriefLintAliases parses Section=Heading1|Heading2 values. Malformed
// entries are returned in input order and valid entries remain active.
func ParseBriefLintAliases(value string) (map[string][]string, []string) {
	sections := make(map[string][]string)
	ignored := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			continue
		}
		eq := strings.IndexByte(item, '=')
		name := ""
		heads := []string(nil)
		if eq >= 0 {
			name = item[:eq]
			for _, heading := range strings.Split(item[eq+1:], "|") {
				if heading != "" {
					heads = append(heads, heading)
				}
			}
		}
		valid := false
		for _, section := range lintSectionOrder {
			if name == section {
				valid = true
				break
			}
		}
		if eq < 0 || !valid || len(heads) == 0 {
			ignored = append(ignored, item)
			continue
		}
		sections[name] = heads
	}
	return sections, ignored
}

func normalizeBriefHeading(value string) string {
	return text.JSLower(text.StripMarksNFD(value))
}

func startsWithHeadingPrefix(title string, prefixes []string, strictBoundary bool) bool {
	normalizedTitle := normalizeBriefHeading(title)
	for _, prefix := range prefixes {
		normalizedPrefix := normalizeBriefHeading(prefix)
		if normalizedPrefix == "" || !strings.HasPrefix(normalizedTitle, normalizedPrefix) {
			continue
		}
		if !strictBoundary {
			return true
		}
		remainder := strings.TrimLeftFunc(normalizedTitle[len(normalizedPrefix):], isLintWhitespace)
		if remainder == "" {
			return true
		}
		var r rune
		for _, first := range remainder {
			r = first
			break
		}
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func isLintWhitespace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ' ||
		unicode.Is(unicode.Zs, r) || r == '\u2028' || r == '\u2029' || r == '\ufeff'
}

func briefHeadingTitle(line string) (string, bool) {
	if strings.ContainsAny(line, "\r\u2028\u2029") {
		return "", false
	}
	level := 0
	for level < len(line) && level < 4 && line[level] == '#' {
		level++
	}
	if level < 1 || level > 3 || level >= len(line) || line[level] != ' ' {
		return "", false
	}
	i := level
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i == len(line) {
		return "", false
	}
	return line[i:], true
}

// BriefSectionTitleMatches shares the lint and ownership heading rules,
// including strict Portuguese boundaries and explicitly configured aliases.
func BriefSectionTitleMatches(title, label string, aliases map[string][]string) bool {
	return startsWithHeadingPrefix(title, briefSectionHeadings[label], false) ||
		startsWithHeadingPrefix(title, briefSectionPortugueseHeadings[label], true) ||
		startsWithHeadingPrefix(title, aliases[label], false)
}

func hasBriefSectionHeading(body, label string, aliases map[string][]string) bool {
	for _, line := range strings.Split(body, "\n") {
		title, ok := briefHeadingTitle(line)
		if !ok {
			continue
		}
		if BriefSectionTitleMatches(title, label, aliases) {
			return true
		}
	}
	return false
}

// BriefMissingSections returns the ordered, display-ready missing section list.
func BriefMissingSections(body string, readOnly bool, aliases map[string][]string) string {
	checks := lintSectionOrder
	if readOnly {
		checks = []string{"Goal", "Expected result", "Forbidden", "Report"}
	}
	var missing strings.Builder
	for _, label := range checks {
		if !hasBriefSectionHeading(body, label, aliases) {
			fmt.Fprintf(&missing, " [%s]", label)
		}
	}
	if !strings.Contains(text.ASCIILower(body), "commit") && !strings.Contains(text.ASCIILower(body), "push") {
		fmt.Fprintf(&missing, " [%s]", noGitLintMarker)
	}
	return missing.String()
}

// MissingSectionsReasons explains each missing label in the input order;
// labels without a known reason (the unfilled placeholder marks) add none.
func MissingSectionsReasons(missing string) string {
	labels := missingSectionPattern.FindAllStringSubmatch(missing, -1)
	reasons := make([]string, 0, len(labels))
	for _, label := range labels {
		reason := lintSectionReasons[label[1]]
		if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return strings.Join(reasons, "; ")
}

// FailureMatrixMissing checks only a level 1-3 Failure matrix section.
func FailureMatrixMissing(body string) []string {
	lines := strings.Split(body, "\n")
	start, level := -1, 0
	for i, line := range lines {
		title, gotLevel, ok := lintHeading(line)
		if ok && strings.HasPrefix(text.ASCIILower(title), "failure matrix") {
			start, level = i, gotLevel
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if gotLevel, ok := lintHeadingPrefix(lines[i]); ok && gotLevel <= level {
			end = i
			break
		}
	}
	section := strings.Join(lines[start+1:end], "\n")
	missing := make([]string, 0, 3)
	for _, item := range failureMatrixMarkers {
		if !strings.Contains(section, "["+item.marker+"]") {
			missing = append(missing, item.marker)
		}
	}
	return missing
}

func lintHeadingPrefix(line string) (int, bool) {
	level := 0
	for level < len(line) && level < 4 && line[level] == '#' {
		level++
	}
	if level < 1 || level > 3 || level == len(line) || line[level] != ' ' {
		return 0, false
	}
	return level, true
}

func lintHeading(line string) (string, int, bool) {
	level, ok := lintHeadingPrefix(line)
	if !ok {
		return "", 0, false
	}
	title, ok := briefHeadingTitle(line)
	return title, level, ok
}

// briefFenceMask reports, for each line, whether it belongs to a fenced code
// block: a fence line (three or more ` or ~, up to three leading spaces) or a
// line inside one. A fence closes only with the same character, at least as
// many marks, and nothing after but spaces or tabs.
func briefFenceMask(lines []string) []bool {
	mask := make([]bool, len(lines))
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0
	for i, line := range lines {
		indent := 0
		for indent < len(line) && indent < 4 && line[indent] == ' ' {
			indent++
		}
		if indent <= 3 && indent < len(line) && (line[indent] == '`' || line[indent] == '~') {
			end := indent
			for end < len(line) && line[end] == line[indent] {
				end++
			}
			if end-indent >= 3 {
				if !inFence {
					inFence, fenceChar, fenceLen = true, line[indent], end-indent
				} else if line[indent] == fenceChar && end-indent >= fenceLen && strings.Trim(line[end:], " \t\r") == "" {
					inFence = false
				}
				mask[i] = true
				continue
			}
		}
		mask[i] = inFence
	}
	return mask
}

// EmptyCodeLines reports 1-based lines with an exact double-backtick run
// outside fenced code blocks.
func EmptyCodeLines(body string) []int {
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	var out []int
	for i, line := range lines {
		if mask[i] {
			continue
		}
		for _, loc := range emptyBacktickRun.FindAllStringIndex(line, -1) {
			if loc[1]-loc[0] == 2 {
				out = append(out, i+1)
				break
			}
		}
	}
	return out
}

// briefLintPlaceholderMarkers returns the built-in unfilled placeholder
// markers followed by the comma-separated items of brief_lint_placeholders,
// trimmed, de-duplicated, in order.
func briefLintPlaceholderMarkers(value string) []string {
	out := make([]string, 0, len(unfilledPlaceholderBuiltins))
	for _, marker := range unfilledPlaceholderBuiltins {
		out = append(out, marker)
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		duplicate := false
		for _, existing := range out {
			if existing == item {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, item)
		}
	}
	return out
}

// UnfilledPlaceholders returns one mark per brief line (1-based) outside a
// fenced code block that contains a marker, in line order then marker order:
// "unfilled placeholder '<marker>' (line <n>)".
func UnfilledPlaceholders(body string, markers []string) []string {
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	var out []string
	for i, line := range lines {
		if mask[i] {
			continue
		}
		for _, marker := range markers {
			hit := strings.Contains(line, marker)
			if marker == "<slot>" {
				hit = slotInPath.MatchString(line)
			}
			if hit {
				out = append(out, fmt.Sprintf("unfilled placeholder '%s' (line %d)", marker, i+1))
			}
		}
	}
	return out
}

// BriefLintFindings computes the diagnostics used by both lint and dispatch.
func BriefLintFindings(brief string, ctx *core.Config, env platform.Env, options BriefLintOptions) BriefLintResult {
	mode := core.Cfg(ctx, "brief_lint", "warn", env)
	result := BriefLintResult{Mode: mode, Warnings: []string{}}
	if mode == "off" {
		return result
	}
	aliases, ignored := ParseBriefLintAliases(core.Cfg(ctx, "brief_lint_aliases", "", env))
	for _, item := range ignored {
		result.Warnings = append(result.Warnings, fmt.Sprintf("brief_lint_aliases: ignored '%s' (use Section=Heading|Heading)", item))
	}
	bodyBytes, err := os.ReadFile(brief)
	if err != nil {
		bodyBytes = nil
	}
	body := strings.ToValidUTF8(string(bodyBytes), "\uFFFD")
	if options.WorkerCwd != "" {
		project := platform.ProjectRoot(env, options.OrchestratorCwd)
		state := platform.StateProjectRoot(env, options.OrchestratorCwd)
		if !SamePath(options.WorkerCwd, project, platform.Current()) || !SamePath(options.WorkerCwd, state, platform.Current()) {
			result.Warnings = append(result.Warnings, unseenPathWarnings(brief, body, options.WorkerCwd, []string{project, state})...)
		}
	}
	if options.PathsOnly {
		return result
	}
	badLines := EmptyCodeLines(body)
	for _, line := range badLines[:min(3, len(badLines))] {
		result.Warnings = append(result.Warnings, fmt.Sprintf("brief %s line %d has empty inline code (``): a shell heredoc without quotes may have run the backticks", brief, line))
	}
	if len(badLines) > 3 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("… and %d more line(s)", len(badLines)-3))
	}
	missingMarkers := FailureMatrixMissing(body)
	if len(missingMarkers) > 0 {
		markers, reasons := make([]string, 0, len(missingMarkers)), make([]string, 0, len(missingMarkers))
		for _, marker := range missingMarkers {
			markers = append(markers, "["+marker+"]")
			for _, item := range failureMatrixMarkers {
				if item.marker == marker {
					reasons = append(reasons, item.reason)
					break
				}
			}
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("brief %s failure matrix is missing: %s — %s", brief, strings.Join(markers, " "), strings.Join(reasons, "; ")))
	}
	missing := BriefMissingSections(body, options.ReadOnly, aliases)
	placeholderMarks := UnfilledPlaceholders(body, briefLintPlaceholderMarkers(core.Cfg(ctx, "brief_lint_placeholders", "", env)))
	if missing == "" && len(placeholderMarks) == 0 {
		return result
	}
	missingList := missing
	for _, mark := range placeholderMarks {
		missingList += " [" + mark + "]"
	}
	message := fmt.Sprintf("brief %s is missing sections:%s", brief, missingList)
	if reasons := MissingSectionsReasons(missingList); reasons != "" {
		message += " — " + reasons
	}
	result.MissingMessage = message
	return result
}

func unseenPathWarnings(brief, body, workerCwd string, roots []string) []string {
	lines := strings.Split(body, "\n")
	out := []string{}
	seen := map[string]bool{}
	add := func(value string, line int, inline bool) {
		candidate := strings.TrimRight(strings.TrimSpace(value), "),.;:")
		if candidate == "" || filepath.IsAbs(candidate) {
			return
		}
		looks := strings.ContainsAny(candidate, "/\\") || strings.HasPrefix(candidate, ".") || strings.HasPrefix(candidate, "~") || regexp.MustCompile(`^[^\s/]+\.[A-Za-z0-9_-]+$`).MatchString(candidate)
		if !inline && !looks {
			return
		}
		if _, err := os.Stat(filepath.Join(workerCwd, candidate)); err == nil {
			return
		}
		absolute := ""
		for _, root := range roots {
			p := filepath.Join(root, candidate)
			rel, e := filepath.Rel(root, p)
			if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				if _, e = os.Stat(p); e == nil {
					absolute = p
					break
				}
			}
		}
		if absolute == "" {
			return
		}
		key := fmt.Sprintf("%d\x00%s", line, candidate)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("brief %s line %d cites '%s', which the worker in %s cannot see; use the absolute path %s", brief, line, candidate, workerCwd, absolute))
	}
	for i, line := range lines {
		for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(line, -1) {
			add(m[1], i+1, true)
		}
		for _, m := range regexp.MustCompile(`(?:^|[\s"'(])([^\s"'<>`+"`"+`]+)`).FindAllStringSubmatch(line, -1) {
			add(m[1], i+1, false)
		}
	}
	return out
}
