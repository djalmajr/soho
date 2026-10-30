package peer

import (
	"strings"
	"testing"
)

// piUnitScreen models the real pi 0.99.2 visible screen: history above, the
// input box between the last two '─' (U+2500) lines (box may be empty), and
// the footer (cwd and token stats) below.
func piUnitScreen(history, box string) string {
	sep := strings.Repeat("─", 66)
	foot := "~/code/herdr-soho (fix/send-pi-input-box...)\n↑5.2M ↓43k 40.3%/262k (auto)           (lbvllm) qwen3.8-27b • high\n"
	return history + "\n\n" + sep + "\n" + box + "\n" + sep + "\n" + foot
}

func TestPiInputRegion(t *testing.T) {
	sep := strings.Repeat("─", 66)
	t.Run("the lines between the last two separator lines are the box", func(t *testing.T) {
		lines, ok := piInputRegion("history\n" + sep + "\nboxed\n" + sep + "\nfooter\n")
		if !ok || len(lines) != 1 || lines[0] != "boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("a separator with surrounding spaces still counts", func(t *testing.T) {
		lines, ok := piInputRegion(sep + "  \nboxed\n\t" + sep + "\n")
		if !ok || len(lines) != 1 || lines[0] != "boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("the real idle screen has an empty box", func(t *testing.T) {
		lines, ok := piInputRegion(piUnitScreen("history", ""))
		if !ok || len(lines) != 1 || lines[0] != "" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("fewer than two separator lines: no region", func(t *testing.T) {
		if _, ok := piInputRegion("history\n" + sep + "\nboxed\nfooter\n"); ok {
			t.Fatal("one separator line must not form a region")
		}
		if _, ok := piInputRegion("history\nboxed\nfooter\n"); ok {
			t.Fatal("no separator line must not form a region")
		}
	})
	t.Run("separator-looking lines above still bound the box with the last two", func(t *testing.T) {
		lines, ok := piInputRegion(sep + "\nhr in history\n" + sep + "\nboxed\n" + sep + "\nfooter\n")
		if !ok || len(lines) != 1 || lines[0] != "boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("a mixed line is not a separator", func(t *testing.T) {
		// The mixed line sits between the two real separators: if it were a
		// separator too, the last two borders would shrink the region to it.
		lines, ok := piInputRegion(sep + "\n─ x\n" + sep + "\n")
		if !ok || len(lines) != 1 || lines[0] != "─ x" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
		if _, ok := piInputRegion(sep + "\nboxed\n" + sep + "x\n"); ok {
			t.Fatal("a trailing non-'─' rune must not count as a separator")
		}
	})
	t.Run("CRLF line endings", func(t *testing.T) {
		lines, ok := piInputRegion("history\r\n" + sep + "\r\nboxed\r\n" + sep + "\r\nfooter\r\n")
		if !ok || len(lines) != 1 || lines[0] != "boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
}

func TestPiSendScreenMarkers(t *testing.T) {
	id := "01020304"
	endLine := PeerEndLine(id)
	sep := strings.Repeat("─", 66)
	// The marker sits in the history (above the top border), the box below.
	screen := func(box string) string {
		return "history " + endLine + " and #" + id + " above\n" + sep + "\n" + box + "\n" + sep + "\n" + "footer\n"
	}
	t.Run("pi: a marker only in the history is not still in the screen or the box", func(t *testing.T) {
		if messageStillInScreen("pi", screen(""), endLine, id) {
			t.Fatal("the delivered message in the history must not count as still on screen")
		}
		if idInInputBox("pi", screen(""), id) {
			t.Fatal("the empty box must not hold the id")
		}
	})
	t.Run("pi: the opening tag or the closing line in the box blocks the screen proof; only the header licenses the Enter", func(t *testing.T) {
		if !messageStillInScreen("pi", screen(PeerOpenTag(id)), endLine, id) {
			t.Fatal("the opening tag in the box must block the screen proof")
		}
		if !messageStillInScreen("pi", screen(endLine), endLine, id) {
			t.Fatal("the end line in the box must block the screen proof")
		}
		// A bare #id without the marker is neither the marker (it no longer
		// blocks the proof) nor permission to press Enter; the opening tag
		// of this message licenses it.
		if messageStillInScreen("pi", screen("#"+id), endLine, id) {
			t.Fatal("a bare #id in the box is not the part B marker")
		}
		if idInInputBox("pi", screen("#"+id), id) {
			t.Fatal("a bare #id without the header must not license the Enter")
		}
		if !idInInputBox("pi", screen(PeerOpenTag(id)), id) {
			t.Fatal("the header in the box must license the Enter")
		}
		if idInInputBox("pi", screen("> "+PeerOpenTag(id)), id) {
			t.Fatal("a quoted header in the box is payload, not the typed header")
		}
	})
	t.Run("pi without borders keeps the whole-screen screen proof but presses no Enter", func(t *testing.T) {
		noBorders := "history " + endLine + " and #" + id + "\n"
		if !messageStillInScreen("pi", noBorders, endLine, id) {
			t.Fatal("without two borders the whole visible screen still counts")
		}
		if idInInputBox("pi", noBorders, id) {
			t.Fatal("without two borders there is no recognized composer: no Enter")
		}
	})
	t.Run("claude reads the box between its borders; without borders no Enter goes", func(t *testing.T) {
		// A claude with two borders has a box between them, like pi: the
		// marker in the history above the top border is taken, not held, so
		// it does not block the proof — the whole-screen expectation no
		// longer applies to it.
		if messageStillInScreen("claude", screen("❯"), endLine, id) {
			t.Fatal("a claude marker above the box borders must not block the proof")
		}
		// The marker is on screen but above the last 15 lines: no Enter.
		pushedDown := screen("") + strings.Repeat("chrome\n", 15)
		if idInInputBox("claude", pushedDown, id) {
			t.Fatal("a claude screen without the recognized box presses no Enter")
		}
		// An unrecognized screen is not a composer: the marker alone is not
		// permission to press Enter.
		if idInInputBox("claude", "tail #"+id+"\n", id) {
			t.Fatal("an unrecognized screen is not a composer: no Enter")
		}
		// A claude without borders stays conservative for the screen proof,
		// but there is no recognized composer to license the Enter.
		pushedNoBorders := "history " + endLine + " and #" + id + "\n" + strings.Repeat("chrome\n", 15)
		if idInInputBox("claude", pushedNoBorders, id) {
			t.Fatal("without borders there is no recognized composer: no Enter")
		}
	})
}
