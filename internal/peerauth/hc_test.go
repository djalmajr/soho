package peerauth_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
)

func TestNativePeerHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, prompt              string
		count                     int
		closed, ambiguous, remote bool
	}{
		{"named", `<hc from="t256 lead">hello</hc>`, 1, true, false, false},
		{"unnamed", "<hc from=\"t2cj\">hello\n</hc>", 1, true, false, false},
		{"remote", `<hc from="omarc/t1">hello</hc>`, 1, true, false, true},
		{"attributes", `<hc from="t3 lead" reach="hc send t3 when done" extra_key="">hello</hc>`, 1, true, false, false},
		{"crlf", "<hc from=\"t3\">hello\r\nlast</hc>  \r\n", 1, true, false, false},
		{"unclosed", `<hc from="t3">hello`, 1, false, false, false},
		{"nested", "<hc from=\"t3\">hello\n<hc from=\"t4\">nested</hc>", 2, true, true, false},
		{"sequential", "<hc from=\"t3\">one</hc>\n<hc from=\"t3\">two</hc>", 2, true, false, false},
		{"note", `<hc>t3 exited (exit 0)</hc>`, 0, false, false, false},
		{"quoted", `> <hc from="t3">hello</hc>`, 0, false, false, false},
		{"middle", `text <hc from="t3">hello</hc>`, 0, false, false, false},
		{"indented", ` <hc from="t3">hello</hc>`, 0, false, false, false},
		{"long-ref", `<hc from="` + strings.Repeat("a", 33) + `">hello</hc>`, 0, false, false, false},
		{"long-name", `<hc from="t3 ` + strings.Repeat("a", 33) + `">hello</hc>`, 0, false, false, false},
		{"quote-in-name", `<hc from="t3 lead"evil">hello</hc>`, 0, false, false, false},
		{"tag-in-name", `<hc from="t3 le>ad">hello</hc>`, 0, false, false, false},
		{"newline-in-tag", "<hc from=\"t3\nlead\">hello</hc>", 0, false, false, false},
		{"bad-attribute", `<hc from="t3" Reach="x">hello</hc>`, 0, false, false, false},
		{"nul-in-ref", "<hc from=\"t3\x00\">hello</hc>", 0, false, false, false},
		{"nul-in-body", "<hc from=\"t3\">\x00</hc>", 1, true, false, false},
		{"quoted-close", "<hc from=\"t3\">hello\n> </hc>", 1, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := peerauth.FindHeaders(tc.prompt)
			if len(h) != tc.count {
				t.Fatalf("headers = %+v, want count %d", h, tc.count)
			}
			if tc.count == 0 {
				return
			}
			if h[0].ID != "" || h[0].Closed != tc.closed || h[0].Ambiguous != tc.ambiguous || h[0].Remote != tc.remote {
				t.Fatalf("header = %+v", h[0])
			}
			// A closed header ends at its closer; an ambiguous one keeps only its
			// own lines, up to the next opening.
			if tc.closed && !tc.ambiguous && (h[0].Text == "" || !strings.HasSuffix(strings.TrimRight(h[0].Text, " \r"), "</hc>")) {
				t.Fatalf("text = %q", h[0].Text)
			}
			if tc.closed && tc.ambiguous && (h[0].Text == "" || strings.Contains(h[0].Text, "\n<hc from=")) {
				t.Fatalf("ambiguous text = %q, want only its own lines", h[0].Text)
			}
		})
	}
	// Native framing must not change the ordering or fields of soho headers.
	h := peerauth.FindHeaders("<hc from=\"t3\">hello</hc>\n" + spec{}.unsigned())
	if len(h) != 2 || h[0].ID != "" || h[1].ID != "0f23aac6" {
		t.Fatalf("mixed headers = %+v", h)
	}
}

func TestNativePeerPolicy(t *testing.T) {
	for _, off := range []bool{false, true} {
		f := newFixture(t)
		f.projectOff = off
		o := f.check(`<hc from="omarc/t1 lead">hello</hc>`)
		if len(o.Verdicts) != 1 || !o.Evaluated || o.Block != off {
			t.Fatalf("off=%v outcome=%+v", off, o)
		}
		v := o.Verdicts[0]
		if v.Format != "hc" || v.Label != "omarc/t1" || v.Header.ID != "" || !v.Header.Remote {
			t.Fatalf("verdict=%+v", v)
		}
		if off {
			if v.Result != peerauth.RefusedInboundOff || !strings.HasPrefix(o.Reason(), "[soho:peer-refused:hc] omarc/t1 refused-inbound-off\n") {
				t.Fatalf("refusal=%+v reason=%q", v, o.Reason())
			}
		} else if v.Result != peerauth.Accepted {
			t.Fatalf("accepted=%+v", v)
		}
	}
}

// Many native openings and one closer: each header keeps only its own lines,
// so the text the parser materializes stays bounded by the prompt instead of
// growing with the square of the number of openings.
func TestNativePeerHeadersManyOpeningsStayLinear(t *testing.T) {
	const n = 20000
	prompt := strings.Repeat("<hc from=\"t3\">\n", n) + "</hc>\n"
	headers := peerauth.FindHeaders(prompt)
	if len(headers) != n {
		t.Fatalf("headers=%d, want %d", len(headers), n)
	}
	total := 0
	for i, h := range headers {
		if !h.Closed {
			t.Fatalf("header %d not closed", i)
		}
		if i < n-1 && !h.Ambiguous {
			t.Fatalf("header %d not ambiguous", i)
		}
		total += len(h.Text)
	}
	if total > 2*len(prompt) {
		t.Fatalf("materialized %d bytes for a %d-byte prompt", total, len(prompt))
	}
}
