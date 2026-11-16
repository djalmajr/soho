package wait

import (
	"strings"
	"testing"
)

// R422 F1: the example is quoted (control characters escaped) and cut at 80
// runes, counting runes, not bytes.
func TestUnmarkedExample(t *testing.T) {
	if got := unmarkedExample("- a: x\x1b[31m\tb"); got != `"- a: x\x1b[31m\tb"` {
		t.Fatalf("control characters must be escaped: %s", got)
	}
	long := "- " + strings.Repeat("é", 100)
	got := unmarkedExample(long)
	inner := []rune(strings.Trim(got, `"`))
	if len(inner) != 80 || inner[79] != '…' || !strings.HasPrefix(got, `"- éé`) {
		t.Fatalf("want 79 runes plus an ellipsis, got %d runes: %s", len(inner), got)
	}
	if got := unmarkedExample(strings.Repeat("x", 80)); got != `"`+strings.Repeat("x", 80)+`"` {
		t.Fatalf("an 80-rune line stays whole: %s", got)
	}
}
