package peer

import (
	"strings"
	"testing"
)

// TestCodexComposerStillReceivesTheHeader is the regression proof of the
// review finding, updated for the part B format: the header is at most five
// lines (the send time has its own Sent line), and on a codex composer screen
// the opening line must stay among the last 8 non-empty lines, or
// codexComposerLine does not recognize the composer and idInInputBox loses
// the typed header, so the single recovery Enter is skipped and the send
// exits. Without the two status footer lines the five header lines plus the
// one-line quoted body and the closing line still sit inside the window;
// with them the opening line leaves the window and the composer is not
// recognized (the provider's 8-line window is fixed; the header is the part
// B one).
func TestCodexComposerStillReceivesTheHeader(t *testing.T) {
	id := "01020304"
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"local header with a Sender line", PeerHeader("local/w14:p1", "orchestrator-2", "claude", "-", id)},
		{"the longest header: remote with hostname and all four Sender fields", PeerHeaderRemoteFor(PeerSender{Name: "orchestrator-2", Kind: "claude", Role: "sub-orchestrator", Model: "claude-opus-5"}, "w14:p1", "Run2Biz.local", id)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(tc.header, "\n")
			if got := len(lines); got > 5 {
				t.Fatalf("the header has %d lines; want at most 5 (opening, Sender, Sent, reply, follows)", got)
			}
			screen := "› " + lines[0]
			for _, line := range lines[1:] {
				screen += "\n  " + line
			}
			screen += "\n\n> hello\n" + PeerEndLine(id) + "\n"
			if !idInInputBox("codex", screen, id) {
				t.Fatalf("idInInputBox lost the header opening line in the codex composer without the status footer:\n%s", screen)
			}
			// With the two status footer lines the opening line leaves the
			// 8-line window: the composer is not recognized and the Enter is
			// not licensed (the window is the provider's; the header is the
			// part B one).
			if idInInputBox("codex", screen+d15cStatus, id) {
				t.Fatalf("the five-line header with the status footer still recognizes the composer:\n%s", screen+d15cStatus)
			}
		})
	}
}
