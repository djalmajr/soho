package peer

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/peerauth"
)

// refusalResultRE is the result a refusal line names, as it reads once the
// screen's spaces are gone: `refused-bad-signature`.
var refusalResultRE = regexp.MustCompile(`^refused(?:-[a-z]+)+`)

// maxTranscriptScan bounds how much of the end of a session transcript the
// refusal search reads.
const maxTranscriptScan = 8 << 20

// refusalLineIn reports whether the line is the receiver's refusal of this
// message: a line that, normalized as receiptLineIn normalizes, and not
// quoted, starts with `[soho:peer-refused] #<id>` followed by something that
// is not hex. result is the refusal's name ("refused" when the line names
// none). Another message's id is not this refusal.
func refusalLineIn(line, id string) (result string, ok bool) {
	norm := NormalizeScreen(line)
	if strings.HasPrefix(norm, ">") {
		return "", false
	}
	rest := strings.TrimPrefix(norm, "›")
	open := strings.ReplaceAll(peerauth.RefusalMarker, " ", "") + id
	if !strings.HasPrefix(rest, open) {
		return "", false
	}
	after := rest[len(open):]
	if after != "" && isHexRune(rune(after[0])) {
		return "", false
	}
	if named := refusalResultRE.FindString(after); named != "" {
		return named, true
	}
	return "refused", true
}

// refusalIn searches a screen or a recent read for the refusal of this
// message.
func refusalIn(text, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if result, ok := refusalLineIn(line, id); ok {
			return result, true
		}
	}
	return "", false
}

// refusalInTranscript searches the end of a Claude Code session transcript for
// the `system` entry the receiver's hook block leaves there: any string of the
// entry that holds the refusal line.
func refusalInTranscript(path, id string) (string, bool) {
	if path == "" || id == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > maxTranscriptScan {
		if _, err := f.Seek(info.Size()-maxTranscriptScan, io.SeekStart); err != nil {
			return "", false
		}
	}
	reader := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := reader.ReadString('\n')
		if strings.Contains(line, "soho:peer-refused") && strings.Contains(line, id) {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) == nil && entry["type"] == "system" {
				if result, ok := refusalInValue(entry, id); ok {
					return result, true
				}
			}
		}
		if err != nil {
			return "", false
		}
	}
}

func refusalInValue(v any, id string) (string, bool) {
	switch x := v.(type) {
	case string:
		return refusalIn(x, id)
	case map[string]any:
		for _, item := range x {
			if result, ok := refusalInValue(item, id); ok {
				return result, true
			}
		}
	case []any:
		for _, item := range x {
			if result, ok := refusalInValue(item, id); ok {
				return result, true
			}
		}
	}
	return "", false
}

// tooLargeLine reports whether the line is the receiver's refusal of a prompt
// that was too large: a line that, normalized as refusalLineIn normalizes, and
// not quoted, starts with `[soho:peer-refused] prompt-too-large`. The hook
// cannot name the message (it never parsed it), so the line has no id and
// stands for no message in particular.
func tooLargeLine(line string) bool {
	norm := NormalizeScreen(line)
	if strings.HasPrefix(norm, ">") {
		return false
	}
	return strings.HasPrefix(strings.TrimPrefix(norm, "›"), strings.ReplaceAll(peerauth.TooLargeLine, " ", ""))
}

// countTooLarge is how many lines of a screen or a recent read are the
// too-large refusal. A refusal is this send's only when a read after the
// prompt holds more of them than the visible screen before it.
func countTooLarge(text string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if tooLargeLine(line) {
			n++
		}
	}
	return n
}

// transcriptEnd is the size of the transcript now: the point after which a
// too-large refusal in it is new. ok is false when the file cannot be read.
func transcriptEnd(path string) (int64, bool) {
	if path == "" {
		return 0, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// tooLargeInTranscript reports whether the transcript holds, after the byte
// offset from, a `system` entry with the too-large refusal line. Entries
// before from are earlier refusals of earlier messages.
func tooLargeInTranscript(path string, from int64) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= from {
		return false
	}
	if info.Size()-from > maxTranscriptScan {
		from = info.Size() - maxTranscriptScan
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return false
	}
	reader := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := reader.ReadString('\n')
		if strings.Contains(line, "soho:peer-refused") && strings.Contains(line, "prompt-too-large") {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) == nil && entry["type"] == "system" && tooLargeInValue(entry) {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

func tooLargeInValue(v any) bool {
	switch x := v.(type) {
	case string:
		return countTooLarge(x) > 0
	case map[string]any:
		for _, item := range x {
			if tooLargeInValue(item) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if tooLargeInValue(item) {
				return true
			}
		}
	}
	return false
}
