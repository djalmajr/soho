package cli

import (
	"strings"
	"testing"
)

func TestReportFileRuledOutScopeMarker(t *testing.T) {
	const marker = "blocked-by-scope: internal/parser.go:10 — example evidence"
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{
			name: "discarded marker is not actionable",
			body: "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n## Ruled out\n\n- checked candidate\n- " + marker + "\n",
			want: 0,
		},
		{
			name: "marker after the section is actionable",
			body: "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n## Ruled out\n\n- checked candidate\n\n## Active\n\n- " + marker + "\n",
			want: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd, _, report := reportFixture(t)
			writeReportFixtureFile(t, report, tc.body)
			code, out, stderr := runIn(t, []string{"report", "--file", report, "--json"}, env, cwd)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			got := parseReportOutput(t, out)
			if len(got.BlockedByScope) != tc.want {
				t.Fatalf("blocked_by_scope=%v, want %d entries", got.BlockedByScope, tc.want)
			}
			if tc.want == 1 && !strings.Contains(got.BlockedByScope[0], "internal/parser.go:10 — example evidence") {
				t.Fatalf("active marker missing: %v", got.BlockedByScope)
			}
		})
	}
}
