package peer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/peerauth"
)

// refusalResultRE is the result a refusal line names, as it reads once the
// screen's spaces are gone: `refused-inbound-off`.
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
	rest := refusalText(line)
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
	return strings.HasPrefix(refusalText(line), strings.ReplaceAll(peerauth.TooLargeLine, " ", ""))
}

func refusalText(line string) string {
	norm := NormalizeScreen(line)
	if strings.HasPrefix(norm, ">") {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(norm, "›"), "Error:")
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

// transcriptBaseline is what the send knows of the transcript when it begins:
// which file it is, how long it is, the bytes that end it and when the send
// started. A refusal written after that is this send's; the file can be
// truncated or replaced meanwhile, and even grow back past its old length,
// which a byte offset alone would not survive.
type transcriptBaseline struct {
	info  os.FileInfo
	size  int64
	tail  []byte
	start time.Time
}

// baselineTailBytes is how much of the end of the transcript the baseline
// keeps to tell the same content from a file rewritten in place.
const baselineTailBytes = 4096

// tooLargeClockSkew is how much older than the start of the send an entry's
// timestamp may be and still count as new.
const tooLargeClockSkew = 2 * time.Second

// transcriptEnd takes the baseline of the transcript now. ok is false when the
// file cannot be read.
func transcriptEnd(path string, now time.Time) (transcriptBaseline, bool) {
	if path == "" {
		return transcriptBaseline{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return transcriptBaseline{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return transcriptBaseline{}, false
	}
	tail, ok := readTail(f, info.Size())
	if !ok {
		return transcriptBaseline{}, false
	}
	return transcriptBaseline{info: info, size: info.Size(), tail: tail, start: now}, true
}

// readTail reads the baselineTailBytes that end at offset end.
func readTail(f *os.File, end int64) ([]byte, bool) {
	from := end - baselineTailBytes
	if from < 0 {
		from = 0
	}
	buf := make([]byte, end-from)
	if len(buf) == 0 {
		return buf, true
	}
	if _, err := f.ReadAt(buf, from); err != nil {
		return nil, false
	}
	return buf, true
}

// tooLargeInTranscript reports whether the transcript holds a `system` entry
// with the too-large refusal line that is new since the baseline.
//   - The same file, as long as or longer than at the baseline, whose bytes
//     before the baseline's offset still end as they did: only the entries
//     after that offset count.
//   - Another file at the path, a shorter one, or one rewritten in place (the
//     baseline lost its validity): the end of the file, within
//     maxTranscriptScan, is read and an
//     entry counts when its timestamp is not older than the start of the send
//     minus tooLargeClockSkew. An entry whose timestamp is missing or not
//     readable counts too: it leans to the refusal, the safe side.
func tooLargeInTranscript(path string, base transcriptBaseline) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	from, byTime := base.size, false
	if !os.SameFile(base.info, info) || info.Size() < base.size {
		from, byTime = 0, true
	} else if tail, ok := readTail(f, base.size); !ok || !bytes.Equal(tail, base.tail) {
		from, byTime = 0, true
	}
	if info.Size() <= from {
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
			if json.Unmarshal([]byte(line), &entry) == nil && entry["type"] == "system" && tooLargeInValue(entry) &&
				(!byTime || entryNotBefore(entry, base.start.Add(-tooLargeClockSkew))) {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// entryNotBefore reports whether the entry's `timestamp` is at or after
// since; an entry with no readable timestamp is not before anything.
func entryNotBefore(entry map[string]any, since time.Time) bool {
	raw, _ := entry["timestamp"].(string)
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return true
	}
	return !ts.Before(since)
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
