package text

import (
	"strings"
	"testing"
	"unicode"
)

func TestQuoteControls(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "C:\\work\\soho", `C:\work\soho`},
		{"repeated spaces", "a  b   c", "a  b   c"},
		{"unicode", "espaço livre", "espaço livre"},
		{"tab", "a\tb", `a\tb`},
		{"newline", "a\nb", `a\nb`},
		{"carriage return", "a\rb", `a\rb`},
		{"null", "a\x00b", `a\x00b`},
		{"bell", "a\x07b", `a\x07b`},
		{"unit separator", "a\x1fb", `a\x1fb`},
		{"delete", "a\x7fb", `a\x7fb`},
		{"c1", "a\u0085b", `a\u0085b`},
		{"last c1", "a\u009fb", `a\u009fb`},
		{"backslash before a control", "a\\\tb", `a\\tb`},
		{"backslash before an escape word", `a\tb`, `a\tb`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuoteControls(tc.in); got != tc.want {
				t.Fatalf("QuoteControls(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestQuoteControlsKeepsLengthAndPrintability(t *testing.T) {
	long := "/tmp/" + strings.Repeat("p", 300) + "/soho with space"
	got := QuoteControls(long)
	if got != long {
		t.Fatalf("a long path changed: length %d, want %d", len(got), len(long))
	}
	mixed := "/tmp/espaço  livre/\tnew\nline\x00\x7f" + string(rune(0x85)) + "/soho"
	quoted := QuoteControls(mixed)
	if strings.ContainsAny(quoted, "\r\n\t") {
		t.Fatalf("a raw control survived: %q", quoted)
	}
	for _, r := range quoted {
		if unicode.IsControl(r) {
			t.Fatalf("a raw control survived: %q", quoted)
		}
	}
	if !strings.Contains(quoted, `\u0085`) || !strings.Contains(quoted, `\x00`) {
		t.Fatalf("the C0/C1 escapes are missing: %q", quoted)
	}
	if n := strings.Count(quoted, "espaço  livre"); n != 1 {
		t.Fatalf("Unicode or repeated spaces changed: %q", quoted)
	}
}
