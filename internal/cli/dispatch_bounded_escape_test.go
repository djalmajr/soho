package cli

import "testing"

// A literal backslash is escaped too, so a message holding the two
// characters \ and n cannot read like an escaped newline.
func TestBoundedEscapedEscapesABackslash(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`a\nb`, `a\\nb`},
		{"a\nb", `a\nb`},
		{`c:\x`, `c:\\x`},
	} {
		if got := boundedEscaped(tc.in, 200); got != tc.want {
			t.Fatalf("boundedEscaped(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
