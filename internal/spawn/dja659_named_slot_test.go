package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// These fixtures expose a compatible idle worker and the old rename endpoint.
// An unexpected rename must reach the fixture so it fails the no-rename assertion.
func dja659NamedSlotRules() []fakecli.Rule {
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"old","pane_id":"p-old","agent_status":"idle"}]}}`},
		idleHerdrRule("old"),
		{Argv: []string{"agent", "rename", "p-old", "fresh"}, Stdout: `{"result":{}}`},
	}
	return append(rules, freshSpawnRules()...)
}

func dja659AgentCalls(calls []fakecli.Call, verb string) int {
	n := 0
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == verb {
			n++
		}
	}
	return n
}

func TestDJA659NamedSlotCreatesNewWorker(t *testing.T) {
	// RED on the pre-fix code. The behavior protected: an explicit --name
	// that names no live lane worker opens a new worker in the spare slot
	// (lane and caller-adjusted global capacity both permit it) instead of
	// renaming the compatible idle worker; the old worker keeps its name,
	// pane, roster line and session context. The believable regression that
	// makes it fail: reverting the new-name spare-slot preference, so the
	// pre-fix rename flow takes the name away from the idle worker.
	f := a12Fixture(t, dja659NamedSlotRules(), "2")
	f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	// The native boundary: exactly one create/start and no rename.
	if started, renamed := dja659AgentCalls(calls, "start"), dja659AgentCalls(calls, "rename"); started != 1 || renamed != 0 {
		t.Fatalf("native create/start vs rename: started=%d renamed=%d calls=%#v", started, renamed, calls)
	}
	if !spawnJSONHasField(t, stdout, `"name": "fresh"`) || !spawnJSONHasField(t, stdout, `"created_pane": true`) || !spawnJSONHasField(t, stdout, `"pane_id": "p-new"`) || !spawnJSONHasField(t, stdout, `"status": "ready"`) {
		t.Fatalf("JSON=%q want the new worker fresh in the new pane p-new, ready", stdout)
	}
	if spawnJSONHasField(t, stdout, `"reused": true`) {
		t.Fatalf("the compatible idle worker was reused instead of a new one: %q", stdout)
	}
	if strings.Contains(stderr, "renamed to 'fresh'") || strings.Contains(stderr, "reusing idle lane") {
		t.Fatalf("the rename warning went out: %q", stderr)
	}
	roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	// The old worker's name, pane and roster line are intact, and the new
	// worker is registered in its own row.
	if !strings.Contains(string(roster), "old\tp-old\tgrok\tworker") {
		t.Fatalf("the old worker lost its name or pane: %q", roster)
	}
	if !strings.Contains(string(roster), "fresh\tp-new\tgrok\tworker") {
		t.Fatalf("the new worker row is missing or misregistered: %q", roster)
	}
}

func TestDJA659NamedSlotControls(t *testing.T) {
	t.Run("an already existing requested name keeps the reuse as-is", func(t *testing.T) {
		// The requested name names the live idle lane worker: the A12
		// rule reuses it without a rename, even though the lane has a
		// spare slot. The fix must not change this path.
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("fresh")}, "2")
		f.roster(t, a12Row("fresh", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 0 || started != 0 {
			t.Fatalf("native calls for the existing name: renamed=%d started=%d calls=%#v", renamed, started, calls)
		}
		if !spawnJSONHasField(t, stdout, `"name": "fresh"`) || !spawnJSONHasField(t, stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "reusing idle lane 'build' worker 'fresh' as worker; existing session context is retained") {
			t.Fatalf("stderr=%q", stderr)
		}
	})

	t.Run("an omitted name keeps the reuse preference despite a spare slot", func(t *testing.T) {
		// No --name: the established lane reuse takes the compatible idle
		// worker even when the lane has a slot; the fix must not open a
		// second worker for an unnamed spawn.
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "2")
		f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 0 || started != 0 {
			t.Fatalf("native calls for the omitted name: renamed=%d started=%d calls=%#v", renamed, started, calls)
		}
		if !spawnJSONHasField(t, stdout, `"name": "old"`) || !spawnJSONHasField(t, stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "reusing idle lane 'build' worker 'old' as worker; existing session context is retained") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}

func TestDJA659NamedSlotFullCapacityRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, panes, laneLimit, globalLimit  string
		globalRoom, extraLive, explicitReuse bool
	}{
		{name: "both limits", panes: "1", laneLimit: "lane 'build' has no room for a new worker (1 of 1)", globalLimit: "max_workers=1 is at capacity (1 of 1)"},
		{name: "lane limit only", panes: "1", laneLimit: "lane 'build' has no room for a new worker (1 of 1)", globalRoom: true},
		{name: "global limit only", panes: "2", globalLimit: "max_workers=2 is at capacity (2 of 2)", extraLive: true},
		{name: "explicit reuse still preserves the name", panes: "1", laneLimit: "lane 'build' has no room for a new worker (1 of 1)", globalLimit: "max_workers=1 is at capacity (1 of 1)", explicitReuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := dja659NamedSlotRules()
			if tc.extraLive {
				rules = append([]fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"old","pane_id":"p-old","agent_status":"idle"},{"name":"stale","pane_id":"p-stale","agent_status":"idle"}]}}`}}, rules[1:]...)
			}
			f := a12Fixture(t, rules, tc.panes)
			if tc.globalRoom {
				f.ctx.Entries["lane_review_roles"] = core.ConfigEntry{Value: "reviewer", Source: "project"}
				f.ctx.Order = append(f.ctx.Order, "lane_review_roles")
			}
			rows := []string{a12Row("old", "grok", "grok-4.7", f.cwd)}
			if tc.extraLive {
				stale := strings.Split(a12Row("stale", "grok", "grok-4.7", f.cwd), "\t")
				stale[11] = "gone" // Counts globally but belongs to a retired lane.
				rows = append(rows, strings.Join(stale, "\t"))
			}
			f.roster(t, rows...)
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			before, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			argv := []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"}
			if tc.explicitReuse {
				argv = append(argv, "--reuse")
			}
			code, stdout, stderr, calls := runCmdSpawn(t, f, argv)
			if code != 10 || !spawnJSONHasField(t, stdout, `"status": "busy"`) || !spawnJSONHasField(t, stdout, `"name": "old"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want busy on old", code, stdout, stderr)
			}
			if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 0 || started != 0 {
				t.Fatalf("capacity refusal called rename %d times, start %d times: %#v", renamed, started, calls)
			}
			after, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("roster changed: before=%q after=%q", before, after)
			}
			for _, want := range []string{"idle worker 'old' was not reused as 'fresh'", "reuse it by its existing name", "release a slot before retrying with --fresh", tc.laneLimit, tc.globalLimit} {
				if want != "" && !strings.Contains(stderr, want) {
					t.Fatalf("stderr=%q is missing %q", stderr, want)
				}
			}
			if tc.laneLimit == "" && strings.Contains(stderr, "has no room for a new worker") || tc.globalLimit == "" && strings.Contains(stderr, "max_workers") {
				t.Fatalf("stderr names a limit that still has room: %q", stderr)
			}
		})
	}
}
