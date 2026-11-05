package wait

// Review-response coverage of settled correction reports.
//
// checkReviewResponses matches the report's ## Review responses section
// against the review the composed brief names with a Review: line, and
// publishes the counts on the done line. It warns — once per report, never
// changing the wait result — about findings without a response, answers to
// numbers the review does not have, lines without the exact form, and
// review findings without a valid number. The wait never executes anything
// from the project.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
	"github.com/djalmajr/soho/internal/text"
)

// reviewResponsesLimit is the review inspection cap: 4 MiB.
const reviewResponsesLimit = 4 << 20

// checkReviewResponses is the done case's review-response coverage: when
// the composed brief names a review with a Review: line and the role is an
// edit role, the report's answers go to the line and the warnings fire at
// most once per report, through the review-responses-warned wait file. A
// brief without the line changes nothing, and an unreadable report stays
// exactly as today.
func checkReviewResponses(sd, agent, report, reportText string, line *jsonjs.Object, role string, env platform.Env, cwd string) {
	if reportText == "" {
		return // an unreadable report stays exactly as today
	}
	// Only the composed brief's Review: line decides, and only an edit role
	// answers: a read role or an unreadable role is no role.
	declared := declaredBriefReview(composedBriefBody(sd, report))
	if declared == "" || !core.RoleIsEditReadOnly(role, env, cwd) {
		return
	}
	data, err := core.ReadScratchReportLimited(declared, reviewResponsesLimit)
	if err != nil {
		warnReviewResponses(sd, agent, report, []string{fmt.Sprintf("brief of '%s' names a review that cannot be read: %s", agent, text.SanitizeCause(declared))})
		return
	}
	reviewText := platform.DecodeText(data)
	reviewHeader := reportscan.ReviewHeader(reviewText)
	if reviewHeader == nil {
		warnReviewResponses(sd, agent, report, []string{fmt.Sprintf("review named by the brief of '%s' has no review header", agent)})
		return
	}
	marks := reportscan.FindingMarks(reviewText, reviewHeader)
	reviewNumbers := marks.Numbers
	responses := reportscan.ReviewResponses(reportText)
	fixed, declined := responses.Fixed, responses.Declined
	missing := reviewNumbersMissing(reviewNumbers, fixed, declined)
	unknown := reviewNumbersUnknown(reviewNumbers, fixed, declined)
	unnumbered := int(reviewHeader.Findings) - len(reviewNumbers)
	if unnumbered < 0 {
		unnumbered = 0
	}
	line.Set("review_responses", jsonjs.O(
		"fixed", reviewNumberArray(fixed),
		"declined", reviewNumberArray(declined),
		"missing", reviewNumberArray(missing),
		"unknown", reviewNumberArray(unknown),
		"unmarked", responses.Unmarked,
		"unnumbered", unnumbered,
	))
	warnings := []string{}
	if len(missing) > 0 {
		warnings = append(warnings, fmt.Sprintf("report of '%s' leaves %d finding(s) of the review without a response (%s): read them before commit, push or release", agent, len(missing), formatFindingNumbers(missing)))
	}
	if len(unknown) > 0 {
		warnings = append(warnings, fmt.Sprintf("report of '%s' answers %d finding(s) the review does not have (%s)", agent, len(unknown), formatFindingNumbers(unknown)))
	}
	if responses.Unmarked > 0 {
		warnings = append(warnings, fmt.Sprintf("report of '%s' has %d Review responses line(s) without a valid answer", agent, responses.Unmarked))
	}
	if unnumbered > 0 {
		warnings = append(warnings, fmt.Sprintf("review named by the brief of '%s' has %d finding(s) without a number, so their responses cannot be checked", agent, unnumbered))
	}
	warnReviewResponses(sd, agent, report, warnings)
}

// warnReviewResponses sends every message at most once per report, through
// the review-responses-warned wait file. A marker that cannot persist only
// lets the warning repeat on the next poll, never aborting the settlement.
func warnReviewResponses(sd, agent, report string, messages []string) {
	if len(messages) == 0 {
		return
	}
	prev, _ := readWaitFile(sd, agent, "review-responses-warned")
	if prev == report {
		return
	}
	for _, message := range messages {
		sendWarning(message)
	}
	// The warning already went out; a marker that cannot persist only lets
	// it repeat on the next poll, never aborting the settlement.
	_ = os.WriteFile(filepath.Join(sd, "wait", agent+".review-responses-warned"), []byte(report+"\n"), 0o666)
}

// declaredBriefReview returns the absolute path of the first Review: line in
// the composed brief body, outside fenced code blocks, read exactly the way
// dispatch.DeclaredReview reads it (the wait cannot import dispatch: that
// package imports wait): the path between backticks when the value is
// quoted (an unclosed quote has no review), else the first field. A
// relative or malformed path has no review, and the first declaration
// decides.
func declaredBriefReview(body string) string {
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
		if !strings.HasPrefix(raw, "Review:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(raw, "Review:"))
		if value == "" {
			return ""
		}
		if rest, quoted := strings.CutPrefix(value, "`"); quoted {
			tree, _, closed := strings.Cut(rest, "`")
			if !closed || !filepath.IsAbs(tree) {
				return ""
			}
			return tree
		}
		if fields := strings.Fields(value); len(fields) > 0 {
			if filepath.IsAbs(fields[0]) {
				return fields[0]
			}
		}
		return ""
	}
	return ""
}

// reviewNumbersMissing lists the review's valid numbers the report never
// answered, in review report order.
func reviewNumbersMissing(reviewNumbers, fixed, declined []int) []int {
	answered := map[int]bool{}
	for _, n := range append(append([]int{}, fixed...), declined...) {
		answered[n] = true
	}
	out := []int{}
	for _, n := range reviewNumbers {
		if !answered[n] {
			out = append(out, n)
		}
	}
	return out
}

// reviewNumbersUnknown lists the answered numbers the review does not have,
// in answer order (fixed first, then declined): a number appears in at most
// one list, so the order is the section's.
func reviewNumbersUnknown(reviewNumbers, fixed, declined []int) []int {
	review := map[int]bool{}
	for _, n := range reviewNumbers {
		review[n] = true
	}
	out := []int{}
	for _, n := range append(append([]int{}, fixed...), declined...) {
		if !review[n] {
			out = append(out, n)
		}
	}
	return out
}

// reviewNumberArray is the JSON array form of a finding-number list.
func reviewNumberArray(numbers []int) []any {
	out := make([]any, len(numbers))
	for i, n := range numbers {
		out[i] = n
	}
	return out
}

// formatFindingNumbers is the warning form of a finding-number list: F1, F3.
func formatFindingNumbers(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("F%d", n)
	}
	return strings.Join(parts, ", ")
}
