package layout

// DJA-393: the declared caller-tab columns.
//
// pane_columns declares the column shape for the caller's tab (a row count
// per column, filled left to right, caller first) and pane_overflow decides
// what happens when the declared slots are all full. The empty columns value
// is today's auto-sized square grid; every consumer of this file falls back
// to that shape, so the feature degrades to the current behavior when the
// key is absent, empty or invalid.

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

const (
	// MaxDeclaredColumns caps the number of declared columns (a 6-column
	// tab is already wide enough for every realistic team).
	MaxDeclaredColumns = 6
	// MaxDeclaredRows caps one declared column (a 9-row stack is the
	// deepest a terminal can show sensibly).
	MaxDeclaredRows = 9
	// DefaultPaneOverflow is what pane_overflow resolves to when unset.
	DefaultPaneOverflow = "last-column"
)

// declaredColumnsRE is the pane_columns contract: 1 to 6 single-digit row
// counts (1 to 9) separated by commas, no spaces (the same regex the
// config value validation uses, so an invalid value is treated as empty —
// the default grid — in every consumer).
var declaredColumnsRE = regexp.MustCompile(`^[1-9](,[1-9]){0,5}$`)

// declaredCols parses pane_columns into a row count per column. Empty and
// invalid values parse to nil: the caller tab falls back to the default
// grid.
func declaredCols(raw string) []int {
	if raw == "" || !declaredColumnsRE.MatchString(raw) {
		return nil
	}
	cols := []int{}
	for _, part := range strings.Split(raw, ",") {
		n, _ := strconv.Atoi(part)
		cols = append(cols, n)
	}
	return cols
}

// PaneColumns resolves the declared caller-tab shape: the row counts, the
// overflow mode (last-column is the default) and whether the value was
// actually declared (a non-empty, valid pane_columns).
func PaneColumns(ctx *core.Config, env platform.Env) ([]int, string, bool) {
	cols := declaredCols(core.Cfg(ctx, "pane_columns", "", env))
	overflow := core.Cfg(ctx, "pane_overflow", DefaultPaneOverflow, env)
	if overflow != "last-column" && overflow != "new-column" && overflow != "tab" {
		overflow = DefaultPaneOverflow
	}
	return cols, overflow, cols != nil
}

// DeclaredGrid builds the grid for n cells under the declared column shape.
// Cells fill the columns left to right, caller first: each column takes its
// declared rows until the cells run out, and a column that runs out early is
// dropped. Under last-column the last column keeps growing beyond its
// declared count; under new-column the excess cells open new columns right
// of the last one, each holding the last declared column's row count (the
// final new column holds the remainder); under tab there is no excess — the
// caller never holds more than the declared total (the shape is capped at
// the total; the overflow never enters the caller tab).
func DeclaredGrid(cols []int, overflow string, n int) Grid {
	if n < 1 {
		return Grid{Rows: []int{}}
	}
	if len(cols) == 0 {
		return GridSizes(n)
	}
	rows := []int{}
	remaining := n
	for _, count := range cols {
		if remaining == 0 {
			break
		}
		take := min(count, remaining)
		rows = append(rows, take)
		remaining -= take
	}
	if remaining == 0 {
		return Grid{Cols: len(rows), Rows: rows}
	}
	switch overflow {
	case "new-column":
		// The excess opens new columns right of the last declared one, each
		// with the last declared column's row count (1,2,2 + 4 excess is
		// 1+2+2+2+2, never 1+2+2+1+1).
		lastCol := cols[len(cols)-1]
		for remaining > 0 {
			take := min(lastCol, remaining)
			rows = append(rows, take)
			remaining -= take
		}
	case "last-column":
		// The last declared column keeps growing (1 + an excess of 2 is a
		// column of 3, not two columns of 2).
		rows[len(rows)-1] += remaining
	}
	return Grid{Cols: len(rows), Rows: rows}
}

// SplitCapDeclared resolves the caller-tab cap with the declared shape in
// force. An explicit split_max_panes (env or a non-defaults layer, including
// 0, which keeps today's "a full tab" meaning) always wins. last-column and
// new-column leave the caller tab uncapped (unlimited is true); tab caps it
// at the declared total. Without a valid declaration the cap is exactly
// today's SplitCap.
func SplitCapDeclared(ctx *core.Config, env platform.Env) (int, bool) {
	if core.ConfigExplicit(ctx, "split_max_panes", env) {
		if n, err := strconv.Atoi(core.Cfg(ctx, "split_max_panes", "", env)); err == nil {
			return n, false
		}
	}
	cols, overflow, declared := PaneColumns(ctx, env)
	if !declared {
		return SplitCap(ctx, env), false
	}
	switch overflow {
	case "tab":
		total := 0
		for _, count := range cols {
			total += count
		}
		return total, false
	default: // last-column and new-column: no tab cap.
		return 0, true
	}
}

// SplitAnchorDeclared is SplitAnchor with the declared caller-tab shape in
// force. The candidate parse and the tie-break (largest area, then a
// non-caller over the caller, then top-most, then left-most) are Split
// Anchor's, unchanged, except two declared-shape decisions:
//
//   - while the first declared column holds exactly the caller's row, the
//     caller is excluded from the anchor candidates (it would steal the slot
//     the new worker needs); with more than one row in the first column the
//     caller stays a candidate as today, and with no worker candidate at
//     all the caller is split as today.
//   - under last-column and new-column split_min_pane is ignored (the
//     columns absorb the excess); under tab the minimum still overflows and
//     the full-tab cap is the declared total (the caller still counts).
//
// Without a valid declaration (cols nil) the decision is today's
// SplitAnchor.
func SplitAnchorDeclared(raw []byte, me string, mine []string, cols []int, overflow string, cap int, unlimited bool, min float64) *Anchor {
	if cols == nil {
		return SplitAnchor(raw, me, mine, cap, min)
	}
	doc, err := jsonjs.Parse(raw)
	if err != nil {
		return nil
	}
	areaW, areaH, usable := LayoutArea(raw)
	if !usable {
		return nil
	}
	panes, ok := layoutField(layoutField(layoutField(doc, "result"), "layout"), "panes").([]any)
	if !ok {
		return nil
	}
	type cand struct {
		id               string
		caller           bool
		x, y, w, h, area float64
	}
	var cs []cand
	for _, p := range panes {
		paneID, ok := layoutField(p, "pane_id").(string)
		if !ok {
			continue
		}
		isCaller := paneID == me
		isMine := isCaller
		if !isMine {
			for _, mineID := range mine {
				if paneID == mineID {
					isMine = true
					break
				}
			}
		}
		if !isMine {
			continue
		}
		r := layoutField(p, "rect")
		x, y := jsNumber(layoutField(r, "x")), jsNumber(layoutField(r, "y"))
		wRaw, hRaw := jsNumber(layoutField(r, "width")), jsNumber(layoutField(r, "height"))
		w, h := math.Round(wRaw/areaW*100)/100, math.Round(hRaw/areaH*100)/100
		if math.IsNaN(w) || math.IsInf(w, 0) || math.IsNaN(h) || math.IsInf(h, 0) {
			continue
		}
		if math.IsNaN(x) || x == 0 {
			x = 0
		}
		if math.IsNaN(y) || y == 0 {
			y = 0
		}
		cs = append(cs, cand{paneID, isCaller, x, y, w, h, w * h})
	}
	if len(cs) == 0 {
		return nil
	}
	hasWorker := false
	for _, c := range cs {
		if !c.caller {
			hasWorker = true
			break
		}
	}
	// The exclusion applies only while a worker exists: with the caller
	// alone there is no other anchor and the caller is split as today (the
	// declared shape is then restored by the regrid).
	excludeCaller := me != "" && cols[0] == 1 && hasWorker
	if !unlimited && len(cs) >= cap {
		return &Anchor{Overflow: "full"}
	}
	var best *cand
	for i := range cs {
		c := &cs[i]
		if c.caller && excludeCaller {
			continue
		}
		if overflow == "tab" && math.Max(c.w, c.h)/2 < min {
			continue
		}
		if best == nil || c.area > best.area || c.area == best.area && (best.caller && !c.caller || best.caller == c.caller && (c.y < best.y || c.y == best.y && c.x < best.x)) {
			best = c
		}
	}
	if best == nil {
		return &Anchor{Overflow: "min"}
	}
	direction := "down"
	if best.w >= best.h {
		direction = "right"
	}
	return &Anchor{Anchor: best.id, Direction: direction}
}

// buildGridShape places the caller tab's panes into the given grid. The
// cells order is caller first (the caller's pane in front of the workers);
// the moves are buildGrid's, driven by the given rows instead of the square
// ones.
func buildGridShape(tab string, cells []string, shape Grid, env platform.Env) gridResult {
	g := shape
	ids := append([]string(nil), cells...)
	heads := []int{}
	idx := 0
	for _, n := range g.Rows {
		heads = append(heads, idx)
		idx += n
	}
	changes := [][2]string{}
	moved := []string{}
	note := func(i int, m herdr.PaneMoveResult) {
		if m.Pane != "" && m.Pane != ids[i] {
			changes = append(changes, [2]string{ids[i], m.Pane})
			ids[i] = m.Pane
		}
	}
	if len(cells) < 1 || len(g.Rows) < 1 {
		return gridResult{Ok: false, Changes: changes, Moved: moved}
	}
	prev := ids[0]
	for c := 1; c < g.Cols; c++ {
		i := heads[c]
		m := moveInTab(ids[i], tab, "right", prev, ratio4(g.Cols-c+1), env)
		if !m.Ok {
			return gridResult{Ok: false, Changes: changes, Moved: moved}
		}
		moved = append(moved, ids[i])
		note(i, m)
		prev = ids[i]
	}
	for c, n := range g.Rows {
		idx := heads[c]
		prev = ids[idx]
		for j := 1; j < n; j++ {
			idx++
			m := moveInTab(ids[idx], tab, "down", prev, ratio4(n-j+1), env)
			if !m.Ok {
				return gridResult{Ok: false, Changes: changes, Moved: moved}
			}
			moved = append(moved, ids[idx])
			note(idx, m)
			prev = ids[idx]
		}
	}
	return gridResult{Ok: true, Changes: changes, Moved: moved}
}

// gridApplyShape runs the shape's moves and, after every successful move —
// even when a later move fails — records the returned pane ids in the
// roster, exactly as gridApply does for the square grid.
func gridApplyShape(ctx *core.Config, tab string, cells []string, shape Grid, env platform.Env, cwd string) ([][2]string, []string, bool) {
	r := buildGridShape(tab, cells, shape, env)
	dir := core.StateDirPath(ctx, env, cwd)
	for _, change := range r.Changes {
		core.RosterReplacePane(dir, change[0], change[1])
	}
	return r.Changes, r.Moved, r.Ok
}
