package wait

import (
	"encoding/json"
	"testing"
)

func TestWaitFollowupsPartialNotCounted(t *testing.T) {
	body := reviewReportBody(0, 0, 0, 0, "pass") + "\n## Follow-ups\n\n- [partial] identical to main\n"
	out, _, code := waitVerdictRun(t, body)
	if code != 0 {
		t.Fatalf("code=%d out=%q", code, out)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(out), &line); err != nil {
		t.Fatalf("invalid JSON out=%q: %v", out, err)
	}
	if line["verdict"] != "pass" || line["verdict_effective"] != "pass" {
		t.Fatalf("want verdict and verdict_effective pass, got %q", out)
	}
	if _, ok := line["partial"]; ok {
		t.Fatalf("partial key must be absent when the only item is under Follow-ups: %q", out)
	}
}
