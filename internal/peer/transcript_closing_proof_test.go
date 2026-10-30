package peer_test

import (
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestTranscriptProofNeedsTheCurrentClosingLine covers the DJA-610 part B
// review F2: the transcript delivery proof requires the exact unquoted
// current closing line of this message's id. A user record that only holds
// the opening tag — or the opening plus the previous format's closing line,
// or the opening plus a quoted current closing line — is not delivery, while
// the complete message (the whole prompt with its closing line) still
// proves it.
func TestTranscriptProofNeedsTheCurrentClosingLine(t *testing.T) {
	for _, shape := range []string{"complete", "opening-only", "opening-and-previous-closing", "opening-and-quoted-current-closing", "opening-and-glued-current-closing"} {
		t.Run(shape, func(t *testing.T) {
			id, prompt := stalledID(t)
			const cwd = "/tmp/review-compact-transcript"
			root := t.TempDir()
			transcript := claudeTranscriptPath(t, root, cwd)
			writeClaudeTranscript(t, transcript, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "history without any peer marker\n"},
				{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
			}
			f := newFixture(t, rules)
			f.env["CLAUDE_CONFIG_DIR"] = root
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			observed := prompt
			switch shape {
			case "opening-only":
				observed = peer.PeerOpenTag(id)
			case "opening-and-previous-closing":
				observed = peer.PeerOpenTag(id) + "\n[/soho:peer] #" + id + " end of message"
			case "opening-and-quoted-current-closing":
				observed = peer.PeerOpenTag(id) + "\n> " + peer.PeerEndLine(id)
			case "opening-and-glued-current-closing":
				observed = peer.PeerOpenTag(id) + "\nstatus ok." + peer.PeerEndLine(id)
			}
			done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcript, id, transcriptUserLine(t, observed))
			code, out, _ := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
			if !<-done {
				t.Fatal("fixture did not append its transcript record after the actual prompt")
			}
			t.Logf("shape=%s exit=%d stdout=%q extra_enter=%d", shape, code, out, countSendKeyEnters(t, f))
			if shape == "complete" {
				if code != 0 {
					t.Fatal("complete-message control failed")
				}
			} else if code == 0 {
				t.Error("sender claims delivery without an unquoted current-format closing line")
			}
		})
	}
}
