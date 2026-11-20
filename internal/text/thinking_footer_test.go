package text

import "testing"

// The level words and the boundary rule are shared with compact and spawn:
// `cutoff` is not `off`, a word inside a line is not a footer, and only the
// last bullet line counts.
func TestThinkingFooterLevel(t *testing.T) {
	for _, tc := range []struct {
		name, screen, want string
	}{
		{"bullet and colon", "model\n• thinking: high\n", "high"},
		{"low", "model • low\n", "low"},
		{"cutoff is not off", "[compaction] Compacted\n• cutoff\n", ""},
		{"word inside a line is not a footer", "thinking high here\n", ""},
		{"earlier bullet is history", "old • high\nmodel • unavailable\n", ""},
		{"no bullet", "model\n", ""},
		{"cut mid draw", "model • hig\n", ""},
		{"CRLF", "model\r\n• medium\r\n", "medium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ThinkingFooterLevel(tc.screen); got != tc.want {
				t.Fatalf("ThinkingFooterLevel(%q)=%q want %q", tc.screen, got, tc.want)
			}
		})
	}
}
