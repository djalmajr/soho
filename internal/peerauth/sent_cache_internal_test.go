package peerauth

import (
	"strings"
	"testing"
)

// TestSpanShapeReadsTheIndexedSentValue pins that a header takes its send
// time from the value stored while indexing, never by trimming the shared
// line again: many overlapping headers can share one long padded `Sent:`
// line, and a per-header trim would be quadratic work in the prompt.
func TestSpanShapeReadsTheIndexedSentValue(t *testing.T) {
	d := &document{
		lines:      []string{"[soho:peer:00000001]", "   Sent: from the line   ", "[/soho:peer:00000001]"},
		sentLines:  []int{1},
		sentValues: []string{"from the index"},
	}
	if _, sent := d.spanShape(0, 2); sent != "from the index" {
		t.Fatalf("spanShape sent = %q, want the indexed value %q", sent, "from the index")
	}
	if _, sent := d.spanShape(2, 2); sent != "" {
		t.Fatalf("spanShape outside the Sent line = %q, want none", sent)
	}
}

// TestSharedPaddedSentLineKeepsItsValue checks the observable value when many
// overlapping current-format headers share one heavily padded `Sent:` line,
// the input shape of review R343's F1.
func TestSharedPaddedSentLineKeepsItsValue(t *testing.T) {
	const n = 2000
	for _, sentLine := range []string{strings.Repeat(" ", 32*n) + "Sent: T", "Sent: T" + strings.Repeat(" ", 32*n)} {
		prompt := strings.Repeat("[soho:peer:00000001]\n", n) + sentLine + "\n[/soho:peer:00000001]"
		headers := FindHeaders(prompt)
		if len(headers) != n {
			t.Fatalf("headers = %d, want %d", len(headers), n)
		}
		for _, k := range []int{0, n / 2, n - 1} {
			if h := headers[k]; !h.Closed || !h.Ambiguous || h.Sent != "T" {
				t.Fatalf("header %d = closed %v ambiguous %v sent %q, want closed, ambiguous, sent T", k, h.Closed, h.Ambiguous, h.Sent)
			}
		}
	}
}
