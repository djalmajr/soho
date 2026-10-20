package layout

// DJA-393 L1: declared caller-tab columns (pane_columns, pane_overflow).
// The caller (the orchestrator) sits alone in its first declared column and
// the workers fill the remaining slots left to right, top to bottom. With an
// empty pane_columns everything is exactly today's behavior (GridSizes,
// SplitCap, SplitAnchor) — pinned by the pre-existing tests and the frozen
// goldens, which this file never touches.

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func columnsOf(raw string) []int {
	cols := []int{}
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == ',' {
			n, err := strconv.Atoi(raw[start:i])
			if err != nil {
				panic("bad fixture " + raw)
			}
			cols = append(cols, n)
			start = i + 1
		}
	}
	return cols
}

func shapeOf(t *testing.T, cols, overflow string, n int) Grid {
	t.Helper()
	return DeclaredGrid(columnsOf(cols), overflow, n)
}

func TestPaneOverflowBalanceExamples(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		workers int
		want    []int
	}{
		{"1,2,3", 6, []int{1, 3, 3}},
		{"1,2,3", 7, []int{1, 3, 4}},
		{"1,2,3", 8, []int{1, 4, 4}},
		{"1,3,3", 4, []int{1, 3, 1}},
		{"2,2", 4, []int{2, 3}},
		{"auto", 16, []int{1, 3, 3, 3, 3, 4}},
		{"3", 2, []int{3}},
		{"3", 3, []int{3, 1}},
		{"3", 4, []int{3, 2}},
		{"3", 5, []int{3, 3}},
		{"3", 6, []int{3, 3, 1}},
		{"3", 17, []int{3, 3, 3, 3, 3, 3}},
		{"3", 18, []int{3, 3, 3, 3, 3, 4}},
	} {
		t.Run(tc.mode+"/"+strconv.Itoa(tc.workers), func(t *testing.T) {
			for _, overflow := range []string{"", "balance"} {
				env := platform.Env{"SOHO_PANE_COLUMNS": tc.mode, "SOHO_PANE_COLUMN_ROWS": "3"}
				if overflow != "" {
					env["SOHO_PANE_OVERFLOW"] = overflow
				}
				ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
				got := GridShapeFor(ctx, env, tc.workers+1)
				if got.Cols != len(tc.want) || !reflect.DeepEqual(got.Rows, tc.want) {
					t.Fatalf("overflow=%q shape=%+v want=%v", overflow, got, tc.want)
				}
			}
		})
	}
}

func TestPaneOverflowBalanceProperty(t *testing.T) {
	autoBeforeCap := [][]int{{1}, {1, 1}, {1, 2}, {1, 3}, {1, 1, 3}, {1, 2, 3}, {1, 3, 3}, {1, 1, 3, 3}, {1, 2, 3, 3}, {1, 3, 3, 3}, {1, 1, 3, 3, 3}, {1, 2, 3, 3, 3}, {1, 3, 3, 3, 3}, {1, 1, 3, 3, 3, 3}, {1, 2, 3, 3, 3, 3}, {1, 3, 3, 3, 3, 3}}
	for _, mode := range []string{"1,2,3", "1,3,3", "2,2", "auto", "1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		t.Run(mode, func(t *testing.T) {
			cols := []int{1, 3, 3, 3, 3, 3}
			if mode != "auto" {
				cols = columnsOf(mode)
			}
			capacity := 0
			for _, n := range cols {
				capacity += n
			}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
			env := platform.Env{"SOHO_PANE_COLUMNS": mode, "SOHO_PANE_COLUMN_ROWS": "3", "SOHO_PANE_OVERFLOW": "balance"}
			lastWorker := 30
			if len(cols) == 1 {
				lastWorker = max(lastWorker, 6*cols[0]+5)
			}
			for workers := 0; workers <= lastWorker; workers++ {
				got := GridShapeFor(ctx, env, workers+1)
				total := 0
				for _, n := range got.Rows {
					total += n
				}
				if total != workers+1 || got.Cols != len(got.Rows) {
					t.Fatalf("workers=%d invalid shape=%+v", workers, got)
				}
				if got.Cols > 6 || got.Rows[0] > cols[0] {
					t.Fatalf("workers=%d exceeded column/caller bounds: cols=%v got=%+v", workers, cols, got)
				}
				if workers+1 <= capacity {
					want := DeclaredGrid(cols, "last-column", workers+1).Rows
					if mode == "auto" {
						want = autoBeforeCap[workers]
					}
					if !reflect.DeepEqual(got.Rows, want) {
						t.Fatalf("workers=%d changed declared fill: got=%v want=%v", workers, got.Rows, want)
					}
					continue
				}
				previous := GridShapeFor(ctx, env, workers).Rows
				openWorkerColumn := len(cols) == 1 && len(previous) < 6
				for c := 1; c < len(previous); c++ {
					if previous[c] < cols[0] {
						openWorkerColumn = false
					}
				}
				if openWorkerColumn {
					if len(got.Rows) != len(previous)+1 || !reflect.DeepEqual(got.Rows[:len(previous)], previous) || got.Rows[len(previous)] != 1 {
						t.Fatalf("workers=%d must open one worker column to the right: %v -> %v", workers, previous, got.Rows)
					}
					continue
				}
				if len(got.Rows) != len(previous) || got.Rows[0] != previous[0] {
					t.Fatalf("workers=%d caller/columns changed: before=%v after=%v", workers, previous, got.Rows)
				}
				changed := -1
				for c := range previous {
					if got.Rows[c] == previous[c] {
						continue
					}
					if changed != -1 || got.Rows[c] != previous[c]+1 {
						t.Fatalf("workers=%d not one addition: %v -> %v", workers, previous, got.Rows)
					}
					changed = c
				}
				if changed < 1 {
					t.Fatalf("workers=%d no worker column grew: %v -> %v", workers, previous, got.Rows)
				}
				for c := 1; c < len(previous); c++ {
					if previous[c] < previous[changed] || previous[c] == previous[changed] && c > changed {
						t.Fatalf("workers=%d selected col=%d despite emptier/rightmost col=%d: %v -> %v", workers, changed, c, previous, got.Rows)
					}
				}
			}
		})
	}
}

func TestDeclaredGridShapeTable(t *testing.T) {
	t.Run(`declared shape: 1,2,3, 1,2,2 and 1,1,2 for n 0 to 9 in all three overflow modes`, func(t *testing.T) {
		// The slots fill left to right, top to bottom; columns without a pane
		// do not appear. Beyond the declared total, last-column grows the
		// last column, new-column opens columns to the right with the last
		// declared column's rows, and tab never passes the declared total
		// (the excess does not enter the caller tab).
		tests := []struct {
			cols, overflow string
			n              int
			want           []int
		}{
			{"1,2,3", "last-column", 0, nil},
			{"1,2,3", "last-column", 1, []int{1}},
			{"1,2,3", "last-column", 2, []int{1, 1}},
			{"1,2,3", "last-column", 3, []int{1, 2}},
			{"1,2,3", "last-column", 4, []int{1, 2, 1}},
			{"1,2,3", "last-column", 5, []int{1, 2, 2}},
			{"1,2,3", "last-column", 6, []int{1, 2, 3}},
			{"1,2,3", "last-column", 7, []int{1, 2, 4}},
			{"1,2,3", "last-column", 8, []int{1, 2, 5}},
			{"1,2,3", "last-column", 9, []int{1, 2, 6}},
			{"1,2,3", "new-column", 0, nil},
			{"1,2,3", "new-column", 1, []int{1}},
			{"1,2,3", "new-column", 2, []int{1, 1}},
			{"1,2,3", "new-column", 3, []int{1, 2}},
			{"1,2,3", "new-column", 4, []int{1, 2, 1}},
			{"1,2,3", "new-column", 5, []int{1, 2, 2}},
			{"1,2,3", "new-column", 6, []int{1, 2, 3}},
			{"1,2,3", "new-column", 7, []int{1, 2, 3, 1}},
			{"1,2,3", "new-column", 8, []int{1, 2, 3, 2}},
			{"1,2,3", "new-column", 9, []int{1, 2, 3, 3}},
			{"1,2,3", "tab", 0, nil},
			{"1,2,3", "tab", 1, []int{1}},
			{"1,2,3", "tab", 4, []int{1, 2, 1}},
			{"1,2,3", "tab", 6, []int{1, 2, 3}},
			{"1,2,3", "tab", 7, []int{1, 2, 4}}, // past the total (explicit cap): the last column grows
			{"1,2,3", "tab", 9, []int{1, 2, 6}},
			{"1,2,2", "last-column", 0, nil},
			{"1,2,2", "last-column", 1, []int{1}},
			{"1,2,2", "last-column", 2, []int{1, 1}},
			{"1,2,2", "last-column", 3, []int{1, 2}},
			{"1,2,2", "last-column", 4, []int{1, 2, 1}},
			{"1,2,2", "last-column", 5, []int{1, 2, 2}},
			{"1,2,2", "last-column", 6, []int{1, 2, 3}},
			{"1,2,2", "last-column", 7, []int{1, 2, 4}},
			{"1,2,2", "last-column", 8, []int{1, 2, 5}},
			{"1,2,2", "last-column", 9, []int{1, 2, 6}},
			{"1,2,2", "new-column", 0, nil},
			{"1,2,2", "new-column", 4, []int{1, 2, 1}},
			{"1,2,2", "new-column", 5, []int{1, 2, 2}},
			{"1,2,2", "new-column", 6, []int{1, 2, 2, 1}},
			{"1,2,2", "new-column", 7, []int{1, 2, 2, 2}},
			{"1,2,2", "new-column", 8, []int{1, 2, 2, 2, 1}},
			{"1,2,2", "new-column", 9, []int{1, 2, 2, 2, 2}},
			{"1,2,2", "tab", 0, nil},
			{"1,2,2", "tab", 4, []int{1, 2, 1}},
			{"1,2,2", "tab", 5, []int{1, 2, 2}},
			{"1,2,2", "tab", 7, []int{1, 2, 4}},
			{"1,2,2", "tab", 9, []int{1, 2, 6}},
			{"1,1,2", "last-column", 0, nil},
			{"1,1,2", "last-column", 1, []int{1}},
			{"1,1,2", "last-column", 2, []int{1, 1}},
			{"1,1,2", "last-column", 3, []int{1, 1, 1}},
			{"1,1,2", "last-column", 4, []int{1, 1, 2}},
			{"1,1,2", "last-column", 5, []int{1, 1, 3}},
			{"1,1,2", "last-column", 6, []int{1, 1, 4}},
			{"1,1,2", "last-column", 7, []int{1, 1, 5}},
			{"1,1,2", "last-column", 8, []int{1, 1, 6}},
			{"1,1,2", "last-column", 9, []int{1, 1, 7}},
			{"1,1,2", "new-column", 0, nil},
			{"1,1,2", "new-column", 3, []int{1, 1, 1}},
			{"1,1,2", "new-column", 4, []int{1, 1, 2}},
			{"1,1,2", "new-column", 5, []int{1, 1, 2, 1}},
			{"1,1,2", "new-column", 6, []int{1, 1, 2, 2}},
			{"1,1,2", "new-column", 7, []int{1, 1, 2, 2, 1}},
			{"1,1,2", "new-column", 8, []int{1, 1, 2, 2, 2}},
			{"1,1,2", "new-column", 9, []int{1, 1, 2, 2, 2, 1}},
			{"1,1,2", "tab", 0, nil},
			{"1,1,2", "tab", 3, []int{1, 1, 1}},
			{"1,1,2", "tab", 4, []int{1, 1, 2}},
			{"1,1,2", "tab", 6, []int{1, 1, 4}},
			{"1,1,2", "tab", 9, []int{1, 1, 7}},
		}
		for _, tt := range tests {
			got := shapeOf(t, tt.cols, tt.overflow, tt.n)
			if tt.n == 0 {
				if got.Cols != 0 || len(got.Rows) != 0 {
					t.Fatalf("DeclaredGrid(%s, %s, 0)=%+v, want the empty grid, as GridSizes(0)", tt.cols, tt.overflow, got)
				}
				continue
			}
			if got.Cols != len(tt.want) || !reflect.DeepEqual(got.Rows, tt.want) {
				t.Fatalf("DeclaredGrid(%s, %s, %d)=%+v, want cols %d rows %v", tt.cols, tt.overflow, tt.n, got, len(tt.want), tt.want)
			}
			if tt.n >= 1 {
				sum := 0
				for _, r := range got.Rows {
					sum += r
				}
				if sum != tt.n {
					t.Fatalf("DeclaredGrid(%s, %s, %d) rows sum %d, want %d (every pane must be placed)", tt.cols, tt.overflow, tt.n, sum, tt.n)
				}
			}
		}
	})
	t.Run(`declared shape: empty or invalid pane_columns resolves to today's grid`, func(t *testing.T) {
		// Empty is the default; an invalid value coming from the environment
		// or a hand-edited file is treated as empty (the default grid).
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		if cols, overflow, ok := PaneColumns(ctx, platform.Env{}); ok {
			t.Fatalf("empty pane_columns resolved: cols=%v overflow=%q", cols, overflow)
		}
		if _, _, ok := PaneColumns(ctx, platform.Env{"SOHO_PANE_COLUMNS": "1,,2"}); ok {
			t.Fatal("an invalid pane_columns resolved instead of falling back to the default grid")
		}
		cols, overflow, ok := PaneColumns(ctx, platform.Env{"SOHO_PANE_COLUMNS": "1,2,3"})
		if !ok || !reflect.DeepEqual(cols, []int{1, 2, 3}) || overflow != "balance" {
			t.Fatalf("valid pane_columns: cols=%v overflow=%q ok=%v, want [1 2 3] balance", cols, overflow, ok)
		}
		if _, overflow, _ = PaneColumns(ctx, platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "tab"}); overflow != "tab" {
			t.Fatalf("overflow=%q, want tab", overflow)
		}
		// An invalid pane_overflow falls back to the default balance.
		if _, overflow, _ = PaneColumns(ctx, platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "side"}); overflow != "balance" {
			t.Fatalf("invalid overflow must fall back to balance, got %q", overflow)
		}
	})
}

func TestBuildGridDeclaredShapeMoveSequence(t *testing.T) {
	t.Run(`buildGrid with the declared shape: the exact pane-move sequence for 1,2,3 with 6 and 4 panes`, func(t *testing.T) {
		// Mutation captured: computing the shape with GridSizes instead of
		// the declared one, or reordering the column heads before the rows,
		// changes the recorded move sequence.
		tests := []struct {
			cells int
			shape Grid
			want  [][]string
		}{
			{6, Grid{Cols: 3, Rows: []int{1, 2, 3}}, [][]string{
				{"p2", "right", "p1", "0.3333"},
				{"p4", "right", "p2", "0.5000"},
				{"p3", "down", "p2", "0.5000"},
				{"p5", "down", "p4", "0.3333"},
				{"p6", "down", "p5", "0.5000"},
			}},
			{4, Grid{Cols: 3, Rows: []int{1, 2, 1}}, [][]string{
				{"p2", "right", "p1", "0.3333"},
				{"p4", "right", "p2", "0.5000"},
				{"p3", "down", "p2", "0.5000"},
			}},
		}
		for _, tt := range tests {
			t.Run(strconv.Itoa(tt.cells), func(t *testing.T) {
				rules := make([]fakecli.Rule, 0, len(tt.want))
				wantArgs := make([][]string, 0, len(tt.want))
				for _, step := range tt.want {
					args := []string{"pane", "move", step[0], "--tab", "t1", "--split", step[1], "--target-pane", step[2], "--ratio", step[3], "--no-focus"}
					rules = append(rules, fakecli.Rule{Argv: args, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"` + step[0] + `"}}}}`})
					wantArgs = append(wantArgs, args)
				}
				env, _, _, _ := layoutFake(t, rules)
				cells := make([]string, tt.cells)
				for i := range cells {
					cells[i] = "p" + strconv.Itoa(i+1)
				}
				if got := buildGridShape("t1", cells, tt.shape, env); !got.Ok || len(got.Changes) != 0 {
					t.Fatalf("buildGridShape result: %#v", got)
				}
				calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				got := make([][]string, len(calls))
				for i := range calls {
					got[i] = calls[i].Argv
				}
				if !reflect.DeepEqual(got, wantArgs) {
					t.Fatalf("calls=%#v want %#v", got, wantArgs)
				}
			})
		}
	})
}

// DJA-393 review: a shape that holds fewer cells than the panes given would
// leave the rest parked; buildGridShape refuses it before any move.
func TestBuildGridShapeRefusesAShapeThatDropsCells(t *testing.T) {
	// The fake accepts the one move the short shape would make, so only the
	// guard can fail the call.
	env, _, _, _ := layoutFake(t, []fakecli.Rule{
		{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1"}}}}`},
	})
	r := buildGridShape("t0", []string{"C", "p1", "p2"}, Grid{Cols: 2, Rows: []int{1, 1}}, env)
	if r.Ok || len(r.Moved) != 0 {
		t.Fatalf("buildGridShape with 2 slots for 3 panes = %+v, want a failure before any move", r)
	}
}
