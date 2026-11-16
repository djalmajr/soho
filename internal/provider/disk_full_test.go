package provider

import (
	"strings"
	"testing"
)

func TestDiskFullDetection(t *testing.T) {
	lines := []string{
		"soho: gate: cannot write /tmp/soho/hc-t1/reports/crit-kas283c2-unit1.result.json: write: disk quota exceeded",
		"write failed: No space left on device",
		"write failed: EDQUOT",
		"write failed: ENOSPC",
	}
	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			if got := QuotaDetect("idle", line); got != nil {
				t.Fatalf("disk line classified as quota: %q", got)
			}
			if got := DiskFullDetect("idle", line); got != line {
				t.Fatalf("DiskFullDetect=%q, want %q", got, line)
			}
		})
	}
	t.Run("mixed screen preserves real quota", func(t *testing.T) {
		line := "You've hit your usage limit"
		for _, screen := range []string{lines[0] + "\n" + line, line + "\n" + lines[0], "EDQUOT and quota exceeded\n" + line} {
			got := QuotaDetect("idle", screen)
			if len(got) != 2 || got[0] != line {
				t.Fatalf("QuotaDetect(%q)=%q, want %q", screen, got, line)
			}
		}
	})
	t.Run("boundaries and redaction", func(t *testing.T) {
		for _, tc := range []struct{ state, screen, want string }{
			{"working", lines[0], ""}, {"idle", "", ""},
			{"idle", `return "disk quota exceeded"`, ""},
			{"idle", `"disk quota exceeded"`, ""},
			{"idle", "`ENOSPC`", ""},
			{"idle", "// ENOSPC", ""},
			{"idle", "Error: ENOSPC token=sk_live_abcdefghij\r\nsecond line", "Error: ENOSPC token=[redacted]"},
			{"idle", "Error: ENOSPC\nError: EDQUOT", "Error: ENOSPC"},
		} {
			if got := DiskFullDetect(tc.state, tc.screen); got != tc.want {
				t.Errorf("DiskFullDetect(%q,%q)=%q, want %q", tc.state, tc.screen, got, tc.want)
			}
		}
		if got := DiskFullDetect("idle", "Error: ENOSPC \x1b[31mred\x1b[0m"); strings.Contains(got, "\x1b") {
			t.Fatalf("escape sequence in %q", got)
		}
	})
}
