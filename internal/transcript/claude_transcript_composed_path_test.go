package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The dispatch arrival proof uses CountClaudeUserMarkerLines with the
// composed prompt path as the marker (not a peer #id). These tests exercise
// that use of the function, with the path the way a Claude Code session
// transcript writes it inside a JSON string: a Windows separator is written
// as two backslashes and a quote escaped, while a Unix path comes through
// unchanged.

func writeTranscriptLine(t *testing.T, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCountClaudeUserMarkerLinesWithComposedPath(t *testing.T) {
	unixPath := "/var/folders/herdr-soho/w14/reports/build-2-20261001T164144.brief.md"
	windowsRaw := `C:\herdr-soho\w14\reports\build-2-20261001T164144.brief.md`
	windowsJSON := `C:\\herdr-soho\\w14\\reports\\build-2-20261001T164144.brief.md`
	prompt := "Read the file %s in full and execute it. It amends the brief you are working on."

	t.Run("a unix composed path inside a user line counts", func(t *testing.T) {
		line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + fmt.Sprintf(prompt, unixPath) + `"}]}}`
		path := writeTranscriptLine(t, line)
		if n, ok := CountClaudeUserMarkerLines(path, unixPath); !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a windows path escaped the way JSON writes it counts", func(t *testing.T) {
		line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + fmt.Sprintf(prompt, windowsJSON) + `"}]}}`
		path := writeTranscriptLine(t, line)
		if n, ok := CountClaudeUserMarkerLines(path, windowsJSON); !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("the unescaped windows path does not match the escaped transcript", func(t *testing.T) {
		line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + fmt.Sprintf(prompt, windowsJSON) + `"}]}}`
		path := writeTranscriptLine(t, line)
		if n, ok := CountClaudeUserMarkerLines(path, windowsRaw); !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("a quote in the path is escaped the way JSON writes it", func(t *testing.T) {
		raw := `C:\Users\x\re"port.md`
		escaped := `C:\\Users\\x\\re\"port.md`
		line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + fmt.Sprintf(prompt, escaped) + `"}]}}`
		path := writeTranscriptLine(t, line)
		if n, ok := CountClaudeUserMarkerLines(path, escaped); !ok || n != 1 {
			t.Fatalf("escaped count=(%d,%v) want (1,true)", n, ok)
		}
		if n, ok := CountClaudeUserMarkerLines(path, raw); !ok || n != 0 {
			t.Fatalf("raw count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("an assistant line citing the path does not count", func(t *testing.T) {
		line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + fmt.Sprintf(prompt, unixPath) + `"}]}}`
		path := writeTranscriptLine(t, line)
		if n, ok := CountClaudeUserMarkerLines(path, unixPath); !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("a system line holding the path does not count", func(t *testing.T) {
		line := `{"type":"system","subtype":"file_history_snapshot","content":"` + unixPath + `"}`
		path := writeTranscriptLine(t, line)
		if n, ok := CountClaudeUserMarkerLines(path, unixPath); !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
}
