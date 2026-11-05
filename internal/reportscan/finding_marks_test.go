package reportscan

import (
	"strings"
	"testing"
)

// TestFindingMarks covers the finding-mark table of the DJA-684 slice B
// brief: scenarios 1-4. A line counts only with all three valid markers in
// any position, outside fenced blocks; Unmarked is the header total minus
// the counted lines, never negative.
func TestFindingMarks(t *testing.T) {
	header := func(findings int) *ReviewHeaderResult {
		return &ReviewHeaderResult{Findings: float64(findings), Severity: map[string]float64{"P0": 0, "P1": 0, "P2": 0, "P3": 0}, Verdict: "pass"}
	}
	markedLine := func(n, origin, class string) string {
		return "- [" + n + "] [origin: " + origin + "] [class: " + class + "] a finding line\n"
	}
	tests := []struct {
		name     string
		text     string
		header   *ReviewHeaderResult
		brief    int
		impl     int
		unknown  int
		unmarked int
		classes  map[string]int
	}{
		// scenario 1: the three markers count, in any position, in a
		// heading, a table row or a list line.
		{"a bullet line counts", markedLine("F1", "impl", "missing-bound"), header(1), 0, 1, 0, 0, map[string]int{"missing-bound": 1}},
		{"markers in reversed order", "[origin: brief] [class: doc-drift] [F1] tail\n", header(1), 1, 0, 0, 0, map[string]int{"doc-drift": 1}},
		{"a heading line counts", "## [F1] [origin: unknown] [class: follows-link] The title\n", header(1), 0, 0, 1, 0, map[string]int{"follows-link": 1}},
		{"a table row counts", "| [F1] | [origin: brief] | [class: race-read-write] |\n", header(1), 1, 0, 0, 0, map[string]int{"race-read-write": 1}},
		{"a numbered list line counts", "1. [F1] [origin: impl] [class: error-swallowed] text\n", header(1), 0, 1, 0, 0, map[string]int{"error-swallowed": 1}},
		// F<n> is the finding's number in the report: a summary and a detail
		// that both carry it are one finding, and the first valid line decides.
		{"two lines with the same F are one finding", markedLine("F1", "impl", "missing-bound") + markedLine("F1", "brief", "doc-drift"), header(2), 0, 1, 0, 1, map[string]int{"missing-bound": 1}},
		{"an invalid line does not take the number", "- [F1] [origin: other] [class: doc-drift] x\n" + markedLine("F1", "impl", "missing-bound"), header(1), 0, 1, 0, 0, map[string]int{"missing-bound": 1}},
		{"a number too large for an int does not count", "- [F99999999999999999999999] [origin: brief] [class: doc-drift] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		// scenario 2: an origin outside the list, a class outside the rule,
		// or a missing marker: the line does not count.
		{"origin other does not count", markedLine("F1", "other", "missing-bound"), header(1), 0, 0, 0, 1, map[string]int{}},
		{"class Bad Slug does not count", "- [F1] [origin: brief] [class: Bad Slug] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"an uppercase class does not count", "- [F1] [origin: brief] [class: MissingBound] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"a 42-character class does not count", "- [F1] [origin: brief] [class: " + strings.Repeat("a", 42) + "] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"missing origin does not count", "- [F1] [class: missing-bound] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"missing class does not count", "- [F1] [origin: brief] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"missing F does not count", "- [origin: brief] [class: missing-bound] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"F0 does not count", "- [F0] [origin: brief] [class: missing-bound] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"a case-shifted marker does not count", "- [f1] [origin: brief] [class: missing-bound] x\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"a 41-character class counts", "- [F1] [origin: brief] [class: " + strings.Repeat("a", 41) + "] x\n", header(1), 1, 0, 0, 0, map[string]int{strings.Repeat("a", 41): 1}},
		// scenario 3: inside a fenced block the lines do not count,
		// including a fence closed with a tab.
		{"fenced block does not count", "```\n" + strings.TrimLeft(markedLine("F1", "impl", "missing-bound"), "- ") + "```\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"a tab-closed fence does not count", "```\n" + strings.TrimLeft(markedLine("F1", "impl", "missing-bound"), "- ") + "```\t\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"a mark after a tab-closed fence counts", "```\n" + strings.TrimLeft(markedLine("F1", "impl", "missing-bound"), "- ") + "```\t\n" + markedLine("F2", "brief", "doc-drift"), header(2), 1, 0, 0, 1, map[string]int{"doc-drift": 1}},
		{"a tilde fence does not count", "~~~\n" + strings.TrimLeft(markedLine("F1", "impl", "missing-bound"), "- ") + "~~~\n", header(1), 0, 0, 0, 1, map[string]int{}},
		{"a mark before and after a fence counts once", markedLine("F1", "brief", "doc-drift") + "```\n[F1] [origin: impl] [class: missing-bound]\n```\n" + markedLine("F2", "impl", "missing-bound"), header(2), 1, 1, 0, 0, map[string]int{"doc-drift": 1, "missing-bound": 1}},
		// scenario 4: Unmarked is the total minus the counted lines, never
		// negative.
		{"more marked lines than the total: zero unmarked", markedLine("F1", "impl", "missing-bound") + markedLine("F2", "brief", "doc-drift") + markedLine("F3", "unknown", "follows-link"), header(2), 1, 1, 1, 0, map[string]int{"missing-bound": 1, "doc-drift": 1, "follows-link": 1}},
		{"marks without a header: zero unmarked", markedLine("F1", "impl", "missing-bound") + markedLine("F2", "brief", "doc-drift"), nil, 1, 1, 0, 0, map[string]int{"missing-bound": 1, "doc-drift": 1}},
		{"an empty body keeps the whole total unmarked", "", header(3), 0, 0, 0, 3, map[string]int{}},
		{"nil header and empty body: all zero", "", nil, 0, 0, 0, 0, map[string]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindingMarks(tt.text, tt.header)
			if got.Brief != tt.brief || got.Impl != tt.impl || got.Unknown != tt.unknown || got.Unmarked != tt.unmarked {
				t.Fatalf("counts = {brief:%d impl:%d unknown:%d unmarked:%d}, want {brief:%d impl:%d unknown:%d unmarked:%d}", got.Brief, got.Impl, got.Unknown, got.Unmarked, tt.brief, tt.impl, tt.unknown, tt.unmarked)
			}
			if len(got.Classes) != len(tt.classes) {
				t.Fatalf("classes = %v, want %v", got.Classes, tt.classes)
			}
			for slug, want := range tt.classes {
				if got.Classes[slug] != want {
					t.Fatalf("class %q = %d, want %d (classes %v)", slug, got.Classes[slug], want, got.Classes)
				}
			}
		})
	}
}
