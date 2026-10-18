package cli

// DJA-393 L1: layout-plan mirrors the declared caller-tab shape (the grid
// for candidates+1 panes, the caller excluded from the anchors), and
// release --close with regrid=on rebuilds the declared shape through the
// full regrid. Without the key the outputs are byte-identical to today —
// the frozen layout-plan and spawn goldens pin that.

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

func TestLayoutPlanDeclaredColumns(t *testing.T) {
	t.Run(`layout-plan with pane_columns: the declared grid for candidates+1 and the caller outside the anchors`, func(t *testing.T) {
		// The caller (60 of 100 width) is strictly larger than the worker;
		// today's largest-area rule anchors the caller, the declared shape
		// (a lone first column) anchors the worker instead.
		dir := t.TempDir()
		file := filepath.Join(dir, "layout.json")
		raw := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"C","rect":{"x":0,"y":0,"width":60,"height":100}},{"pane_id":"A","rect":{"x":60,"y":0,"width":40,"height":100}}]}}}`
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		env := layoutPlanEnv(t)
		env["SOHO_PANE_COLUMNS"] = "1,2,3"
		ctx := core.LoadConfig(env, dir)
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		defer func() { platform.Stdout = old }()
		if code := cmdLayoutPlan([]string{"--layout", file, "--me", "C", "--mine", "A"}, &ctx, env, dir); code != 0 {
			t.Fatalf("exit code=%d", code)
		}
		// n = candidates + 1 = 3 panes: the declared 1,2,3 shape is 1+2,
		// and the uncapped tab (last-column) reports cap null.
		want := `{"placement":"split","anchor":"A","direction":"down","reason":"largest-area","cap":null,"min_pane":0.18,"candidates":[{"pane_id":"C","caller":true,"width":0.6,"height":1},{"pane_id":"A","caller":false,"width":0.4,"height":1}],"grid":{"cells":3,"cols":2,"rows_per_col":[1,2]}}` + "\n"
		if out.String() != want {
			t.Fatalf("output=%q, want %q", out.String(), want)
		}
	})
	t.Run(`layout-plan with pane_columns tab: the declared total caps the plan`, func(t *testing.T) {
		// 1,1 declares 2 slots; the caller + 1 worker already fill them, so
		// the next spawn overflows to a herd tab (cap 2, no grid).
		dir := t.TempDir()
		file := filepath.Join(dir, "layout.json")
		raw := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"C","rect":{"x":0,"y":0,"width":60,"height":100}},{"pane_id":"A","rect":{"x":60,"y":0,"width":40,"height":100}}]}}}`
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		env := layoutPlanEnv(t)
		env["SOHO_PANE_COLUMNS"], env["SOHO_PANE_OVERFLOW"] = "1,1", "tab"
		ctx := core.LoadConfig(env, dir)
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		defer func() { platform.Stdout = old }()
		if code := cmdLayoutPlan([]string{"--layout", file, "--me", "C", "--mine", "A"}, &ctx, env, dir); code != 0 {
			t.Fatalf("exit code=%d", code)
		}
		got := out.String()
		if !strings.Contains(got, `"placement":"herd"`) || !strings.Contains(got, `"reason":"full","cap":2`) || !strings.Contains(got, `"grid":null`) {
			t.Fatalf("output=%q, want a herd overflow with the declared total cap", got)
		}
	})
}

func TestReleaseCloseRegridsTheDeclaredShape(t *testing.T) {
	t.Run(`release --close with regrid=on rebuilds the declared columns on the caller tab`, func(t *testing.T) {
		// Three panes (the caller + 2 workers) under 1,1,2: the regrid is
		// 1+1+1 (three columns), not today's 1+2 — a different first move
		// and a different third move.
		env, _ := commandFixture(t)
		env["HERDR_ENV"], env["HERDR_PANE_ID"] = "1", "C"
		env["SOHO_REGRID"], env["SOHO_PANE_COLUMNS"] = "on", "1,1,2"
		state := filepath.Join(env.Get("SOHO_DIR"), "ws")
		if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		roster := "# header\n" + releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work") + "\n" +
			releaseRow("w1", "p1", "1", "tasker", false, "/tmp/work") + "\n" +
			releaseRow("w2", "p2", "1", "reviewer", false, "/tmp/work") + "\n"
		if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		addReleaseReport(t, state, "worker", false)
		live := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"},{"pane_id":"p-worker","tab_id":"t0"}]}}`
		fakeDir := t.TempDir()
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"name":"w1","agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w2"}, Stdout: `{"result":{"agent":{"name":"w2","agent_status":"working"}}}`},
			{Argv: []string{"pane", "close", "p-worker"}},
			{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, ArgvPrefix: true, Stdout: live},
			{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			// The declared 1,1,2 grid over [C p1 p2] is 1+1+1: three
			// columns, no rows — today's 1+2 grid would move p1 right and
			// p2 down of p1.
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
		}
		if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run(append([]string{"release"}, "worker", "--close"), env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(fakeDir, "herdr.json"))
		if os.IsNotExist(err) {
			calls = []fakecli.Call{}
		} else if err != nil {
			t.Fatal(err)
		}
		if code != 0 {
			t.Fatalf("release status=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
		}
		wantMoves := [][]string{
			{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.3333", "--no-focus"},
			{"pane", "move", "p2", "--tab", "t0", "--split", "right", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"},
		}
		var got [][]string
		for _, call := range calls {
			if len(call.Argv) > 4 && call.Argv[0] == "pane" && call.Argv[1] == "move" && call.Argv[3] == "--tab" && call.Argv[4] == "t0" {
				got = append(got, call.Argv)
			}
		}
		if !reflect.DeepEqual(got, wantMoves) {
			t.Fatalf("caller-tab moves=%#v, want the declared 1,1,2 grid %#v", got, wantMoves)
		}
	})
}
