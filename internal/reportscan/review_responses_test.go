package reportscan

import (
	"reflect"
	"testing"
)

// TestReviewResponses (DJA-684 slice C, scenarios 2-4) covers the ## Review
// responses section: the three valid forms, the invalid lines that are
// unmarked, fenced blocks ignored, the section ending at the next # or ##
// title, and a repeated number keeping the first answer.
func TestReviewResponses(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		found    bool
		fixed    []int
		declined []int
		unmarked int
	}{
		{name: "absent section", text: "# Report\n\nno responses here\n", found: false, fixed: []int{}, declined: []int{}, unmarked: 0},
		{name: "one fixed bare", text: "## Review responses\n\n- F1: fixed\n", found: true, fixed: []int{1}, declined: []int{}, unmarked: 0},
		{name: "fixed with ignored note", text: "## Review responses\n\n- F2: fixed: ran the proof\n", found: true, fixed: []int{2}, declined: []int{}, unmarked: 0},
		{name: "declined with reason", text: "## Review responses\n\n- F3: declined: the case is out of scope\n", found: true, fixed: []int{}, declined: []int{3}, unmarked: 0},
		{name: "all three forms", text: "## Review responses\n\n- F1: fixed\n- F2: fixed: note\n- F3: declined: because\n", found: true, fixed: []int{1, 2}, declined: []int{3}, unmarked: 0},
		{name: "declined without reason is unmarked", text: "## Review responses\n\n- F3: declined\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "declined punctuation-only reason is unmarked", text: "## Review responses\n\n- F3: declined: ...\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "uppercase fixed is unmarked", text: "## Review responses\n\n- F1: FIXED\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "F0 is unmarked", text: "## Review responses\n\n- F0: fixed\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "bold fixed is unmarked", text: "## Review responses\n\n- F1: **fixed**\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "extra words after fixed is unmarked", text: "## Review responses\n\n- F1: fixed extra\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "declined reason with a unicode letter", text: "## Review responses\n\n- F3: declined: não cabe\n", found: true, fixed: []int{}, declined: []int{3}, unmarked: 0},
		{name: "fenced block is ignored", text: "## Review responses\n\n```\n- F1: fixed\n- F9: declined: inside a fence\n```\n\n- F2: fixed\n", found: true, fixed: []int{2}, declined: []int{}, unmarked: 0},
		{name: "next h2 title ends the section", text: "## Review responses\n\n- F1: fixed\n\n## Next\n- F2: fixed\n", found: true, fixed: []int{1}, declined: []int{}, unmarked: 0},
		{name: "next h1 title ends the section", text: "## Review responses\n\n- F1: fixed\n\n# Top\n- F2: fixed\n", found: true, fixed: []int{1}, declined: []int{}, unmarked: 0},
		{name: "h3 title does not end the section", text: "## Review responses\n\n- F1: fixed\n\n### Note\n- F2: fixed\n", found: true, fixed: []int{1, 2}, declined: []int{}, unmarked: 0},
		{name: "repeated number keeps the first answer", text: "## Review responses\n\n- F1: fixed\n- F1: declined: because\n", found: true, fixed: []int{1}, declined: []int{}, unmarked: 0},
		{name: "repeated number reversed keeps the first", text: "## Review responses\n\n- F1: declined: because\n- F1: fixed\n", found: true, fixed: []int{}, declined: []int{1}, unmarked: 0},
		{name: "non-list line in the section is skipped", text: "## Review responses\n\nA prose line, not a list.\n\n- F1: fixed\n", found: true, fixed: []int{1}, declined: []int{}, unmarked: 0},
		{name: "numbered list line without the form is unmarked", text: "## Review responses\n\n1. F1: fixed\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "a star bullet is not the form", text: "## Review responses\n\n* F1: fixed\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "a plus bullet is not the form", text: "## Review responses\n\n+ F1: fixed\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 1},
		{name: "empty section", text: "## Review responses\n\n", found: true, fixed: []int{}, declined: []int{}, unmarked: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ReviewResponses(tc.text)
			if got.Found != tc.found {
				t.Fatalf("Found = %v, want %v", got.Found, tc.found)
			}
			if !reflect.DeepEqual(got.Fixed, tc.fixed) {
				t.Fatalf("Fixed = %v, want %v", got.Fixed, tc.fixed)
			}
			if !reflect.DeepEqual(got.Declined, tc.declined) {
				t.Fatalf("Declined = %v, want %v", got.Declined, tc.declined)
			}
			if got.Unmarked != tc.unmarked {
				t.Fatalf("Unmarked = %d, want %d", got.Unmarked, tc.unmarked)
			}
		})
	}
}

// TestFindingMarksNumbers (DJA-684 slice C, decision 3): FindingMarksResult
// carries the valid finding numbers in report order, each number once.
func TestFindingMarksNumbers(t *testing.T) {
	header := &ReviewHeaderResult{Findings: 3, Severity: map[string]float64{"P0": 0, "P1": 0, "P2": 0, "P3": 3}, Verdict: "pass"}
	tests := []struct {
		name    string
		text    string
		header  *ReviewHeaderResult
		numbers []int
	}{
		{name: "three valid marks in order", text: "- [F1] [origin: impl] [class: a] x\n- [F2] [origin: brief] [class: b] x\n- [F3] [origin: unknown] [class: c] x\n", header: header, numbers: []int{1, 2, 3}},
		{name: "out of order keeps report order", text: "- [F2] [origin: impl] [class: a] x\n- [F1] [origin: brief] [class: b] x\n", header: &ReviewHeaderResult{Findings: 2, Severity: map[string]float64{"P0": 0, "P1": 0, "P2": 0, "P3": 2}, Verdict: "pass"}, numbers: []int{2, 1}},
		{name: "a repeated number counts once", text: "- [F1] [origin: impl] [class: a] summary\n- [F1] [origin: impl] [class: a] detail\n", header: &ReviewHeaderResult{Findings: 1, Severity: map[string]float64{"P0": 0, "P1": 0, "P2": 0, "P3": 1}, Verdict: "pass"}, numbers: []int{1}},
		{name: "an invalid mark keeps no number", text: "- [F1] [origin: other] [class: a] x\n", header: &ReviewHeaderResult{Findings: 1, Severity: map[string]float64{"P0": 0, "P1": 0, "P2": 0, "P3": 1}, Verdict: "pass"}},
		{name: "a fenced mark keeps no number", text: "```\n- [F1] [origin: impl] [class: a] x\n```\n", header: &ReviewHeaderResult{Findings: 1, Severity: map[string]float64{"P0": 0, "P1": 0, "P2": 0, "P3": 1}, Verdict: "pass"}},
		{name: "nil header still collects numbers", text: "- [F1] [origin: impl] [class: a] x\n", header: nil, numbers: []int{1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FindingMarks(tc.text, tc.header)
			if !reflect.DeepEqual(got.Numbers, tc.numbers) {
				t.Fatalf("Numbers = %v, want %v", got.Numbers, tc.numbers)
			}
		})
	}
}
