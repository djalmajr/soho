package dispatch

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// DJA-685: the per-`Type:` sections of an edit-role brief. The lint warns
// (never refuses, not under brief_lint=strict) when the brief lacks the
// section its Type: line requires, or the Mutations section for backend and
// security. Read-only roles and amendments (PathsOnly) get no warning; the
// check reads the brief only.

type typeSectionSpec struct {
	title   string // English title: the warning name and the match prefix
	synonym string // the Portuguese synonym, matched alike
	reason  string // literal reason of the missing-section warning
	markers []struct {
		marker string
		reason string
	}
	content bool // require a table data row or list item (or whole-section n/a)
}

var typeSectionSpecs = map[string]typeSectionSpec{
	"backend": {
		title:   "Risk markers",
		synonym: "Marcadores de risco",
		reason:  "nothing makes the slice state its risks",
		markers: []struct{ marker, reason string }{
			{"boundary", "limits, empties and sizes are not covered"},
			{"lifetime", "what opens and who closes it is not covered"},
			{"shared", "state other callers touch is not covered"},
			{"identity", "who owns what, and a reused id, are not covered"},
			{"hostile", "input built to break it is not covered"},
		},
	},
	"security": {
		title:   "Input and response",
		synonym: "Entrada e resposta",
		reason:  "nothing lists the inputs and the expected response",
		content: true,
	},
	"ui": {
		title:   "UI lifecycle",
		synonym: "Ciclo de vida da interface",
		reason:  "nothing covers async states or what the tests cannot reproduce",
		markers: []struct{ marker, reason string }{
			{"async", "loading, late and failed states are not covered"},
			{"test-env", "what the test environment does not reproduce is not named"},
		},
	},
}

var mutationSectionSpec = typeSectionSpec{
	title:   "Mutations",
	synonym: "Mutações",
	reason:  "name the mutation and the case that must fail, for each rule",
	content: true,
}

// mutationTypes need the Mutations section in addition to their own.
var mutationTypes = map[string]bool{"backend": true, "security": true}

// TypeSectionWarnings returns, for an edit-role brief, the per-`Type:`
// section warnings in check order: the section required by the Type: line
// (Risk markers, Input and response or UI lifecycle, English or Portuguese
// synonym, a level 1-3 title), then the Mutations section for backend and
// security. Warnings only: the caller appends them to Warnings, never to the
// refusal message.
func TypeSectionWarnings(briefPath, body string) []string {
	typ := BriefType(body)
	spec, ok := typeSectionSpecs[typ]
	if !ok {
		return nil
	}
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	var out []string
	check := func(spec typeSectionSpec) {
		start, end, found := findTypeSection(lines, mask, spec.title, spec.synonym)
		if !found {
			out = append(out, fmt.Sprintf("brief %s has Type: %s and no '%s' section: %s", briefPath, typ, spec.title, spec.reason))
			return
		}
		if sectionWholeNA(lines, mask, start, end) {
			return
		}
		if spec.content {
			if !sectionHasTableOrListContent(lines, mask, start, end) {
				out = append(out, fmt.Sprintf("brief %s '%s' section is empty", briefPath, spec.title))
			}
			return
		}
		for _, marker := range spec.markers {
			if !sectionHasMarker(lines, mask, start, end, marker.marker) {
				out = append(out, fmt.Sprintf("brief %s '%s' section has no [%s]: %s", briefPath, spec.title, marker.marker, marker.reason))
			}
		}
	}
	check(spec)
	if mutationTypes[typ] {
		check(mutationSectionSpec)
	}
	return out
}

// typeSectionTitleMatches accepts the title, case- and accent-insensitively,
// alone or followed by a parenthesis, as in the template's "Risk markers (Type:
// backend)". A title that only begins with it, like "Risk markers review", is
// another section.
func typeSectionTitleMatches(heading string, want []string) bool {
	title := normalizeBriefHeading(heading)
	for _, candidate := range want {
		if rest, ok := strings.CutPrefix(title, candidate); ok && (rest == "" || strings.HasPrefix(rest, " (")) {
			return true
		}
	}
	return false
}

// findTypeSection locates the first level 1-3 heading (outside a fenced
// block) whose title matches the English title or the Portuguese synonym, and
// returns the section's body
// range (the lines after the title up to the next heading of the same level
// or higher). As in FailureMatrixMissing, when two titles are equal the
// first one wins.
func findTypeSection(lines []string, mask []bool, title, synonym string) (int, int, bool) {
	want := []string{normalizeBriefHeading(title), normalizeBriefHeading(synonym)}
	start, level := -1, 0
	for i, line := range lines {
		if mask[i] {
			continue
		}
		heading, gotLevel, ok := lintHeading(line)
		if !ok {
			continue
		}
		if typeSectionTitleMatches(heading, want) {
			start, level = i, gotLevel
			break
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if mask[i] {
			continue
		}
		if gotLevel, ok := lintHeadingPrefix(lines[i]); ok && gotLevel <= level {
			end = i
			break
		}
	}
	return start, end, true
}

// naReasonValid reports whether the text after `n/a:` carries at least one
// letter or digit (any kind, Unicode): `n/a:` alone, or with only
// punctuation, counts as absent.
func naReasonValid(reason string) bool {
	for _, r := range strings.TrimSpace(reason) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// sectionWholeNA reports whether the section's first non-empty line outside
// a fenced block is `n/a: <reason>` with a valid reason.
func sectionWholeNA(lines []string, mask []bool, start, end int) bool {
	for i := start + 1; i < end; i++ {
		if mask[i] {
			continue
		}
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "n/a:") {
			return false
		}
		return naReasonValid(strings.TrimPrefix(line, "n/a:"))
	}
	return false
}

// sectionHasMarker reports whether the section body (outside fenced blocks)
// carries the marker in its exact form: the bare `[marker]`, or
// `[marker] n/a:` with a valid reason on the same line. A different case
// ([Boundary]) and an `n/a:` without a valid reason do not count.
func sectionHasMarker(lines []string, mask []bool, start, end int, marker string) bool {
	token := "[" + marker + "]"
	for i := start + 1; i < end; i++ {
		if mask[i] {
			continue
		}
		line := lines[i]
		from := 0
		for {
			pos := strings.Index(line[from:], token)
			if pos < 0 {
				break
			}
			rest := strings.TrimSpace(line[from+pos+len(token):])
			if strings.HasPrefix(rest, "n/a:") {
				if naReasonValid(strings.TrimPrefix(rest, "n/a:")) {
					return true
				}
			} else {
				return true
			}
			from += pos + len(token)
		}
	}
	return false
}

var listContentPattern = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])\s+\S`)

// sectionHasTableOrListContent reports whether the section body (outside
// fenced blocks) carries at least one table data row or list item. The
// table's separator row and the header row directly above it do not count,
// and a row or item without text counts as nothing.
func sectionHasTableOrListContent(lines []string, mask []bool, start, end int) bool {
	for i := start + 1; i < end; i++ {
		if mask[i] {
			continue
		}
		if listContentPattern.MatchString(lines[i]) || isTableDataLine(lines, mask, i) {
			return true
		}
	}
	return false
}

func isTableDataLine(lines []string, mask []bool, i int) bool {
	line := lines[i]
	if !strings.Contains(line, "|") || isTableSeparatorLine(line) {
		return false
	}
	if i+1 < len(lines) && !mask[i+1] && isTableSeparatorLine(lines[i+1]) {
		return false // the header row directly above the separator
	}
	for _, r := range line {
		if r != '|' && !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func isTableSeparatorLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") || !strings.Contains(trimmed, "-") {
		return false
	}
	for _, r := range trimmed {
		if r != '|' && r != '-' && r != ':' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
