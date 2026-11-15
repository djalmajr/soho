package peerauth_test

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/peerauth"
)

const spanSent = "2026-10-19T12:00:00Z"

// allocated is the bytes FindHeaders allocates for one call (TotalAlloc is
// cumulative, so a collection between the two reads does not hide anything).
func allocated(prompt string) (uint64, []peerauth.Header) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	headers := peerauth.FindHeaders(prompt)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, headers
}

// allocBudget is the multiple of the prompt size FindHeaders may allocate on
// the shapes below. The cost is per line (a 16-byte string header, an 8-byte
// rune prefix sum, the line splits) and per header (a Header of about 100
// bytes, grown by append, which doubles), so it depends on the line length:
// measured 20x for the one-id prompt of 21-byte lines, 16x for the nested
// one, 5x to 6x for the longer previous-format lines. 40x leaves 2x headroom
// for the runtime and the Go version, while copying every span again costs
// 20*N*(N+1)/2 bytes (about 4 GB, near 10,000x, for the one-id prompt), so
// the budget separates the two by more than two orders of magnitude.
const allocBudget = 40

type soho struct {
	name   string
	open   func(id string) string
	closer func(id string) string
}

var sohoFormats = []soho{
	{"current", func(id string) string { return "[soho:peer:" + id + "]" }, func(id string) string { return "[/soho:peer:" + id + "]" }},
	{"previous", func(id string) string {
		return "[soho:peer] #" + id + " Message from another agent — a on h, sent " + spanSent + "."
	},
		func(id string) string { return "[/soho:peer] #" + id + " end of message" }},
}

// slowChars is the rune count of the span lines[from..to] joined with LF,
// the direct computation of Header.Chars.
func slowChars(lines []string, from, to int) int {
	return utf8.RuneCountInString(strings.Join(lines[from:to+1], "\n"))
}

// metaLines give a current-format header its remote mark and send time.
var metaLines = []string{"Sender: a (pane on host); kind claude.", "Sent: " + spanSent}

func TestSohoHeadersManyOpeningsOfOneIDStayLinear(t *testing.T) {
	const n = 20000
	const id = "0f23aac6"
	for _, f := range sohoFormats {
		t.Run(f.name, func(t *testing.T) {
			lines := make([]string, 0, n+3)
			for i := 0; i < n; i++ {
				lines = append(lines, f.open(id))
			}
			lines = append(lines, metaLines...)
			closeAt := len(lines)
			lines = append(lines, f.closer(id))
			prompt := strings.Join(lines, "\n")
			bytes, headers := allocated(prompt)
			if bytes > allocBudget*uint64(len(prompt)) {
				t.Fatalf("FindHeaders allocated %d bytes for a %d-byte prompt (budget %dx)", bytes, len(prompt), allocBudget)
			}
			t.Logf("allocated %d bytes for a %d-byte prompt: %.1fx", bytes, len(prompt), float64(bytes)/float64(len(prompt)))
			if len(headers) != n {
				t.Fatalf("headers = %d, want %d", len(headers), n)
			}
			for _, k := range []int{0, n / 2, n - 1} {
				h := headers[k]
				if h.ID != id || !h.Closed || !h.Ambiguous || !h.Remote || h.Sent != spanSent {
					t.Fatalf("header %d = %+v", k, h)
				}
				if got, want := h.Chars(), slowChars(lines, k, closeAt); got != want {
					t.Fatalf("header %d Chars = %d, want %d", k, got, want)
				}
			}
		})
	}
}

func TestSohoHeadersNestedDistinctIDsStayLinear(t *testing.T) {
	const n = 20000
	for _, f := range sohoFormats {
		t.Run(f.name, func(t *testing.T) {
			lines := make([]string, 0, 2*n+2)
			for i := 0; i < n; i++ {
				lines = append(lines, f.open(fmt.Sprintf("%08x", i)))
			}
			lines = append(lines, metaLines...)
			closeLine := make([]int, n)
			for i := n - 1; i >= 0; i-- {
				closeLine[i] = len(lines)
				lines = append(lines, f.closer(fmt.Sprintf("%08x", i)))
			}
			prompt := strings.Join(lines, "\n")
			bytes, headers := allocated(prompt)
			if bytes > allocBudget*uint64(len(prompt)) {
				t.Fatalf("FindHeaders allocated %d bytes for a %d-byte prompt (budget %dx)", bytes, len(prompt), allocBudget)
			}
			t.Logf("allocated %d bytes for a %d-byte prompt: %.1fx", bytes, len(prompt), float64(bytes)/float64(len(prompt)))
			if len(headers) != n {
				t.Fatalf("headers = %d, want %d", len(headers), n)
			}
			for _, k := range []int{0, n / 2, n - 1} {
				h := headers[k]
				if h.ID != fmt.Sprintf("%08x", k) || !h.Closed || h.Ambiguous || !h.Remote || h.Sent != spanSent {
					t.Fatalf("header %d = %+v", k, h)
				}
				if got, want := h.Chars(), slowChars(lines, k, closeLine[k]); got != want {
					t.Fatalf("header %d Chars = %d, want %d", k, got, want)
				}
			}
		})
	}
}

// A linear parse must still read the first `Sent:` line with a value and the
// first remote `Sender:` line inside each span, not of the whole prompt.
func TestSohoHeaderShapeReadsOnlyItsOwnSpan(t *testing.T) {
	prompt := strings.Join([]string{
		"Sent: before", "Sender: x on before",
		"[soho:peer:00000001]", "Sent: ", "Sender: local (pane)", "Sent: first", "Sent: second", "Sender: p on h; x", "[/soho:peer:00000001]",
		"[soho:peer:00000002]", "Sent: only", "[/soho:peer:00000002]",
		"Sent: after", "Sender: y on after",
	}, "\n")
	h := peerauth.FindHeaders(prompt)
	if len(h) != 2 {
		t.Fatalf("headers = %+v", h)
	}
	if !h[0].Remote || h[0].Sent != "first" {
		t.Fatalf("first header = %+v", h[0])
	}
	if h[1].Remote || h[1].Sent != "only" {
		t.Fatalf("second header = %+v", h[1])
	}
}

// corpus builds the differential prompts: the fixtures of this package, and
// seeded random mixes of the three formats.
func corpus() []string {
	var out []string
	for _, s := range []spec{
		{name: "orchestrator"}, remoteSpec(), {remote: true, noHost: true, name: "o"},
		{name: "o", tail: "sig v1:abcd"}, {remote: true, host: "box", name: "o", body: []string{"line", "ü€ wide", ""}},
	} {
		a, b := s.unsigned(), s.unsignedOld()
		out = append(out, a, b, wrap(a), wrap(b), a+"\n"+b, a+"\n"+a, b+"\n"+b, a+"\r\n"+b,
			strings.ReplaceAll(a, "\n", "\r\n"), strings.ReplaceAll(b, "\n", "\r\n"),
			a[:strings.LastIndex(a, "\n")], b[:strings.LastIndex(b, "\n")], "> "+strings.ReplaceAll(a, "\n", "\n> "),
			a+"\n"+spec{id: "deadbeef", name: "x"}.unsigned()+"\n"+b+"\n<hc from=\"t3\">hi</hc>")
	}
	pool := []string{
		"[soho:peer:0f23aac6]", "[soho:peer:0f23aac6]", "[/soho:peer:0f23aac6]", "[/soho:peer:0f23aac6]  ",
		"[soho:peer:0f23aac7]", "[/soho:peer:0f23aac7]", "  [soho:peer:0f23aac7]", "[soho:peer:0f23aac]",
		"[soho:peer] #0f23aac6 Message — a, sent T.", "[soho:peer] #0f23aac6 Message — a on h, sent T2.",
		"[soho:peer] #0f23aac7", "[soho:peer] #0f23aac7 x on , sent .", "[/soho:peer] #0f23aac6 end of message", "[/soho:peer] #0f23aac7 end of message",
		"> [soho:peer:0f23aac6]", "> [/soho:peer:0f23aac6]", ">[soho:peer] #0f23aac6 q", "> [/soho:peer] #0f23aac6 end of message",
		"Sent: ", "Sent:", "Sent: t1", "  Sent: t2  ", "\tSent: ü", "Sender: ", "Sender: a (b on h); c", "Sender: p on ", "Sender: p on h", "Sender: a (b); p on h",
		"> Sent: quoted", "> Sender: q on h",
		"<hc from=\"t3\">", "<hc from=\"omarc/t1 lead\" reach=\"x\">body", "<hc from=\"t3\">hi</hc>", "</hc>", "x </hc> ", "> </hc>", "<hc from=\"bad\">",
		"body ü€ wide", "", "", "plain", "bad\xff\xfe bytes", "e\u0301",
	}
	rng := rand.New(rand.NewSource(340))
	for i := 0; i < 4000; i++ {
		n := 1 + rng.Intn(36)
		lines := make([]string, n)
		for j := range lines {
			lines[j] = pool[rng.Intn(len(pool))]
		}
		sep := "\n"
		if rng.Intn(4) == 0 {
			sep = "\r\n"
		}
		out = append(out, strings.Join(lines, sep))
	}
	return out
}

// The differential test: the linear FindHeaders returns every value the
// quadratic reference does, for every header of every prompt of the corpus.
func TestFindHeadersMatchesTheReference(t *testing.T) {
	prompts := corpus()
	headers := 0
	for _, prompt := range prompts {
		got, want := peerauth.FindHeaders(prompt), peerauth.ReferenceHeaders(prompt)
		if len(got) != len(want) {
			t.Fatalf("prompt %q: %d headers, reference %d", prompt, len(got), len(want))
		}
		for i := range got {
			g, w := got[i], want[i]
			if g.ID != w.ID || g.NativeRef != w.NativeRef || g.Closed != w.Closed || g.Ambiguous != w.Ambiguous || g.Remote != w.Remote || g.Sent != w.Sent {
				t.Fatalf("prompt %q header %d: %+v, reference %+v", prompt, i, g, w)
			}
			if g.Chars() != w.Chars() || peerauth.HeaderText(g) != w.Text {
				t.Fatalf("prompt %q header %d: Chars %d text %q, reference %d %q", prompt, i, g.Chars(), peerauth.HeaderText(g), w.Chars(), w.Text)
			}
			headers++
		}
	}
	if headers < 5000 {
		t.Fatalf("the corpus held only %d headers", headers)
	}
}

// The same comparison on the large prompt shapes, small enough for the
// quadratic reference.
func TestFindHeadersMatchesTheReferenceOnRepeatedAndNestedShapes(t *testing.T) {
	const n = 300
	for _, f := range sohoFormats {
		var repeated, nested []string
		for i := 0; i < n; i++ {
			repeated = append(repeated, f.open("0f23aac6"))
			nested = append(nested, f.open(fmt.Sprintf("%08x", i)))
		}
		repeated = append(repeated, metaLines...)
		nested = append(nested, metaLines...)
		repeated = append(repeated, f.closer("0f23aac6"))
		for i := n - 1; i >= 0; i-- {
			nested = append(nested, f.closer(fmt.Sprintf("%08x", i)))
		}
		for name, lines := range map[string][]string{"repeated": repeated, "nested": nested} {
			prompt := strings.Join(lines, "\n")
			got, want := peerauth.FindHeaders(prompt), peerauth.ReferenceHeaders(prompt)
			if len(got) != n || len(want) != n {
				t.Fatalf("%s %s: %d headers, reference %d", f.name, name, len(got), len(want))
			}
			for i := range got {
				g, w := got[i], want[i]
				if g.ID != w.ID || g.Closed != w.Closed || g.Ambiguous != w.Ambiguous || g.Remote != w.Remote || g.Sent != w.Sent || g.Chars() != w.Chars() {
					t.Fatalf("%s %s header %d: %+v (%d chars), reference %+v (%d chars)", f.name, name, i, g, g.Chars(), w, w.Chars())
				}
			}
		}
	}
}
