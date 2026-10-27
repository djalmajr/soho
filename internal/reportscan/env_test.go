package reportscan

import "testing"

func TestReportEnvCounts(t *testing.T) {
	for _, tc := range []struct {
		name, text   string
		env, partial int
	}{
		{"reason", "- [env] no local ports; host: go test ./...\n", 1, 0},
		{"empty", "- [env]\n", 0, 1},
		{"unicode-whitespace-only", "- [env] \uFEFF\u00a0\t\n", 0, 1},
		{"formatting-only", "- **[env]** — \n", 0, 1},
		{"punctuation-only", "- [env] .\n", 0, 1},
		{"punctuation-run", "- [env] !!! ...\n", 0, 1},
		{"digit-reason", "- [env] 403\n", 1, 0},
		{"table-reason", "| check | [env] | no native Windows; host: go test ./... |\n", 1, 0},
		{"table-empty", "| check | [env] | |\n", 0, 1},
		{"fence", "```md\n- [env] no ports\n```\n~~~\n[env]\n~~~\n", 0, 0},
		{"two-same-markers", "state: [env] no network; detail: [env] no ports\n", 1, 0},
		{"quoted-state-list", "States: [done] / [partial] / [env] / [skipped] + reason\n", 0, 0},
		{"mixed-items", "- [partial] failed check\n- [env] no browser; host: browser test\n", 1, 1},
		{"case-insensitive", "# [ENV] native OS unavailable\n", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, p := EnvCount(tc.text), PartialCount(tc.text)
			if e != tc.env || p != tc.partial {
				t.Fatalf("env=%d partial=%d want %d/%d text=%q", e, p, tc.env, tc.partial, tc.text)
			}
			t.Logf("env=%d partial=%d", e, p)
		})
	}
}
