package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// m1Rules is freshSpawnRules with the M1 state added: one exact per-worker
// `agent get` per old lane worker and the live list carrying each old worker
// with its agent_status. The exact rules precede the generic prefix rules so
// they win the fake's first-match dispatch.
func m1Rules(t *testing.T, oldKind, oldState string, olds ...string) []fakecli.Rule {
	t.Helper()
	rules := []fakecli.Rule{}
	live := []string{}
	for _, name := range olds {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", name}, Stdout: `{"result":{"agent":{"name":"` + name + `","agent_status":"` + oldState + `"}}}`})
		live = append(live, `{"name":"`+name+`","pane_id":"ws:p-`+name+`","agent_status":"`+oldState+`"}`)
	}
	for _, r := range freshSpawnRules() {
		if len(r.Argv) >= 2 && r.Argv[0] == "agent" && r.Argv[1] == "list" && !r.ArgvPrefix {
			r.Stdout = `{"result":{"agents":[` + strings.Join(live, ",") + `]}}`
		}
		rules = append(rules, r)
	}
	return rules
}

// m1Fixture builds the M1 scenario: the lane "build" holds capacity 2 and its
// kind was changed to codex, while the roster still holds the old lane worker
// (roster kind oldKind) on that lane, live in Herdr with state oldState.
func m1Fixture(t *testing.T, oldKind, oldState string, extraOld ...string) spawnFixture {
	t.Helper()
	olds := append([]string{"oldgrok"}, extraOld...)
	f := newSpawnFixture(t, m1Rules(t, oldKind, oldState, olds...))
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
	f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "codex", Source: "project"}
	f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: "2", Source: "project"}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_kind", "lane_build_panes"}
	rows := []string{}
	for _, name := range olds {
		// name, pane, kind, role, family, created_pane, cwd, started,
		// model, approvals, roles, lane.
		rows = append(rows, strings.Join([]string{name, "p-" + name, oldKind, "tasker", "xai", "1", "/tmp/work", "now", "grok-4.7", "full", "tasker", "build", "", "", "high"}, "\t"))
	}
	f.roster(t, rows...)
	return f
}

// M1: with the lane kind changed to codex and an old idle grok still on the
// lane, the spawn opens the new codex worker and warns once, with the
// release --close remedy. Nothing is closed and the exit code stays 0.
func TestSpawnWarnsOnceAboutIdleOtherKindWorkerOnTheLane(t *testing.T) {
	// Mutation captured: dropping the idle-other-kind scan (or warning more
	// than once per worker) hides the old worker a lane-kind change left
	// behind, or nags per probe.
	f := m1Fixture(t, "grok", "idle")
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--fresh"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q; the spawn must proceed", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"created_pane": true`) || !spawnJSONHasField(t, stdout, `"kind": "codex"`) {
		t.Fatalf("the spawn did not open the new codex worker:\n%s", stdout)
	}
	want := "soho: warning: lane 'build' still holds idle 'oldgrok' (grok), not the requested kind 'codex' from lane.build.kind (project), with lane.build.kind=codex; release it with: soho release oldgrok --close\n"
	if n := strings.Count(stderr, want); n != 1 {
		t.Fatalf("warning count=%d, want exactly once\nstderr:\n%s", n, stderr)
	}
	// Nothing is closed or driven: no pane close, no send-keys anywhere in
	// the call log.
	for _, c := range calls {
		if len(c.Argv) >= 2 && c.Argv[0] == "pane" && c.Argv[1] == "close" {
			t.Fatalf("the spawn closed a pane: %#v", c.Argv)
		}
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "send-keys" {
			t.Fatalf("the spawn typed into the old worker: %#v", c.Argv)
		}
	}
	// The old worker is left on the lane; the roster holds it plus the new
	// worker.
	rows := core.RosterRows(filepath.Join(f.state, "ws"))
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "oldgrok\t") || !strings.HasPrefix(rows[1], "worker\t") {
		t.Fatalf("the roster lost the old worker: %#v", rows)
	}
}

// M1: an old worker Herdr reports done (the second idle class of the rule)
// gets the warning too.
func TestSpawnWarnsAboutDoneOtherKindWorker(t *testing.T) {
	f := m1Fixture(t, "grok", "done")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--fresh"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q; the spawn must proceed", code, stdout, stderr)
	}
	want := "soho: warning: lane 'build' still holds idle 'oldgrok' (grok), not the requested kind 'codex' from lane.build.kind (project), with lane.build.kind=codex; release it with: soho release oldgrok --close\n"
	if n := strings.Count(stderr, want); n != 1 {
		t.Fatalf("warning count=%d, want exactly once for the done worker\nstderr:\n%s", n, stderr)
	}
}

// M1: the same lane with the old worker working gets no warning; the spawn
// still opens the new worker.
func TestSpawnDoesNotWarnAboutWorkingOtherKindWorker(t *testing.T) {
	f := m1Fixture(t, "grok", "working")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--fresh"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q; the spawn must proceed", code, stdout, stderr)
	}
	if strings.Contains(stderr, "still holds idle") {
		t.Fatalf("a working other-kind worker was warned as idle:\n%s", stderr)
	}
}

// M1: an idle worker of the lane's own kind is not warned about.
func TestSpawnDoesNotWarnAboutIdleSameKindWorker(t *testing.T) {
	f := m1Fixture(t, "codex", "idle")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--fresh"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q; the spawn must proceed", code, stdout, stderr)
	}
	if strings.Contains(stderr, "still holds idle") {
		t.Fatalf("a same-kind idle worker was warned:\n%s", stderr)
	}
}

// M1: two idle old workers on the lane get one warning each, in roster
// order, and the spawn proceeds. The lane holds capacity 3 so the two
// idle workers leave a slot: at capacity 2 the spawn is busy (exit 10)
// before it opens the new worker.
func TestSpawnWarnsOncePerIdleOtherKindWorker(t *testing.T) {
	f := m1Fixture(t, "grok", "idle", "oldgrok2")
	f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: "3", Source: "project"}
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--fresh"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q; the spawn must proceed", code, stdout, stderr)
	}
	want1 := "soho: warning: lane 'build' still holds idle 'oldgrok' (grok), not the requested kind 'codex' from lane.build.kind (project), with lane.build.kind=codex; release it with: soho release oldgrok --close"
	want2 := "soho: warning: lane 'build' still holds idle 'oldgrok2' (grok), not the requested kind 'codex' from lane.build.kind (project), with lane.build.kind=codex; release it with: soho release oldgrok2 --close"
	i1, i2 := strings.Index(stderr, want1), strings.Index(stderr, want2)
	if i1 < 0 || i2 < 0 || strings.Count(stderr, want1) != 1 || strings.Count(stderr, want2) != 1 {
		t.Fatalf("want one warning per worker:\n%s", stderr)
	}
	if i1 > i2 {
		t.Fatalf("the warnings are not in roster order:\n%s", stderr)
	}
}

func TestSpawnLaneReuseRequiresSameKind(t *testing.T) {
	for _, oldKind := range []string{"grok", "codex"} {
		t.Run(oldKind, func(t *testing.T) {
			f := m1Fixture(t, oldKind, "idle")
			f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
			f.ctx.Entries["role_worker_kind"] = core.ConfigEntry{Value: "codex", Source: "user"}
			f.roster(t, strings.Join([]string{"oldgrok", "p-oldgrok", oldKind, "tasker", "", "1", f.cwd, "now", "", "ask", "tasker", "build", "", "", "high", "inherit"}, "\t"))
			report := filepath.Join(f.root, "complete.md")
			if err := os.WriteFile(report, []byte("complete\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(core.LastReportPath(filepath.Join(f.state, "ws"), "oldgrok"), []byte(report+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--effort", "high"})
			if code != 0 || !spawnJSONHasField(t, out, `"kind": "codex"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if oldKind == "codex" {
				if !spawnJSONHasField(t, out, `"reused": true`) || len(herdrActs(calls)) != 0 {
					t.Fatalf("matching worker was not reused: %q; native actions=%v", out, herdrActs(calls))
				}
			} else if !spawnJSONHasField(t, out, `"created_pane": true`) || len(core.RosterRows(filepath.Join(f.state, "ws"))) != 2 {
				t.Fatalf("different-kind worker was not left alone: %q; roster=%q", out, rosterBytes(t, f))
			}
		})
	}
}
