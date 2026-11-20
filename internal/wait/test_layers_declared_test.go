package wait

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/reportscan"
)

// SOH-442: a brief's `Test layers:` line hides the non-pass warning for a
// NOT RUN layer outside the declared list; FAIL and BLOCKED still warn.

func TestDeclaredTestLayersLine(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"missing", "# Brief\n\nType: backend\n", nil},
		{"declared", "# Brief\n\nTest layers: unit, mutation\n", []string{"unit", "mutation"}},
		{"padded", "Test layers:  unit ,  mutation ,\n", []string{"unit", "mutation"}},
		{"empty", "Test layers:\n", nil},
		{"first wins", "Test layers: unit\nTest layers: e2e\n", []string{"unit"}},
		{"fenced", "```md\nTest layers: unit\n```\n", nil},
		{"tilde fenced", "~~~\nTest layers: unit\n~~~\n", nil},
		{"prefix only", "My Test layers: unit\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DeclaredTestLayers(tc.body)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("DeclaredTestLayers=%q want %q", got, tc.want)
			}
		})
	}
}

func TestDeclaredTestLayersWarning(t *testing.T) {
	const declaredBrief = "# Brief — declared test layers\n\nType: backend\n\nTest layers: unit, mutation\n"
	const undeclaredNotRun = "# Report\n\n## Test layers\n- unit: PASS\n- mutation: PASS\n- e2e: NOT RUN: no browser in this slice\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
	const declaredNotRun = "# Report\n\n## Test layers\n- unit: NOT RUN: the unit command failed to start\n- e2e: n/a: no browser in this slice\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
	const undeclaredFail = "# Report\n\n## Test layers\n- unit: PASS\n- e2e: FAIL: e2e crashed\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
	const undeclaredBlocked = "# Report\n\n## Test layers\n- unit: PASS\n- e2e: BLOCKED: no browser installed\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
	for _, tc := range []struct {
		name, brief, report, wantWarning string
	}{
		{"declared list hides undeclared NOT RUN", declaredBrief, undeclaredNotRun, ""},
		{"declared layer NOT RUN still warns", declaredBrief, declaredNotRun, "lists 1 test layer(s) that did not pass (unit NOT RUN)"},
		{"undeclared FAIL still warns", declaredBrief, undeclaredFail, "lists 1 test layer(s) that did not pass (e2e FAIL)"},
		{"undeclared BLOCKED still warns", declaredBrief, undeclaredBlocked, "lists 1 test layer(s) that did not pass (e2e BLOCKED)"},
		{"no line keeps today's warning", metricsBriefBackend, undeclaredNotRun, "lists 1 test layer(s) that did not pass (e2e NOT RUN)"},
		{"empty list keeps today's warning", "# Brief\n\nType: backend\n\nTest layers:\n", undeclaredNotRun, "lists 1 test layer(s) that did not pass (e2e NOT RUN)"},
		{"fenced line keeps today's warning", "# Brief\n\nType: backend\n\n```md\nTest layers: unit\n```\n", undeclaredNotRun, "lists 1 test layer(s) that did not pass (e2e NOT RUN)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMetricsFixture(t, "off", metricsImplementerRoster, tc.report, tc.brief, "")
			out, errText, rc := f.run(t)
			if rc != 0 || !strings.Contains(out, `"status":"done"`) {
				t.Fatalf("rc=%d out=%q err=%q", rc, out, errText)
			}
			if tc.wantWarning == "" {
				if strings.Contains(errText, "did not pass") {
					t.Fatalf("undeclared NOT RUN still warned: %q", errText)
				}
				if !strings.Contains(out, `"test_layers":{"pass":2,"fail":0,"not_run":1,"blocked":0,"na":0,"unmarked":0}`) {
					t.Fatalf("the published counts changed: %q", out)
				}
				return
			}
			if !strings.Contains(errText, tc.wantWarning) {
				t.Fatalf("missing %q: %q", tc.wantWarning, errText)
			}
		})
	}
}

func TestDeclaredTestLayersBuilderFilter(t *testing.T) {
	layers := reportscan.TestLayersResult{Found: true, NonPass: []reportscan.LayerResult{
		{Name: "unit", Result: "NOT RUN"},
		{Name: "e2e", Result: "NOT RUN"},
		{Name: "contract", Result: "FAIL"},
	}}
	got := TestLayerWarningsWithDeclared("worker", layers, []string{"unit"}, "", false, false)
	if len(got) != 1 || !strings.Contains(got[0].Message, "(unit NOT RUN, contract FAIL)") {
		t.Fatalf("filtered warnings=%q", messagesOf(got))
	}
	kept := TestLayerWarnings("worker", layers, "", false, false)
	if len(kept) != 1 || !strings.Contains(kept[0].Message, "(unit NOT RUN, e2e NOT RUN, contract FAIL)") {
		t.Fatalf("unfiltered warnings=%q", messagesOf(kept))
	}
}

func messagesOf(warnings []ReportWarning) []string {
	out := make([]string, len(warnings))
	for i, warning := range warnings {
		out[i] = warning.Message
	}
	return out
}
