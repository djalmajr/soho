package peerauth

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// RefHeader is a header as the quadratic implementation of FindHeaders
// produced it, before the spans became indices into a shared line slice. It
// is kept here, unexported from production, only to compare the linear
// implementation against: the differential test requires the same values for
// every header of every prompt.
type RefHeader struct {
	ID, NativeRef string
	Text          string
	Closed        bool
	Ambiguous     bool
	Remote        bool
	Sent          string
}

// Chars is the rune count of the text, as the reference computes it.
func (h RefHeader) Chars() int { return utf8.RuneCountInString(h.Text) }

// ReferenceHeaders is the reference FindHeaders: each header copies its span.
func ReferenceHeaders(prompt string) []RefHeader {
	lines := strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")
	var out []RefHeader
	oldOpenings, newOpenings := map[string]int{}, map[string]int{}
	oldClosers, newClosers := map[string][]int{}, map[string][]int{}
	var hcOpenings, hcClosers []int
	for i, line := range lines {
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
			h := RefHeader{NativeRef: ref, Remote: strings.Contains(ref, "/")}
			after := hcClosers[sort.SearchInts(hcClosers, i):]
			if len(after) > 0 {
				end := after[0]
				h.Closed = true
				next := sort.SearchInts(hcOpenings, i+1)
				h.Ambiguous = next < len(hcOpenings) && hcOpenings[next] <= end
				last := end
				if h.Ambiguous {
					last = hcOpenings[next] - 1
				}
				h.Text = strings.Join(lines[i:last+1], "\n")
			} else {
				h.Ambiguous = sort.SearchInts(hcOpenings, i+1) < len(hcOpenings)
			}
			out = append(out, h)
			continue
		}
		if id, ok := oldOpeningID(line); ok {
			out = append(out, refHeaderFor(id, i, oldOpenings[id], oldClosers[id], lines, line, func(line, _ string) (bool, string) { return openingShape(line) }))
			continue
		}
		if id, ok := newOpeningID(line); ok {
			out = append(out, refHeaderFor(id, i, newOpenings[id], newClosers[id], lines, line, func(_, text string) (bool, string) { return ReferenceHeaderShape(text) }))
		}
	}
	return out
}

func refHeaderFor(id string, i, openings int, closers []int, lines []string, line string, shape func(line, text string) (bool, string)) RefHeader {
	h := RefHeader{ID: id, Ambiguous: openings > 1}
	after := closers[sort.SearchInts(closers, i+1):]
	if len(after) > 0 {
		h.Closed = true
		h.Text = strings.Join(lines[i:after[0]+1], "\n")
		h.Ambiguous = h.Ambiguous || len(after) > 1
	}
	h.Remote, h.Sent = shape(line, h.Text)
	return h
}

// ReferenceHeaderShape is the reference reading of a current-format header's
// remote mark and send time: a scan of every line of the header text.
func ReferenceHeaderShape(text string) (remote bool, sent string) {
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
