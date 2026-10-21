package provider

import (
	"reflect"
	"strings"
	"testing"
)

// The reported working-Claude screen, with neutral session and sender names.
const claudeLabeledScreen = `❯ [soho:peer] #01020304 Message from another agent — w16:p1 on
  Example, sent 2026-10-21T02:14:38Z.
  Sender: kind claude; from example-win; sig v1:c2589db12c3f6c89f8f601ea6828ea33.
  The message follows, each line quoted with "> ".

  > sample message

  [/soho:peer] #01020304 end of message

⏺ Example response…

· Booping… (51s · ↓ 3.5k tokens · thought for 3s)
                         ✔ Update installed · Restart to update
────────────────────────────────────────────────────────── Session ─
❯
─────────────────────────────────────────────────────────────────
  [Opus 5.5] █░░░░░░░░░ 10% [5h:6% 2h4m] [7d:9% 6d9h] [main]
  ⏵⏵ auto mode on (shift+tab to cycle) · ← 3 agents
`

func TestClaudeLabeledBorderBounds(t *testing.T) {
	typed := "❯ [soho:peer] #01020304 Message from another agent\n  > sample message\n  [/soho:peer] #01020304 end of message"
	for _, tc := range []struct {
		name, screen string
		want         []string
	}{
		{"empty", claudeLabeledScreen, []string{"❯"}},
		{"typed", strings.Replace(claudeLabeledScreen, "\n❯\n", "\n"+typed+"\n", 1), strings.Split(typed, "\n")},
		{"plain-border", strings.Replace(claudeLabeledScreen, " Session ", "─", 1), []string{"❯"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := ComposerRegionBounds("claude", tc.screen)
			if !ok {
				t.Fatalf("composer not recognized: %s", tc.screen)
			}
			lines := strings.Split(tc.screen, "\n")
			if start < 0 || end < start || end > len(lines) {
				t.Fatalf("invalid bounds=[%d,%d) for %d lines", start, end, len(lines))
			}
			if !reflect.DeepEqual(lines[start:end], tc.want) {
				t.Fatalf("bounds=[%d,%d) region=%q want=%q", start, end, lines[start:end], tc.want)
			}
		})
	}
}

func TestClaudeLabeledBorderValidation(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       bool
	}{
		{"prose", "some prose with ─ in the middle", false},
		{"short-single-prefix", "─ abc ─", false},
		{"minority-dashes", "── long session label ─", false},
		{"single-prefix-majority", "─ label ───────────────", false},
		{"missing-final-dash", "──────── Session", false},
		{"rune-half", "──名名名─", true},
		{"rune-below-half", "──名名名名─", false},
		{"whitespace", " \t──── Session ────── \t", true},
		{"plain", "──────", true},
		{"single-plain", "─", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isClaudeBorderLine(tc.line); got != tc.want {
				t.Fatalf("line=%q got=%t want=%t", tc.line, got, tc.want)
			}
		})
	}
	for _, screen := range []string{"──── Session ──────\nnot a composer\n──────\n", "──── Session ──────\n❯\n──────\n❯ another draft\n", "──── Session ──────\n❯\n──────\n" + strings.Repeat("footer\n", 11)} {
		if _, _, ok := ComposerRegionBounds("claude", screen); ok {
			t.Fatalf("composer guard/window bypassed: %q", screen)
		}
	}
}
