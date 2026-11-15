package peerauth

import (
	"regexp"
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
	// NativeRef is the sender of a native Core message, which has no id.
	NativeRef string
	Closed    bool
	// Ambiguous reports that the framing does not say where the message
	// ends: within its own format, another closing line for the same id
	// after the one that closed it, or another opening line with the same
	// id. The text past the first closing line is outside what the header
	// names. For Core messages, another opening before the first close is
	// ambiguous; subsequent independently closed messages are not.
	Ambiguous bool
	// Remote reports a remote opening (the previous format's ` on <host>,
	// sent `, the current format's `<pane> on <host>` reference, or a native
	// Core reference with a machine/ prefix).
	Remote bool
	// Sent is the header's send time as written ("" when there is none).
	Sent string
	// doc and first..last name the message text: the lines from the opening
	// line to the closing line (an ambiguous native header stops before the
	// next opening), inclusive, shared with every other header of the prompt.
	// The text is never copied: overlapping spans would copy the same tail
	// once per opening. doc is nil when Closed is false.
	doc         *document
	first, last int
}

// document is the line index every header of one prompt shares.
type document struct {
	lines []string
	// runes[k] is the rune count of lines[:k] joined without separators, so
	// the rune count of a span of lines joined by LF is a difference of two
	// entries plus one LF per join.
	runes []int
	// sentLines and remoteSenderLines are the ascending indices of the lines
	// that give a current-format header its send time (a `Sent:` line with a
	// non-empty value) and its remote mark (a `Sender:` line whose reference
	// is remote). A header reads the first of each inside its span.
	sentLines, remoteSenderLines []int
}

// Chars is the number of characters of the message text: the runes of the
// span from the opening line to the closing line, joined by LF. It is 0 when
// Closed is false.
func (h Header) Chars() int {
	if h.doc == nil {
		return 0
	}
	return h.doc.runes[h.last+1] - h.doc.runes[h.first] + h.last - h.first
}

// FindHeaders returns every header in the prompt, in prompt order. Only the
// lines matter, so the wrapper Claude Code puts around a paste is not
// interpreted. Three formats are accepted, and an opening line is paired only
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
// Native Core openings use the grammar in nativeOpening and close at the
// first unquoted line ending in </hc>, including their opening line. They
// are Ambiguous when another native opening precedes that closing.
// Each header keeps the indices of its span in the shared lines, never a
// copy, so the work and memory are linear in the prompt for every format.
func FindHeaders(prompt string) []Header {
	lines := strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")
	d := &document{lines: lines, runes: make([]int, len(lines)+1)}
	var out []Header
	// One pass indexes the opening lines per id and format and the closing
	// lines per id and format, so a prompt of many lines is not scanned once
	// per header, and one format's opening never pairs with the other
	// format's closing of the same id.
	oldOpenings, newOpenings := map[string]int{}, map[string]int{}
	oldClosers, newClosers := map[string][]int{}, map[string][]int{}
	var hcOpenings, hcClosers []int
	for i, line := range lines {
		d.runes[i+1] = d.runes[i] + utf8.RuneCountInString(line)
		if rest := strings.TrimLeft(line, " \t"); strings.HasPrefix(rest, sentLinePrefix) {
			if strings.TrimSpace(rest[len(sentLinePrefix):]) != "" {
				d.sentLines = append(d.sentLines, i)
			}
		} else if strings.HasPrefix(rest, senderLinePrefix) && senderRefRemote(rest[len(senderLinePrefix):]) {
			d.remoteSenderLines = append(d.remoteSenderLines, i)
		}
		if nativeOpeningRef(line) != "" {
			hcOpenings = append(hcOpenings, i)
		}
		if nativeClosing(line) {
			hcClosers = append(hcClosers, i)
		}
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
		if ref := nativeOpeningRef(line); ref != "" {
			h := Header{NativeRef: ref, Remote: strings.Contains(ref, "/")}
			after := hcClosers[sort.SearchInts(hcClosers, i):]
			if len(after) > 0 {
				end := after[0]
				h.Closed = true
				next := sort.SearchInts(hcOpenings, i+1)
				h.Ambiguous = next < len(hcOpenings) && hcOpenings[next] <= end
				// An ambiguous native header keeps only its own lines, up to the
				// next opening.
				last := end
				if h.Ambiguous {
					last = hcOpenings[next] - 1
				}
				h.doc, h.first, h.last = d, i, last
			} else {
				h.Ambiguous = sort.SearchInts(hcOpenings, i+1) < len(hcOpenings)
			}
			out = append(out, h)
			continue
		}
		if id, ok := oldOpeningID(line); ok {
			out = append(out, headerFor(id, i, oldOpenings[id], oldClosers[id], d, true))
			continue
		}
		if id, ok := newOpeningID(line); ok {
			out = append(out, headerFor(id, i, newOpenings[id], newClosers[id], d, false))
		}
	}
	return out
}

// Core tags start at column zero. Attribute values cannot span lines; the
// body after the opening tag is unrestricted. Notes without from are not peers.
var nativeOpening = regexp.MustCompile(`^<hc from="(([a-z0-9_-]+/)?(t[0-9]+|[a-z0-9_-]{1,32}))( [a-z0-9_-]{1,32})?"( [a-z_]+="[^"\r\n]*")*>`)

func nativeOpeningRef(line string) string {
	if match := nativeOpening.FindStringSubmatch(line); match != nil {
		return match[1]
	}
	return ""
}

func nativeClosing(line string) bool {
	return !strings.HasPrefix(strings.TrimLeft(line, " \t"), ">") && strings.HasSuffix(strings.TrimRight(line, " \r"), "</hc>")
}

// headerFor builds the header for one opening line at index i, pairing it
// with the first closing line of the same format after it. The previous
// format reads its remote and send-time fields from the opening line, the
// current one from the lines of its span.
func headerFor(id string, i, openings int, closers []int, d *document, previous bool) Header {
	h := Header{ID: id, Ambiguous: openings > 1}
	after := closers[sort.SearchInts(closers, i+1):]
	if len(after) > 0 {
		h.Closed = true
		h.doc, h.first, h.last = d, i, after[0]
		h.Ambiguous = h.Ambiguous || len(after) > 1
	}
	if previous {
		h.Remote, h.Sent = openingShape(d.lines[i])
	} else if h.Closed {
		h.Remote, h.Sent = d.spanShape(i, h.last)
	}
	return h
}

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

// spanShape reads a current-format header's (the lines from its opening line
// to its closing line, inclusive) format and send time: the first `Sent:`
// line with a value holds the time, and the span is remote when a `Sender:`
// line's reference holds `<pane> on <host>`. The body lines are quoted with
// "> ", so neither prefix can hit a body line. The first matching line comes
// from the precomputed indices, so a header costs a binary search, not a scan
// of its span. An unclosed header holds no lines: not remote, no time.
func (d *document) spanShape(first, last int) (remote bool, sent string) {
	if at := sort.SearchInts(d.sentLines, first); at < len(d.sentLines) && d.sentLines[at] <= last {
		rest := strings.TrimLeft(d.lines[d.sentLines[at]], " \t")
		sent = strings.TrimSpace(rest[len(sentLinePrefix):])
	}
	at := sort.SearchInts(d.remoteSenderLines, first)
	return at < len(d.remoteSenderLines) && d.remoteSenderLines[at] <= last, sent
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
