package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// laneKindFixture is the DJA-397 scenario: the lane "build" holds the role
// "scouter" and has lane.build.kind=pi. The roster carries one lane worker
// that Herdr no longer lists, so the lane decision would drop it with a
// RosterRemove: a refusal that runs first leaves the roster file untouched.
func laneKindFixture(t *testing.T, laneKind string) spawnFixture {
	t.Helper()
	// Herdr answers agent_not_found for the ghost, so the lane decision lists
	// it as gone and would drop it from the roster.
	rules := freshSpawnRules(
		fakecli.Rule{Argv: []string{"agent", "get", "ghost"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
		piHerdrIdentityRule("scouter", "p2", "/sessions/scouter.jsonl"),
	)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "scouter", Source: "project"}
	f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: "2", Source: "project"}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_panes"}
	if laneKind != "" {
		f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: laneKind, Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "lane_build_kind")
	}
	f.roster(t, strings.Join([]string{"ghost", "p-ghost", "pi", "scouter", "alibaba", "1", "/tmp/work", "now", "", "ask", "scouter", "build", "", "", ""}, "\t"))
	return f
}

func rosterBytes(t *testing.T, f spawnFixture) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// herdrActs lists the calls that open, close or start a pane or an agent.
func herdrActs(calls []fakecli.Call) []string {
	acts := []string{}
	for _, c := range calls {
		if len(c.Argv) < 2 {
			continue
		}
		switch c.Argv[0] + " " + c.Argv[1] {
		case "pane split", "pane close", "tab create", "agent start":
			acts = append(acts, strings.Join(c.Argv, " "))
		}
	}
	return acts
}

func frictionLog(t *testing.T, f spawnFixture) string {
	t.Helper()
	body, _ := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
	return string(body)
}

func TestSpawnAcceptsAKindFlagThatDiffersFromTheGroupKind(t *testing.T) {
	for _, tc := range []struct {
		args []string
		kind string
	}{
		{[]string{"scouter", "--kind", "codex"}, "codex"},
		{[]string{"scouter"}, "pi"},
		{[]string{"scouter", "--kind", "pi"}, "pi"},
	} {
		f := laneKindFixture(t, "pi")
		code, stdout, stderr, calls := runCmdSpawn(t, f, tc.args)
		if code != 0 || !spawnJSONHasField(t, stdout, `"kind": "`+tc.kind+`"`) || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", tc.args, code, stdout, stderr)
		}
		rows := core.RosterRows(filepath.Join(f.state, "ws"))
		if len(herdrActs(calls)) == 0 || len(rows) != 1 || fieldAt(strings.Split(rows[0], "\t"), 11) != "build" {
			t.Fatalf("%v: no new worker recorded in build: roster=%v calls=%v", tc.args, rows, calls)
		}
	}
}

func TestSpawnKindFlagControlsDoNotChange(t *testing.T) {
	t.Run("a lane without lane.<name>.kind accepts any --kind", func(t *testing.T) {
		f := laneKindFixture(t, "")
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"scouter", "--kind", "codex"})
		if code != 0 || !spawnJSONHasField(t, stdout, `"kind": "codex"`) || !spawnJSONHasField(t, stdout, `"created_pane": true`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if len(herdrActs(calls)) == 0 || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("the spawn did not take the normal path: stderr=%q calls=%#v", stderr, calls)
		}
	})

	t.Run("a given --pane skips the lane decision", func(t *testing.T) {
		f := laneKindFixture(t, "pi")
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"scouter", "--kind", "codex", "--pane", "p-given"})
		if code != 0 || strings.Contains(stdout, "kind-mismatch") || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		started := false
		for _, c := range calls {
			if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "start" && containsArgPair(c.Argv, "--kind", "codex") && containsArgPair(c.Argv, "--pane", "p-given") {
				started = true
			}
		}
		if !started {
			t.Fatalf("no codex agent started in the given pane: %#v", calls)
		}
	})

	t.Run("lanes switched off for the invocation do not refuse", func(t *testing.T) {
		f := laneKindFixture(t, "pi")
		f.env["SOHO_LANES"] = "off"
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"scouter", "--kind", "codex"})
		if code != 0 || !spawnJSONHasField(t, stdout, `"kind": "codex"`) || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("a role outside the lane keeps its --kind", func(t *testing.T) {
		f := laneKindFixture(t, "pi")
		f.ctx.Entries["lane_review_roles"] = core.ConfigEntry{Value: "reviewer", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "lane_review_roles")
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"reviewer", "--kind", "codex"})
		if code != 0 || !spawnJSONHasField(t, stdout, `"kind": "codex"`) || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}
