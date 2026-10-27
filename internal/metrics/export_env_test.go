package metrics

import (
	"encoding/json"
	"strings"
	"testing"
)

// The items projection keeps the env counter wait writes for
// environment-limited items (DJA-340), and still drops unrelated keys.
func TestExportKeepsEnvItems(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": `{"ts":"2026-10-26T15:00:00Z","agent":"rev","report":"rev.md","role":"reviewer","kind":"codex","model":"gpt-6.1-sol","items":{"done":2,"partial":0,"skipped":0,"env":3,"path":"/x"},"verdict":"pass","verdict_effective":"pass"}` + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var line map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &line); err != nil {
		t.Fatalf("output is not one JSON object: %v: %s", err, stdout)
	}
	var items map[string]int
	if err := json.Unmarshal(line["items"], &items); err != nil {
		t.Fatalf("items=%s: %v", line["items"], err)
	}
	if items["env"] != 3 || items["done"] != 2 || len(items) != 4 {
		t.Fatalf("items=%v want done/partial/skipped/env with env=3 and no path", items)
	}
}
