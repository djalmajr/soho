package wait

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestWaitReportEnvVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		env, partial int
		verdict      string
	}{
		{"env-only", "- [env] no ports; host: go test ./...\n- [env] no Windows; host: GOOS=windows go test ./...\n", 2, 0, "pass"},
		{"mixed", "- [partial] failing assertion\n- [env] no browser; host: browser test\n", 1, 1, "fail"},
		{"empty-reason", "- [env]\n", 0, 1, "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := waitVerdictRun(t, reviewReportBody(0, 0, 0, 0, "pass")+tc.body)
			var obj map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
				t.Fatalf("out=%s err=%v", out, err)
			}
			number := func(key string) int { n, _ := obj[key].(float64); return int(n) }
			if code != 0 || number("env") != tc.env || number("partial") != tc.partial || obj["verdict_effective"] != tc.verdict {
				t.Fatalf("exit=%d JSON=%s stderr=%s", code, out, stderr)
			}
			envWarning := "report of 'worker' marks " + strconv.Itoa(tc.env) + " item(s) env-limited: the orchestrator must cover them by other proof (host run, CI, native platform) before commit, push or release"
			if tc.env > 0 && !strings.Contains(stderr, envWarning) {
				t.Fatalf("missing exact env warning: %s", stderr)
			}
			wantEnvWarnings := 0
			if tc.env > 0 {
				wantEnvWarnings = 1
			}
			if strings.Count(stderr, "env-limited:") != wantEnvWarnings {
				t.Fatalf("env warning count mismatch: %s", stderr)
			}
			if (tc.partial > 0) != strings.Contains(stderr, "a partial item is not a pass") {
				t.Fatalf("partial warning mismatch: %s", stderr)
			}
			t.Logf("exit=%d JSON=%s stderr=%s", code, out, stderr)
		})
	}
}
func TestMetricsReportEnvCounts(t *testing.T) {
	body := reviewReportBody(0, 0, 0, 0, "pass") + "- [done] delivered\n- [env] no ports; host: go test ./...\n- [env]\n"
	if got := metricsMarkerCount(body, "[env]"); got != 1 {
		t.Fatalf("env count=%d", got)
	}
	if got := metricsMarkerCount(body, "[done]"); got != 1 {
		t.Fatalf("done count=%d", got)
	}
}

func TestMetricsReportEnvVerdict(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			body := reviewReportBody(0, 0, 0, 0, "pass") + "- [done] delivered\n- [env] no ports; host: go test ./...\n"
			wantPartial, wantVerdict := 0.0, "pass"
			if partial {
				body += "- [partial] assertion failed\n"
				wantPartial, wantVerdict = 1, "fail"
			}
			m := newMetricsFixture(t, "on", metricsReviewerRoster, body, metricsBriefBackend, "")
			out, stderr, code := m.run(t)
			records := m.lines(t)
			if code != 0 || len(records) != 1 {
				t.Fatalf("exit=%d out=%s stderr=%s records=%v", code, out, stderr, records)
			}
			record := parseMetricsLine(t, records[0])
			items, _ := record["items"].(map[string]any)
			if items["env"] != 1.0 || items["done"] != 1.0 || items["partial"] != wantPartial || record["verdict_effective"] != wantVerdict {
				t.Fatalf("metrics=%s", records[0])
			}
			t.Log(records[0])
		})
	}
}
