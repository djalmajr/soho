package cli

import (
	"strings"
	"testing"
)

// TestDJA636DirectSiblings: two owned folders with the same immediate parent
// never cross, in both orders and with or without the trailing slash, through
// the comparison, the warning path and the full dispatch. A comparison that
// crossed folders sharing a parent fails every case.
func TestDJA636DirectSiblings(t *testing.T) {
	for _, pair := range [][2]string{
		{"qa/first/", "qa/second/"},
		{"qa/second/", "qa/first/"},
		{"qa/first", "qa/second/"},
		{"qa/first/", "qa/second"},
	} {
		mine, theirs := pair[0], pair[1]
		t.Run(mine+" vs "+theirs, func(t *testing.T) {
			if pathsCross(mine, theirs) {
				t.Errorf("pathsCross(%q, %q) = true; direct siblings must not cross", mine, theirs)
			}
			if _, stderr := dja636WarnFixture(t, "- "+mine, "- "+theirs+"\n"); strings.Contains(stderr, "owns files") {
				t.Errorf("the warning path reported direct siblings: %q", stderr)
			}
			if code, stderr := dja636DispatchFixture(t, "- "+mine, "- "+theirs+"\n"); code != 0 || strings.Contains(stderr, "owns files") {
				t.Errorf("dispatch: exit %d, stderr %q; want exit 0 and no warning", code, stderr)
			}
		})
	}
}
