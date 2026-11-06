package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA659 stage 1: consumer regressions for an explicit --name against a
// compatible idle lane worker, written against the pre-fix production code.
// The red tests (TestDJA659NamedSlotCreatesNewWorker and
// TestDJA659NamedSlotFullCapacityWarning) assert the next authorized defect
// fix: a genuinely new explicit name opens a spare slot instead of renaming
// the compatible idle worker when both the lane and the caller-adjusted
// global capacity permit it, and the full-capacity rename warning says the
// old name ceased to work, suggests --fresh and acknowledges its capacity
// requirement. The control tests assert the established reuse behavior the
// fix must keep: an already existing requested name is reused as-is, and an
// omitted name keeps the reuse preference even when a slot is free.
// TestDJA659NamedSlotCapControls separates the two capacity conditions:
// the lane at its panes limit with global room, and the caller-adjusted
// global cap reached with the lane still holding a slot. F3 closes the
// oracle gap: each separated case now positively requires the actually
// limiting cap's phrase (the lane statement for a full lane, the
// max_workers statement for a full global cap) together with the old-name
// and --fresh lines, while still forbidding the inapplicable cap claim;
// the both-full case requires both reasons. Those controls passed on the
// pre-fix code when they only forbade the false claim; with the positive
// reasons pinned they are red on the pre-fix code, whose warning carries
// only the established base phrase, and that transition is recorded
// honestly. The fixtures are the existing a12 lane machinery
// (lane build, role worker, kind grok) with the lane panes pinning the
// capacity: the global cap is the sum of the lane capacities, so panes=1
// with one live occupant fills the lane and the global cap together, and
// panes=2 with one live occupant leaves spare capacity in both.

// dja659NamedSlotRules is the herdr surface of a lane holding the one idle
// 'old' worker: 'old' is live (so the global cap counts it), 'old' reports
// idle, and the rename the pre-fix code takes is available so the red run
// fails on the assertions, not on a missing rule.
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
	if !strings.Contains(stdout, `"name": "fresh"`) || !strings.Contains(stdout, `"created_pane": true`) || !strings.Contains(stdout, `"pane_id": "p-new"`) || !strings.Contains(stdout, `"status": "ready"`) {
		t.Fatalf("JSON=%q want the new worker fresh in the new pane p-new, ready", stdout)
	}
	if strings.Contains(stdout, `"reused": true`) {
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
		if !strings.Contains(stdout, `"name": "fresh"`) || !strings.Contains(stdout, `"reused": true`) {
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
		if !strings.Contains(stdout, `"name": "old"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "reusing idle lane 'build' worker 'old' as worker; existing session context is retained") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}

func TestDJA659NamedSlotFullCapacityWarning(t *testing.T) {
	// RED on the pre-fix code. The lane is at its panes limit and the
	// caller-adjusted global cap is reached by the same occupant (one
	// slot, one live worker): opening is not permitted, so the compatible
	// rename stays, but the warning must say the old name ceased to work,
	// suggest --fresh and acknowledge BOTH actually limiting caps — the
	// lane and the caller-adjusted global cap. The believable regression
	// that makes it fail: suppressing the extended warning, or either of
	// the two capacity acknowledgements, from the full-capacity rename.
	f := a12Fixture(t, dja659NamedSlotRules(), "1")
	f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	// Full capacity keeps the rename path: one rename, no create.
	if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 1 || started != 0 {
		t.Fatalf("native calls at full capacity: renamed=%d started=%d calls=%#v", renamed, started, calls)
	}
	if !strings.Contains(stdout, `"name": "fresh"`) || !strings.Contains(stdout, `"reused": true`) {
		t.Fatalf("JSON=%q", stdout)
	}
	roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(roster), "fresh\tp-old\tgrok\tworker") || strings.Contains(string(roster), "old\tp-old") {
		t.Fatalf("the roster rename did not keep the old pane: %q", roster)
	}
	// The established warning stays, and gains the required parts: the old
	// name ceased to work, --fresh is suggested, and both actually limiting
	// caps are acknowledged with their positive phrases.
	if !strings.Contains(stderr, "reusing idle lane 'build' worker 'old' as worker, renamed to 'fresh'; existing session context is retained") {
		t.Fatalf("stderr=%q missed the established rename warning", stderr)
	}
	if !strings.Contains(stderr, "the old name 'old' no longer works") {
		t.Fatalf("stderr=%q does not say the old name ceased to work", stderr)
	}
	if !strings.Contains(stderr, "use --fresh to open a new worker") {
		t.Fatalf("stderr=%q does not suggest --fresh", stderr)
	}
	if !strings.Contains(stderr, "the lane 'build' is at capacity (1 of 1), so no new pane can open until a worker is released") {
		t.Fatalf("stderr=%q does not acknowledge the full lane cap", stderr)
	}
	if !strings.Contains(stderr, "max_workers=1 is at capacity (1 of 1)") {
		t.Fatalf("stderr=%q does not acknowledge the full caller-adjusted global cap", stderr)
	}
}

func TestDJA659NamedSlotCapControls(t *testing.T) {
	// F1 + F3: two small controls separating the lane and global capacity
	// conditions, so neither can disappear unnoticed. Both keep the
	// compatible rename (no create) on the pre-fix code. F3 closes the
	// oracle gap: each case now positively requires the actually limiting
	// cap's phrase and the old-name/--fresh lines, in addition to keeping
	// the rename behavior, the established warning and the ban on claiming
	// the cap that has room. RED on the pre-fix code: its warning carries
	// only the established base phrase.

	t.Run("lane full with global room keeps the rename and does not claim the global cap", func(t *testing.T) {
		// The build lane is at its panes limit (1 of 1); a second
		// configured lane gives the caller-adjusted global cap room
		// (max_workers is the sum of the lane capacities: 2, with one
		// live occupant).
		f := a12Fixture(t, dja659NamedSlotRules(), "1")
		f.ctx.Entries["lane_review_roles"] = core.ConfigEntry{Value: "reviewer", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "lane_review_roles")
		f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 1 || started != 0 {
			t.Fatalf("native calls with the lane full and global room: renamed=%d started=%d calls=%#v", renamed, started, calls)
		}
		if !strings.Contains(stdout, `"name": "fresh"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "renamed to 'fresh'") {
			t.Fatalf("stderr=%q lost the established rename warning", stderr)
		}
		// The lane is full (1 of 1): the warning must carry the lane's
		// positive reason, and the old-name and --fresh lines the both-full
		// case pins.
		if !strings.Contains(stderr, "the lane 'build' is at capacity (1 of 1), so no new pane can open until a worker is released") {
			t.Fatalf("stderr=%q does not acknowledge the full lane cap", stderr)
		}
		if !strings.Contains(stderr, "the old name 'old' no longer works") {
			t.Fatalf("stderr=%q does not say the old name ceased to work", stderr)
		}
		if !strings.Contains(stderr, "use --fresh to open a new worker") {
			t.Fatalf("stderr=%q does not suggest --fresh", stderr)
		}
		// The global cap has room (1 live of 2): no warning may claim it;
		// max_workers is the token the established global refusal uses.
		if strings.Contains(stderr, "max_workers") {
			t.Fatalf("the warning claims the global cap, which has room: %q", stderr)
		}
	})

	t.Run("global full with lane room keeps the rename and does not claim the lane is full", func(t *testing.T) {
		// The build lane has a free slot (1 of 2); a live worker whose
		// roster line names a lane that is no longer configured fills the
		// caller-adjusted global cap (the cap counts roster names that are
		// live in the agent list: 2 of max_workers 2).
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"old","pane_id":"p-old","agent_status":"idle"},{"name":"stale","pane_id":"p-stale","agent_status":"idle"}]}}`},
			idleHerdrRule("old"),
			{Argv: []string{"agent", "rename", "p-old", "fresh"}, Stdout: `{"result":{}}`},
		}
		rules = append(rules, freshSpawnRules()...)
		f := a12Fixture(t, rules, "2")
		stale := strings.Split(a12Row("stale", "grok", "grok-4.7", f.cwd), "\t")
		stale[11] = "gone" // a roster line whose lane no longer exists
		f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd), strings.Join(stale, "\t"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 1 || started != 0 {
			t.Fatalf("native calls with the global cap full and lane room: renamed=%d started=%d calls=%#v", renamed, started, calls)
		}
		if !strings.Contains(stdout, `"name": "fresh"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "renamed to 'fresh'") {
			t.Fatalf("stderr=%q lost the established rename warning", stderr)
		}
		// The caller-adjusted global cap is full (2 live of max_workers 2):
		// the warning must carry the global positive reason with the actual
		// caller-adjusted counts, and the old-name and --fresh lines.
		if !strings.Contains(stderr, "max_workers=2 is at capacity (2 of 2)") {
			t.Fatalf("stderr=%q does not acknowledge the full global cap", stderr)
		}
		if !strings.Contains(stderr, "the old name 'old' no longer works") {
			t.Fatalf("stderr=%q does not say the old name ceased to work", stderr)
		}
		if !strings.Contains(stderr, "use --fresh to open a new worker") {
			t.Fatalf("stderr=%q does not suggest --fresh", stderr)
		}
		// The lane is not full (1 of 2): no warning may claim it is, using
		// the phrase the both-full case pins as the lane-full statement.
		if strings.Contains(stderr, "the lane 'build' is at capacity") {
			t.Fatalf("the warning claims the lane is full, which it is not: %q", stderr)
		}
	})
}
