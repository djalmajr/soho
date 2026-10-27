package wait

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReferenceParityWait(t *testing.T) {
	for _, tc := range []struct {
		name, brief, body, warn      string
		same, diverges, na, unmarked int
	}{
		{"complete", "Reference: /fixture@abcdef0\n", "## Reference parity\n- [same] delete/insert\n- [diverges: local schema needs a different key] key\n- [n/a] unavailable feature\n", "", 1, 1, 1, 0},
		{"missing", "Reference: /fixture@abcdef0\n", "# Report\nDone\n", "has no Reference parity section", 0, 0, 0, 0},
		{"unmarked", "Reference: /fixture@abcdef0\n", "## Reference parity\n- missing comparison\n", "1 parity item(s) without a mark", 0, 0, 0, 1},
		{"bare-diverges", "Reference: /fixture@abcdef0\n", "## Reference parity\n- [diverges] changed\n", "1 parity item(s) without a mark", 0, 0, 0, 1},
		{"blank-reason", "Reference: /fixture@abcdef0\n", "## Reference parity\n- [diverges: \u00a0 ] changed\n", "1 parity item(s) without a mark", 0, 0, 0, 1},
		{"no-reference", "Type: backend\n", "# Report\nDone\n", "", 0, 0, 0, 0},
		{"fenced-reference", "```\nReference: /example@abcdef0\n```\n", "# Report\nDone\n", "", 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMetricsFixture(t, "off", metricsImplementerRoster, tc.body, tc.brief, "")
			out, errText, rc := f.run(t)
			if rc != 0 {
				t.Fatalf("rc %d %s %s", rc, out, errText)
			}
			if (tc.warn == "" && strings.Contains(errText, "parity")) || (tc.warn != "" && !strings.Contains(errText, tc.warn)) {
				t.Fatalf("warning %q want %q", errText, tc.warn)
			}
			var line map[string]any
			if err := json.Unmarshal([]byte(out), &line); err != nil {
				t.Fatal(err)
			}
			if line["status"] != "done" {
				t.Fatalf("status %v", line)
			}
			parity, present := line["reference_parity"]
			if tc.name == "no-reference" || tc.name == "fenced-reference" {
				if present {
					t.Fatal("unrelated brief changed JSON")
				}
				return
			}
			want := map[string]any{"same": float64(tc.same), "diverges": float64(tc.diverges), "na": float64(tc.na), "unmarked": float64(tc.unmarked)}
			got, _ := json.Marshal(parity)
			expected, _ := json.Marshal(want)
			if string(got) != string(expected) {
				t.Fatalf("parity got %s want %s", got, expected)
			}
			t.Logf("status=done exit=0 parity=%s warning=%q", got, tc.warn)
		})
	}
}
