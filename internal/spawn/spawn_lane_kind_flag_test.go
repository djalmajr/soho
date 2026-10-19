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
	rules := append([]fakecli.Rule{{Argv: []string{"agent", "get", "ghost"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}}, freshSpawnRules()...)
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

const laneKindRefusalJSON = `{"status":"kind-mismatch","lane":"build","role":"scouter","lane_kind":"pi","requested_kind":"codex"}` + "\n"
const laneKindRefusalText = "spawn: role 'scouter' runs in lane 'build', whose kind is pi; --kind codex would put a codex worker in it and block the lane's next spawns. Spawn with --kind pi, change lane.build.kind, or set SOHO_LANES=off for this spawn to open it outside the lanes."

// DJA-397: a --kind that differs from lane.<lane>.kind is refused before any
// Herdr call that opens or starts anything, before the lane decision drops a
// gone worker from the roster, and before a reuse; the lane stays free for
// the next spawn of its own kind.
func TestSpawnRefusesAKindFlagThatDiffersFromTheLaneKind(t *testing.T) {
	// Mutation captured: dropping the refusal opens a codex worker in the pi
	// lane; moving it after the pane opening leaves herdr calls behind.
	f := laneKindFixture(t, "pi")
	before := rosterBytes(t, f)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"scouter", "--kind", "codex"})
	if code != 13 {
		t.Fatalf("exit=%d, want 13\nstdout=%q\nstderr=%q", code, stdout, stderr)
	}
	if stdout != laneKindRefusalJSON {
		t.Fatalf("stdout=%q\nwant   %q", stdout, laneKindRefusalJSON)
	}
	// The caller-rename warning before it is the fixture's (the fake herdr has
	// no caller agent); the refusal is the one line that follows it.
	if want := "soho: warning: " + laneKindRefusalText + "\n"; strings.Count(stderr, want) != 1 || !strings.HasSuffix(stderr, want) {
		t.Fatalf("stderr=%q\nwant it to end with %q once", stderr, want)
	}
	if got := frictionLog(t, f); !strings.Contains(got, laneKindRefusalText) {
		t.Fatalf("friction.log lacks the refusal:\n%s", got)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("the refusal ran after herdr acted: %v\nall calls: %#v", acts, calls)
	}
	if after := rosterBytes(t, f); after != before {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// The lane is free: the next spawn, with no --kind and then with the
	// lane's own kind, takes the normal path and opens a pi worker.
	for _, args := range [][]string{{"scouter"}, {"scouter", "--kind", "pi"}} {
		g := laneKindFixture(t, "pi")
		code, stdout, stderr, calls := runCmdSpawn(t, g, args)
		if code != 0 || !strings.Contains(stdout, `"kind": "pi"`) || !strings.Contains(stdout, `"created_pane": true`) {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		if len(herdrActs(calls)) == 0 {
			t.Fatalf("%v: no pane or agent was opened: %#v", args, calls)
		}
		if strings.Contains(stdout, "kind-mismatch") || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("%v: refused although the kind matches the lane\nstdout=%q\nstderr=%q", args, stdout, stderr)
		}
	}
}

// DJA-397: the refusal compares the flag with the lane's kind only. The
// controls that must not change: a lane without a kind, a given --pane, the
// lanes switched off for this invocation, and a role outside the lane.
func TestSpawnKindFlagControlsDoNotChange(t *testing.T) {
	t.Run("a lane without lane.<name>.kind accepts any --kind", func(t *testing.T) {
		f := laneKindFixture(t, "")
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"scouter", "--kind", "codex"})
		if code != 0 || !strings.Contains(stdout, `"kind": "codex"`) || !strings.Contains(stdout, `"created_pane": true`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if len(herdrActs(calls)) == 0 || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("the spawn did not take the normal path: stderr=%q calls=%#v", stderr, calls)
		}
	})

	t.Run("a given --pane skips the lane decision and the refusal", func(t *testing.T) {
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
		// SOHO_LANES=off is read per invocation: it is the way to spawn a role
		// of a lane with another kind without touching the project file.
		f := laneKindFixture(t, "pi")
		f.env["SOHO_LANES"] = "off"
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"scouter", "--kind", "codex"})
		if code != 0 || !strings.Contains(stdout, `"kind": "codex"`) || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("a role outside the lane keeps its --kind", func(t *testing.T) {
		f := laneKindFixture(t, "pi")
		f.ctx.Entries["lane_review_roles"] = core.ConfigEntry{Value: "reviewer", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "lane_review_roles")
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"reviewer", "--kind", "codex"})
		if code != 0 || !strings.Contains(stdout, `"kind": "codex"`) || strings.Contains(stderr, "whose kind is") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}
