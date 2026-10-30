package provider

import "strings"

// Pi's 1.4.0 TUI renders the active compaction as a status inside the input
// box's top border (custom-editor.ts:74-79), and the input box is the last
// thing on the screen. The working border is "── ⠋ Working ────", while a
// compaction replaces the status with one of these labels (status-indicator.ts:88-97,
// keybindings.ts:26 for the escape hint):
//
//	manual:   Compacting context... (escape to cancel)
//	threshold: Auto-compacting... (escape to cancel)
//	overflow: Context overflow detected, Auto-compacting... (escape to cancel)
//
// The spinner frame advances every 80ms (loader.ts:13-14), so the frame
// character must never be part of a match. The post-compaction summary line
// "[compaction] Compacted from N tokens" (compaction-summary-message.ts)
// carries neither label and must not match.
//
// The line is recognized by shape, not by isPiBorderLine: the embedded label
// dilutes the dash density, so at pane widths below roughly 82 columns the
// line has fewer than half box-drawing runes and isPiBorderLine rejects it,
// and pi's border builder can close a truncated label with a single dash.
// A line is the compacting border when it starts and ends with a box
// drawing character and carries one of the two labels. Transcript prose and
// the footer never start and end with box drawing dashes; the footer's
// " (auto)" token is a setting indicator, not a compaction state.
const (
	piManualCompactingLabel = "Compacting context..."
	piAutoCompactingLabel   = "Auto-compacting..."
)

// PiCompactingBorder reports whether the screen shows pi compacting its
// context: its input box top border carries one of the compaction labels
// with any spinner frame, including the context-overflow variant and narrow
// borders where the label is truncated.
func PiCompactingBorder(screen string) bool {
	for _, line := range strings.Split(screen, "\n") {
		content := strings.TrimFunc(line, isJSWhitespace)
		if content == "" {
			continue
		}
		if !strings.HasPrefix(content, "─") || !strings.HasSuffix(content, "─") {
			continue
		}
		if strings.Contains(content, piAutoCompactingLabel) || strings.Contains(content, piManualCompactingLabel) {
			return true
		}
	}
	return false
}
