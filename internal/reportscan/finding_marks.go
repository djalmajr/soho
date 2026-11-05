package reportscan

import (
	"regexp"
	"strconv"
	"strings"
)

// FindingMarksResult is the origin and class breakdown of the findings of a
// review report, plus the header's findings without a valid mark, and the
// valid finding numbers in report order.
type FindingMarksResult struct {
	Brief    int
	Impl     int
	Unknown  int
	Classes  map[string]int
	Unmarked int
	Numbers  []int
}

var (
	findingIDRE     = regexp.MustCompile(`\[F([0-9]+)\]`)
	findingOriginRE = regexp.MustCompile(`\[origin: ([^\]]*)\]`)
	findingClassRE  = regexp.MustCompile(`\[class: ([^\]]*)\]`)
	classSlugRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
)

// FindingMarks scans the report outside fenced code blocks for the finding
// marks: a line counts only when it carries all three markers in the exact
// form — [F<n>] with n at least 1, [origin: brief|impl|unknown], and
// [class: <slug>] with the slug matching ^[a-z0-9][a-z0-9-]{0,40}$ — in any
// position of the line (heading, table row or list line alike). Each line
// counts at most once, and the first marker of each kind on the line decides.
// A marker with an origin outside the list, a class outside the rule or a
// missing sibling is not the form, and the line does not count. F<n> is the
// finding's number in the report, so a number counts once: a summary and a
// detail that both carry it are one finding, and the first valid line decides.
// Unmarked is the header's finding total minus the counted lines, never
// negative; without a header the total is 0.
func FindingMarks(text string, header *ReviewHeaderResult) FindingMarksResult {
	out := FindingMarksResult{Classes: map[string]int{}}
	total := 0
	if header != nil {
		total = int(header.Findings)
	}
	if text != "" {
		inFence, fenceChar, fenceLen := false, byte(0), 0
		seen := map[int]bool{}
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
			countFindingLine(raw, seen, &out)
		}
	}
	out.Unmarked = total - (out.Brief + out.Impl + out.Unknown)
	if out.Unmarked < 0 {
		out.Unmarked = 0
	}
	return out
}

// countFindingLine counts the line only when all three markers are valid;
// the first marker of each kind on the line decides. The form is exact:
// the markers are matched case-sensitively and the class slug against its
// rule, so an origin outside the list, a class outside the rule or a
// missing sibling makes the whole line not count.
func countFindingLine(line string, seen map[int]bool, out *FindingMarksResult) {
	id := findingIDRE.FindStringSubmatch(line)
	if id == nil {
		return
	}
	n, err := strconv.Atoi(id[1])
	if err != nil || n < 1 || seen[n] {
		return
	}
	originMatch := findingOriginRE.FindStringSubmatch(line)
	classMatch := findingClassRE.FindStringSubmatch(line)
	if originMatch == nil || classMatch == nil || !classSlugRE.MatchString(classMatch[1]) {
		return
	}
	switch originMatch[1] {
	case "brief":
		out.Brief++
	case "impl":
		out.Impl++
	case "unknown":
		out.Unknown++
	default:
		return
	}
	seen[n] = true
	out.Classes[classMatch[1]]++
	out.Numbers = append(out.Numbers, n)
}
