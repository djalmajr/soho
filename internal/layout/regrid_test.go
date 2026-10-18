package layout

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

func layoutFake(t *testing.T, rules []fakecli.Rule) (platform.Env, *core.Config, string, string) {
	t.Helper()
	// The tests of this package call CmdRegrid directly, without cli.Run
	// (which resets the caller-pane memo): each fixture starts with a
	// clean memo so the call-list assertions never depend on test order
	// (DJA-394).
	herdr.ResetCallerCheck()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	env["HERDR_ENV"], env["HERDR_WORKSPACE_ID"], env["SOHO_DIR"] = "1", "ws", filepath.Join(root, "state")
	env["SOHO_SKILL_DIR"], env["TMPDIR"] = "../../skills/soho", filepath.Join(root, "tmp")
	cwd := root
	ctx := core.LoadConfig(env, cwd)
	dir := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return env, &ctx, cwd, dir
}

func TestBuildGridCallOrder(t *testing.T) {
	t.Run(`buildGrid: the exact pane-move sequence for 2 to 7 cells`, func(t *testing.T) { // JS: "buildGrid: the exact pane-move sequence for 2 to 7 cells"
		// Mutation captured: changing a column head, split direction, target, or ratio changes the recorded move sequence.
		tests := []struct {
			cells int
			want  [][]string
		}{
			{2, [][]string{{"p2", "right", "p1", "0.5000"}}},
			{3, [][]string{{"p2", "right", "p1", "0.5000"}, {"p3", "down", "p2", "0.5000"}}},
			{4, [][]string{{"p3", "right", "p1", "0.5000"}, {"p2", "down", "p1", "0.5000"}, {"p4", "down", "p3", "0.5000"}}},
			{5, [][]string{{"p2", "right", "p1", "0.3333"}, {"p4", "right", "p2", "0.5000"}, {"p3", "down", "p2", "0.5000"}, {"p5", "down", "p4", "0.5000"}}},
			{6, [][]string{{"p3", "right", "p1", "0.3333"}, {"p5", "right", "p3", "0.5000"}, {"p2", "down", "p1", "0.5000"}, {"p4", "down", "p3", "0.5000"}, {"p6", "down", "p5", "0.5000"}}},
			// 7 is 1+3+3 (the caller alone in its column): heads p1, p2, p5.
			{7, [][]string{{"p2", "right", "p1", "0.3333"}, {"p5", "right", "p2", "0.5000"}, {"p3", "down", "p2", "0.3333"}, {"p4", "down", "p3", "0.5000"}, {"p6", "down", "p5", "0.3333"}, {"p7", "down", "p6", "0.5000"}}},
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
				if got := buildGrid("t1", cells, env); !got.Ok || len(got.Changes) != 0 {
					t.Fatalf("buildGrid result: %#v", got)
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

func TestBuildGridNoopAndStopsOnMoveFailure(t *testing.T) {
	t.Run(`buildGrid: one cell is a no-op, a failed move stops the grid`, func(t *testing.T) { // JS: "buildGrid: one cell is a no-op, a failed move stops the grid"
		// Mutation captured: splitting a one-cell grid or continuing after a failed move changes Herdr calls.
		env, _, _, _ := layoutFake(t, []fakecli.Rule{
			{Argv: []string{"pane", "move", "p2", "--tab", "t1", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}, Code: 1},
		})
		if got := buildGrid("t1", []string{"p1"}, env); !got.Ok || len(got.Changes) != 0 {
			t.Fatalf("one-cell buildGrid()=%#v, want successful no-op", got)
		}
		if got := buildGrid("t1", []string{"p1", "p2", "p3"}, env); got.Ok {
			t.Fatalf("failed move buildGrid()=%#v, want failure", got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, []string{"pane", "move", "p2", "--tab", "t1", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}) {
			t.Fatalf("calls=%#v, want exactly the first move", calls)
		}
	})
}

func TestApplyGridUpdatesRosterOnlyAfterSuccessfulMoves(t *testing.T) {
	t.Run(`applyGrid: the roster follows the new pane ids, and nothing changes otherwise`, func(t *testing.T) { // JS: "applyGrid: the roster follows the new pane ids, and nothing changes otherwise"
		// Mutation captured: ignoring returned pane ids or committing a failed grid leaves roster state inconsistent.
		move := []string{"pane", "move", "p2", "--tab", "t1", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}
		env, ctx, cwd, state := layoutFake(t, []fakecli.Rule{
			{Argv: move, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p2-new"}}}}`},
		})
		roster := filepath.Join(state, "agents.tsv")
		if err := os.WriteFile(roster, []byte("worker\tp2\timplementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !applyGrid(ctx, "t1", []string{"p1", "p2"}, env, cwd) {
			t.Fatal("applyGrid() failed")
		}
		got, err := os.ReadFile(roster)
		if err != nil || string(got) != "worker\tp2-new\timplementer\n" {
			t.Fatalf("roster=%q err=%v", got, err)
		}
		failedEnv, failedCtx, failedCwd, failedState := layoutFake(t, []fakecli.Rule{{Argv: move, Code: 1}})
		failedRoster := filepath.Join(failedState, "agents.tsv")
		if err := os.WriteFile(failedRoster, []byte("worker\tp2\timplementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if applyGrid(failedCtx, "t1", []string{"p1", "p2"}, failedEnv, failedCwd) {
			t.Fatal("applyGrid() reported success after a failed move")
		}
		unchanged, err := os.ReadFile(failedRoster)
		if err != nil || string(unchanged) != "worker\tp2\timplementer\n" {
			t.Fatalf("failed roster=%q err=%v", unchanged, err)
		}
	})
}

func TestPullBackRespectsFullTab(t *testing.T) {
	t.Run(`cmdRegrid: the cap stops the pull-back (caller + workers stays within split_max_panes)`, func(t *testing.T) { // JS: "cmdRegrid: the cap stops the pull-back (caller + workers stays within split_max_panes)"
		// Mutation captured: changing the full-tab boundary moves another worker into the caller's tab.
		if canPullBack(2, 4) != true || canPullBack(3, 4) != false {
			t.Fatalf("pull-back capacity boundary is wrong")
		}
	})
}

func TestCmdRegridSplitPullsAndRebuilds(t *testing.T) {
	t.Run(`cmdRegrid: layout=split pulls workers back up to the cap, parks and grids, forgets the emptied tab`, func(t *testing.T) { // JS: "cmdRegrid: layout=split pulls workers back up to the cap, parks and grids, forgets the emptied tab"
		// Mutation captured: skipping pull-back or parking leaves workers split across tabs and stale herd-tab state.
		before := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t1"}]}}`
		after := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: before},
			{Argv: []string{"agent", "get", "caller-worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"agent", "get", "remote-worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p2"}}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: after},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: after},
		}
		env, ctx, cwd, dir := layoutFake(t, rules)
		env["HERDR_TAB_ID"], env["HERDR_PANE_ID"], env["SOHO_SPLIT_MAX_PANES"] = "t0", "C", "4"
		roster := "caller-worker\tp1\tcursor\timplementer\nremote-worker\tp2\tcursor\tscouter\n"
		if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "herd-tab")
		if err := os.WriteFile(file, []byte("t1\therd\tauto\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		defer func() { platform.Stdout = old }()
		if code := CmdRegrid(nil, ctx, env, cwd); code != 0 {
			t.Fatalf("CmdRegrid()=%d", code)
		}
		if out.String() != "{\"regridded\":[{\"tab\":\"t0\",\"label\":\"caller\",\"panes\":3,\"cols\":2}]}\n" {
			t.Fatalf("output=%q", out.String())
		}
		stored, err := os.ReadFile(file)
		if err != nil || len(stored) != 0 {
			calls, _ := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
			t.Fatalf("herd-tab=%q err=%v calls=%#v", stored, err, calls)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		var moves [][]string
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "pane" && call.Argv[1] == "move" {
				moves = append(moves, call.Argv)
			}
		}
		if len(moves) != 5 || !reflect.DeepEqual(moves[0], rules[5].Argv) || !reflect.DeepEqual(moves[3], rules[9].Argv) || !reflect.DeepEqual(moves[4], rules[10].Argv) {
			t.Fatalf("moves=%#v", moves)
		}
	})
}

func TestCmdRegridTabLayoutKeepsAndRebuilds(t *testing.T) {
	t.Run(`cmdRegrid: layout=tab keeps a single-pane herd tab, rebuilds a 3-pane one, forgets an empty one`, func(t *testing.T) { // JS: "cmdRegrid: layout=tab keeps a single-pane herd tab, rebuilds a 3-pane one, forgets an empty one"
		// Mutation captured: retaining an empty tab or skipping the multi-pane rebuild changes the persisted tabs and summary.
		panes := `{"result":{"panes":[{"pane_id":"q1","tab_id":"t1"},{"pane_id":"q2","tab_id":"t2"},{"pane_id":"q3","tab_id":"t2"},{"pane_id":"q4","tab_id":"t2"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list", "--workspace", "ws"}, ArgvPrefix: true, Stdout: panes},
			{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"tab", "get"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-new"},"root_pane":{"pane_id":"r-new"}}}`},
			{Argv: []string{"pane", "move"}, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"moved"}}}}`},
			{Argv: []string{"pane", "close"}, ArgvPrefix: true},
		}
		env, ctx, cwd, dir := layoutFake(t, rules)
		env["SOHO_LAYOUT"] = "tab"
		if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte("solo\tq1\tcursor\timplementer\nb1\tq2\tcursor\timplementer\nb2\tq3\tcursor\treviewer\nb3\tq4\tcursor\timplementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "herd-tab")
		if err := os.WriteFile(file, []byte("t1\tsolo\tmanual\nt2\therd\tmanual\nt3\tempty\tmanual\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		defer func() { platform.Stdout = old }()
		if code := CmdRegrid(nil, ctx, env, cwd); code != 0 {
			t.Fatalf("CmdRegrid()=%d", code)
		}
		if !strings.Contains(out.String(), `"label":"herd","panes":3,"cols":2`) {
			t.Fatalf("regrid summary=%q", out.String())
		}
		stored, err := os.ReadFile(file)
		if err != nil || string(stored) != "t1\tsolo\tmanual\nt-new\therd\tmanual\n" {
			t.Fatalf("herd-tab=%q err=%v", stored, err)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(callsToArgs(calls, "tab", "create", "--workspace", "ws", "--cwd", cwd, "--label", "herd", "--no-focus")) != 1 || len(callsToArgs(calls, "pane", "close", "r-new")) != 1 {
			t.Fatalf("tab rebuild calls=%#v", calls)
		}
	})
}

func TestCmdRegridFailedMoveReportsDieError(t *testing.T) {
	t.Run(`cmdRegrid: a failed move throws DieError 4 with the bash message`, func(t *testing.T) { // JS: "cmdRegrid: a failed move throws DieError 4 with the bash message"
		// Mutation captured: changing the parking failure code or message hides the recovery location for workers.
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"}]}}`},
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"}]}}`},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Code: 1},
		}
		env, ctx, cwd, dir := layoutFake(t, rules)
		env["HERDR_TAB_ID"], env["HERDR_PANE_ID"] = "t0", "C"
		if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte("worker\tp1\tcursor\timplementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			value := recover()
			e, ok := value.(*platform.ExitError)
			if !ok || e.Code != 4 || e.Msg != "regrid: could not park the workers of tab t0 in a temporary tab" {
				t.Fatalf("panic=%#v", value)
			}
		}()
		CmdRegrid(nil, ctx, env, cwd)
	})
}

func callsToArgs(calls []fakecli.Call, args ...string) [][]string {
	var out [][]string
	for _, call := range calls {
		if reflect.DeepEqual(call.Argv, args) {
			out = append(out, call.Argv)
		}
	}
	return out
}

func TestParkPanesTargetsHerdPark(t *testing.T) {
	t.Run(`parkPanes: herd-park tab, the rest split down off the first`, func(t *testing.T) { // JS: "parkPanes: herd-park tab, the rest split down off the first"
		// Mutation captured: changing the parking label or split target leaves temporary panes in an untracked tab.
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
		}
		env, _, _, _ := layoutFake(t, rules)
		if ok, tab := parkPanes([]string{"p1", "p2"}, env); !ok || tab != "park-1" {
			t.Fatalf("parkPanes()=%v,%q", ok, tab)
		}
	})
}

func TestParkPanesFailsClosed(t *testing.T) {
	t.Run(`parkPanes: a failed move or an empty list fails closed`, func(t *testing.T) { // JS: "parkPanes: a failed move or an empty list fails closed"
		// Mutation captured: accepting an empty set or a failed move reports workers safely parked when they are not.
		env, _, _, _ := layoutFake(t, []fakecli.Rule{
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Code: 1},
		})
		if ok, tab := parkPanes(nil, env); ok || tab != "" {
			t.Fatalf("empty parkPanes()=%v,%q", ok, tab)
		}
		if ok, tab := parkPanes([]string{"p1", "p2"}, env); ok || tab != "" {
			t.Fatalf("failed parkPanes()=%v,%q", ok, tab)
		}
	})
}

func TestRestoreFocusReturnsToSource(t *testing.T) {
	t.Run(`restore focus: only the steal, only while the keyboard is still there`, func(t *testing.T) { // JS: "restore focus: only the steal, only while the keyboard is still there"
		// Mutation captured: returning focus when it was not stolen or the user moved breaks keyboard focus ownership.
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"stolen","focused":true}]}}`},
			{Argv: []string{"agent", "focus", "source"}, Code: 1},
			{Argv: []string{"pane", "focus", "--direction", "left", "--pane", "stolen"}},
		}
		env, _, _, _ := layoutFake(t, rules)
		env["HERDR_PANE_ID"] = "source"
		RestoreFocusIfStolen("source", "stolen", "right", env)
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 3 || !reflect.DeepEqual(calls[2].Argv, rules[2].Argv) {
			t.Fatalf("focus calls=%#v", calls)
		}
		t.Run("leave focus alone after movement or without a source", func(t *testing.T) {
			for _, tc := range []struct {
				name, previous, stolen, focused string
				wantCalls                       int
			}{
				{"user moved", "source", "stolen", "other", 1},
				{"empty source", "", "stolen", "stolen", 0},
				{"unchanged source", "stolen", "stolen", "stolen", 0},
				{"agent focus succeeds", "source", "stolen", "stolen", 2},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rules := []fakecli.Rule{{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"` + tc.focused + `","focused":true}]}}`}}
					if tc.wantCalls == 2 {
						rules = append(rules, fakecli.Rule{Argv: []string{"agent", "focus", tc.previous}})
					}
					env, _, _, _ := layoutFake(t, rules)
					RestoreFocusIfStolen(tc.previous, tc.stolen, "right", env)
					calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if len(calls) != tc.wantCalls {
						t.Fatalf("calls=%#v, want %d", calls, tc.wantCalls)
					}
				})
			}
		})
	})
}

func TestAutoDirectionUsesPaneGeometry(t *testing.T) {
	t.Run(`autoDirectionFor: wide pane right, tall pane down, missing pane right`, func(t *testing.T) { // JS: "autoDirectionFor: wide pane right, tall pane down, missing pane right"
		// Mutation captured: reversing the width/height comparison or missing-layout default changes the split direction.
		for _, tc := range []struct {
			name string
			pane string
			raw  string
			want string
		}{
			{`wide pane right`, "wide", `{"result":{"layout":{"panes":[{"pane_id":"wide","rect":{"width":320,"height":100}}]}}}`, "right"},
			{`tall pane down`, "tall", `{"result":{"layout":{"panes":[{"pane_id":"tall","rect":{"width":100,"height":200}}]}}}`, "down"},
			{`missing pane right`, "missing", ``, "right"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rules := []fakecli.Rule{{Argv: []string{"pane", "layout", "--pane", tc.pane}, Stdout: tc.raw}}
				env, _, _, _ := layoutFake(t, rules)
				if got := AutoDirectionFor(env, tc.pane); got != tc.want {
					t.Fatalf("AutoDirectionFor()=%q, want %q", got, tc.want)
				}
			})
		}
	})
}

func TestPickSplitAnchorFallsBackToCallerPane(t *testing.T) {
	t.Run(`pickSplitAnchor: no usable layout falls back to the caller pane right`, func(t *testing.T) { // JS: "pickSplitAnchor: no usable layout falls back to the caller pane right"
		rules := []fakecli.Rule{{Argv: []string{"pane", "layout", "--current"}}}
		env, ctx, cwd, _ := layoutFake(t, rules)
		env["HERDR_PANE_ID"] = "caller"
		if got := PickSplitAnchor(ctx, env, cwd); got == nil || *got != (Anchor{Anchor: "caller", Direction: "right"}) {
			t.Fatalf("PickSplitAnchor()=%+v, want caller/right", got)
		}
	})
}

func TestPaneMoveFailureDoesNotLookSuccessful(t *testing.T) {
	t.Run(`movePane: output jq rejects is a failed move, like bash move_pane`, func(t *testing.T) { // JS: "movePane: output jq rejects is a failed move, like bash move_pane"
		// Mutation captured: accepting an invalid JSON response as a successful pane move hides an unsuccessful layout change.
		rules := []fakecli.Rule{{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `[]`}}
		env, _, _, _ := layoutFake(t, rules)
		if result := herdr.PaneMove([]string{"p1", "--new-tab", "--label", "herd-park", "--no-focus"}, env); result.Ok {
			t.Fatalf("malformed move accepted: %#v", result)
		}
	})
}

func TestRelabelPanicWarnsAndRecordsFriction(t *testing.T) {
	// Mutation captured: silently recovering the relabel panic drops the user-visible warning and friction event.
	env, ctx, cwd, _ := layoutFake(t, nil)
	old := platform.Stderr
	var stderr bytes.Buffer
	platform.Stderr = &stderr
	defer func() { platform.Stderr = old }()
	relabelCommand := "regrid"
	core.SetFrictionCommand(relabelCommand)
	defer core.SetFrictionCommand("")
	relabelHerdTabs(func() { panic("relabel failed") }, ctx, env, cwd)
	want := "regrid: relabel of the herd tabs failed"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q, want warning %q", stderr.String(), want)
	}
	log, err := os.ReadFile(filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "warning\tregrid\t"+want) {
		t.Fatalf("friction log=%q, want warning record", log)
	}
}

func TestAutomaticRegridWarningUsesInvokingCommand(t *testing.T) {
	t.Run(`spawn: a failed automatic regrid is the bash warning and keeps the spawn code`, func(t *testing.T) { // JS: "spawn: a failed automatic regrid is the bash warning and keeps the spawn code"
		// Mutation captured: hard-coding regrid in AutoRegrid mislabels an automatic spawn-triggered warning in friction.log.
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"p1","tab_id":"tc","focused":true}]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"pc","tab_id":"tc"},{"pane_id":"p1","tab_id":"tc"}]}}`},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"pc","tab_id":"tc"},{"pane_id":"p1","tab_id":"tc"}]}}`},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `[]`},
		}
		env, ctx, cwd, dir := layoutFake(t, rules)
		env["HERDR_TAB_ID"], env["HERDR_PANE_ID"] = "tc", "pc"
		if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nw1\tp1\tcursor\timplementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		spawnCommand := "spawn"
		core.SetFrictionCommand(spawnCommand)
		defer core.SetFrictionCommand("")
		AutoRegrid(ctx, env, cwd, "spawn: regrid failed")
		log, err := os.ReadFile(filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(log), "\terror(exit 4)\tspawn\tregrid: could not park") || !strings.Contains(string(log), "\twarning\tspawn\tspawn: regrid failed\n") {
			t.Fatalf("friction log=%q, want error and warning categories set to spawn", log)
		}
	})
}

func TestAutomaticRegridSpawnSuccessIsSilent(t *testing.T) {
	t.Run(`spawn: the automatic regrid rebuilds the herd tab silently`, func(t *testing.T) { // JS: "spawn: the automatic regrid rebuilds the herd tab silently"
		// Mutation captured: leaking CmdRegrid output or omitting its rebuild changes the spawn command's output and pane layout.
		panes := `{"result":{"panes":[{"pane_id":"q1","tab_id":"t1"},{"pane_id":"q2","tab_id":"t1"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list", "--workspace", "ws"}, ArgvPrefix: true, Stdout: panes},
			{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"tab", "get"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-new"},"root_pane":{"pane_id":"r-new"}}}`},
			{Argv: []string{"pane", "move"}, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"q1"}}}}`},
			{Argv: []string{"pane", "close"}, ArgvPrefix: true},
		}
		env, ctx, cwd, dir := layoutFake(t, rules)
		env["SOHO_LAYOUT"] = "tab"
		if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte("b1\tq1\tcursor\tscouter\nb2\tq2\tcursor\timplementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "herd-tab"), []byte("t1\therd\tmanual\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		AutoRegrid(ctx, env, cwd, "spawn regrid warning")
		platform.Stdout, platform.Stderr = oldOut, oldErr
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil || out.Len() != 0 || stderr.Len() != 0 || !hasLayoutCallPrefix(calls, "tab", "create") {
			t.Fatalf("stdout=%q stderr=%q calls=%#v err=%v", out.String(), stderr.String(), calls, err)
		}
	})
}

func hasLayoutCallPrefix(calls []fakecli.Call, prefix ...string) bool {
	for _, call := range calls {
		if len(call.Argv) >= len(prefix) && reflect.DeepEqual(call.Argv[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}
