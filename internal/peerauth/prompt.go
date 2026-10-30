package peerauth

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	openMarker = "[soho:peer] #"
	endMarker  = "[/soho:peer] #"
	endSuffix  = " end of message"
	// idLen is the size of a message id: 4 random bytes in hex.
	idLen = 8
	// sentSep and onSep take the opening line apart: the header reads
	// `<ref>, sent <time>.` and the remote one `<pane> on <host>, sent <time>.`.
	sentSep = ", sent "
	onSep   = " on "
)

// Header is one peer message found in a prompt.
type Header struct {
	ID string
	// Text is the message from its opening line to the first closing line
	// after it, joined with LF; "" when Closed is false.
	Text   string
	Closed bool
	// Ambiguous reports that the framing does not say where the message
	// ends: another closing line for the same id after the one that closed
	// it, or another opening line with the same id. The text past the first
	// closing line is outside what a signature covers.
	Ambiguous bool
	// Remote reports the remote opening (` on <host>, sent `).
	Remote bool
	// Sent is the opening line's send time as written ("" when there is none).
	Sent string
}

// Chars is the number of characters of the message text.
func (h Header) Chars() int { return utf8.RuneCountInString(h.Text) }

// FindHeaders returns every header in the prompt, in prompt order. Only the
// lines matter, so the wrapper Claude Code puts around a paste is not
// interpreted. A header is a line that, with its left whitespace gone, starts
// with `[soho:peer] #<8 hex>` followed by a character that is not hex; a line
// that starts with `>` is quoted text and never counts. The message runs to
// the first closing line `[/soho:peer] #<same id> end of message` after it; a
// header with no closing line is not Closed. A header whose id has a second
// closing line after the first, or a second opening line anywhere in the
// prompt, is Ambiguous.
func FindHeaders(prompt string) []Header {
	lines := strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")
	var out []Header
	// One pass indexes the opening lines per id and the closing lines per id,
	// so a prompt of many lines is not scanned once per header.
	openings := map[string]int{}
	closers := map[string][]int{}
	for i, line := range lines {
		if id, ok := openingID(line); ok {
			openings[id]++
		}
		if id, ok := closingID(line); ok {
			closers[id] = append(closers[id], i)
		}
	}
	for i, line := range lines {
		id, ok := openingID(line)
		if !ok {
			continue
		}
		h := Header{ID: id, Ambiguous: openings[id] > 1}
		after := closers[id]
		after = after[sort.SearchInts(after, i+1):]
		if len(after) > 0 {
			h.Closed = true
			h.Text = strings.Join(lines[i:after[0]+1], "\n")
			h.Ambiguous = h.Ambiguous || len(after) > 1
		}
		h.Remote, h.Sent = openingShape(line)
		out = append(out, h)
	}
	return out
}

// closingID is the id of a closing line `[/soho:peer] #<id> end of message`,
// with the whitespace around the line gone.
func closingID(line string) (string, bool) {
	rest := strings.TrimSpace(line)
	if !strings.HasPrefix(rest, endMarker) {
		return "", false
	}
	rest = rest[len(endMarker):]
	if len(rest) != idLen+len(endSuffix) || rest[idLen:] != endSuffix {
		return "", false
	}
	for i := 0; i < idLen; i++ {
		if !isHex(rest[i]) {
			return "", false
		}
	}
	return rest[:idLen], true
}

func openingID(line string) (string, bool) {
	rest := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(rest, openMarker) {
		return "", false
	}
	rest = rest[len(openMarker):]
	if len(rest) < idLen {
		return "", false
	}
	for i := 0; i < idLen; i++ {
		if !isHex(rest[i]) {
			return "", false
		}
	}
	if len(rest) > idLen && isHex(rest[idLen]) {
		return "", false
	}
	return rest[:idLen], true
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// openingShape reads the opening line's format and send time: the part before
// the last `, sent ` names the sender (remote when it holds ` on `), the part
// after it, up to the closing period, is the time.
func openingShape(line string) (remote bool, sent string) {
	at := strings.LastIndex(line, sentSep)
	if at < 0 {
		return false, ""
	}
	who := line[:at]
	if i := strings.LastIndex(who, onSep); i >= 0 && strings.TrimSpace(who[i+len(onSep):]) != "" {
		remote = true
	}
	sent = strings.TrimSuffix(strings.TrimRight(line[at+len(sentSep):], " \t"), ".")
	return remote, sent
}
