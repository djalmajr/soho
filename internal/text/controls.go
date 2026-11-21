package text

import (
	"fmt"
	"strings"
	"unicode"
)

// QuoteControls escapes every control character of s as its Go escape: \t,
// \n, \r, \xHH for the other C0 controls and DEL, and \u00HH for the C1
// controls. Every other rune is kept verbatim — spaces, repeated spaces,
// non-ASCII letters and backslashes are neither collapsed nor doubled. The
// result stays on one line and is never truncated, so a message can name the
// exact path it checked.
func QuoteControls(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			out.WriteString(`\t`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case unicode.IsControl(r) && r >= 0x80:
			fmt.Fprintf(&out, `\u%04x`, r)
		case unicode.IsControl(r):
			fmt.Fprintf(&out, `\x%02x`, r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
