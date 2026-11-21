package wait

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaitRuledOutCount(t *testing.T) {
	t.Run("review report with a ruled out section", func(t *testing.T) {
		body := reviewReportBody(0, 0, 0, 0, "pass") + "\n## Ruled out\n\n- checked cache\n- checked fixture\n"
		out, _, code := waitVerdictRun(t, body)
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
			t.Fatalf("out=%s err=%v", out, err)
		}
		if code != 0 || obj["ruled_out"] != 2.0 {
			t.Fatalf("exit=%d JSON=%s", code, out)
		}
		if !strings.Contains(out, `"P3":0},"ruled_out":2`) {
			t.Fatalf("ruled_out is not in the review fields: %s", out)
		}
	})

	t.Run("review report without the section", func(t *testing.T) {
		out, _, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 0, "pass")+"\n## Findings\n\n- one\n")
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
			t.Fatalf("out=%s err=%v", out, err)
		}
		if code != 0 {
			t.Fatalf("exit=%d JSON=%s", code, out)
		}
		if _, ok := obj["ruled_out"]; ok {
			t.Fatalf("unexpected ruled_out key: %s", out)
		}
	})

	t.Run("non-review report with the section", func(t *testing.T) {
		out, _, code := waitVerdictRun(t, "# Report\n\n## Ruled out\n\n- checked cache\n- checked fixture\n")
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
			t.Fatalf("out=%s err=%v", out, err)
		}
		if code != 0 {
			t.Fatalf("exit=%d JSON=%s", code, out)
		}
		if strings.Contains(out, `"verdict"`) {
			t.Fatalf("non-review report gained review fields: %s", out)
		}
		if _, ok := obj["ruled_out"]; ok {
			t.Fatalf("unexpected ruled_out key: %s", out)
		}
	})
}

func TestWaitRuledOutExcludedMarkersKeepPass(t *testing.T) {
	body := reviewReportBody(0, 0, 0, 0, "pass") + "\n## Ruled out\n\n" +
		"- [partial] discarded state\n" +
		"- [env] no host; host: go test\n" +
		"- [F2] [origin: impl] [class: x] discarded evidence\n"
	out, stderr, code := waitVerdictRun(t, body)
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if code != 0 || obj["ruled_out"] != 3.0 || obj["verdict_effective"] != "pass" {
		t.Fatalf("exit=%d JSON=%s stderr=%s", code, out, stderr)
	}
	for _, key := range []string{"partial", "env", "finding_marks"} {
		if _, ok := obj[key]; ok {
			t.Fatalf("ruled-out %s leaked into the settled line: %s", key, out)
		}
	}
	for _, frag := range []string{"marks 1 item(s) partial", "item(s) env-limited", "without an origin and a class"} {
		if strings.Contains(stderr, frag) {
			t.Fatalf("unexpected warning %q: %s", frag, stderr)
		}
	}
}

func TestWaitRuledOutFindingMarksDoNotAnswerReviewQuestions(t *testing.T) {
	dir := t.TempDir()
	review := filepath.Join(dir, "review.md")
	writeReviewFile(t, review, "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: fail\n\n## Findings\n\n- [F1] [origin: impl] [class: y] active finding\n\n## Ruled out\n\n- [F2] [origin: impl] [class: x] discarded evidence\n")
	f := newMetricsFixture(t, "off", metricsImplementerRoster, responsesReport("- F1: fixed\n"), reviewBrief(review), "")
	out, errText, code := f.run(t)
	if code != 0 || hasReviewWarning(errText) {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(out, `"review_responses":{"fixed":[1],"declined":[],"missing":[]`) || strings.Contains(out, `"finding_marks"`) || strings.Contains(errText, "F2") {
		t.Fatalf("a discarded finding mark became a review finding: out=%s stderr=%s", out, errText)
	}
}

func TestWaitRuledOutScopeMarkerNotActionable(t *testing.T) {
	const marker = "blocked-by-scope: internal/parser.go:10 — change called out in earlier output"
	for _, tc := range []struct {
		name, section string
		wantScope     bool
	}{
		{"inside Ruled out", "## Ruled out\n\n- checked candidate\n- " + marker + "\n", false},
		{"outside the section", "## Ruled out\n\n- checked candidate\n\n## Active\n\n- " + marker + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 0, "pass")+"\n"+tc.section)
			var obj map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
				t.Fatalf("out=%s err=%v", out, err)
			}
			if code != 0 {
				t.Fatalf("exit=%d JSON=%s", code, out)
			}
			_, present := obj["blocked_by_scope"]
			if tc.wantScope != present {
				t.Fatalf("blocked_by_scope present=%v want=%v: %s", present, tc.wantScope, out)
			}
			if tc.wantScope && !strings.Contains(out, "internal/parser.go:10 — change called out in earlier output") {
				t.Fatalf("active scope marker missing: %s", out)
			}
		})
	}
}
