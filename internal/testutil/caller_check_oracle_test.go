package testutil

import (
	"strings"
	"testing"
)

func TestCallerCheckOracleRowFormat(t *testing.T) {
	// The frozen anchor and the inserted line match the row format of soho
	// config (key padded to 18, value padded to 30, single-space columns):
	// a row-format or default drift fails here and in the parity comparison.
	if callerCheckAnchor != "inbound"+strings.Repeat(" ", 11)+" "+"auto"+strings.Repeat(" ", 26)+" "+"defaults" {
		t.Fatalf("anchor drifted: %q", callerCheckAnchor)
	}
	if !strings.HasSuffix(callerCheckInsert,
		"caller_check"+strings.Repeat(" ", 6)+" "+"refuse"+strings.Repeat(" ", 24)+" "+"defaults") {
		t.Fatalf("caller_check line drifted: %q", callerCheckInsert)
	}
	if !strings.HasPrefix(callerCheckInsert, callerCheckAnchor+"\n") {
		t.Fatalf("the insert must start with the anchor: %q", callerCheckInsert)
	}
}

func TestCallerCheckOracleInsertsTheListingLine(t *testing.T) {
	in := "metrics            off                            defaults\n" +
		callerCheckAnchor + "\n" +
		"worker_messages    off                            defaults\n"
	want := "metrics            off                            defaults\n" +
		callerCheckInsert + "\n" +
		"worker_messages    off                            defaults\n"
	if got := RewriteCallerCheck(in); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestCallerCheckOraclePassesThroughWithoutTheAnchor(t *testing.T) {
	in := "metrics            off                            defaults\n" +
		"worker_messages    off                            defaults\n"
	if got := RewriteCallerCheck(in); got != in {
		t.Fatalf("a string without the anchor changed: %q", got)
	}
	if got := RewriteCallerCheck(""); got != "" {
		t.Fatalf("the empty string changed: %q", got)
	}
}

func TestCallerCheckOracleAdaptsEveryOccurrence(t *testing.T) {
	in := callerCheckAnchor + "\n" + callerCheckAnchor + "\n"
	if got := RewriteCallerCheck(in); strings.Count(got, "caller_check") != 2 {
		t.Fatalf("both occurrences must be adapted: %q", got)
	}
}

func TestApplyCallerCheckOracleWalksJSONTrees(t *testing.T) {
	value := map[string]any{"out": callerCheckAnchor + "\n", "n": 2, "list": []any{callerCheckAnchor}}
	got, ok := ApplyCallerCheckOracle(value).(map[string]any)
	if !ok {
		t.Fatalf("the map came back as %T", value)
	}
	if out, _ := got["out"].(string); !strings.Contains(out, "caller_check") {
		t.Fatalf("the nested string was not adapted: %q", out)
	}
	list, _ := got["list"].([]any)
	if line, _ := list[0].(string); !strings.Contains(line, "caller_check") {
		t.Fatalf("the array string was not adapted: %q", line)
	}
	if got["n"] != 2 {
		t.Fatalf("the non-string value changed: %v", got["n"])
	}
}

func TestRewriteCallerCheckLog(t *testing.T) {
	if got := RewriteCallerCheckLog(nil, "c"); got != nil {
		t.Fatalf("a nil log must stay nil, got %q", *got)
	}
	log := "agent get c\npane list\n"
	got := RewriteCallerCheckLog(&log, "c")
	if got == nil || *got != "pane process-info --pane c\nagent get c\npane list\n" {
		t.Fatalf("RewriteCallerCheckLog = %v", got)
	}
	if log != "agent get c\npane list\n" {
		t.Fatalf("the frozen log was changed in place: %q", log)
	}
}
