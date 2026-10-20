package layout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestAutoPaneColumnsShape(t *testing.T) {
	wants := [][]int{{1}, {1, 1}, {1, 2}, {1, 3}, {1, 1, 3}, {1, 2, 3}, {1, 3, 3}, {1, 1, 3, 3}, {1, 2, 3, 3}, {1, 3, 3, 3}, {1, 1, 3, 3, 3}, {1, 2, 3, 3, 3}, {1, 3, 3, 3, 3}, {1, 1, 3, 3, 3, 3}, {1, 2, 3, 3, 3, 3}, {1, 3, 3, 3, 3, 3}, {1, 3, 3, 3, 3, 4}}
	for n, want := range wants {
		for _, overflow := range []string{"last-column", "new-column", "tab"} {
			t.Run(strconv.Itoa(n)+"/"+overflow, func(t *testing.T) {
				expected := want
				if n == 16 && overflow == "new-column" {
					expected = []int{1, 3, 3, 3, 3, 3, 1}
				}
				got := autoGrid(n, 3, overflow)
				if got.Cols != len(expected) || !reflect.DeepEqual(got.Rows, expected) {
					t.Fatalf("n=%d overflow=%s shape=%+v want=%v", n, overflow, got, expected)
				}
			})
		}
	}
	for _, tc := range []struct {
		n, rows int
		want    []int
	}{
		{0, 1, []int{1}}, {4, 1, []int{1, 1, 1, 1, 1}}, {5, 1, []int{1, 1, 1, 1, 1, 1}}, {6, 1, []int{1, 1, 1, 1, 1, 2}},
		{3, 2, []int{1, 1, 2}}, {4, 2, []int{1, 2, 2}}, {10, 2, []int{1, 2, 2, 2, 2, 2}}, {11, 2, []int{1, 2, 2, 2, 2, 3}},
		{10, 9, []int{1, 1, 9}}, {45, 9, []int{1, 9, 9, 9, 9, 9}}, {46, 9, []int{1, 9, 9, 9, 9, 10}},
	} {
		if got := autoGrid(tc.n, tc.rows, "last-column"); !reflect.DeepEqual(got.Rows, tc.want) || got.Cols != len(tc.want) {
			t.Fatalf("n=%d N=%d got=%+v want=%v", tc.n, tc.rows, got, tc.want)
		}
	}
}

func TestAutoPaneColumnsCapacityAndFallback(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	for _, raw := range []string{"", "0", "10", "x", "03"} {
		env := platform.Env{"SOHO_PANE_COLUMNS": "auto", "SOHO_PANE_COLUMN_ROWS": raw, "SOHO_PANE_OVERFLOW": "tab"}
		cols, _, ok := PaneColumns(ctx, env)
		cap, unlimited := SplitCapDeclared(ctx, env)
		if !ok || !reflect.DeepEqual(cols, []int{1, 3, 3, 3, 3, 3}) || cap != 16 || unlimited || PaneColumnRows(ctx, env) != 3 {
			t.Fatalf("raw=%q cols=%v cap=%d unlimited=%v", raw, cols, cap, unlimited)
		}
	}
	for _, mode := range []string{"last-column", "new-column", "tab"} {
		env := platform.Env{"SOHO_PANE_COLUMNS": "auto", "SOHO_PANE_COLUMN_ROWS": "2", "SOHO_PANE_OVERFLOW": mode}
		cap, unlimited := SplitCapDeclared(ctx, env)
		if mode == "tab" && (cap != 11 || unlimited) || mode != "tab" && (cap != 0 || !unlimited) {
			t.Fatalf("mode=%s cap=%d unlimited=%v", mode, cap, unlimited)
		}
		env["SOHO_SPLIT_MAX_PANES"] = "7"
		if cap, unlimited := SplitCapDeclared(ctx, env); cap != 7 || unlimited {
			t.Fatalf("explicit cap=%d unlimited=%v", cap, unlimited)
		}
	}
}

func TestCmdRegridAutoPaneColumnsArrivalOrder(t *testing.T) {
	// Live topology deliberately reverses roster order. Repeated regrids
	// model a spawn (4 -> 5), a release (5 -> 4), and manual regrid=off.
	panes := []map[string]string{{"pane_id": "C", "tab_id": "t0"}}
	for i := 5; i >= 1; i-- {
		panes = append(panes, map[string]string{"pane_id": "p" + strconv.Itoa(i), "tab_id": "t0"})
	}
	raw, err := json.Marshal(map[string]any{"result": map[string]any{"panes": panes}})
	if err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: string(raw)},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"pane", "move"}, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"tab_id":"park"}}}}`},
	}
	env, ctx, cwd, dir := regridDeclaredFixture(t, platform.Env{"SOHO_PANE_COLUMNS": "auto", "SOHO_REGRID": "off"}, "", "", rules)
	beforeCalls := 0
	for _, workers := range []int{4, 5, 4} {
		t.Run(strconv.Itoa(workers), func(t *testing.T) {
			roster := ""
			for i := 1; i <= workers; i++ {
				roster += "w" + strconv.Itoa(i) + "\tp" + strconv.Itoa(i) + "\tgrok\timplementer\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(roster), 0600); err != nil {
				t.Fatal(err)
			}
			out, calls := runCmdRegridCaptured(t, ctx, env, cwd)
			newCalls := calls[beforeCalls:]
			beforeCalls = len(calls)
			// Left column gets newest p4(/p5); oldest p1,p2,p3 stay right.
			want := [][]string{
				{"pane", "move", "p4", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"},
				{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"},
			}
			if workers == 5 {
				want = append(want, []string{"pane", "move", "p5", "--tab", "t0", "--split", "down", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"})
			}
			want = append(want, []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.3333", "--no-focus"}, []string{"pane", "move", "p3", "--tab", "t0", "--split", "down", "--target-pane", "p2", "--ratio", "0.5000", "--no-focus"})
			if !reflect.DeepEqual(callerTabMoves(newCalls), want) {
				t.Fatalf("moves=%q want=%q out=%s", callerTabMoves(newCalls), want, out)
			}
			if !strings.Contains(out, `"cols":3`) {
				t.Fatalf("out=%s", out)
			}
			body, err := os.ReadFile(filepath.Join(dir, "agents.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != roster {
				t.Fatalf("regrid changed arrival order: %q want %q", body, roster)
			}
		})
	}
}

func TestCmdRegridAutoPaneColumnsHerdArrivalOrder(t *testing.T) {
	initial := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p3","tab_id":"t0"},{"pane_id":"p4","tab_id":"t0"},{"pane_id":"p1","tab_id":"herd"},{"pane_id":"p2","tab_id":"herd"}]}}`
	after := strings.ReplaceAll(initial, `"tab_id":"herd"`, `"tab_id":"t0"`)
	rules := []fakecli.Rule{
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: initial},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: after},
		{Argv: []string{"tab", "get", "herd"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"pane", "move"}, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"tab_id":"park"}}}}`},
	}
	roster := "w1\tp1\tgrok\timplementer\nw2\tp2\tgrok\timplementer\nw3\tp3\tgrok\timplementer\nw4\tp4\tgrok\timplementer\n"
	env, ctx, cwd, _ := regridDeclaredFixture(t, platform.Env{"SOHO_PANE_COLUMNS": "auto"}, roster, "herd\therd\tmanual\n", rules)
	_, calls := runCmdRegridCaptured(t, ctx, env, cwd)
	want := [][]string{
		{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus"},
		{"pane", "move", "p2", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus"},
		{"pane", "move", "p4", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"},
		{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"},
		{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.3333", "--no-focus"},
		{"pane", "move", "p3", "--tab", "t0", "--split", "down", "--target-pane", "p2", "--ratio", "0.5000", "--no-focus"},
	}
	if !reflect.DeepEqual(callerTabMoves(calls), want) {
		t.Fatalf("merged roster arrival moves=%q want=%q", callerTabMoves(calls), want)
	}
}
