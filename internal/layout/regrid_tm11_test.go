package layout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type tm11RegridGolden struct {
	Err   string             `json:"err"`
	Files map[string]*string `json:"files"`
	Out   string             `json:"out"`
	RC    int                `json:"rc"`
}

func TestTM11RegridCommandParity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-regrid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var goldens map[string]tm11RegridGolden
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ title, golden string }{
		{`parity: regrid pulls a worker back to the caller tab, parks, grids, forgets emptied tabs`, "pull"},
		{`parity: regrid with layout=tab keeps, rebuilds and forgets the herd tabs`, "herd"},
		{`parity: regrid keeps an unqueryable worker pane and drops a gone one`, "kept"},
	}
	for _, tc := range cases {
		t.Run(`JS: "`+tc.title+`"`, func(t *testing.T) {
			want, ok := goldens[tc.golden]
			if !ok {
				t.Fatalf("missing parity-regrid golden %q", tc.golden)
			}
			rules, roster, panes, tabFile := tm11RegridFixture(tc.golden)
			env, ctx, cwd, state := layoutFake(t, rules)
			repo := filepath.Join(cwd, "repo")
			if err := os.MkdirAll(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			cwd = repo
			if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "herd-tab"), []byte(tabFile), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.golden == "pull" || tc.golden == "kept" {
				env["HERDR_TAB_ID"], env["HERDR_PANE_ID"] = "t0", "C"
			}
			if tc.golden == "herd" {
				env["SOHO_LAYOUT"] = "tab"
			}
			if tc.golden == "pull" {
				env["SOHO_SPLIT_MAX_PANES"] = "4"
			}
			_ = panes
			old := platform.Stdout
			var out strings.Builder
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			code := CmdRegrid(nil, ctx, env, cwd)
			if code != want.RC || out.String() != want.Out {
				t.Fatalf("code=%d want=%d stdout=%q want=%q", code, want.RC, out.String(), want.Out)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			var log strings.Builder
			for _, call := range calls {
				log.WriteString(strings.Join(call.Argv, " "))
				log.WriteByte('\n')
			}
			actualLog := strings.ReplaceAll(log.String(), cwd, "<ROOT>/repo")
			if expected := want.Files["herdr.log"]; expected != nil {
				expectedLog := *expected
				// Keep the frozen legacy capture intact. The caller grid now
				// reads the topology once more before rebuilding herd tabs;
				// the re-id regression separately verifies why it is needed.
				lastCallerMove := ""
				switch tc.golden {
				case "pull":
					lastCallerMove = "pane move p2 --tab t0 --split down --target-pane p1 --ratio 0.5000 --no-focus\n"
				case "kept":
					lastCallerMove = "pane move p2 --tab t0 --split right --target-pane C --ratio 0.5000 --no-focus\n"
				}
				if lastCallerMove != "" {
					if strings.Count(expectedLog, lastCallerMove) != 1 {
						t.Fatal("legacy capture must contain exactly one final caller move")
					}
					expectedLog = strings.Replace(expectedLog, lastCallerMove, lastCallerMove+"pane list --workspace ws\n", 1)
				}
				// DJA-394: the caller has HERDR_PANE_ID, so the regrid reads
				// its pane's shell pid first (the fake has no rule for it:
				// unknown, and the step's output and files are unchanged).
				if tc.golden == "pull" || tc.golden == "kept" {
					expectedLog = *testutil.RewriteCallerCheckLog(&expectedLog, "C")
				}
				if actualLog != expectedLog {
					t.Fatalf("Go call-log divergence after the explicit topology refresh; Go=%q expected=%q", actualLog, expectedLog)
				}
			}
			if expected := want.Files["state/ws/agents.tsv"]; expected != nil {
				actual, readErr := os.ReadFile(filepath.Join(state, "agents.tsv"))
				if readErr != nil || string(actual) != *expected {
					t.Fatalf("roster=%q err=%v want=%q", actual, readErr, *expected)
				}
			}
			if expected := want.Files["state/ws/herd-tab"]; expected != nil {
				actual, readErr := os.ReadFile(filepath.Join(state, "herd-tab"))
				if readErr != nil || string(actual) != *expected {
					t.Fatalf("herd-tab=%q err=%v want=%q", actual, readErr, *expected)
				}
			}
		})
	}
}

func tm11RegridFixture(name string) ([]fakecli.Rule, string, string, string) {
	header := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\n"
	row := func(name, pane, role, tab string) string {
		return name + "\t" + pane + "\tgrok\t" + role + "\txai\t1\t/tmp\t\n"
	}
	working := func(name string) fakecli.Rule {
		return fakecli.Rule{Argv: []string{"agent", "get", name}, Stdout: `{"result":{"agent":{"name":"` + name + `","agent_status":"idle"}}}`}
	}
	switch name {
	case "pull":
		before := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t1"}]}}`
		after := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: before},
			working("w1"),
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"tab_id":"t1","label":"herd"}}}`},
			{Argv: []string{"tab", "get", "t2"}, Stdout: `{"result":{"tab":{"tab_id":"t2","label":"herd"}}}`},
			working("w2"),
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p2"}}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: after},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p1","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "park-1", "--split", "down", "--target-pane", "p1", "--ratio", "0.5", "--no-focus"}},
			{Argv: []string{"pane", "move", "p1", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "down", "--target-pane", "p1", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"herd"}}}`},
			{Argv: []string{"tab", "get", "t2"}, Stdout: `{"result":{"tab":{"tab_id":"t2","label":"herd"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: after},
		}
		return rules, header + row("w1", "p1", "implementer", "t0") + "\n" + row("w2", "p2", "scouter", "t1") + "\n", before, "t1\therd\tauto\nt2\therd\tauto\n"
	case "herd":
		panes := `{"result":{"panes":[{"pane_id":"q1","tab_id":"t1"},{"pane_id":"q2","tab_id":"t2"},{"pane_id":"q3","tab_id":"t2"},{"pane_id":"q4","tab_id":"t2"}]}}`
		after := `{"result":{"panes":[{"pane_id":"q1","tab_id":"t1"},{"pane_id":"q2","tab_id":"t-new-1"},{"pane_id":"q3","tab_id":"t-new-1"},{"pane_id":"q4","tab_id":"t-new-1"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: panes},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: after},
			{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"tab", "get", "t1"}, Call: 1, Stdout: `{"result":{"tab":{"tab_id":"t1","label":"solo"}}}`},
			{Argv: []string{"tab", "get", "t2"}, Call: 1, Stdout: `{"result":{"tab":{"tab_id":"t2","label":"herd"}}}`},
			{Argv: []string{"tab", "get", "t3"}, Call: 1, Stdout: `{"result":{"tab":{"tab_id":"t3","label":"herd"}}}`},
			{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-new-1","label":"herd"},"root_pane":{"pane_id":"r-new-1"}}}`},
			{Argv: []string{"pane", "move", "q2"}, Call: 1, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"q2"}}}}`},
			{Argv: []string{"pane", "move", "q3"}, Call: 1, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"q3"}}}}`},
			{Argv: []string{"pane", "move", "q4"}, Call: 1, ArgvPrefix: true, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"q4"}}}}`},
			{Argv: []string{"pane", "close"}, ArgvPrefix: true},
			{Argv: []string{"tab", "get", "t1"}, Call: 2, Stdout: `{"result":{"tab":{"tab_id":"t1","label":"solo"}}}`},
			{Argv: []string{"tab", "get", "t-new-1"}, Call: 1, Stdout: `{"result":{"tab":{"tab_id":"t-new-1","label":"herd"}}}`},
			{Argv: []string{"tab", "rename", "t1", "impl"}},
			{Argv: []string{"tab", "rename", "t-new-1", "impl 2"}},
			{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		}
		roster := header + row("s1", "q1", "implementer", "t1") + "\n" + row("b1", "q2", "implementer", "t2") + "\n" + row("b2", "q3", "implementer", "t2") + "\n" + row("b3", "q4", "implementer", "t2") + "\n" + row("g1", "q5", "implementer", "t3") + "\n"
		return rules, roster, panes, "t1\tsolo\tauto\nt2\t-\tauto\nt3\therd\tauto\n"
	case "kept":
		before := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"},{"pane_id":"p3","tab_id":"t0"}]}}`
		after := `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p2","tab_id":"t0"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: before},
			{Argv: []string{"agent", "get", "stuck"}, Code: 1, Stderr: `{"error":{"code":"permission_denied"}}`},
			{Argv: []string{"agent", "get", "dead"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found"}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: before},
			{Argv: []string{"pane", "move", "p2", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"p2","tab_id":"park-1"}}}}`},
			{Argv: []string{"pane", "move", "p2", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 3, Stdout: after},
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		}
		return rules, header + row("stuck", "p2", "implementer", "t0") + "\n" + row("dead", "p3", "implementer", "t0") + "\n", before, ""
	}
	panic("unknown regrid parity fixture: " + name)
}
