package peerauth

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// The previous format's markers: the id after the prefix, and the
	// closing line's fixed suffix. The receiver accepts that format for one
	// release, because a machine on the previous version keeps sending it.
	oldOpenMarker = "[soho:peer] #"
	oldEndMarker  = "[/soho:peer] #"
	oldEndSuffix  = " end of message"
	// The current format's markers: the id sits inside the tag, and the
	// closing line is the tag alone.
	newOpenMarker = "[soho:peer:"
	newEndMarker  = "[/soho:peer:"
	// idLen is the size of a message id: 4 random bytes in hex.
	idLen = 8
	// sentSep and onSep take the previous format's opening line apart: the
	// header reads `<ref>, sent <time>.` and the remote one
	// `<pane> on <host>, sent <time>.`
	sentSep = ", sent "
	onSep   = " on "
	// The current format's metadata lines, inside the header between the
	// opening line and the closing line. The body is quoted with "> ", so
	// neither prefix can hit a body line.
	sentLinePrefix   = "Sent: "
	senderLinePrefix = "Sender: "
)

// Header is one peer message found in a prompt.
type Header struct {
	ID string
	// Text is the message from its opening line to the first closing line
	// after it, joined with LF; "" when Closed is false.
	Text   string
	Closed bool
	// Ambiguous reports that the framing does not say where the message
	// ends: within its own format, another closing line for the same id
	// after the one that closed it, or another opening line with the same
	// id. The text past the first closing line is outside what the header
	// names.
	Ambiguous bool
	// Remote reports a remote opening (the previous format's ` on <host>,
	// sent ` or the current format's `<pane> on <host>` reference).
	Remote bool
	// Sent is the header's send time as written ("" when there is none).
	Sent string
}

// Chars is the number of characters of the message text.
func (h Header) Chars() int { return utf8.RuneCountInString(h.Text) }

// FindHeaders returns every header in the prompt, in prompt order. Only the
// lines matter, so the wrapper Claude Code puts around a paste is not
// interpreted. Two formats are accepted, and an opening line is paired only
// with a closing line of its own format: the current format is an opening
// line `[soho:peer:<8 hex>]` (the id inside the tag) closed by the line
// `[/soho:peer:<same id>]`; the previous format is an opening line
// `[soho:peer] #<8 hex>` followed by a character that is not hex, closed by
// the line `[/soho:peer] #<same id> end of message`. An old opening with a
// new closing of the same id is not one message: each is an unclosed header.
// A line that starts with `>` is quoted text and never counts. A header with
// no closing line of its format is not Closed. A header whose id has a
// second closing line of its format after the first, or a second opening
// line of its format anywhere in the prompt, is Ambiguous.
func FindHeaders(prompt string) []Header {
	lines := strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")
	var out []Header
	// One pass indexes the opening lines per id and format and the closing
	// lines per id and format, so a prompt of many lines is not scanned once
	// per header, and one format's opening never pairs with the other
	// format's closing of the same id.
	oldOpenings, newOpenings := map[string]int{}, map[string]int{}
	oldClosers, newClosers := map[string][]int{}, map[string][]int{}
	for i, line := range lines {
		if id, ok := oldOpeningID(line); ok {
			oldOpenings[id]++
		}
		if id, ok := newOpeningID(line); ok {
			newOpenings[id]++
		}
		if id, ok := oldClosingID(line); ok {
			oldClosers[id] = append(oldClosers[id], i)
		}
		if id, ok := newClosingID(line); ok {
			newClosers[id] = append(newClosers[id], i)
		}
	}
	for i, line := range lines {
		if id, ok := oldOpeningID(line); ok {
			out = append(out, headerFor(id, i, oldOpenings[id], oldClosers[id], lines, line, oldShape))
			continue
		}
		if id, ok := newOpeningID(line); ok {
			out = append(out, headerFor(id, i, newOpenings[id], newClosers[id], lines, line, newShape))
		}
	}
	return out
}

// headerFor builds the header for one opening line at index i, pairing it
// with the first closing line of the same format after it. shape reads the
// format's remote and send-time fields from the opening line and the header
// text (the previous format reads both from the opening line, the current
// one from the header text).
func headerFor(id string, i, openings int, closers []int, lines []string, line string, shape func(line, text string) (bool, string)) Header {
	h := Header{ID: id, Ambiguous: openings > 1}
	after := closers[sort.SearchInts(closers, i+1):]
	if len(after) > 0 {
		h.Closed = true
		h.Text = strings.Join(lines[i:after[0]+1], "\n")
		h.Ambiguous = h.Ambiguous || len(after) > 1
	}
	h.Remote, h.Sent = shape(line, h.Text)
	return h
}

// oldShape reads the previous format's fields from the opening line.
func oldShape(line, text string) (bool, string) { return openingShape(line) }

// newShape reads the current format's fields from the header text.
func newShape(line, text string) (bool, string) { return newHeaderShape(text) }

// oldClosingID is the id of a previous-format closing line
// `[/soho:peer] #<id> end of message`, with the whitespace around the line
// gone.
func oldClosingID(line string) (string, bool) {
	rest := strings.TrimSpace(line)
	if !strings.HasPrefix(rest, oldEndMarker) {
		return "", false
	}
	rest = rest[len(oldEndMarker):]
	if len(rest) != idLen+len(oldEndSuffix) || rest[idLen:] != oldEndSuffix {
		return "", false
	}
	for i := 0; i < idLen; i++ {
		if !isHex(rest[i]) {
			return "", false
		}
	}
	return rest[:idLen], true
}

// newClosingID is the id of a current-format closing line: the whole line,
// with the whitespace around it gone, is `[/soho:peer:<id>]`.
func newClosingID(line string) (string, bool) {
	rest := strings.TrimSpace(line)
	if !strings.HasPrefix(rest, newEndMarker) {
		return "", false
	}
	rest = rest[len(newEndMarker):]
	if len(rest) != idLen+1 || rest[idLen] != ']' {
		return "", false
	}
	for i := 0; i < idLen; i++ {
		if !isHex(rest[i]) {
			return "", false
		}
	}
	return rest[:idLen], true
}

// oldOpeningID is the id of a previous-format opening line: with its left
// whitespace gone, the line starts with `[soho:peer] #<8 hex>` followed by a
// character that is not hex.
func oldOpeningID(line string) (string, bool) {
	rest := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(rest, oldOpenMarker) {
		return "", false
	}
	rest = rest[len(oldOpenMarker):]
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

// newOpeningID is the id of a current-format opening line: with its left
// whitespace gone, the line starts with the opening tag `[soho:peer:<8
// hex>]`, the closing bracket right after the id, so a longer id is not it.
func newOpeningID(line string) (string, bool) {
	rest := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(rest, newOpenMarker) {
		return "", false
	}
	rest = rest[len(newOpenMarker):]
	if len(rest) < idLen+1 || rest[idLen] != ']' {
		return "", false
	}
	for i := 0; i < idLen; i++ {
		if !isHex(rest[i]) {
			return "", false
		}
	}
	return rest[:idLen], true
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// openingShape reads a previous-format opening line's format and send time:
// the part before the last `, sent ` names the sender (remote when it holds
// ` on `), the part after it, up to the closing period, is the time.
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

// newHeaderShape reads a current-format header's (the text from its opening
// line to its closing line) format and send time: the `Sent:` line holds the
// time, and the `Sender:` line's reference is remote when it holds
// `<pane> on <host>`. The body lines are quoted with "> ", so neither prefix
// can hit a body line. An unclosed header holds no text: not remote, no time.
func newHeaderShape(text string) (remote bool, sent string) {
	for _, line := range strings.Split(text, "\n") {
		rest := strings.TrimLeft(line, " \t")
		switch {
		case sent == "" && strings.HasPrefix(rest, sentLinePrefix):
			sent = strings.TrimSpace(rest[len(sentLinePrefix):])
		case !remote && strings.HasPrefix(rest, senderLinePrefix):
			remote = senderRefRemote(rest[len(senderLinePrefix):])
		}
	}
	return remote, sent
}

// senderRefRemote reports whether a Sender line's reference is remote: the
// reference is the head before the first `;` — the name with the reference
// in parentheses, or the reference alone — and the reference is remote when
// its last ` on ` names a non-empty hostname.
func senderRefRemote(head string) bool {
	if i := strings.IndexByte(head, ';'); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimSpace(head)
	if strings.HasSuffix(head, ")") {
		if i := strings.LastIndex(head, "("); i >= 0 {
			head = head[i+1 : len(head)-1]
		}
	}
	head = strings.TrimSpace(head)
	i := strings.LastIndex(head, onSep)
	return i >= 0 && strings.TrimSpace(head[i+len(onSep):]) != ""
}
