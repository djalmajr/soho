package testutil

import (
	"strings"
	"testing"
)

func TestBackgroundPatternsOracleRowFormat(t *testing.T) {
	// The frozen anchor and the inserted line match the row format of
	// soho config: a row-format or default drift fails here and in the
	// parity comparison.
	if backgroundPatternsAnchor != "effort.cursor"+strings.Repeat(" ", 5)+" "+"xhigh"+strings.Repeat(" ", 25)+" "+"defaults" {
		t.Fatalf("anchor drifted: %q", backgroundPatternsAnchor)
	}
	key, rest, _ := strings.Cut(backgroundPatternsRow, " ")
	if key != "background_patterns.claude" || len(key) <= 18 {
		t.Fatalf("row key = %q, want background_patterns.claude (longer than its column, so one space follows)", key)
	}
	value, source, _ := strings.Cut(rest, " defaults")
	if value != `\d+ (shells?|monitors?)(, \d+ (shells?|monitors?))* still running|· \d+ (shells?|monitors?)(, \d+ (shells?|monitors?))*( ·|$)` || len([]rune(value)) <= 30 || source != "" {
		t.Fatalf("row value = %q source=%q: the default or the row format drifted", value, source)
	}
}

func TestBackgroundPatternsOracleInsertsTheListingLineBeforeTheAnchor(t *testing.T) {
	in := "pressure_swap_percent 80                             defaults\n" +
		backgroundPatternsAnchor + "\n" +
		"effort.grok        xhigh                          defaults\n"
	want := "pressure_swap_percent 80                             defaults\n" +
		backgroundPatternsRow + "\n" +
		backgroundPatternsAnchor + "\n" +
		"effort.grok        xhigh                          defaults\n"
	if got := RewriteBackgroundPatterns(in); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	// Every occurrence is adapted, like the other oracles.
	both := backgroundPatternsAnchor + "\n" + backgroundPatternsAnchor
	if got := strings.Count(RewriteBackgroundPatterns(both), "background_patterns.claude"); got != 2 {
		t.Fatalf("occurrences adapted = %d, want 2", got)
	}
}

func TestBackgroundPatternsOraclePassesThroughWithoutTheAnchor(t *testing.T) {
	in := "pressure_swap_percent 80                             defaults\n" +
		"effort.grok        xhigh                          defaults\n"
	if got := RewriteBackgroundPatterns(in); got != in {
		t.Fatalf("string without the anchor changed:\n%q", got)
	}
}
