package wait

import (
	"strings"
	"testing"
)

// R422 F1: the example is quoted (control characters escaped) and cut at 120
// runes, counting runes, not bytes.
func TestUnmarkedExample(t *testing.T) {
	if got := unmarkedExample("- a: x\x1b[31m\tb"); got != `"- a: x\x1b[31m\tb"` {
		t.Fatalf("control characters must be escaped: %s", got)
	}
	long := "- " + strings.Repeat("é", 120)
	got := unmarkedExample(long)
	inner := []rune(strings.Trim(got, `"`))
	if len(inner) != 120 || inner[119] != '…' || !strings.HasPrefix(got, `"- éé`) {
		t.Fatalf("want 119 runes plus an ellipsis, got %d runes: %s", len(inner), got)
	}
	if got := unmarkedExample(strings.Repeat("x", 120)); got != `"`+strings.Repeat("x", 120)+`"` {
		t.Fatalf("a 120-rune line stays whole: %s", got)
	}
}
