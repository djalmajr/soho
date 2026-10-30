package peer_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestSendClaudeReceiptNeedsAUserRootRecord: a fresh assistant record that
// quotes the closing line and nests a user-typed object does not prove
// delivery; a genuine user record does.
func TestSendClaudeReceiptNeedsAUserRootRecord(t *testing.T) {
	for _, shape := range []string{"user-control", "assistant-with-nested-user-type"} {
		t.Run(shape, func(t *testing.T) {
			id, prompt := stalledID(t)
			const cwd = "/tmp/soho-claude-record-role"
			root := t.TempDir()
			path := claudeTranscriptPath(t, root, cwd)
			writeClaudeTranscript(t, path, transcriptUserLine(t, "earlier conversation")+"\n")
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "history without any peer marker\n"},
				{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
			}
			f := newFixture(t, rules)
			f.env["CLAUDE_CONFIG_DIR"] = root
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			record := transcriptUserLine(t, prompt)
			if shape != "user-control" {
				raw, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": peer.PeerEndLine(id)}, map[string]any{"type": "tool_use", "id": "fixture", "name": "fixture", "input": map[string]any{"type": "user"}}}}})
				if err != nil {
					t.Fatal(err)
				}
				record = string(raw)
			}
			done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), path, id, record)
			code, out, _ := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
			if !<-done {
				t.Fatal("record did not append after actual prompt")
			}
			enters := countSendKeyEnters(t, f)
			t.Logf("shape=%s exit=%d stdout=%q extra_enter=%d", shape, code, out, enters)
			if shape == "user-control" {
				if code != 0 {
					t.Error("positive user control failed")
				}
			} else if code != 15 || enters != 0 {
				t.Error("non-user transcript record was used as proof of delivery")
			}
		})
	}
}
