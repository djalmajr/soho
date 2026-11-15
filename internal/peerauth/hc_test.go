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
			if tc.closed && (h[0].Text == "" || !strings.HasSuffix(strings.TrimRight(h[0].Text, " \r"), "</hc>")) {
				t.Fatalf("text = %q", h[0].Text)
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
