package spawn

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// A12: reuse respects kind, resolved model and cwd; --name never swaps the
// names of live lane workers.

// a12Row is a 15-column roster line for a build-lane worker: name, pane,
// kind, role, family, created, cwd, stamp, model, approvals, roles, lane,
// burst, args, effort.
func a12Row(name, kind, model, wdir string) string {
	return strings.Join([]string{name, "p-" + name, kind, "worker", "xai", "1", wdir, "now", model, "ask", "worker", "build", "", "", "high"}, "\t")
}

func a12Fixture(t *testing.T, rules []fakecli.Rule, panes string) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
	f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
	if panes != "" {
		f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: panes, Source: "project"}
		f.ctx.Order = []string{"lane_build_roles", "lane_build_kind", "lane_build_panes"}
	} else {
		f.ctx.Order = []string{"lane_build_roles", "lane_build_kind"}
	}
	return f
}

func TestSpawnLaneReuseRequiresRecordedEffort(t *testing.T) {
	for _, tc := range []struct {
		name, effort, requestedName string
		legacy, alternative         bool
		wantName                    string
		wantCode                    int
	}{
		{name: "direct equal", effort: "high", wantName: "old"},
		{name: "direct different", effort: "medium", wantName: "build"},
		{name: "direct missing", legacy: true, wantName: "build"},
		{name: "named equal", effort: "high", requestedName: "old", wantName: "old"},
		{name: "named different", effort: "medium", requestedName: "old", wantCode: 10},
		{name: "named missing", legacy: true, requestedName: "old", wantCode: 10},
		{name: "alternative equal", effort: "medium", alternative: true, wantName: "right"},
		{name: "alternative different", effort: "medium", alternative: true, wantName: "build"},
		{name: "alternative missing", legacy: true, alternative: true, wantName: "build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := a12Fixture(t, append([]fakecli.Rule{idleHerdrRule("old"), idleHerdrRule("right")}, freshSpawnRules()...), "3")
			row := strings.Split(a12Row("old", "grok", "grok-4.7", f.cwd), "\t")
			row[14] = tc.effort
			if tc.legacy {
				row = row[:14]
			}
			rows := []string{strings.Join(row, "\t")}
			if tc.alternative {
				right := append([]string(nil), row...)
				right[0], right[1] = "right", "p-right"
				if tc.name == "alternative equal" {
					right[14] = "high"
				}
				rows = append(rows, strings.Join(right, "\t"))
			}
			f.roster(t, rows...)
			args := []string{"worker", "--model", "grok-4.7"}
			if tc.requestedName != "" {
				args = append(args, "--name", tc.requestedName)
			}
			code, stdout, stderr, calls := runCmdSpawn(t, f, args)
			if code != tc.wantCode {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, tc.wantCode, stdout, stderr)
			}
			if code != 0 {
				assertNoStart(t, calls)
				return
			}
			if !strings.Contains(stdout, `"name": "`+tc.wantName+`"`) {
				t.Fatalf("want name=%s stdout=%s", tc.wantName, stdout)
			}
			if tc.wantName == "build" {
				if strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"created_pane": true`) {
					t.Fatalf("wanted fresh worker: %s", stdout)
				}
			} else {
				if !strings.Contains(stdout, `"reused": true`) {
					t.Fatalf("wanted reuse: %s", stdout)
				}
				assertNoStart(t, calls)
			}
		})
	}
}

func TestSpawnA12ReuseRespectsModelAndCwd(t *testing.T) {
	other := filepath.Join("sibling", "worktree")

	t.Run("an idle worker on another model is not reused; the lane opens a new pane when it has a slot", func(t *testing.T) {
		f := a12Fixture(t, append([]fakecli.Rule{idleHerdrRule("old")}, freshSpawnRules()...), "2")
		f.roster(t, a12Row("old", "grok", "old-model", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"name": "build"`) || !strings.Contains(stdout, `"created_pane": true`) {
			t.Fatalf("the mismatched idle was not treated as occupied (no new pane): %s", stdout)
		}
		if strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("the mismatched idle was reused: %s", stdout)
		}
		started, renamed := false, false
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				started = true
			}
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "rename" {
				renamed = true
			}
		}
		if !started || renamed {
			t.Fatalf("started=%v renamed=%v calls=%#v", started, renamed, calls)
		}
	})

	t.Run("an idle worker on another model with no slot is 13 with the exact message", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, a12Row("old", "grok", "old-model", f.cwd))
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		want := fmt.Sprintf("spawn: lane 'build' has no idle worker matching kind 'grok', model 'grok-4.7' and cwd '%s' (idle: 'old' runs grok old-model in %s); release it or raise the lane's panes", f.cwd, f.cwd)
		if code != 13 || message != want {
			t.Fatalf("exit=%d message=%q want %q calls=%#v", code, message, want, calls)
		}
	})

	t.Run("an idle worker in another cwd is not reused; the lane opens a new pane when it has a slot", func(t *testing.T) {
		f := a12Fixture(t, append([]fakecli.Rule{idleHerdrRule("old")}, freshSpawnRules()...), "2")
		f.roster(t, a12Row("old", "grok", "grok-4.7", other))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		if code != 0 || !strings.Contains(stdout, `"created_pane": true`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("an idle worker in another cwd with no slot is 13 with the exact message", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, a12Row("old", "grok", "grok-4.7", other))
		code, message, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		want := fmt.Sprintf("spawn: lane 'build' has no idle worker matching kind 'grok', model 'grok-4.7' and cwd '%s' (idle: 'old' runs grok grok-4.7 in %s); release it or raise the lane's panes", f.cwd, other)
		if code != 13 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
	})

	t.Run("an idle worker that matches kind, model and cwd is reused", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"name": "old"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "reusing idle lane 'build' worker 'old' as worker") {
			t.Fatalf("stderr=%q", stderr)
		}
	})

	t.Run("a matching later idle wins over a mismatched first idle", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("wrong"), idleHerdrRule("right")}, "2")
		// The first idle runs another model; the second one matches.
		f.roster(t, a12Row("wrong", "grok", "old-model", f.cwd), a12Row("right", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"name": "right"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("the matching later idle was not picked: %s", stdout)
		}
	})
}

func TestSpawnA12NameDoesNotSwapNames(t *testing.T) {
	t.Run("two idles A and B: spawn --name A then spawn --name B renames nothing", func(t *testing.T) {
		// The defect: two consecutive --name spawns swapped the names of two
		// idle workers. With the fix, each spawn reuses the live worker that
		// already carries the requested name.
		row := func(name string) []fakecli.Rule {
			return []fakecli.Rule{idleHerdrRule("b"), idleHerdrRule(name)}
		}
		for _, name := range []string{"a", "b"} {
			f := a12Fixture(t, row(name), "2")
			// Roster order is [b, a]: 'a' is not the first idle, so the old
			// flow renamed the first idle to the requested name.
			f.roster(t, a12Row("b", "grok", "grok-4.7", f.cwd), a12Row("a", "grok", "grok-4.7", f.cwd))
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", name})
			if code != 0 {
				t.Fatalf("%s: exit=%d stdout=%q stderr=%q", name, code, stdout, stderr)
			}
			if renameCalls(calls) != 0 {
				t.Fatalf("%s: unexpected rename calls: %#v", name, calls)
			}
			if !strings.Contains(stdout, fmt.Sprintf(`"name": %q`, name)) || !strings.Contains(stdout, `"reused": true`) {
				t.Fatalf("%s: JSON=%q", name, stdout)
			}
		}
	})

	t.Run("--name of a busy lane worker is the busy-lane rule (10)", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "a"}, Stdout: `{"result":{"agent":{"name":"a","agent_status":"working"}}}`},
			idleHerdrRule("b"),
		}, "1")
		f.roster(t, a12Row("a", "grok", "grok-4.7", f.cwd), a12Row("b", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "a"})
		if code != 10 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"status":"busy"`) || !strings.Contains(stdout, `"name":"a"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if renameCalls(calls) != 0 {
			t.Fatalf("unexpected rename calls: %#v", calls)
		}
	})

	t.Run("--name of an idle lane worker that does not match is the busy-lane rule (10)", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("a"), idleHerdrRule("b")}, "1")
		// 'a' is idle but runs another model: it counts as occupied.
		f.roster(t, a12Row("a", "grok", "old-model", f.cwd), a12Row("b", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "a"})
		if code != 10 || !strings.Contains(stdout, `"name":"a"`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("--name not on the lane still renames a candidate to it (the pre-existing flow)", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{
			idleHerdrRule("b"),
			{Argv: []string{"agent", "rename", "p-b", "a"}, Stdout: `{"result":{}}`},
		}, "1")
		f.roster(t, a12Row("b", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "a"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renameCalls(calls) != 1 {
			t.Fatalf("rename calls=%#v", calls)
		}
		if !strings.Contains(stdout, `"name": "a"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "renamed to 'a'") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}
