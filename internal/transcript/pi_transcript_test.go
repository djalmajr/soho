package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piSessionFile is the session file name the pi counter tests write under
// their temp dir, the same fixture name the herdr package's PiSessionPath
// tests use.
const piSessionFile = "2026-10-02T06-18-06-537Z_01a0fb43-4b49-70d7-8f62-5e7a10390e69.jsonl"

func TestCountPiUserMarkerLines(t *testing.T) {
	marker := "#01020304"
	userLine := `{"type":"message","id":"9a234373","parentId":"7dfce086","message":{"role":"user","content":[{"type":"text","text":"[herdr-soho:peer] ` + marker + ` end of message"}]}}`
	t.Run("only the user message lines that hold the marker count", func(t *testing.T) {
		// The assistant and toolResult lines model pi citing the id back in
		// its own reply or in a tool result: a marker without the user
		// message record is not delivery.
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		content := strings.Join([]string{
			`{"type":"message","id":"a","parentId":"0","message":{"role":"assistant","content":[{"type":"text","text":"did you mean ` + marker + `? I am still working"}]}}`,
			userLine,
			`{"type":"message","id":"b","parentId":"a","message":{"role":"toolResult","content":[{"type":"text","text":"the brief says ` + marker + ` but I have not started"}]}}`,
			`{"type":"message","id":"c","parentId":"b","message":{"role":"user","content":[{"type":"text","text":"an earlier message without the marker"}]}}`,
			`{"type":"message","id":"d","parentId":"c","message":{"role":"user","content":[{"type":"text","text":"[herdr-soho:peer] #99999999 end of message"}]}}`,
			userLine,
		}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 2 {
			t.Fatalf("count=(%d,%v) want (2,true)", n, ok)
		}
	})
	t.Run("an assistant line whose text quotes the user record marker does not count", func(t *testing.T) {
		// pi writes the text the way JSON.stringify does: the quotes of a
		// quoted `"role":"user"` are escaped, so the record marker never
		// appears as plain bytes in a non-user line.
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		content := `{"type":"message","id":"a","parentId":"0","message":{"role":"assistant","content":[{"type":"text","text":"the record looks like \"type\":\"message\" \"role\":\"user\" with ` + marker + `"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("a line that holds the user record and the marker twice still counts once", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		content := `{"type":"message","id":"a","parentId":"0","message":{"role":"user","content":[{"type":"text","text":"` + marker + ` ` + marker + `"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("an assistant line with a tool call whose arguments hold a nested user role and the marker does not count", func(t *testing.T) {
		// A tool call's arguments are the tool's own data and may quote the
		// user record (a nested "role":"user") and the marker; the role is
		// read from the message itself, so the line does not count. The file
		// also holds the real user line with the marker: the count comes only
		// from it.
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		assistantToolCall := `{"type":"message","id":"a","parentId":"0","message":{"role":"assistant","content":[{"type":"text","text":"running the tool"},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"herdr-soho send w0test:p0a hi","context":{"role":"user","content":[{"type":"text","text":"[herdr-soho:peer] ` + marker + ` nested in the arguments"}]}}}]}}`
		content := assistantToolCall + "\n" + userLine + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a matching line without a trailing newline still counts", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		content := `{"type":"message","id":"a","parentId":"0","message":{"role":"user","content":[{"type":"text","text":"earlier"}]}}` + "\n" +
			`{"type":"message","id":"b","parentId":"a","message":{"role":"user","content":[{"type":"text","text":"` + marker + `"}]}}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a line far past the default 64 KiB token still counts", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		long := `{"type":"message","id":"a","parentId":"0","message":{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("a", 200*1024) + marker + `"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(long), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("an empty file counts zero", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), piSessionFile)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("an unreadable file reports not ok with no count", func(t *testing.T) {
		n, ok := CountPiUserMarkerLines(filepath.Join(t.TempDir(), "missing.jsonl"), marker)
		if ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,false)", n, ok)
		}
	})
	t.Run("the function returns no line text: its result is only the number", func(t *testing.T) {
		// The signature is the contract (int, bool): no line content leaves
		// the function. Run it on a session whose matching lines hold
		// distinctive sentinel payloads and check only the count comes back.
		dir := t.TempDir()
		path := filepath.Join(dir, piSessionFile)
		content := strings.Join([]string{
			`{"type":"message","id":"a","parentId":"0","message":{"role":"user","content":[{"type":"text","text":"USER-SENTINEL-1 ` + marker + `"}]}}`,
			`{"type":"message","id":"b","parentId":"a","message":{"role":"user","content":[{"type":"text","text":"USER-SENTINEL-2 ` + marker + `"}]}}`,
			`{"type":"message","id":"c","parentId":"b","message":{"role":"assistant","content":[{"type":"text","text":"ASSISTANT-SENTINEL ` + marker + `"}]}}`,
		}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountPiUserMarkerLines(path, marker)
		if !ok || n != 2 {
			t.Fatalf("count=(%d,%v) want (2,true)", n, ok)
		}
	})
}

// TestCountPiUserClosingLines pins the receipt contract of the pi closing
// line counter (DJA-610 part B amendment, F2): a user message record counts
// only when it carries the message's exact closing line as one of its own
// lines. The role is read from the message itself, and the opening line
// alone, the previous format's closing line, a quoted line and a marker
// glued to other text are payload, not a receipt.
func TestCountPiUserClosingLines(t *testing.T) {
	const closing = "[/soho:peer:01020304]"
	cases := []struct {
		name   string
		record string
		want   int
	}{
		{"a user line with the complete prompt", `{"type":"message","id":"a","parentId":"0","message":{"role":"user","content":"line one\n` + closing + `\nlast line"}}`, 1},
		{"a user line holding the whole content", `{"type":"message","id":"b","parentId":"a","message":{"role":"user","content":"` + closing + `"}}`, 1},
		{"a user text block", `{"type":"message","id":"c","parentId":"b","message":{"role":"user","content":[{"type":"text","text":"x\n` + closing + `"}]}}`, 1},
		{"a user line with the opening line alone", `{"type":"message","id":"d","parentId":"c","message":{"role":"user","content":"[soho:peer:01020304]"}}`, 0},
		{"a user line with the previous format's closing line", `{"type":"message","id":"e","parentId":"d","message":{"role":"user","content":"[/soho:peer] #01020304 end of message"}}`, 0},
		{"a user line with a quoted closing line", `{"type":"message","id":"f","parentId":"e","message":{"role":"user","content":"> ` + closing + `"}}`, 0},
		{"a user line with a glued closing line", `{"type":"message","id":"g","parentId":"f","message":{"role":"user","content":"status ok."` + closing + `"}}`, 0},
		{"an assistant reply citing the closing line", `{"type":"message","id":"h","parentId":"g","message":{"role":"assistant","content":[{"type":"text","text":"done\n` + closing + `"}]}}`, 0},
		{"a tool result citing the closing line", `{"type":"message","id":"i","parentId":"h","message":{"role":"toolResult","content":[{"type":"text","text":"the prompt ends with ` + closing + `"}]}}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, piSessionFile)
			content := `{"type":"message","id":"0","message":{"role":"user","content":"earlier conversation"}}` + "\n" + c.record + "\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			n, ok := CountPiUserClosingLines(path, closing)
			if !ok || n != c.want {
				t.Fatalf("count=(%d,%v) want (%d,true)", n, ok, c.want)
			}
		})
	}
	t.Run("a record missing or unreadable", func(t *testing.T) {
		if n, ok := CountPiUserClosingLines(filepath.Join(t.TempDir(), piSessionFile), closing); ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,false)", n, ok)
		}
	})
}
