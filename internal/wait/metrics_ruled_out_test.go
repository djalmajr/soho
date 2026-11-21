package wait

import "testing"

func TestMetricsRuledOutDiscardedMarkersDoNotCount(t *testing.T) {
	const header = "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n"
	for _, tc := range []struct {
		name, body         string
		wantDone, wantSkip float64
	}{
		{
			name: "discard-only report",
			body: header + "## Ruled out\n\n" +
				"- Prior output: [done] gate had not run, checked at parser.go:10.\n" +
				"- Prior output: [skipped] skipped was disproved at parser.go:11.\n",
			wantDone: 0,
			wantSkip: 0,
		},
		{
			name: "repeated discard blocks with active items",
			body: header + "## Ruled out\n\n" +
				"- [done] old one\n- [skipped] old one\n\n## Active\n\n" +
				"- [done] real one\n- [skipped] real one\n\n## Ruled out\n\n" +
				"- [done] old two\n- [skipped] old two\n",
			wantDone: 1,
			wantSkip: 1,
		},
		{
			name: "Follow-ups keeps the baseline count",
			body: header + "## Follow-ups\n\n" +
				"- [done] follow-up item\n- [skipped] follow-up item\n",
			wantDone: 1,
			wantSkip: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMetricsFixture(t, "on", metricsReviewerRoster, tc.body, "", "")
			stdout, stderr, code := fixture.run(t)
			if code != 0 {
				t.Fatalf("settlement exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			items := parseMetricsLine(t, fixture.lines(t)[0])["items"].(map[string]any)
			if items["done"] != tc.wantDone || items["skipped"] != tc.wantSkip {
				t.Fatalf("persisted metrics: done=%v skipped=%v; want %v and %v", items["done"], items["skipped"], tc.wantDone, tc.wantSkip)
			}
		})
	}
}
