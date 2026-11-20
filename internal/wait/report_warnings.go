package wait

// Pure builders for the warnings a settled report can produce. wait's checks
// and the read-only `soho report` command call the same functions, so a
// warning's bytes cannot drift between the two paths. The builders format
// strings only; gating, marker files and output stay with their callers.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
	"github.com/djalmajr/soho/internal/text"
)

// ReportWarning is one warning string and the kind that identifies it to a
// wait marker file. The review check's historical key/message pair uses the
// same two fields.
type ReportWarning struct {
	Kind    string
	Message string
}

// Warning kinds. Review kinds are the marker keys already written to
// <agent>.review-responses-warned; the other kinds name the marker or the
// check that owns the warning. The worker's own missing header is
// distinguished from the declared review's missing header: both once shared
// the key "no-header", which made an acknowledged review header silence the
// worker's own warning in the read-only projection.
const (
	WarningPartial              = "partial"
	WarningEnv                  = "env"
	WarningHeaderVerdictOpen    = "verdict-open"
	WarningHeaderFindingsSum    = "findings-sum"
	WarningHeaderMissing        = "own-no-header"
	WarningTestLayersNonPass    = "test-layers-non-pass"
	WarningTestLayersUnmarked   = "test-layers-unmarked"
	WarningValidation           = "validation"
	WarningTestLayersMissing    = "test-layers-missing"
	WarningFindingMarks         = "finding-marks"
	WarningReviewUnreadable     = "unreadable"
	WarningReviewNoHeader       = "no-header"
	WarningReviewMissing        = "missing"
	WarningReviewUnknown        = "unknown"
	WarningReviewResponses      = "unmarked"
	WarningReviewSectionMissing = "section-missing"
	WarningReviewUnnumbered     = "unnumbered"
	WarningParityMissing        = "reference-parity-missing"
	WarningParityUnmarked       = "reference-parity-unmarked"
	WarningReportSize           = "report-size"
)

// ReportSizeWarnBytes is the boundary for the oversized-report warning: a
// report is warned about only when it is strictly larger than 1 MiB.
const ReportSizeWarnBytes int64 = 1 << 20

// WarningReviewNoHeaderPublic is the public kind published for the declared
// review's missing header. wait keeps writing the historical "no-header"
// marker key, and the read-only projection tells the two missing-header
// warnings apart with this name instead.
const WarningReviewNoHeaderPublic = "review-no-header"

// PublicKind is the warning kind the report projection publishes: the review
// check's historical "no-header" key is published as "review-no-header", and
// every other kind is published as itself.
func PublicKind(kind string) string {
	if kind == WarningReviewNoHeader {
		return WarningReviewNoHeaderPublic
	}
	return kind
}

// ReportWarningContext is the context a warning check needs beyond the report
// text: the state dir for marker and sidecar reads, the settled role, the
// project root for the role check, and whether the role answers a Review:
// declaration under the same gate wait uses.
type ReportWarningContext struct {
	Agent, Report, Text, StateDir, Role, Cwd string
	Env                                      platform.Env
	EditRole, ReviewRole                     bool
}

// ReportAnalysis is the parsed report plus the warnings wait would print for
// it under the supplied context. The read-only command projects these fields
// into JSON; it never calls a check that writes a marker.
type ReportAnalysis struct {
	Partial, EnvCount int
	Header            *reportscan.ReviewHeaderResult
	Layers            reportscan.TestLayersResult
	LayerEntries      []reportscan.LayerEntry
	Validation        string
	HasValidation     bool
	FindingMarks      *reportscan.FindingMarksResult
	Responses         reportscan.ReviewResponsesResult
	Missing, Unknown  []int
	Unnumbered        int
	BlockedByScope    []string
	Warnings          []ReportWarning
}

// ReportSizeWarning builds the oversized-report warning shared by wait and
// collect. The size is printed in MiB with one decimal; a report of exactly
// the threshold, an empty one and a missing one produce no warning.
func ReportSizeWarning(agent string, size int64) (ReportWarning, bool) {
	if size <= ReportSizeWarnBytes {
		return ReportWarning{}, false
	}
	return ReportWarning{WarningReportSize, fmt.Sprintf("report of '%s' is %.1f MB; keep long logs in a file beside it and cite short excerpts", agent, float64(size)/float64(ReportSizeWarnBytes))}, true
}

// PartialWarnings builds the two once-per-report item warnings.
func PartialWarnings(agent string, partial, envCount int) []ReportWarning {
	out := []ReportWarning{}
	if partial > 0 {
		out = append(out, ReportWarning{WarningPartial, fmt.Sprintf("report of '%s' marks %d item(s) partial: a partial item is not a pass; read them before commit, push or release", agent, partial)})
	}
	if envCount > 0 {
		out = append(out, ReportWarning{WarningEnv, fmt.Sprintf("report of '%s' marks %d item(s) env-limited: the orchestrator must cover them by other proof (host run, CI, native platform) before commit, push or release", agent, envCount)})
	}
	return out
}

// HeaderWarnings builds the review-header warnings. reviewRole is wait's
// core.IsReviewRole gate for the missing-header warning.
func HeaderWarnings(agent string, header *reportscan.ReviewHeaderResult, reviewRole bool) []ReportWarning {
	out := []ReportWarning{}
	if header != nil && header.Verdict == "pass" {
		open := header.Severity["P0"] + header.Severity["P1"] + header.Severity["P2"]
		if open > 0 {
			out = append(out, ReportWarning{WarningHeaderVerdictOpen, fmt.Sprintf("report of '%s' says verdict pass with %s open P0-P2 finding(s): read them before commit, push or release", agent, numberString(open))})
		}
	}
	if header != nil {
		sum := header.Severity["P0"] + header.Severity["P1"] + header.Severity["P2"] + header.Severity["P3"]
		if sum != header.Findings {
			out = append(out, ReportWarning{WarningHeaderFindingsSum, fmt.Sprintf("report of '%s': findings %s but P0..P3 add up to %s", agent, numberString(header.Findings), numberString(sum))})
		}
	} else if reviewRole {
		out = append(out, ReportWarning{WarningHeaderMissing, fmt.Sprintf("report of '%s' has no 'findings: N (P0 a, P1 b, P2 c, P3 d) | verdict: pass|fail' first line", agent)})
	}
	return out
}

// ItemWarnings is the command-side combination of the partial/env and
// header warnings, in the order wait prints them.
func ItemWarnings(agent string, partial, envCount int, header *reportscan.ReviewHeaderResult, reviewRole bool) []ReportWarning {
	return append(PartialWarnings(agent, partial, envCount), HeaderWarnings(agent, header, reviewRole)...)
}

// TestLayerWarnings builds the warnings of checkTestLayers in that check's
// order. A brief with no declared list keeps today's rule: every non-pass
// entry warns.
func TestLayerWarnings(agent string, layers reportscan.TestLayersResult, validation string, hasValidation, missingSection bool) []ReportWarning {
	return testLayerWarnings(agent, layers, nil, false, validation, hasValidation, missingSection)
}

// TestLayerWarningsWithDeclared applies the brief's declared list: a NOT RUN
// entry of a layer the brief does not declare is not a non-pass warning. FAIL
// and BLOCKED always warn, whatever the brief says, and the published
// test_layers counts never change. Names are compared the way layerName
// renders them for the warning.
func TestLayerWarningsWithDeclared(agent string, layers reportscan.TestLayersResult, declared []string, validation string, hasValidation, missingSection bool) []ReportWarning {
	return testLayerWarnings(agent, layers, declared, true, validation, hasValidation, missingSection)
}

func testLayerWarnings(agent string, layers reportscan.TestLayersResult, declared []string, filter bool, validation string, hasValidation, missingSection bool) []ReportWarning {
	out := []ReportWarning{}
	if layers.Found {
		nonPass := layers.NonPass
		if filter {
			nonPass = make([]reportscan.LayerResult, 0, len(layers.NonPass))
			for _, entry := range layers.NonPass {
				if entry.Result == "NOT RUN" && !declaredLayerName(declared, entry.Name) {
					continue
				}
				nonPass = append(nonPass, entry)
			}
		}
		if len(nonPass) > 0 {
			names := make([]string, len(nonPass))
			for i, entry := range nonPass {
				names[i] = layerName(entry.Name) + " " + entry.Result
			}
			out = append(out, ReportWarning{WarningTestLayersNonPass, fmt.Sprintf("report of '%s' lists %d test layer(s) that did not pass (%s): read them before commit, push or release", agent, len(nonPass), strings.Join(names, ", "))})
		}
	}
	if layers.Found && layers.Unmarked > 0 {
		reason := ""
		if layers.FirstUnmarkedReason != "" {
			reason = "; " + layers.FirstUnmarkedReason
		}
		out = append(out, ReportWarning{WarningTestLayersUnmarked, fmt.Sprintf("report of '%s' has %d Test layers line(s) without a result; expected '- <layer>: <RESULT>[: reason]' with one of PASS, FAIL, NOT RUN, BLOCKED or n/a, a reason after FAIL, NOT RUN and BLOCKED, an optional note after PASS or n/a, and ': ', ' — ', ' – ' or ' (' before the text (first: %s%s)", agent, layers.Unmarked, unmarkedExample(layers.FirstUnmarked), reason)})
	}
	if hasValidation && validation != "PASS" {
		out = append(out, ReportWarning{WarningValidation, fmt.Sprintf("validation of '%s' is %s: read it before commit, push or release", agent, validation)})
	}
	if missingSection {
		out = append(out, ReportWarning{WarningTestLayersMissing, fmt.Sprintf("report of '%s' has no Test layers section, and the project has a VALIDATION.md", agent)})
	}
	return out
}

// FindingMarkWarnings builds the finding-marks warning; zero unmarked
// findings yields no warning.
func FindingMarkWarnings(agent string, unmarked int) []ReportWarning {
	if unmarked == 0 {
		return []ReportWarning{}
	}
	return []ReportWarning{{WarningFindingMarks, fmt.Sprintf("report of '%s' has %d finding(s) without an origin and a class", agent, unmarked)}}
}

// UnreadableReviewWarning builds the warning for a Review: path that cannot
// be read. cause is already sanitized by the caller, as in wait.
func UnreadableReviewWarning(agent, cause string) ReportWarning {
	return ReportWarning{WarningReviewUnreadable, fmt.Sprintf("brief of '%s' names a review that cannot be read: %s", agent, cause)}
}

// NoReviewHeaderWarning builds the warning for a named review without a
// review header.
func NoReviewHeaderWarning(agent string) ReportWarning {
	return ReportWarning{WarningReviewNoHeader, fmt.Sprintf("review named by the brief of '%s' has no review header", agent)}
}

// ReviewResponseWarnings builds the answer warnings in check order. A report
// without a Review responses section at all warns once that the section is
// missing, instead of counting every finding of the review as unanswered —
// when the review has findings, numbered or not; the per-finding warning stays
// for a section that misses a finding.
func ReviewResponseWarnings(agent string, responses reportscan.ReviewResponsesResult, missing, unknown []int, unnumbered int) []ReportWarning {
	out := []ReportWarning{}
	if !responses.Found {
		if len(missing) > 0 || unnumbered > 0 {
			out = append(out, ReportWarning{WarningReviewSectionMissing, fmt.Sprintf("report of '%s' has no Review responses section, and the brief names a review to answer; expected '- F<n>: <fixed|declined>: <note>', one line per finding", agent)})
		}
		if unnumbered > 0 {
			out = append(out, ReportWarning{WarningReviewUnnumbered, fmt.Sprintf("review named by the brief of '%s' has %d finding(s) without a number, so their responses cannot be checked", agent, unnumbered)})
		}
		return out
	}
	if len(missing) > 0 {
		out = append(out, ReportWarning{WarningReviewMissing, fmt.Sprintf("report of '%s' leaves %d finding(s) of the review without a response (%s): read them before commit, push or release", agent, len(missing), formatFindingNumbers(missing))})
	}
	if len(unknown) > 0 {
		out = append(out, ReportWarning{WarningReviewUnknown, fmt.Sprintf("report of '%s' answers %d finding(s) the review does not have (%s)", agent, len(unknown), formatFindingNumbers(unknown))})
	}
	if responses.Unmarked > 0 {
		out = append(out, ReportWarning{WarningReviewResponses, fmt.Sprintf("report of '%s' has %d Review responses line(s) without a valid answer; expected '- F<n>: <fixed|declined>: <note>' (first: %s)", agent, responses.Unmarked, unmarkedExample(responses.FirstUnmarked))})
	}
	if unnumbered > 0 {
		out = append(out, ReportWarning{WarningReviewUnnumbered, fmt.Sprintf("review named by the brief of '%s' has %d finding(s) without a number, so their responses cannot be checked", agent, unnumbered)})
	}
	return out
}

// ReferenceParityCounts is the parsed ## Reference parity section used by
// wait's metrics and by the read-only command.
type ReferenceParityCounts struct {
	Found                        bool
	Same, Diverges, NA, Unmarked int
}

// ReferenceParity parses parity items with the same fence and section rules
// as checkReferenceParity.
func ReferenceParity(text string) ReferenceParityCounts {
	out := ReferenceParityCounts{}
	inSection := false
	fence := ""
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
				out.Found = true
			}
			continue
		}
		if strings.HasPrefix(trim, "# ") {
			inSection = false
		}
		if !inSection || !parityItem.MatchString(raw) {
			continue
		}
		if strings.HasPrefix(trim, "|") && (strings.Contains(trim, "---") || strings.EqualFold(strings.Trim(trim, "| "), "item | parity")) {
			continue
		}
		marks := 0
		if paritySame.MatchString(raw) {
			out.Same++
			marks++
		}
		if markedReferenceDivergence(raw) {
			out.Diverges++
			marks++
		}
		if parityNA.MatchString(raw) {
			out.NA++
			marks++
		}
		if marks != 1 {
			out.Unmarked++
		}
	}
	return out
}

// ReferenceParityWarnings builds the two parity warnings in check order.
func ReferenceParityWarnings(agent string, counts ReferenceParityCounts) []ReportWarning {
	out := []ReportWarning{}
	if !counts.Found {
		out = append(out, ReportWarning{WarningParityMissing, fmt.Sprintf("report of '%s' has no Reference parity section", agent)})
	} else if counts.Unmarked > 0 {
		out = append(out, ReportWarning{WarningParityUnmarked, fmt.Sprintf("report of '%s' has %d parity item(s) without a mark", agent, counts.Unmarked)})
	}
	return out
}

// BriefHasReference reports whether the composed brief declares a Reference:
// line outside a fence, with the same scan checkReferenceParity uses.
func BriefHasReference(body string) bool {
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
		if strings.HasPrefix(strings.TrimSpace(raw), "Reference:") {
			return true
		}
	}
	return false
}

// DeclaredReview is the review path declared by the composed brief, using
// exactly the same grammar as wait's review-response check.
func DeclaredReview(body string) string { return declaredBriefReview(body) }

// AnalyzeReport parses a report and computes the warnings wait would print
// now. It reads the report text and the context files, but writes nothing.
func AnalyzeReport(ctx ReportWarningContext) ReportAnalysis {
	out := ReportAnalysis{}
	out.Partial = reportscan.PartialCount(ctx.Text)
	out.EnvCount = reportscan.EnvCount(ctx.Text)
	out.Header = reportscan.ReviewHeader(ctx.Text)
	out.Layers = reportscan.TestLayers(ctx.Text)
	out.LayerEntries = reportscan.TestLayerEntries(ctx.Text)
	out.Validation, out.HasValidation = reportscan.ValidationLine(ctx.Text)
	out.BlockedByScope = reportscan.BlockedByScope(ctx.Text)
	if out.Header != nil {
		marks := reportscan.FindingMarks(ctx.Text, out.Header)
		out.FindingMarks = &marks
	}
	out.Responses = reportscan.ReviewResponses(ctx.Text)
	out.Missing, out.Unknown = []int{}, []int{}

	out.Warnings = append(out.Warnings, ItemWarnings(ctx.Agent, out.Partial, out.EnvCount, out.Header, ctx.ReviewRole)...)
	declaredLayers := declaredTestLayersForReport(ctx.StateDir, ctx.Report)
	missingSection := !out.Layers.Found && missingSectionWarns(ctx.Role, testLayersRoot(ctx.StateDir, ctx.Report))
	warnings := TestLayerWarnings(ctx.Agent, out.Layers, out.Validation, out.HasValidation, missingSection)
	if len(declaredLayers) > 0 {
		warnings = TestLayerWarningsWithDeclared(ctx.Agent, out.Layers, declaredLayers, out.Validation, out.HasValidation, missingSection)
	}
	out.Warnings = append(out.Warnings, warnings...)
	if out.FindingMarks != nil {
		out.Warnings = append(out.Warnings, FindingMarkWarnings(ctx.Agent, out.FindingMarks.Unmarked)...)
	}

	body := composedBriefBody(ctx.StateDir, ctx.Report)
	declared := declaredBriefReview(body)
	if declared != "" && ctx.EditRole {
		data, err := core.ReadScratchReportLimited(declared, reviewResponsesLimit)
		if err != nil {
			out.Warnings = append(out.Warnings, UnreadableReviewWarning(ctx.Agent, text.SanitizeCause(declared)))
		} else {
			reviewHeader := reportscan.ReviewHeader(string(data))
			if reviewHeader == nil {
				out.Warnings = append(out.Warnings, NoReviewHeaderWarning(ctx.Agent))
			} else {
				marks := reportscan.FindingMarks(string(data), reviewHeader)
				out.Missing = reviewNumbersMissing(marks.Numbers, out.Responses.Fixed, out.Responses.Declined)
				out.Unknown = reviewNumbersUnknown(marks.Numbers, out.Responses.Fixed, out.Responses.Declined)
				out.Unnumbered = int(reviewHeader.Findings) - len(marks.Numbers)
				if out.Unnumbered < 0 {
					out.Unnumbered = 0
				}
				out.Warnings = append(out.Warnings, ReviewResponseWarnings(ctx.Agent, out.Responses, out.Missing, out.Unknown, out.Unnumbered)...)
			}
		}
	}
	if BriefHasReference(body) {
		counts := ReferenceParity(ctx.Text)
		out.Warnings = append(out.Warnings, ReferenceParityWarnings(ctx.Agent, counts)...)
	}
	return out
}

// AlreadyWarned reports whether wait's marker for this warning already names
// this report. kind is the public kind (PublicKind). A state dir of "" (the
// --file form) has no marker identity, and a warning wait prints without a
// marker (the worker's own missing header, the verdict and findings-sum
// warnings) is never already warned.
func AlreadyWarned(sd, agent, report, kind string) bool {
	if sd == "" || agent == "" {
		return false
	}
	marker, key := markerEntry(kind)
	if marker == "" {
		return false
	}
	prev, ok := readWaitFile(sd, agent, marker)
	if !ok {
		return false
	}
	if marker != "review-responses-warned" {
		return prev == report
	}
	lines := strings.Split(prev, "\n")
	if lines[0] != strconv.Quote(report) {
		return false
	}
	for _, sent := range lines[1:] {
		if sent == key {
			return true
		}
	}
	return false
}

// markerEntry maps a warning kind to the wait marker that records it and the
// key inside that marker. The declared review's missing header is published
// as "review-no-header" while its marker entry stays the historical
// "no-header" key, so existing markers keep working. The worker's own
// missing header has no marker: wait prints it on every settlement.
func markerEntry(kind string) (marker, key string) {
	switch kind {
	case WarningPartial:
		return "partial-warned", WarningPartial
	case WarningEnv:
		return "env-warned", WarningEnv
	case WarningTestLayersNonPass, WarningTestLayersUnmarked, WarningValidation, WarningTestLayersMissing:
		return "test-layers-warned", kind
	case WarningFindingMarks:
		return "finding-marks-warned", kind
	case WarningReportSize:
		return "report-size-warned", WarningReportSize
	case WarningReviewUnreadable, WarningReviewMissing, WarningReviewUnknown, WarningReviewResponses, WarningReviewSectionMissing, WarningReviewUnnumbered:
		return "review-responses-warned", kind
	case WarningReviewNoHeader, WarningReviewNoHeaderPublic:
		return "review-responses-warned", WarningReviewNoHeader
	}
	return "", ""
}
