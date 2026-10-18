package testutil

import (
	"strings"
	"testing"
)

func TestRewriteFootprint(t *testing.T) {
	t.Run("the new listing line is inserted after the frozen anchor", func(t *testing.T) {
		in := "KEY  VALUE  SOURCE\npressure_swap_percent 80                             defaults\npressure_refuse_disk_free_percent 5                              defaults\npressure_refuse_swap_percent 95                             defaults\n\nlayers read: defaults"
		want := "KEY  VALUE  SOURCE\npressure_swap_percent 80                             defaults\npressure_refuse_disk_free_percent 5                              defaults\npressure_refuse_swap_percent 95                             defaults\nfootprint_warn_gb  5                              defaults\n\nlayers read: defaults"
		if got := RewriteFootprint(in); got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("a string without the anchor passes through unchanged", func(t *testing.T) {
		in := "nothing to adapt here"
		if got := RewriteFootprint(in); got != in {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("the anchor is the line the pressure refusal oracle inserts", func(t *testing.T) {
		// Chained: the pressure oracle inserts its second line, and this
		// oracle anchors exactly on it.
		in := "pressure_swap_percent 80                             defaults"
		mid := RewritePressureRefusal(in)
		wantMid := "pressure_swap_percent 80                             defaults\npressure_refuse_disk_free_percent 5                              defaults\npressure_refuse_swap_percent 95                             defaults"
		if mid != wantMid {
			t.Fatalf("the pressure oracle output changed:\n%s", mid)
		}
		got := RewriteFootprint(mid)
		if !strings.Contains(got, "pressure_refuse_swap_percent 95                             defaults\nfootprint_warn_gb  5                              defaults") {
			t.Fatalf("the chained output lacks the new line:\n%s", got)
		}
	})
	t.Run("the tree walk rewrites nested strings", func(t *testing.T) {
		value := map[string]any{
			"steps": []any{
				map[string]any{"out": "pressure_refuse_swap_percent 95                             defaults\n"},
				"untouched",
				42,
			},
		}
		got := ApplyFootprintOracle(value).(map[string]any)
		steps := got["steps"].([]any)
		if gotOut := steps[0].(map[string]any)["out"].(string); !strings.Contains(gotOut, "footprint_warn_gb  5                              defaults\n") {
			t.Fatalf("nested out=%q", gotOut)
		}
		if steps[1] != "untouched" || steps[2] != 42 {
			t.Fatalf("non-anchor values changed: %v", steps[1:])
		}
	})
}
