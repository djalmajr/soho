package testutil

import (
	"strings"
	"testing"
)

func TestCompactTimeoutOracleRowFormat(t *testing.T) {
	// The frozen anchor and the inserted line match the row format of
	// soho config (key padded to 18, value padded to 30, single-space
	// columns): a row-format or default drift fails here and in the parity
	// comparison.
	if compactTimeoutAnchor != "dispatch_timeout"+strings.Repeat(" ", 2)+" "+"900000"+strings.Repeat(" ", 24)+" "+"defaults" {
		t.Fatalf("anchor drifted: %q", compactTimeoutAnchor)
	}
	if !strings.HasSuffix(compactTimeoutInsert,
		"compact_timeout"+strings.Repeat(" ", 3)+" "+"300000"+strings.Repeat(" ", 24)+" "+"defaults") {
		t.Fatalf("compact_timeout line drifted: %q", compactTimeoutInsert)
	}
}

func TestCompactTimeoutOracleInsertsTheListingLine(t *testing.T) {
	in := "spawn_timeout      60000                          defaults\n" +
		compactTimeoutAnchor + "\n" +
		"provider_retries   3                              defaults\n"
	want := "spawn_timeout      60000                          defaults\n" +
		compactTimeoutInsert + "\n" +
		"provider_retries   3                              defaults\n"
	if got := RewriteCompactTimeout(in); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	// Every occurrence is adapted, like the other oracles.
	both := compactTimeoutAnchor + "\n" + compactTimeoutAnchor
	if got := RewriteCompactTimeout(both); !strings.Contains(got, "defaults\ncompact_timeout    300000                         defaults\ndispatch_timeout") {
		t.Fatalf("double insertion wrong:\n%q", got)
	}
}

func TestCompactTimeoutOraclePassesThroughWithoutTheAnchor(t *testing.T) {
	in := "spawn_timeout      60000                          defaults\n" +
		"provider_retries   3                              defaults\n"
	if got := RewriteCompactTimeout(in); got != in {
		t.Fatalf("string without the anchor changed:\n%q", got)
	}
}

func TestApplyCompactTimeoutOracleWalksTheTree(t *testing.T) {
	value := map[string]any{
		"out":    "before\n" + compactTimeoutAnchor + "\nafter",
		"nested": []any{compactTimeoutAnchor, 7, "plain"},
	}
	got := ApplyCompactTimeoutOracle(value).(map[string]any)
	if got["out"] != "before\n"+compactTimeoutInsert+"\nafter" {
		t.Fatalf("string leaf not rewritten: %q", got["out"])
	}
	nested := got["nested"].([]any)
	if nested[0] != compactTimeoutInsert || nested[1] != 7 || nested[2] != "plain" {
		t.Fatalf("array walk wrong: %#v", nested)
	}
}
