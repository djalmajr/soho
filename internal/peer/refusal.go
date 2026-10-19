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
