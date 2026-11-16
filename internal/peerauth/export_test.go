package peerauth

import "strings"

// HeaderText is the message text a header names: the lines of its span, from
// the opening line to the closing line, joined with LF; "" when the header is
// not Closed. Production code never builds it (Chars reads the span's
// prefix sums), so the tests that assert on the text go through this helper.
func HeaderText(h Header) string {
	if h.doc == nil {
		return ""
	}
	return strings.Join(h.doc.lines[h.first:h.last+1], "\n")
}
