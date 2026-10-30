package provider

import "testing"

// piCompactingScreen is assembled from pi 1.4.0 source (see
// .soho/w6X/reports/build-6-20261029T212047.md §4): the input box top border
// with the threshold auto-compaction label (status-indicator.ts:88-97), the
// footer with the (auto) setting token (footer.ts:200-204), and the
// post-compaction summary line (compaction-summary-message.ts). It is not a
// real captured screen.
const piCompactingScreen = `> Run the failing suite and fix it
  ⏵ 81% 138k/200k  (auto)

────────────────────────────────────────────────────────────
[compaction] Compacted from 96,412 tokens (ctrl+o to expand)
────────────────────────────────────────────────────────────

── ⠸ Auto-compacting... (escape to cancel) ─────────────────────────
────────────────────────────────────────────────────────────────────
`

func TestPiCompactingBorder(t *testing.T) {
	compacting := map[string]string{
		"the 1.4.0 threshold screen from the research report": piCompactingScreen,
		"the overflow variant label": "── ⠸ Context overflow detected, Auto-compacting... (escape to cancel) ───────\n" +
			"────────────────────────────────────────────────────────────────────────────────────────\n",
		"the manual label": "── ⠸ Compacting context... (escape to cancel) ──────────────────────────────────\n" +
			"────────────────────────────────────────────────────────────────────────────────────────\n",
		"another spinner frame":                  "── ⠋ Auto-compacting... (escape to cancel) ────────────────────────────────\n",
		"a narrow border with a truncated label": "── ⠴ Auto-compacting... ───────\n──────────────────────────────\n",
	}
	for name, screen := range compacting {
		t.Run(name, func(t *testing.T) {
			if !PiCompactingBorder(screen) {
				t.Fatalf("screen=%q", screen)
			}
		})
	}
	plain := map[string]string{
		"the working border": "── ⠴ Working ────────────────────────────────────────────────────────────────────────\n" +
			"────────────────────────────────────────────────────────────────────────────────────────────────────\n",
		"the idle all-dash borders": "────────────────────────────────────────────────────────────────────────────────\n" +
			"────────────────────────────────────────────────────────────────────────────────────────────────────────\n",
		"the footer with the (auto) setting token": "  ⏵ 81% 138k/200k  (auto)  [esc] interrupt  [1m] 1 queued  [alt+↑] 2\n",
		"the post-compaction summary line":         "[compaction] Compacted from 96,412 tokens (ctrl+o to expand)\n",
		"prose quoting the label":                  "pi said Auto-compacting... in the log line\n",
		"an empty screen":                          "",
	}
	for name, screen := range plain {
		t.Run(name, func(t *testing.T) {
			if PiCompactingBorder(screen) {
				t.Fatalf("screen=%q", screen)
			}
		})
	}
	// The label must match through the ellipsis only, so that neither the
	// summary wording nor a bare "Auto-compacting" prefix without the
	// ellipsis counts as the compaction label.
	for _, mutated := range []string{
		"── ⠸ Auto-compacting (escape to cancel) ───────────────────────────────────\n",
	} {
		if PiCompactingBorder(mutated) {
			t.Fatalf("label without the ellipsis matched: %q", mutated)
		}
	}
}
