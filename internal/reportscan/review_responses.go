package reportscan

import (
	"regexp"
	"strconv"
	"strings"
)

// ReviewResponsesResult is the parsed ## Review responses section of a report:
// whether the section exists, the finding numbers answered fixed or declined
// (each at most once, report order), and the list lines without a valid form.
type ReviewResponsesResult struct {
	Found    bool
	Fixed    []int
	Declined []int
	Unmarked int
}

// responseIDColon is a finding number at the start of a response body: F<n>
// followed by a colon and at least one space or tab.
var responseIDColon = regexp.MustCompile(`^F([0-9]+):[ \t]+`)

// ReviewResponses parses the report's ## Review responses section, outside
// fenced code blocks: one line per finding in one of the exact forms
//
//   - F<n>: fixed
//   - F<n>: fixed: <note>   (the note is ignored)
//   - F<n>: declined: <reason>   (the reason needs at least one letter or digit)
//
// in any list position. Any other list line in the section is unmarked, and a
// repeated number keeps the first answer. The section starts at the exact
// "## Review responses" title and ends at the next # or ## title; deeper
// titles and non-list lines are skipped. The form is exact on purpose: a
// formatted, uppercase, zero or free-text line is never an answer.
func ReviewResponses(text string) ReviewResponsesResult {
	out := ReviewResponsesResult{Fixed: []int{}, Declined: []int{}}
	inFence, fenceChar, fenceLen := false, byte(0), 0
	inSection := false
	for _, raw := range strings.Split(text, "\n") {
		if open := fenceOpen.FindStringSubmatch(raw); !inFence && open != nil {
			inFence, fenceChar, fenceLen = true, open[1][0], len(open[1])
			continue
		}
		if inFence {
			if close := fenceClose.FindStringSubmatch(raw); close != nil && close[1][0] == fenceChar && len(close[1]) >= fenceLen {
				inFence = false
			}
			continue
		}
		trim := strings.TrimSpace(raw)
		if level, ok := responseHeadingLevel(trim); ok {
			if level <= 2 {
				inSection = level == 2 && strings.TrimSpace(trim[level:]) == "Review responses"
				if inSection {
					out.Found = true
				}
			}
			continue
		}
		if !inSection {
			continue
		}
		if layerIsStructuralRow(trim) || !layerIsListLine(trim) {
			continue
		}
		if !parseResponseLine(trim, &out) {
			out.Unmarked++
		}
	}
	return out
}

// responseHeadingLevel reports the level (1-6) of a markdown heading line and
// whether it is one: one to six # followed by a space or tab and a title.
func responseHeadingLevel(trim string) (int, bool) {
	level := 0
	for level < len(trim) && level < 6 && trim[level] == '#' {
		level++
	}
	if level < 1 || level == len(trim) || (trim[level] != ' ' && trim[level] != '\t') {
		return 0, false
	}
	return level, true
}

// parseResponseLine accepts only the bullet forms; a numbered or table row is
// a list line without the form, and so is a line whose number, keyword or
// declined reason does not match the exact grammar. A repeated number is a
// valid line that keeps the first answer.
func parseResponseLine(trim string, out *ReviewResponsesResult) bool {
	m := layerBulletRE.FindStringIndex(trim)
	if m == nil {
		return false
	}
	body := strings.TrimSpace(trim[m[1]:])
	idm := responseIDColon.FindStringSubmatchIndex(body)
	if idm == nil {
		return false
	}
	n, err := strconv.Atoi(body[idm[2]:idm[3]])
	if err != nil || n < 1 {
		return false
	}
	rest := strings.TrimSpace(body[idm[1]:])
	switch {
	case rest == "fixed":
		return recordResponse(out, n, true)
	case strings.HasPrefix(rest, "fixed: "):
		return recordResponse(out, n, true)
	case strings.HasPrefix(rest, "declined: "):
		if !layerReasonOK(strings.TrimSpace(rest[len("declined: "):])) {
			return false
		}
		return recordResponse(out, n, false)
	}
	return false
}

// recordResponse stores the answer for a number the report has not answered
// yet; a number already answered keeps its first answer. Either way the line
// had a valid form, so it is not unmarked.
func recordResponse(out *ReviewResponsesResult, n int, fixed bool) bool {
	for _, list := range [][]int{out.Fixed, out.Declined} {
		for _, x := range list {
			if x == n {
				return true
			}
		}
	}
	if fixed {
		out.Fixed = append(out.Fixed, n)
	} else {
		out.Declined = append(out.Declined, n)
	}
	return true
}
