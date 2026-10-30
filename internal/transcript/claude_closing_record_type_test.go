package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCountClaudeUserClosingLinesNeedsAUserRootRecord: an assistant or tool
// record that nests a "type":"user" object is not a user receipt.
func TestCountClaudeUserClosingLinesNeedsAUserRootRecord(t *testing.T) {
	const closing = "[/soho:peer:01020304]"
	for _, tc := range []struct {
		name   string
		record any
		want   int
	}{
		{"user-control", map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": closing}}, 1},
		{"assistant-control", map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": closing}}, 0},
		{"assistant-with-nested-user-type", map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": closing}, map[string]any{"type": "tool_use", "id": "fixture", "name": "fixture", "input": map[string]any{"type": "user"}}}}}, 0},
	} {
		t.Run("claude/"+tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.record)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			n, ok := CountClaudeUserClosingLines(path, closing)
			t.Logf("valid_json=%t count=%d expected=%d readable=%t", json.Valid(raw), n, tc.want, ok)
			if !ok || n != tc.want {
				t.Error("closing counter accepted a non-user record or rejected the positive control")
			}
		})
	}
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{"exact", closing, 1}, {"quoted", "> " + closing, 0}, {"glued", "fixture-prefix" + closing, 0}, {"opening", "[soho:peer:01020304]", 0}, {"previous", "[/soho:peer] #01020304 end of message", 0},
	} {
		t.Run("pi/"+tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"type": "message", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": tc.text}}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			n, ok := CountPiUserClosingLines(path, closing)
			t.Logf("valid_json=%t count=%d expected=%d readable=%t", json.Valid(raw), n, tc.want, ok)
			if !ok || n != tc.want {
				t.Error("pi closing-line boundary violated")
			}
		})
	}
}
