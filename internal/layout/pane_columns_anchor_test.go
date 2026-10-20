package layout

// DJA-393 L1: the declared caller-tab columns change the caller-tab cap
// (SplitCapDeclared), the spawn anchor decision (SplitAnchorDeclared) and
// CmdRegrid (the declared shape on the caller tab only). Without the key
// every one of them falls back to today's decision — the pre-existing
// SplitAnchor/SplitCap/CmdRegrid tests keep passing unedited and pin that.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The fixture layouts. In bigCaller the caller pane (60x100 of a 100x100
// area) is strictly larger than the worker (40x100), so today's largest-area
// rule anchors the caller; in tinyWorker both panes fail a 0.96 minimum.
const bigCallerRaw = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":60,"height":100}},{"pane_id":"worker","rect":{"x":60,"y":0,"width":40,"height":100}}]}}}`
const tinyWorkerRaw = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":100,"height":90}},{"pane_id":"worker","rect":{"x":0,"y":90,"width":100,"height":10}}]}}}`
const callerOnlyWideRaw = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":100,"height":50}}]}}}`
const threePanesRaw = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":40,"height":100}},{"pane_id":"w1","rect":{"x":40,"y":0,"width":30,"height":100}},{"pane_id":"w2","rect":{"x":70,"y":0,"width":30,"height":100}}]}}}`

func TestSplitCapDeclared(t *testing.T) {
	t.Run(`split cap with the declared shape: the three modes, explicit split_max_panes and the explicit 0`, func(t *testing.T) {
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		tests := []struct {
			name      string
			env       platform.Env
			wantCap   int
			wantNoCap bool
		}{
			{"no key keeps today's default cap", platform.Env{}, 4, false},
			{"balance (the default overflow) has no tab cap", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3"}, 0, true},
			{"explicit last-column has no tab cap", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "last-column"}, 0, true},
			{"new-column has no tab cap", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "new-column"}, 0, true},
			{"tab caps at the declared total", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "tab"}, 6, false},
			{"a single column tab caps at one", platform.Env{"SOHO_PANE_COLUMNS": "1", "SOHO_PANE_OVERFLOW": "tab"}, 1, false},
			{"an explicit split_max_panes wins in all cases", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "tab", "SOHO_SPLIT_MAX_PANES": "9"}, 9, false},
			{"an explicit split_max_panes wins for an uncapped overflow too", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_SPLIT_MAX_PANES": "9"}, 9, false},
			{"an explicit split_max_panes=0 keeps today's meaning (a full tab)", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_SPLIT_MAX_PANES": "0"}, 0, false},
			{"an invalid pane_columns keeps today's cap", platform.Env{"SOHO_PANE_COLUMNS": "1,,2"}, 4, false},
			{"no key: an invalid explicit split_max_panes keeps today's fallback", platform.Env{"SOHO_SPLIT_MAX_PANES": "-1"}, 4, false},
			{"declared: an invalid explicit split_max_panes keeps today's fallback", platform.Env{"SOHO_PANE_COLUMNS": "1,2,3", "SOHO_PANE_OVERFLOW": "tab", "SOHO_SPLIT_MAX_PANES": "+3"}, 4, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cap, unlimited := SplitCapDeclared(ctx, tt.env)
				if unlimited != tt.wantNoCap || (!unlimited && cap != tt.wantCap) {
					t.Fatalf("SplitCapDeclared(%v)=%d,%v, want %d,%v", tt.env, cap, unlimited, tt.wantCap, tt.wantNoCap)
				}
				// The uncapped state must not be indistinguishable from the
				// explicit 0: the bool, not the int, carries it.
				if unlimited && cap != 0 {
					t.Fatalf("the uncapped state carries cap=%d; the consumer must read the bool", cap)
				}
			})
		}
	})
}

func TestSplitAnchorDeclared(t *testing.T) {
	t.Run(`split anchor with the declared shape: caller exclusion, uncapped tab and the minimum rule`, func(t *testing.T) {
		// Mutation captured: keeping the caller as an anchor candidate while
		// a worker exists, honoring split_min_pane under last-column, or
		// applying the "full" cap while the tab is uncapped, each changes
		// one of these anchor decisions.
		t.Run(`first column alone: the caller is never the anchor while a worker candidate exists`, func(t *testing.T) {
			got := SplitAnchorDeclared([]byte(bigCallerRaw), "caller", []string{"worker"}, []int{1, 2, 3}, "last-column", 0, true, .18)
			if got == nil || got.Anchor != "worker" || got.Direction != "down" {
				t.Fatalf("SplitAnchorDeclared()=%+v, want worker/down (the larger caller is not a candidate)", got)
			}
			// The same decision with the first column holding two rows: the
			// caller stays a candidate and its larger area wins.
			got = SplitAnchorDeclared([]byte(bigCallerRaw), "caller", []string{"worker"}, []int{2, 3}, "last-column", 0, true, .18)
			if got == nil || got.Anchor != "caller" || got.Direction != "down" {
				t.Fatalf("SplitAnchorDeclared(cols 2,3)=%+v, want caller/down (the exclusion only applies to a lone first column)", got)
			}
		})
		t.Run(`no worker at all: the caller is split, as today`, func(t *testing.T) {
			got := SplitAnchorDeclared([]byte(callerOnlyWideRaw), "caller", nil, []int{1, 2, 3}, "last-column", 0, true, .18)
			if got == nil || got.Anchor != "caller" || got.Direction != "right" {
				t.Fatalf("SplitAnchorDeclared()=%+v, want caller/right (alone, the caller is the anchor)", got)
			}
		})
		t.Run(`last-column: every candidate below the minimum still anchors the largest worker`, func(t *testing.T) {
			got := SplitAnchorDeclared([]byte(tinyWorkerRaw), "caller", []string{"worker"}, []int{1, 2, 3}, "last-column", 0, true, .96)
			if got == nil || got.Anchor != "worker" || got.Direction != "right" {
				t.Fatalf("SplitAnchorDeclared()=%+v, want worker/right (the minimum is ignored under last-column)", got)
			}
			// new-column shares the minimum rule.
			got = SplitAnchorDeclared([]byte(tinyWorkerRaw), "caller", []string{"worker"}, []int{1, 2, 3}, "new-column", 0, true, .96)
			if got == nil || got.Anchor != "worker" {
				t.Fatalf("SplitAnchorDeclared(new-column)=%+v, want worker (the minimum is ignored under new-column too)", got)
			}
		})
		t.Run(`tab: the minimum still overflows, and is the only minimum that overflows`, func(t *testing.T) {
			// min .96: the worker (max/2 = .05) does not fit and the caller
			// is excluded, so the tab overflows min — the same split under
			// last-column anchors the worker instead.
			got := SplitAnchorDeclared([]byte(tinyWorkerRaw), "caller", []string{"worker"}, []int{1, 2, 3}, "tab", 6, false, .96)
			if got == nil || got.Overflow != "min" {
				t.Fatalf("SplitAnchorDeclared(tab)=%+v, want min (the minimum is honored under tab)", got)
			}
			got = SplitAnchorDeclared([]byte(tinyWorkerRaw), "caller", []string{"worker"}, []int{1, 2, 3}, "tab", 6, false, .18)
			if got == nil || got.Anchor != "worker" || got.Overflow != "" {
				t.Fatalf("SplitAnchorDeclared(tab, min .18)=%+v, want worker (the worker fits the minimum)", got)
			}
		})
		t.Run(`tab: the caller alone below the minimum overflows min`, func(t *testing.T) {
			// No worker at all: the caller is still a candidate (the
			// exclusion needs a worker), but the 1.0x0.5 caller fails a
			// 0.95 minimum.
			got := SplitAnchorDeclared([]byte(callerOnlyWideRaw), "caller", nil, []int{1, 2, 3}, "tab", 3, false, .95)
			if got == nil || got.Overflow != "min" {
				t.Fatalf("SplitAnchorDeclared(caller only)=%+v, want min (the caller fails the tab minimum)", got)
			}
		})
		t.Run(`last-column: the caller alone below the minimum still anchors`, func(t *testing.T) {
			got := SplitAnchorDeclared([]byte(callerOnlyWideRaw), "caller", nil, []int{1, 2, 3}, "last-column", 0, true, .95)
			if got == nil || got.Anchor != "caller" || got.Direction != "right" {
				t.Fatalf("SplitAnchorDeclared(caller only, last-column)=%+v, want caller/right (the minimum is ignored under last-column)", got)
			}
		})
		t.Run(`tab: the full cap counts the excluded caller`, func(t *testing.T) {
			got := SplitAnchorDeclared([]byte(threePanesRaw), "caller", []string{"w1", "w2"}, []int{1, 2}, "tab", 2, false, .18)
			if got == nil || got.Overflow != "full" {
				t.Fatalf("SplitAnchorDeclared()=%+v, want full (caller + 2 workers already fill the 2 slots)", got)
			}
		})
		t.Run(`last-column: no full overflow even past today's cap`, func(t *testing.T) {
			got := SplitAnchorDeclared([]byte(threePanesRaw), "caller", []string{"w1", "w2"}, []int{1, 2, 3}, "last-column", 0, true, .18)
			if got == nil || got.Overflow != "" || got.Anchor != "w1" {
				t.Fatalf("SplitAnchorDeclared()=%+v, want w1 (the uncapped tab never overflows full)", got)
			}
		})
	})
}

// regridDeclaredFixture builds the layout fixture (fake herdr, state dir)
// with the pane-list snapshots and the roster/herd-tab state the regrid
// cases need, plus the pane_columns/pane_overflow environment.
func regridDeclaredFixture(t *testing.T, keys platform.Env, roster, herdTab string, rules []fakecli.Rule) (platform.Env, *core.Config, string, string) {
	t.Helper()
	env, ctx, cwd, dir := layoutFake(t, rules)
	env["HERDR_TAB_ID"], env["HERDR_PANE_ID"] = "t0", "C"
	// Hermetic user layer: the machine's own user config must not leak a
	// split_max_panes into the cap resolution (which would hide the
	// declared-overflow decisions under test).
	env["XDG_CONFIG_HOME"] = t.TempDir()
	for k, v := range keys {
		env[k] = v
	}
	// The config is loaded inside layoutFake before the environment above is
	// set; reload it so the hermetic user layer and the keys take effect.
	fresh := core.LoadConfig(env, cwd)
	ctx = &fresh
	if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	if herdTab != "" {
		if err := os.WriteFile(filepath.Join(dir, "herd-tab"), []byte(herdTab), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return env, ctx, cwd, dir
}

func runCmdRegridCaptured(t *testing.T, ctx *core.Config, env platform.Env, cwd string) (string, []fakecli.Call) {
	t.Helper()
	old := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	defer func() { platform.Stdout = old }()
	if code := CmdRegrid(nil, ctx, env, cwd); code != 0 {
		calls, _ := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		t.Fatalf("CmdRegrid()=%d calls=%#v", code, calls)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), calls
}

func callerTabMoves(calls []fakecli.Call) [][]string {
	moves := [][]string{}
	for _, call := range calls {
		if len(call.Argv) > 4 && call.Argv[0] == "pane" && call.Argv[1] == "move" && call.Argv[3] == "--tab" && call.Argv[4] == "t0" {
			moves = append(moves, call.Argv)
		}
	}
	return moves
}

func TestCmdRegridDeclaredColumns(t *testing.T) {
	t.Run(`regrid with pane_columns: the caller tab builds the declared shape and the herd tabs keep the square grid`, func(t *testing.T) {
		// Six panes (the caller + 5 workers) under 1,2,3: the grid is
		// 1+2+3, not today's 2+2+2 — a different first move and a different
		// third row.
		all := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"},{"pane_id":"p3","tab_id":"t0"},{"pane_id":"p4","tab_id":"t0"},{"pane_id":"p5","tab_id":"t0"}]}}`
		roster := "w1\tp1\tgrok\timplementer\nw2\tp2\tgrok\ttasker\nw3\tp3\tgrok\treviewer\nw4\tp4\tgrok\tscouter\nw5\tp5\tgrok\tresearcher\n"
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: all},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w2"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w3"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w4"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w5"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: all},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p3", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p4", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p5", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			// The declared 1,2,3 grid over [C p1 p2 p3 p4 p5].
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"}},
			{Argv: []string{"pane", "move", "p3", "--tab", "t0", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p4", "--tab", "t0", "--split", "down", "--target-pane", "p3", "--ratio", "0.3333", "--no-focus"}},
			{Argv: []string{"pane", "move", "p5", "--tab", "t0", "--split", "down", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: all},
		}
		env, ctx, cwd, _ := regridDeclaredFixture(t, platform.Env{"SOHO_PANE_COLUMNS": "1,2,3"}, roster, "", rules)
		out, calls := runCmdRegridCaptured(t, ctx, env, cwd)
		if out != "{\"regridded\":[{\"tab\":\"t0\",\"label\":\"caller\",\"panes\":6,\"cols\":3}]}\n" {
			t.Fatalf("output=%q", out)
		}
		want := [][]string{
			{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"},
			{"pane", "move", "p3", "--tab", "t0", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"},
			{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"},
			{"pane", "move", "p4", "--tab", "t0", "--split", "down", "--target-pane", "p3", "--ratio", "0.3333", "--no-focus"},
			{"pane", "move", "p5", "--tab", "t0", "--split", "down", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"},
		}
		if !reflect.DeepEqual(callerTabMoves(calls), want) {
			t.Fatalf("caller-tab moves=%#v, want %#v", callerTabMoves(calls), want)
		}
	})
	t.Run(`regrid with pane_columns tab and a larger explicit split_max_panes: no caller-tab worker is left parked`, func(t *testing.T) {
		// 1,1 declares 2 slots, but the explicit cap of 4 let a third pane
		// into the caller tab: the last column grows to hold it (1+2), and
		// both parked workers come back.
		all := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"}]}}`
		roster := "w1\tp1\tgrok\timplementer\nw2\tp2\tgrok\ttasker\n"
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: all},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w2"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: all},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: all},
		}
		env, ctx, cwd, _ := regridDeclaredFixture(t, platform.Env{"SOHO_PANE_COLUMNS": "1,1", "SOHO_PANE_OVERFLOW": "tab", "SOHO_SPLIT_MAX_PANES": "4"}, roster, "", rules)
		out, calls := runCmdRegridCaptured(t, ctx, env, cwd)
		if out != "{\"regridded\":[{\"tab\":\"t0\",\"label\":\"caller\",\"panes\":3,\"cols\":2}]}\n" {
			t.Fatalf("output=%q", out)
		}
		want := [][]string{
			{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"},
			{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"},
		}
		if !reflect.DeepEqual(callerTabMoves(calls), want) {
			t.Fatalf("caller-tab moves=%#v, want %#v (a parked worker was left behind)", callerTabMoves(calls), want)
		}
	})
	t.Run(`regrid with pane_columns last-column: every herd-tab worker is pulled back, past today's cap`, func(t *testing.T) {
		// Today's split cap (4 without the key) would stop at the 4 panes
		// already in the caller tab; last-column is uncapped, so p4 and p5
		// come back and the 6 panes grid as 1,2,3.
		before := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"},{"pane_id":"p3","tab_id":"t0"},{"pane_id":"p4","tab_id":"t1"},{"pane_id":"p5","tab_id":"t1"}]}}`
		after := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"},{"pane_id":"p3","tab_id":"t0"},{"pane_id":"p4","tab_id":"t0"},{"pane_id":"p5","tab_id":"t0"}]}}`
		roster := "w1\tp1\tgrok\timplementer\nw2\tp2\tgrok\ttasker\nw3\tp3\tgrok\treviewer\nw4\tp4\tgrok\tscouter\nw5\tp5\tgrok\tresearcher\n"
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: before},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w2"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w3"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w4"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w5"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"pane", "move", "p4", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p4"}}}}`},
			{Argv: []string{"pane", "move", "p5", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p5"}}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: after},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p3", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p4", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p5", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"}},
			{Argv: []string{"pane", "move", "p3", "--tab", "t0", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p4", "--tab", "t0", "--split", "down", "--target-pane", "p3", "--ratio", "0.3333", "--no-focus"}},
			{Argv: []string{"pane", "move", "p5", "--tab", "t0", "--split", "down", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: after},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			// The relabel pass re-reads the topology.
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 4, Stdout: after},
		}
		env, ctx, cwd, dir := regridDeclaredFixture(t, platform.Env{"SOHO_PANE_COLUMNS": "1,2,3"}, roster, "t1\therd\tauto\n", rules)
		out, calls := runCmdRegridCaptured(t, ctx, env, cwd)
		if out != "{\"regridded\":[{\"tab\":\"t0\",\"label\":\"caller\",\"panes\":6,\"cols\":3}]}\n" {
			t.Fatalf("output=%q", out)
		}
		stored, err := os.ReadFile(filepath.Join(dir, "herd-tab"))
		if err != nil || len(stored) != 0 {
			t.Fatalf("herd-tab=%q err=%v (the emptied tab must be forgotten)", stored, err)
		}
		if len(callerTabMoves(calls)) != 7 {
			t.Fatalf("pull-back + grid into the caller tab=%#v, want 2 pull-backs + 5 declared moves (today's cap must not stop last-column)", callerTabMoves(calls))
		}
		wantGrid := [][]string{
			{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"},
			{"pane", "move", "p3", "--tab", "t0", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"},
			{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"},
			{"pane", "move", "p4", "--tab", "t0", "--split", "down", "--target-pane", "p3", "--ratio", "0.3333", "--no-focus"},
			{"pane", "move", "p5", "--tab", "t0", "--split", "down", "--target-pane", "p4", "--ratio", "0.5000", "--no-focus"},
		}
		if !reflect.DeepEqual(callerTabMoves(calls)[2:], wantGrid) {
			t.Fatalf("declared grid after the pull-backs=%#v, want %#v", callerTabMoves(calls)[2:], wantGrid)
		}
		pull4 := callsToArgs(calls, "pane", "move", "p4", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus")
		pull5 := callsToArgs(calls, "pane", "move", "p5", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus")
		if len(pull4) != 1 || len(pull5) != 1 {
			t.Fatalf("pull-back=%#v+%#v, want both", pull4, pull5)
		}
	})
	t.Run(`regrid with pane_columns tab: the excess beyond the slots stays in the herd tab`, func(t *testing.T) {
		// 1,2 declares 3 slots (1 + 2) and tab caps the caller tab at them:
		// the 3 panes already in the caller tab regrid as 1,2, and p3, p4
		// stay in the herd tab (rebuilt square there).
		both := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"},{"pane_id":"p3","tab_id":"t1"},{"pane_id":"p4","tab_id":"t1"}]}}`
		roster := "w1\tp1\tgrok\timplementer\nw2\tp2\tgrok\ttasker\nw3\tp3\tgrok\tscouter\nw4\tp4\tgrok\tresearcher\n"
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: both},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w2"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w3"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w4"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: both},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			// The declared 1,2 grid over [C p1 p2]: the caller tab stays at
			// its 2 slots.
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: both},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-new"},"root_pane":{"pane_id":"r-new"}}}`},
			{Argv: []string{"pane", "move", "p3", "--tab", "t-new", "--split", "right", "--target-pane", "r-new", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p3"}}}}`},
			{Argv: []string{"pane", "close", "r-new"}},
			// The herd-tab rebuild keeps the square grid (GridSizes(2)).
			{Argv: []string{"pane", "move", "p4", "--tab", "t-new", "--split", "right", "--target-pane", "p3", "--ratio", "0.5000", "--no-focus"}},
			// The relabel pass re-reads the topology and the new tab.
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 4, Stdout: both},
			{Argv: []string{"tab", "get", "t-new"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
		}
		env, ctx, cwd, dir := regridDeclaredFixture(t, platform.Env{"SOHO_PANE_COLUMNS": "1,2", "SOHO_PANE_OVERFLOW": "tab"}, roster, "t1\therd\tauto\n", rules)
		out, calls := runCmdRegridCaptured(t, ctx, env, cwd)
		if !strings.Contains(out, `"label":"caller","panes":3,"cols":2`) || !strings.Contains(out, `"label":"herd","panes":2,"cols":2`) {
			t.Fatalf("output=%q", out)
		}
		// The excess must not have been pulled into the caller tab.
		for _, move := range callerTabMoves(calls) {
			if move[2] == "p3" || move[2] == "p4" {
				t.Fatalf("the excess was pulled into the caller tab: %#v", move)
			}
		}
		stored, err := os.ReadFile(filepath.Join(dir, "herd-tab"))
		if err != nil || string(stored) != "t-new\therd\tauto\n" {
			t.Fatalf("herd-tab=%q err=%v (the rebuilt tab must be re-recorded)", stored, err)
		}
		if got := callsToArgs(calls, "pane", "move", "p4", "--tab", "t-new", "--split", "right", "--target-pane", "p3", "--ratio", "0.5000", "--no-focus"); len(got) != 1 {
			t.Fatalf("herd-tab rebuild=%#v", calls)
		}
	})
}
