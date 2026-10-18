package testutil

// DJA-393 L1: the pane_columns/pane_overflow oracle inserts the two new
// `soho config` listing rows after the frozen split_min_pane anchor row and
// passes everything else through byte-identical.

import (
	"strings"
	"testing"
)

func TestRewritePaneColumns(t *testing.T) {
	t.Run(`the two listing rows are inserted after the split_min_pane anchor row`, func(t *testing.T) {
		in := "KEY                VALUE                          SOURCE\n" +
			"split_max_panes    4                              defaults\n" +
			"split_min_pane     0.18                           defaults\n" +
			"herd_label         {roles}                        defaults\n"
		got := RewritePaneColumns(in)
		want := "KEY                VALUE                          SOURCE\n" +
			"split_max_panes    4                              defaults\n" +
			"split_min_pane     0.18                           defaults\n" +
			"pane_columns" + strings.Repeat(" ", 38) + "defaults\n" +
			"pane_overflow" + strings.Repeat(" ", 6) + "last-column" + strings.Repeat(" ", 20) + "defaults\n" +
			"herd_label         {roles}                        defaults\n"
		if got != want {
			t.Fatalf("RewritePaneColumns()=\n%s\nwant\n%s", got, want)
		}
	})
	t.Run(`the anchor matches any value and any source`, func(t *testing.T) {
		for _, row := range []string{
			"split_min_pane     0.18                           defaults",
			"split_min_pane     0.25                           user",
			"split_min_pane     0.2                            project",
		} {
			got := RewritePaneColumns(row + "\nherd_label         {roles}                        defaults")
			if !strings.Contains(got, row+"\npane_columns") {
				t.Fatalf("anchor %q not adapted:\n%s", row, got)
			}
		}
	})
	t.Run(`a string without the anchor passes through byte-identical`, func(t *testing.T) {
		for _, in := range []string{
			"",
			"ok     config: split_max_panes=4 split_min_pane=0.18\n", // the doctor row, not the listing row
			"split_max_panes    4                              defaults\n",
		} {
			if got := RewritePaneColumns(in); got != in {
				t.Fatalf("RewritePaneColumns(%q)=%q changed a string without the anchor", in, got)
			}
		}
	})
	t.Run(`the tree walk rewrites every string and keeps the other values`, func(t *testing.T) {
		in := map[string]any{
			"steps": []any{
				map[string]any{"out": "split_min_pane     0.18                           defaults\n"},
				42,
				true,
				"no anchor here",
			},
		}
		got, ok := ApplyPaneColumnsOracle(in).(map[string]any)
		if !ok {
			t.Fatalf("the tree walk returned %#v", got)
		}
		steps := got["steps"].([]any)
		out := steps[0].(map[string]any)["out"].(string)
		if !strings.Contains(out, "pane_columns") || !strings.Contains(out, "pane_overflow") {
			t.Fatalf("the listing row was not adapted:\n%s", out)
		}
		if steps[1] != 42 || steps[2] != true || steps[3] != "no anchor here" {
			t.Fatalf("non-string values or unanchored strings changed: %#v", steps)
		}
	})
}
