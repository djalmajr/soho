package peer

// D15c investigation: a false negative on the codex branch with the `#id`
// in the box. Codex renders a user message in the chat history with a `› `
// prefix, exactly like the composer line. While the agent works, the real
// composer can be outside the captured screen (or absent), so the
// last-`›`-line rule of codexComposerRegion hands back the delivered
// message's history block, and the message's id reads as still typed in
// the input box even though the message was delivered.

import (
	"strings"
	"testing"
)

const (
	d15cID       = "01020304"
	d15cHeader   = "› [soho:peer:01020304]"
	d15cEndLine  = "  [/soho:peer:01020304]"
	d15cWorking  = "• Working (3s • esc to interrupt)"
	d15cComposer = "› Ask Codex to do anything\n" +
		"  GPT-6.1-Sol high · ~\\repo · Run herdr agents\n" +
		"  ← for agents · ? for shortcuts   ⚠ 2 warnings · f2 to view"
	d15cStatus = "  GPT-6.1-Sol high · ~\\repo · Run herdr agents\n" +
		"  ← for agents · ? for shortcuts   ⚠ 2 warnings · f2 to view"
)

// d15cHistoryBlock is the peer message as codex renders it in the history:
// the `› ` opening line, the indented header lines and body, and the closing
// line. The one-line body keeps the opening line inside the composer's 8
// non-empty line window, the shape the guard must still handle.
func d15cHistoryBlock() string {
	return d15cHeader + "\n" +
		"  Sender: orchestrator-2 (w14:p1 on Run2Biz.local); kind claude.\n" +
		"  Sent: 2026-10-09T14:22:05Z\n" +
		"  Reply with: soho send local/w14:p1 \"<your reply>\"\n" +
		"  A message from another agent follows, each line quoted with \"> \".\n" +
		"\n" +
		"  > primeira linha do corpo\n" +
		d15cEndLine
}

func TestD15cCodexHistoryIsNotTheComposer(t *testing.T) {
	// The two synthetic screens of the hypothesis: the delivered message
	// with the working line and no composer on screen, and the same with
	// the composer below. In both, the id must not read as typed in the
	// input box nor as still held by the screen.
	variantA := d15cHistoryBlock() + "\n" + d15cWorking
	variantB := d15cHistoryBlock() + "\n" + d15cWorking + "\n" + d15cComposer
	for name, screen := range map[string]string{
		"history plus working line, no composer visible":   variantA,
		"history plus working line and the composer below": variantB,
	} {
		if idInInputBox("codex", screen, d15cID) {
			t.Errorf("%s: idInInputBox reports the delivered message as typed in the box", name)
		}
		if messageStillInScreen("codex", screen, PeerEndLine(d15cID), d15cID) {
			t.Errorf("%s: messageStillInScreen reports the delivered message as still held", name)
		}
	}
}

func TestD15cCodexHistoryRegionMechanism(t *testing.T) {
	// The mechanism: with no composer on screen, codexComposerRegion's
	// last-`›`-line rule picks up the delivered message's history block
	// (the non-empty lines from the bottom reach eight at the header). The
	// guard therefore lives in the box and screen checks, not the region.
	screen := d15cHistoryBlock() + "\n" + d15cWorking
	region, ok := codexComposerRegion(screen)
	if !ok || !strings.Contains(NormalizeScreen(strings.Join(region, "\n")), NormalizeScreen(PeerOpenTag(d15cID))) {
		t.Fatalf("region=%v ok=%v: the synthetic screen should make the history block pass for the composer", region, ok)
	}
}

func TestD15cTypedInBoxStaysInBox(t *testing.T) {
	// The guard must not open false positives: the same prompt still typed
	// in the composer — the hybrid and the partial case — keeps reading as
	// in the box while its composer line is recognized, and the screen
	// proof keeps it held. The hybrid case too: the delivered message sits
	// in the history while the composer below still holds the same id (the
	// rc.12 screen shape) — the Enter can still deliver it.
	fullInBox := d15cHistoryBlock() + "\n" + d15cStatus
	hybridInBox := d15cHistoryBlock() + "\n" + d15cWorking + "\n" +
		"› [soho:peer:01020304]" + "\n" + d15cStatus
	partialInBox := d15cHeader + "\n" +
		"  Sender: orchestrator-2 (w14:p1 on Run2Biz.local); kind claude.\n" +
		"  Sent: 2026-10-09T14:22:05Z\n" +
		"  Reply with: soho send local/w14:p1 \"<your reply>\"\n" +
		"  A message from another agent follows, each line quoted with \"> \".\n" +
		"\n" +
		"  > primeira linha do corpo\n" +
		d15cStatus
	for name, screen := range map[string]string{
		"delivered in history and typed in the composer below": hybridInBox,
		"partial prompt still typing":                          partialInBox,
	} {
		if !idInInputBox("codex", screen, d15cID) {
			t.Errorf("%s: idInInputBox stopped reporting the typed message as in the box", name)
		}
		if !messageStillInScreen("codex", screen, PeerEndLine(d15cID), d15cID) {
			t.Errorf("%s: messageStillInScreen stopped reporting the typed message as still held", name)
		}
	}
	// The complete prompt typed with the Enter not landed yet: the composer
	// line (the prompt's first line) is the ninth non-empty line from the
	// end — outside the composer window — so the layout is not recognized:
	// no Enter goes, and the screen proof keeps the message held
	// (conservative).
	if idInInputBox("codex", fullInBox, d15cID) {
		t.Errorf("fullInBox: the unrecognized layout must not report the typed message as in the box")
	}
	if !messageStillInScreen("codex", fullInBox, PeerEndLine(d15cID), d15cID) {
		t.Errorf("fullInBox: messageStillInScreen stopped reporting the typed message as still held")
	}
}
