// Package reportscan reads completion and review markers from worker reports.
package reportscan

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/djalmajr/soho/internal/platform"
)

var (
	partialMarker  = regexp.MustCompile("[`*]*\\[partial\\][`*]*")
	envMarker      = regexp.MustCompile("[`*]*\\[env\\][`*]*")
	anyItemState   = regexp.MustCompile(`\[(?:done|partial|env|skipped)\]`)
	stateBefore    = regexp.MustCompile(`^[ \t]*(?:[-*+][ \t]+|[0-9]+\.[ \t]+|#{1,6}[ \t]+|>[ \t]*|\[[ xX]\][ \t]+)?[ \t]*$`)
	cellBefore     = regexp.MustCompile(`\|[ \t]*$`)
	cellAfter      = regexp.MustCompile(`^[ \t]*(?:\||$)`)
	dashBefore     = regexp.MustCompile("[:\\x{2014}\\x{2013}-][`*]*[ \\t]+$")
	fenceOpen      = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	fenceClose     = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t\\r]*$")
	anyPartial     = regexp.MustCompile(`\[partial\]`)
	anyDone        = regexp.MustCompile(`\[done\]`)
	anySkipped     = regexp.MustCompile(`\[skipped\]`)
	reviewHeaderRE = regexp.MustCompile(`^findings: ([0-9]+) \(p0 ([0-9]+), p1 ([0-9]+), p2 ([0-9]+), p3 ([0-9]+)\) \| verdict: (pass|fail)$`)
	layerHeadingRE = regexp.MustCompile(`^#{1,6}[ \t]+`)
	layerBulletRE  = regexp.MustCompile(`^[-*+][ \t]+`)
	layerNumberRE  = regexp.MustCompile(`^[0-9]+[.)][ \t]+`)
)

// PartialCount counts report lines that state a [partial] item outside fenced
// code blocks. A line counts at most once.
func PartialCount(text string) int {
	partial, _ := limitCounts(text)
	return partial
}

// EnvCount counts reason-bearing environment limitations in item positions.
func EnvCount(text string) int {
	_, env := limitCounts(text)
	return env
}

func limitCounts(text string) (partial, env int) {
	if text == "" {
		return
	}
	inFence, fenceChar, fenceLen := false, byte(0), 0
	for _, line := range strings.Split(text, "\n") {
		open := fenceOpen.FindStringSubmatch(line)
		if !inFence && open != nil {
			inFence, fenceChar, fenceLen = true, open[1][0], len(open[1])
			continue
		}
		if inFence {
			close := fenceClose.FindStringSubmatch(line)
			if close != nil && close[1][0] == fenceChar && len(close[1]) >= fenceLen {
				inFence = false
			}
			continue
		}
		line = lowerASCII(line)
		if anyDone.MatchString(line) && anySkipped.MatchString(line) {
			continue
		}
		isPartial := anyPartial.MatchString(line) && markerInStatePosition(line)
		for _, loc := range envMarker.FindAllStringIndex(line, -1) {
			before, after := line[:loc[0]], line[loc[1]:]
			if stateBefore.MatchString(before) || (cellBefore.MatchString(before) && cellAfter.MatchString(after)) || dashBefore.MatchString(before) {
				if envReason(after) {
					env++
				} else {
					isPartial = true
				}
				break // Like partial, a line counts at most once per state.
			}
		}
		if isPartial {
			partial++
		}
	}
	return
}

// EnvHasReason checks the text after the first env marker. Callers own fence
// and item-position validation; formatting or another state is not a reason.
func EnvHasReason(line string) bool {
	loc := envMarker.FindStringIndex(lowerASCII(line))
	return loc != nil && envReason(line[loc[1]:])
}

func envReason(after string) bool {
	reason := anyItemState.ReplaceAllString(lowerASCII(after), "")
	// A reason names something: it holds at least one letter or digit, so
	// whitespace, formatting or punctuation alone is no reason.
	return strings.IndexFunc(reason, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) >= 0
}

func markerInStatePosition(line string) bool {
	for _, loc := range partialMarker.FindAllStringIndex(line, -1) {
		before, after := line[:loc[0]], line[loc[1]:]
		if stateBefore.MatchString(before) || (cellBefore.MatchString(before) && cellAfter.MatchString(after)) || dashBefore.MatchString(before) {
			return true
		}
	}
	return false
}

// PartialCountFile counts partial markers in a report; unreadable files count 0.
func PartialCountFile(file string) int {
	text, err := platform.ReadTextFile(file)
	if err != nil {
		return 0
	}
	return PartialCount(text)
}

// LayerResult is a ## Test layers line whose result is not PASS or n/a.
type LayerResult struct {
	Name   string
	Result string // one of FAIL, NOT RUN or BLOCKED
}

// TestLayersResult is the parsed ## Test layers section of a report: the counts by
// result and the layers that did not pass, in report order.
type TestLayersResult struct {
	Found    bool
	Pass     int
	Fail     int
	NotRun   int
	Blocked  int
	NA       int
	Unmarked int
	NonPass  []LayerResult
}

// TestLayers parses the report's ## Test layers section, outside fenced code
// blocks: one line per layer in the exact form `- <layer>: PASS|FAIL|NOT RUN|BLOCKED|n/a`;
// FAIL, NOT RUN and BLOCKED need a reason with at least one letter or digit
// after ": ", and PASS and n/a accept text after ": " that is ignored. Any
// other list line in the section is unmarked, and the section ends at the
// next # or ## title. The form is exact on purpose: the report is not prose,
// so a formatted or free-text line is never a result.
func TestLayers(text string) TestLayersResult {
	out := TestLayersResult{}
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
		if m := layerHeadingRE.FindStringIndex(trim); m != nil {
			inSection = strings.TrimSpace(trim[m[0]:]) == "## Test layers"
			if inSection {
				out.Found = true
			}
			continue
		}
		if !inSection {
			continue
		}
		if layerIsStructuralRow(trim) || !layerIsListLine(trim) {
			continue
		}
		if !parseLayerLine(trim, &out) {
			out.Unmarked++
		}
	}
	return out
}

func layerIsListLine(trim string) bool {
	return layerBulletRE.MatchString(trim) || layerNumberRE.MatchString(trim) || strings.HasPrefix(trim, "|")
}

// layerIsStructuralRow is a table separator row (a | --- | row): pure
// markdown structure, not a layer line; every other table row in the
// section is a list line without the form and so unmarked.
func layerIsStructuralRow(trim string) bool {
	if !strings.HasPrefix(trim, "|") {
		return false
	}
	return strings.IndexFunc(trim, func(r rune) bool {
		return r != '|' && r != '-' && r != ':' && r != ' ' && r != '\t'
	}) < 0
}

// parseLayerLine accepts only the bullet form `- <layer>: <result>`; a
// numbered or table row is a list line without the form, and so is a layer
// line whose result does not match the exact grammar.
func parseLayerLine(trim string, out *TestLayersResult) bool {
	m := layerBulletRE.FindStringIndex(trim)
	if m == nil {
		return false
	}
	body := strings.TrimSpace(trim[m[1]:])
	idx := strings.Index(body, ": ")
	if idx <= 0 {
		return false
	}
	name := strings.TrimSpace(body[:idx])
	if name == "" {
		return false
	}
	result, reason, ok := matchLayerResult(strings.TrimSpace(body[idx+2:]))
	if !ok {
		return false
	}
	switch result {
	case "PASS":
		out.Pass++
	case "n/a":
		out.NA++
	case "FAIL", "NOT RUN", "BLOCKED":
		if !layerReasonOK(reason) {
			return false
		}
		switch result {
		case "FAIL":
			out.Fail++
		case "NOT RUN":
			out.NotRun++
		case "BLOCKED":
			out.Blocked++
		}
		out.NonPass = append(out.NonPass, LayerResult{Name: name, Result: result})
	}
	return true
}

// matchLayerResult matches the exact result form: the bare result, or the
// result followed by ": " and a reason. Free text without the colon is not
// the form, and a trailing colon without the space is not the form.
func matchLayerResult(rest string) (result, reason string, ok bool) {
	for _, candidate := range []string{"NOT RUN", "PASS", "FAIL", "BLOCKED", "n/a"} {
		if rest == candidate {
			return candidate, "", true
		}
		if strings.HasPrefix(rest, candidate+": ") {
			return candidate, strings.TrimSpace(rest[len(candidate)+2:]), true
		}
	}
	return "", "", false
}

// layerReasonOK holds when the reason names something: at least one letter
// or digit, in any language; whitespace, formatting or punctuation alone is
// no reason.
func layerReasonOK(reason string) bool {
	return strings.IndexFunc(reason, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) >= 0
}

// ValidationLine reads the validator's result: the second non-empty line of
// the report in the exact form `validation: PASS|FAIL|NOT RUN|BLOCKED`, and
// only when the first non-empty line is the review header.
func ValidationLine(text string) (string, bool) {
	if ReviewHeader(text) == nil {
		return "", false
	}
	seen := 0
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimFunc(raw, isJSTrimSpace)
		if line == "" {
			continue
		}
		seen++
		if seen != 2 {
			continue
		}
		for _, result := range []string{"PASS", "FAIL", "NOT RUN", "BLOCKED"} {
			if line == "validation: "+result {
				return result, true
			}
		}
		return "", false
	}
	return "", false
}

// ReviewHeader is the parsed first-line review summary.
type ReviewHeaderResult struct {
	Findings float64            `json:"findings"`
	Severity map[string]float64 `json:"severity"`
	Verdict  string             `json:"verdict"`
}

// ParseReviewHeader recognizes a review header only on the first non-empty line.
func ReviewHeader(text string) *ReviewHeaderResult {
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimFunc(raw, isJSTrimSpace)
		if line == "" {
			continue
		}
		match := reviewHeaderRE.FindStringSubmatch(lowerASCII(line))
		if match == nil {
			return nil
		}
		return &ReviewHeaderResult{
			Findings: jsNumber(match[1]),
			Severity: map[string]float64{"P0": jsNumber(match[2]), "P1": jsNumber(match[3]), "P2": jsNumber(match[4]), "P3": jsNumber(match[5])},
			Verdict:  strings.ToLower(match[6]),
		}
	}
	return nil
}

func lowerASCII(value string) string {
	bytes := []byte(value)
	for i, b := range bytes {
		if b >= 'A' && b <= 'Z' {
			bytes[i] = b + ('a' - 'A')
		}
	}
	return string(bytes)
}

func isJSTrimSpace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ' ||
		r == '\u00A0' || r == '\u1680' || (r >= '\u2000' && r <= '\u200A') ||
		r == '\u2028' || r == '\u2029' || r == '\u202F' || r == '\u205F' ||
		r == '\u3000' || r == '\uFEFF'
}

func jsNumber(value string) float64 {
	n, _ := strconv.ParseFloat(value, 64)
	return n
}
