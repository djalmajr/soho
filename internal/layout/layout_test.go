package layout

import (
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestGridSizes(t *testing.T) {
	// Mutation captured: assigning remainder rows to the first columns changes the grids for 3, 5 and 8 cells;
	// 7 is 1+3+3 by the user's decision (the caller alone in its column), which the JS golden does not have.
	t.Run(`grid sizes: the extra rows go to the last columns`, func(t *testing.T) { // JS: "grid sizes: the extra rows go to the last columns"
		tests := []struct {
			n    int
			cols int
			rows []int
		}{
			{0, 0, []int{}}, {1, 1, []int{1}}, {2, 2, []int{1, 1}},
			{3, 2, []int{1, 2}}, {4, 2, []int{2, 2}}, {5, 3, []int{1, 2, 2}},
			{6, 3, []int{2, 2, 2}}, {7, 3, []int{1, 3, 3}}, {8, 3, []int{2, 3, 3}}, {9, 3, []int{3, 3, 3}},
		}
		for _, tt := range tests {
			t.Run("n="+strconv.Itoa(tt.n), func(t *testing.T) {
				got := GridSizes(tt.n)
				if got.Cols != tt.cols || len(got.Rows) != len(tt.rows) {
					t.Fatalf("GridSizes(%d)=%+v", tt.n, got)
				}
				for i := range tt.rows {
					if got.Rows[i] != tt.rows[i] {
						t.Fatalf("GridSizes(%d)=%+v, want rows %v", tt.n, got, tt.rows)
					}
				}
			})
		}
	})
}

func TestSplitAnchor(t *testing.T) {
	t.Run(`split anchor: by area, tie to the worker, foreign panes ignored`, func(t *testing.T) { // JS: "split anchor: by area, tie to the worker, foreign panes ignored"
		// Mutation captured: changing area ranking, the caller tie break, or membership filtering selects the wrong pane.
		raw := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":40,"height":50}},{"pane_id":"worker","rect":{"x":40,"y":0,"width":60,"height":100}},{"pane_id":"foreign","rect":{"x":0,"y":0,"width":100,"height":100}}]}}}`
		if got := SplitAnchor([]byte(raw), "caller", []string{"worker"}, 4, .18); got == nil || got.Anchor != "worker" {
			t.Fatalf("SplitAnchor()=%+v, want larger worker pane", got)
		}
	})
	// Mutation captured: reversing capacity, minimum-pane, or side-length rules changes the selected anchor or overflow.
	const raw = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":50,"height":100}},{"pane_id":"worker","rect":{"x":50,"y":0,"width":50,"height":100}},{"pane_id":"foreign","rect":{"x":0,"y":0,"width":100,"height":100}}]}}}`
	tests := []struct {
		name string
		me   string
		mine []string
		cap  int
		min  float64
		want *Anchor
	}{
		{"JS: split anchor chooses worker on equal areas and ignores foreign panes", "caller", []string{"worker"}, 4, .18, &Anchor{Anchor: "worker", Direction: "down"}},
		{"JS: split anchor reports full when candidate count meets cap", "caller", []string{"worker"}, 2, .18, &Anchor{Overflow: "full"}},
		{"JS: split anchor reports min when no pane can be halved", "caller", nil, 4, .60, &Anchor{Overflow: "min"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitAnchor([]byte(raw), tt.me, tt.mine, tt.cap, tt.min)
			if got == nil || *got != *tt.want {
				t.Fatalf("SplitAnchor()=%+v, want %+v", got, tt.want)
			}
		})
	}
	// Mutation captured: changing the width >= height tie rule to > changes the observable split direction.
	t.Run("JS: split direction chooses right when width and height are equal", func(t *testing.T) {
		equal := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":50,"height":50}}]}}}`
		got := SplitAnchor([]byte(equal), "caller", nil, 4, .18)
		if got == nil || *got != (Anchor{Anchor: "caller", Direction: "right"}) {
			t.Fatalf("SplitAnchor()=%+v, want caller/right", got)
		}
	})
	// Mutation captured: treating absent rect dimensions as zero selects a bogus min-overflow candidate.
	t.Run("JS: missing rectangle dimensions skip the candidate", func(t *testing.T) {
		missing := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{}}]}}}`
		if got := SplitAnchor([]byte(missing), "caller", nil, 4, .18); got != nil {
			t.Fatalf("SplitAnchor()=%+v, want no candidate", got)
		}
		neighbor := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":50,"height":100}}]}}}`
		if got := SplitAnchor([]byte(neighbor), "caller", nil, 4, .18); got == nil || got.Anchor != "caller" {
			t.Fatalf("neighbor SplitAnchor()=%+v, want caller", got)
		}
	})
	// Mutation captured: returning raw area zeros instead of inferred bounds changes the split geometry.
	t.Run("JS: layout area is inferred from pane rectangles", func(t *testing.T) {
		missingArea := `{"result":{"layout":{"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":80,"height":40}}]}}}`
		w, h, ok := LayoutArea([]byte(missingArea))
		if !ok || w != 80 || h != 40 {
			t.Fatalf("LayoutArea()=(%v,%v,%v), want (80,40,true)", w, h, ok)
		}
	})
}

func TestSplitAnchorKeepsInfiniteXForTieBreak(t *testing.T) {
	// Mutation captured: coercing Infinity to zero makes the pane at x=Infinity beat the finite leftmost pane.
	infX := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"a","rect":{"x":10,"y":0,"width":50,"height":40}},{"pane_id":"b","rect":{"x":1e309,"y":0,"width":50,"height":40}}]}}}`
	if got := SplitAnchor([]byte(infX), "none", []string{"a", "b"}, 4, .18); got == nil || got.Anchor != "a" {
		t.Fatalf("SplitAnchor(infinite x)=%+v, want anchor a", got)
	}
	finiteX := strings.Replace(infX, `1e309`, `1`, 1)
	if got := SplitAnchor([]byte(finiteX), "none", []string{"a", "b"}, 4, .18); got == nil || got.Anchor != "b" {
		t.Fatalf("SplitAnchor(finite x)=%+v, want anchor b", got)
	}
}

func TestSplitAnchorKeepsInfiniteYForTieBreak(t *testing.T) {
	// Mutation captured: coercing +Infinity to zero (Number(r.y) || 0 keeps it in the JS) makes the pane at y=Infinity beat the finite topmost pane.
	infY := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"a","rect":{"x":0,"y":10,"width":50,"height":40}},{"pane_id":"b","rect":{"x":0,"y":1e309,"width":50,"height":40}}]}}}`
	if got := SplitAnchor([]byte(infY), "none", []string{"a", "b"}, 4, .18); got == nil || got.Anchor != "a" {
		t.Fatalf("SplitAnchor(infinite y)=%+v, want anchor a", got)
	}
	finiteY := strings.Replace(infY, `1e309`, `1`, 1)
	if got := SplitAnchor([]byte(finiteY), "none", []string{"a", "b"}, 4, .18); got == nil || got.Anchor != "b" {
		t.Fatalf("SplitAnchor(finite y)=%+v, want anchor b", got)
	}
}

func TestSplitLimitsFollowConfiguration(t *testing.T) {
	t.Run(`splitCap/splitMin: validation and the lanes-on follows-panes rule`, func(t *testing.T) { // JS: "splitCap/splitMin: validation and the lanes-on follows-panes rule"
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		if got := SplitCap(ctx, platform.Env{}); got != 5 {
			t.Fatalf("default cap=%d, want 5", got)
		}
		if got := SplitMin(ctx, platform.Env{}); got != .18 {
			t.Fatalf("default minimum=%v, want .18", got)
		}
		if got := SplitCap(ctx, platform.Env{"SOHO_PANES": "3"}); got != 4 {
			t.Fatalf("lanes-on cap=%d, want 4 (1 + the lanes sum 3)", got)
		}
		if got := SplitCap(ctx, platform.Env{"SOHO_PANES": "3", "SOHO_PANE_MODE": "flex", "SOHO_FLEX_EXTRA": "2"}); got != 6 {
			t.Fatalf("flex cap=%d, want 6 (1 + lanes 3 + flex 2)", got)
		}
		if got := SplitCap(ctx, platform.Env{"SOHO_LANES": "off", "SOHO_SPLIT_MAX_PANES": "6"}); got != 6 {
			t.Fatalf("explicit cap=%d, want 6", got)
		}
		if got := SplitMin(ctx, platform.Env{"SOHO_SPLIT_MIN_PANE": "invalid"}); got != .18 {
			t.Fatalf("invalid minimum=%v, want default .18", got)
		}
	})
}

func TestSplitCapFollowsTheWholeTeam(t *testing.T) {
	// A9: with lanes on and no explicit split_max_panes the cap is the caller
	// plus the whole team (1 + the effective max_workers), so the team fits in
	// the caller's tab; max_workers=0 keeps the old panes rule.
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	t.Run(`panes=4 strict without max_workers: the lanes sum (4) plus the caller`, func(t *testing.T) { // A9: the cap follows the team, not panes
		if got := SplitCap(ctx, platform.Env{"SOHO_PANES": "4"}); got != 5 {
			t.Fatalf("cap=%d, want 5 (1 + lanes sum 4)", got)
		}
	})
	t.Run(`flex with flex_extra=1 without max_workers`, func(t *testing.T) {
		if got := SplitCap(ctx, platform.Env{"SOHO_PANE_MODE": "flex", "SOHO_FLEX_EXTRA": "1"}); got != 6 {
			t.Fatalf("flex cap=%d, want 6 (1 + lanes 4 + flex 1)", got)
		}
	})
	t.Run(`an explicit max_workers=5 in flex is the whole team`, func(t *testing.T) {
		if got := SplitCap(ctx, platform.Env{"SOHO_MAX_WORKERS": "5", "SOHO_PANE_MODE": "flex", "SOHO_FLEX_EXTRA": "1"}); got != 6 {
			t.Fatalf("cap=%d, want 6 (1 + max_workers 5)", got)
		}
	})
	t.Run(`max_workers=0 falls back to the panes rule`, func(t *testing.T) {
		if got := SplitCap(ctx, platform.Env{"SOHO_MAX_WORKERS": "0"}); got != 4 {
			t.Fatalf("no-cap strict=%d, want 4 (panes)", got)
		}
		if got := SplitCap(ctx, platform.Env{"SOHO_MAX_WORKERS": "0", "SOHO_PANE_MODE": "flex", "SOHO_FLEX_EXTRA": "1"}); got != 5 {
			t.Fatalf("no-cap flex=%d, want 5 (panes + flex)", got)
		}
	})
	t.Run(`an explicit split_max_panes wins`, func(t *testing.T) {
		if got := SplitCap(ctx, platform.Env{"SOHO_SPLIT_MAX_PANES": "3"}); got != 3 {
			t.Fatalf("explicit cap=%d, want 3", got)
		}
	})
	t.Run(`lanes off keeps today's rule`, func(t *testing.T) {
		if got := SplitCap(ctx, platform.Env{"SOHO_LANES": "off"}); got != 4 {
			t.Fatalf("lanes-off cap=%d, want 4 (default)", got)
		}
		if got := SplitCap(ctx, platform.Env{"SOHO_LANES": "off", "SOHO_SPLIT_MAX_PANES": "6"}); got != 6 {
			t.Fatalf("lanes-off explicit=%d, want 6", got)
		}
	})
}

func TestSplitAnchorCapacityAndMinimumCases(t *testing.T) {
	const raw = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":50,"height":100}},{"pane_id":"worker","rect":{"x":50,"y":0,"width":50,"height":100}},{"pane_id":"foreign","rect":{"x":0,"y":0,"width":100,"height":100}}]}}}`
	t.Run(`split anchor: per-tab capacity`, func(t *testing.T) { // JS: "split anchor: per-tab capacity"
		got := SplitAnchor([]byte(raw), "caller", []string{"worker"}, 2, .18)
		if got == nil || got.Overflow != "full" {
			t.Fatalf("SplitAnchor()=%+v, want full", got)
		}
	})
	t.Run(`split anchor: minimum pane`, func(t *testing.T) { // JS: "split anchor: minimum pane"
		got := SplitAnchor([]byte(raw), "caller", nil, 4, .60)
		if got == nil || got.Overflow != "min" {
			t.Fatalf("SplitAnchor()=%+v, want min", got)
		}
	})
}
