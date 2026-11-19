package wait

import (
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/reportscan"
)

// Golden warning bytes for the shared builders. `soho report` prints these
// same strings, so a change here must be deliberate on both paths.
func TestReportWarningStringsGolden(t *testing.T) {
	header := &reportscan.ReviewHeaderResult{
		Findings: 3,
		Severity: map[string]float64{"P0": 1, "P1": 1, "P2": 1, "P3": 0},
		Verdict:  "pass",
	}
	tests := []struct {
		name string
		got  []ReportWarning
		want []string
	}{
		{
			name: "partial and env",
			got:  PartialWarnings("worker", 2, 1),
			want: []string{
				"report of 'worker' marks 2 item(s) partial: a partial item is not a pass; read them before commit, push or release",
				"report of 'worker' marks 1 item(s) env-limited: the orchestrator must cover them by other proof (host run, CI, native platform) before commit, push or release",
			},
		},
		{
			name: "header",
			got:  HeaderWarnings("worker", header, false),
			want: []string{"report of 'worker' says verdict pass with 3 open P0-P2 finding(s): read them before commit, push or release"},
		},
		{
			name: "test layers",
			got: TestLayerWarnings("worker", reportscan.TestLayersResult{
				Found: true, NonPass: []reportscan.LayerResult{{Name: "unit", Result: "FAIL"}, {Name: "race", Result: "NOT RUN"}},
				Unmarked: 1, FirstUnmarked: "- docs: x", FirstUnmarkedReason: "unknown result word",
			}, "NOT RUN: host gate", true, false),
			want: []string{
				"report of 'worker' lists 2 test layer(s) that did not pass (unit FAIL, race NOT RUN): read them before commit, push or release",
				"report of 'worker' has 1 Test layers line(s) without a result; the form is '- <layer>: PASS|FAIL|NOT RUN|BLOCKED|n/a', with ': <reason>' (or ' — <reason>') after FAIL, NOT RUN and BLOCKED, and an optional note after PASS (first: \"- docs: x\"; unknown result word)",
				"validation of 'worker' is NOT RUN: host gate: read it before commit, push or release",
			},
		},
		{
			name: "missing test layers",
			got:  TestLayerWarnings("worker", reportscan.TestLayersResult{}, "", false, true),
			want: []string{"report of 'worker' has no Test layers section, and the project has a VALIDATION.md"},
		},
		{
			name: "finding marks",
			got:  FindingMarkWarnings("worker", 2),
			want: []string{"report of 'worker' has 2 finding(s) without an origin and a class"},
		},
		{
			name: "review responses",
			got:  ReviewResponseWarnings("worker", []int{1, 3}, []int{7}, 2, 1),
			want: []string{
				"report of 'worker' leaves 2 finding(s) of the review without a response (F1, F3): read them before commit, push or release",
				"report of 'worker' answers 1 finding(s) the review does not have (F7)",
				"report of 'worker' has 2 Review responses line(s) without a valid answer",
				"review named by the brief of 'worker' has 1 finding(s) without a number, so their responses cannot be checked",
			},
		},
		{
			name: "reference parity missing",
			got:  ReferenceParityWarnings("worker", ReferenceParityCounts{}),
			want: []string{"report of 'worker' has no Reference parity section"},
		},
		{
			name: "reference parity unmarked",
			got:  ReferenceParityWarnings("worker", ReferenceParityCounts{Found: true, Unmarked: 2}),
			want: []string{"report of 'worker' has 2 parity item(s) without a mark"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			messages := make([]string, len(tc.got))
			for i, warning := range tc.got {
				messages[i] = warning.Message
			}
			if !reflect.DeepEqual(messages, tc.want) {
				t.Fatalf("warnings:\n got %q\nwant %q", messages, tc.want)
			}
		})
	}
}

func TestReportWarningHeaderEdgesGolden(t *testing.T) {
	if got := HeaderWarnings("worker", nil, false); len(got) != 0 {
		t.Fatalf("worker is not a review role: %v", got)
	}
	got := HeaderWarnings("worker", nil, true)
	want := []string{"report of 'worker' has no 'findings: N (P0 a, P1 b, P2 c, P3 d) | verdict: pass|fail' first line"}
	if len(got) != 1 || got[0].Message != want[0] {
		t.Fatalf("got %v, want %q", got, want)
	}
	header := &reportscan.ReviewHeaderResult{Findings: 4, Severity: map[string]float64{"P0": 1, "P1": 1, "P2": 1, "P3": 0}, Verdict: "fail"}
	got = HeaderWarnings("worker", header, false)
	want = []string{"report of 'worker': findings 4 but P0..P3 add up to 3"}
	if len(got) != 1 || got[0].Message != want[0] {
		t.Fatalf("got %v, want %q", got, want)
	}
}

func TestReportWarningReviewEdgeStringsGolden(t *testing.T) {
	if got := UnreadableReviewWarning("worker", "/tmp/missing").Message; got != "brief of 'worker' names a review that cannot be read: /tmp/missing" {
		t.Fatalf("unreadable: %q", got)
	}
	if got := NoReviewHeaderWarning("worker").Message; got != "review named by the brief of 'worker' has no review header" {
		t.Fatalf("no header: %q", got)
	}
}

func TestAnalyzeReportUsesSharedBuilders(t *testing.T) {
	text := "findings: 1 (P0 0, P1 1, P2 0, P3 0) | verdict: pass\nvalidation: PASS\n\n- [partial] first\n- [env] host only\n\n## Test layers\n\n- unit: PASS\n- race: NOT RUN: host gate\n"
	analysis := AnalyzeReport(ReportWarningContext{Agent: "worker", Report: "/tmp/report.md", Text: text, Role: "implementer"})
	if analysis.Partial != 1 || analysis.EnvCount != 1 {
		t.Fatalf("counts %d/%d", analysis.Partial, analysis.EnvCount)
	}
	got := make([]string, 0, len(analysis.Warnings))
	for _, warning := range analysis.Warnings {
		got = append(got, warning.Message)
	}
	want := []string{
		"report of 'worker' marks 1 item(s) partial: a partial item is not a pass; read them before commit, push or release",
		"report of 'worker' marks 1 item(s) env-limited: the orchestrator must cover them by other proof (host run, CI, native platform) before commit, push or release",
		"report of 'worker' says verdict pass with 1 open P0-P2 finding(s): read them before commit, push or release",
		"report of 'worker' lists 1 test layer(s) that did not pass (race NOT RUN): read them before commit, push or release",
		"report of 'worker' has 1 finding(s) without an origin and a class",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings:\n got %q\nwant %q", got, want)
	}
}
