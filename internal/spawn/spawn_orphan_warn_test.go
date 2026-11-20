package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// laneOrphanRules is freshSpawnRules with a live-agent list that holds an
// idle released worker (build-2) and a working one (build-3), plus a
// workspace pane list that holds both of their panes.
func laneOrphanRules() []fakecli.Rule {
	rules := append([]fakecli.Rule{}, freshSpawnRules()...)
	for i := range rules {
		if len(rules[i].Argv) >= 2 && rules[i].Argv[0] == "agent" && rules[i].Argv[1] == "list" && !rules[i].ArgvPrefix {
			rules[i].Stdout = `{"result":{"agents":[{"name":"build-2","pane_id":"ws:p2","agent_status":"idle"},{"name":"build-3","pane_id":"ws:p3","agent_status":"working"}]}}`
		}
		if rules[i].ArgvPrefix && len(rules[i].Argv) >= 2 && rules[i].Argv[0] == "pane" && rules[i].Argv[1] == "list" {
			rules[i].Stdout = `{"result":{"panes":[{"pane_id":"ws:p2"},{"pane_id":"ws:p3"}]}}`
		}
	}
	return rules
}

func writeReleasedPanesFixture(t *testing.T, f *spawnFixture, rows ...string) {
	t.Helper()
	released := "# name\tpane\tkind\treleased\n"
	if len(rows) > 0 {
		released += strings.Join(rows, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(f.state, "ws", "released-panes.tsv"), []byte(released), 0o600); err != nil {
		t.Fatal(err)
	}
}

func callsTo(calls []fakecli.Call, args ...string) int {
	n := 0
	for _, call := range calls {
		if strings.Join(call.Argv, "\x00") == strings.Join(args, "\x00") {
			n++
		}
	}
	return n
}

func TestSpawnWarnsAboutIdleReleasedOrphanBeforeOpeningThePane(t *testing.T) {
	// Mutation captured: re-adopting the orphan (or silently skipping the
	// warning) loses the operator's released idle worker again.
	f := newSpawnFixture(t, laneOrphanRules())
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
	f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_kind"}
	f.roster(t)
	writeReleasedPanesFixture(t, &f, "build-2\tws:p2\tgrok\t20261001T000000", "build-3\tws:p3\tgrok\t20261001T000000")
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"name": "build"`) || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("the spawn did not proceed to a new pane:\n%s", stdout)
	}
	want := "soho: spawn: 'build-2' is idle outside the roster (ws:p2); close it with: soho release build-2 --close\n"
	if !strings.Contains(stderr, want) {
		t.Fatalf("missing the released orphan warning:\n%s", stderr)
	}
	// The working orphan is not idle: no warning names it.
	if strings.Contains(stderr, "build-3") {
		t.Fatalf("the working orphan was warned as idle:\n%s", stderr)
	}
	// The orphan check runs before the pane is opened: its workspace pane
	// list read is logged before the pane split.
	split, list := -1, -1
	for i, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "split" {
			if split < 0 {
				split = i
			}
		}
		if strings.Join(call.Argv, "\x00") == strings.Join([]string{"pane", "list", "--workspace", "ws"}, "\x00") {
			list = i
		}
	}
	if split < 0 || list < 0 || list > split {
		t.Fatalf("the orphan check did not run before the pane split (list=%d split=%d): %#v", list, split, calls)
	}
	// The orphan is not re-adopted: the roster holds only the new worker.
	rows := core.RosterRows(filepath.Join(f.state, "ws"))
	if len(rows) != 1 || !strings.HasPrefix(rows[0], "build\t") {
		t.Fatalf("the orphan was adopted into the roster: %#v", rows)
	}
}

func TestSpawnHasNoOrphanWarningWithoutReleasedRows(t *testing.T) {
	// The round-1 false positive: a live lane-named idle agent that this
	// project never released is not an orphan, and the spawn warns nothing.
	f := newSpawnFixture(t, laneOrphanRules())
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
	f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_kind"}
	f.roster(t)
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "is idle outside the roster") {
		t.Fatalf("an orphan warning without a released row:\n%s", stderr)
	}
}

func TestSpawnLanesOffDoesNotWarnAboutRoleNamedOrphans(t *testing.T) {
	// The warning is for lane workers only: with lanes=off a role-named idle
	// agent outside the roster does not stop or name the spawn.
	rules := laneOrphanRules()
	for i := range rules {
		if len(rules[i].Argv) >= 2 && rules[i].Argv[0] == "agent" && rules[i].Argv[1] == "list" && !rules[i].ArgvPrefix {
			rules[i].Stdout = `{"result":{"agents":[{"name":"worker-2","pane_id":"ws:p2","agent_status":"idle"}]}}`
		}
	}
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.roster(t)
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "is idle outside the roster") {
		t.Fatalf("lanes=off spawn warned about a role-named orphan:\n%s", stderr)
	}
}

func TestSpawnGivenPaneReRostersRetiresTheReleasedRow(t *testing.T) {
	// A spawn that re-rosters a released pane (spawn --pane on it) retires
	// the pane's released-panes row: the pane is a roster pane again.
	rules := append([]fakecli.Rule{}, freshSpawnRules()...)
	rules = append(rules, fakecli.Rule{Argv: []string{"pane", "get", "ws:px"}, Stdout: `{"result":{"pane":{"pane_id":"ws:px"}}}`})
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.roster(t)
	writeReleasedPanesFixture(t, &f, "worker\tws:px\tgrok\t20261001T000000")
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--pane", "ws:px"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"pane_id": "ws:px"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if n := callsTo(calls, "pane", "get", "ws:px"); n != 1 {
		t.Fatalf("the pruning probe did not run: %#v", calls)
	}
	raw, err := os.ReadFile(filepath.Join(f.state, "ws", "released-panes.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ws:px") {
		t.Fatalf("the released row survived the re-roster: %q", raw)
	}
	row := core.RosterRows(filepath.Join(f.state, "ws"))
	if len(row) != 1 || !strings.HasPrefix(row[0], "worker\tws:px\t") {
		t.Fatalf("the worker was not rostered in the pane: %#v", row)
	}
}
