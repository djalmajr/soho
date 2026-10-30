package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCountClaudeUserMarkerLinesNeedsAUserRootRecord: an assistant or tool
// record that nests a "type":"user" object is not a user record.
func TestCountClaudeUserMarkerLinesNeedsAUserRootRecord(t *testing.T) {
	const marker = "/tmp/review/brief.md"
	for _, role := range []string{"user", "assistant", "tool"} {
		t.Run(role, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"type": role, "message": map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": marker}, map[string]any{"type": "tool_use", "input": map[string]any{"type": "user"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			n, ok := CountClaudeUserMarkerLines(path, marker)
			want := 0
			if role == "user" {
				want = 1
			}
			t.Logf("role=%s valid_json=%t count=%d want=%d readable=%t", role, json.Valid(raw), n, want, ok)
			if !ok || n != want {
				t.Error("marker counter did not enforce root user identity")
			}
		})
	}
}
