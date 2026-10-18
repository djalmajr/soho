package spawn

// Native port of the legacy JS scenario "spawn: the temporary (burst) worker
// — flex only, flex_roles, capped by flex_extra" (skills/herdr-soho/scripts/
// test/spawn.test.mjs). Every case runs the real CmdSpawn against the
// native fakecli Herdr: the burst marker and the cap are observable through
// the spawn JSON, the state roster row, the stderr hints and the recorded
// herdr calls — not through the core helpers alone. Each case group builds
// its own fixture so an earlier start never influences a later case.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type burstLive struct {
	name, pane, status string
}

// burstFixture builds an isolated CmdSpawn fixture whose agent list and the
// per-agent state reads report exactly the given live agents. The exact
// rules precede the generic fresh rules so they win the first-match match.
func burstFixture(t *testing.T, live []burstLive) spawnFixture {
	t.Helper()
	agents := []string{}
	for _, a := range live {
		status := a.status
		if status == "" {
			status = "working"
		}
		agents = append(agents, fmt.Sprintf(`{"name":%q,"pane_id":%q,"agent_status":%q}`, a.name, a.pane, status))
	}
	rules := []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + strings.Join(agents, ",") + `]}}`}}
	for _, a := range live {
		status := a.status
		if status == "" {
			status = "working"
		}
		rules = append(rules, fakecli.Rule{
			Argv:   []string{"agent", "get", a.name},
			Stdout: fmt.Sprintf(`{"result":{"agent":{"name":%q,"agent_status":%q}}}`, a.name, status),
		})
	}
	f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_LANES"] = "on"
	return f
}

// burstConfig sets the config entries the burst cases need, in the given
// order, which is the order the lane and custom-lane scans read them.
func burstConfig(f *spawnFixture, kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		f.ctx.Entries[kv[i]] = core.ConfigEntry{Value: kv[i+1], Source: "project"}
		f.ctx.Order = append(f.ctx.Order, kv[i])
	}
}

// burst12Row is the 12-column legacy roster line: a resident lane worker
// without the burst, args and effort columns. The pane id drops the hyphens
// of the worker name (the legacy p-build2 / p-review2 shape) so it lines up
// with the live agent's pane_id in the agent list.
func burst12Row(name, role, lane string) string {
	pane := "p-" + strings.ReplaceAll(name, "-", "")
	return strings.Join([]string{name, pane, "grok", role, "xai", "1", "/tmp/work", "now", "grok-4.7", "full", role, lane}, "\t")
}

// burst13Row is the roster line of a live temporary worker: the burst
// marker in column 13.
func burst13Row(name, role, lane string) string {
	return burst12Row(name, role, lane) + "\tburst"
}

// burstRosterRows returns the state roster lines without the header.
func burstRosterRows(t *testing.T, f spawnFixture) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatalf("no state roster: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 2 {
		return []string{}
	}
	return lines[1:]
}

// burstJSON parses the spawn stdout JSON object.
func burstJSON(t *testing.T, stdout, where string) *jsonjs.Object {
	t.Helper()
	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		t.Fatalf("%s: stdout is not parseable JSON: %v; %q", where, err, stdout)
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		t.Fatalf("%s: stdout JSON is not an object: %q", where, stdout)
	}
	return obj
}

// burstJSONString returns a string field of the spawn JSON.
func burstJSONString(t *testing.T, obj *jsonjs.Object, key, where string) string {
	t.Helper()
	v, present := obj.Get(key)
	if !present {
		t.Fatalf("%s: spawn JSON has no %q field: %#v", where, key, obj)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s: spawn JSON %q is not a string: %#v", where, key, v)
	}
	return s
}

// burstAgentStarts lists the worker names of the recorded agent start calls.
func burstAgentStarts(calls []fakecli.Call) []string {
	names := []string{}
	for _, c := range calls {
		if len(c.Argv) >= 3 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			names = append(names, c.Argv[2])
		}
	}
	return names
}

func TestSpawnBurstContract(t *testing.T) {
	t.Run("flex docs capacity 0 opens a burst worker with the exact 16-column roster row", func(t *testing.T) {
		// (a) the docs lane holds no resident worker (capacity 0): the
		// documenter opens a temporary (burst) worker.
		f := burstFixture(t, nil)
		burstConfig(&f, "pane_mode", "flex")
		f.roster(t)
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "a")
		if got := burstJSONString(t, obj, "name", "a"); got != "docs" {
			t.Fatalf("name=%q, want docs; %s", got, stdout)
		}
		v, ok := obj.Get("burst")
		if !ok || v != true {
			t.Fatalf("the JSON does not mark the temporary worker: %s", stdout)
		}
		effort := burstJSONString(t, obj, "effort", "a")
		rows := burstRosterRows(t, f)
		if len(rows) != 1 {
			t.Fatalf("the roster gained %d rows, want exactly one: %v", len(rows), rows)
		}
		fields := strings.Split(rows[0], "\t")
		// DJA-392 fatia 2 R2: the last column is the effective MCP choice
		// (the documenter frontmatter mcp: none).
		if len(fields) != 16 {
			t.Fatalf("the burst row has %d columns, want 16 (13 present even when empty): %q", len(fields), rows[0])
		}
		if fields[0] != "docs" || fields[11] != "docs" {
			t.Fatalf("lane column: %q", rows[0])
		}
		if fields[12] != "burst" {
			t.Fatalf("roster column 13 is not the burst marker: %q", rows[0])
		}
		if fields[13] != "" {
			t.Fatalf("roster column 14 (native args) must be empty here, got %q", fields[13])
		}
		wantEffort := effort
		if effort == "default" {
			wantEffort = ""
		}
		if fields[14] != wantEffort {
			t.Fatalf("roster column 15 %q is not the JSON effort %q", fields[14], effort)
		}
		if fields[15] != "none" {
			t.Fatalf("roster column 16 (MCP) %q is not the documenter frontmatter none", fields[15])
		}
		starts := burstAgentStarts(calls)
		if len(starts) != 1 || starts[0] != "docs" {
			t.Fatalf("agent start calls=%v, want exactly [docs]", starts)
		}
	})

	t.Run("strict full build lane refuses 10 with no new row and no start", func(t *testing.T) {
		// (b) the strict mode (the default) never opens a burst: the
		// documenter sits in the build lane, and a full build lane is busy 10.
		f := burstFixture(t, []burstLive{{"build", "p-build", "working"}, {"build-2", "p-build2", "working"}})
		f.roster(t, burst12Row("build", "implementer", "build"), burst12Row("build-2", "tasker", "build"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter"})
		if code != 10 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "b")
		if got := burstJSONString(t, obj, "status", "b"); got != "busy" {
			t.Fatalf("status=%q, want busy; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "lane", "b"); got != "build" {
			t.Fatalf("lane=%q, want build; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "name", "b"); got != "build" {
			t.Fatalf("name=%q, want build; %s", got, stdout)
		}
		if !strings.Contains(stderr, "lane 'build' is full (2 of 2: build build-2)") {
			t.Fatalf("stderr=%q", stderr)
		}
		rows := burstRosterRows(t, f)
		if len(rows) != 2 || strings.Split(rows[0], "\t")[0] != "build" || strings.Split(rows[1], "\t")[0] != "build-2" {
			t.Fatalf("the state roster changed on the refusal: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the refusal opened a worker: %v", starts)
		}
	})

	t.Run("flex full review lane opens the review-2 burst", func(t *testing.T) {
		// (c) a full review lane (capacity 1, no idle) opens a temporary
		// reviewer (the reviewer is a default flex role).
		f := burstFixture(t, []burstLive{{"review", "p-review", "working"}})
		burstConfig(&f, "pane_mode", "flex")
		f.roster(t, burst12Row("review", "reviewer", "review"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "c")
		if got := burstJSONString(t, obj, "name", "c"); got != "review-2" {
			t.Fatalf("name=%q, want review-2; %s", got, stdout)
		}
		if v, ok := obj.Get("burst"); !ok || v != true {
			t.Fatalf("the JSON does not mark the temporary worker: %s", stdout)
		}
		rows := burstRosterRows(t, f)
		if len(rows) != 2 {
			t.Fatalf("roster rows=%d, want 2: %v", len(rows), rows)
		}
		fields := strings.Split(rows[1], "\t")
		if fields[0] != "review-2" || fields[12] != "burst" || fields[11] != "review" {
			t.Fatalf("the review-2 roster row lacks the burst marker or lane: %q", rows[1])
		}
		starts := burstAgentStarts(calls)
		if len(starts) != 1 || starts[0] != "review-2" {
			t.Fatalf("agent start calls=%v, want exactly [review-2]", starts)
		}
	})

	t.Run("flex_extra 1 with a live burst refuses 10", func(t *testing.T) {
		// (d) one temporary worker is already live and flex_extra is 1 (the
		// default): the burst slot is taken, the lane is busy 10.
		f := burstFixture(t, []burstLive{{"review", "p-review", "working"}, {"review-2", "p-review2", "working"}})
		burstConfig(&f, "pane_mode", "flex")
		f.roster(t, burst12Row("review", "reviewer", "review"), burst13Row("review-2", "reviewer", "review"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer"})
		if code != 10 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "d")
		if got := burstJSONString(t, obj, "status", "d"); got != "busy" {
			t.Fatalf("status=%q, want busy; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "lane", "d"); got != "review" {
			t.Fatalf("lane=%q, want review; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "name", "d"); got != "review" {
			t.Fatalf("name=%q, want review; %s", got, stdout)
		}
		if !strings.Contains(stderr, "lane 'review' is full (2 of 1: review review-2)") {
			t.Fatalf("stderr=%q", stderr)
		}
		rows := burstRosterRows(t, f)
		if len(rows) != 2 {
			t.Fatalf("the state roster changed on the refusal: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the refusal opened a worker: %v", starts)
		}
	})

	t.Run("flex_extra 2 allows the second temporary worker", func(t *testing.T) {
		// (d2) with the same live temporary worker, flex_extra=2 opens the
		// second temporary worker.
		f := burstFixture(t, []burstLive{{"review", "p-review", "working"}, {"review-2", "p-review2", "working"}})
		burstConfig(&f, "pane_mode", "flex", "flex_extra", "2")
		f.roster(t, burst12Row("review", "reviewer", "review"), burst13Row("review-2", "reviewer", "review"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "d2")
		if got := burstJSONString(t, obj, "name", "d2"); got != "review-3" {
			t.Fatalf("name=%q, want review-3; %s", got, stdout)
		}
		if v, ok := obj.Get("burst"); !ok || v != true {
			t.Fatalf("the JSON does not mark the temporary worker: %s", stdout)
		}
		starts := burstAgentStarts(calls)
		if len(starts) != 1 || starts[0] != "review-3" {
			t.Fatalf("agent start calls=%v, want exactly [review-3]", starts)
		}
	})

	t.Run("a role outside flex_roles refuses 10 with the role hint", func(t *testing.T) {
		// (e) a role outside flex_roles may not use the temporary panel: the
		// capacity-0 docs lane refuses it with the role message (busy 10).
		f := burstFixture(t, nil)
		burstConfig(&f, "pane_mode", "flex", "flex_roles", "reviewer")
		f.roster(t)
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter"})
		if code != 10 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "e")
		if got := burstJSONString(t, obj, "status", "e"); got != "busy" {
			t.Fatalf("status=%q, want busy; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "lane", "e"); got != "docs" {
			t.Fatalf("lane=%q, want docs; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "name", "e"); got != "" {
			t.Fatalf("name=%q, want empty; %s", got, stdout)
		}
		if !strings.Contains(stderr, "role 'documenter' may not use the temporary panel (flex_roles=reviewer)") {
			t.Fatalf("stderr=%q", stderr)
		}
		if rows := burstRosterRows(t, f); len(rows) != 0 {
			t.Fatalf("the refusal wrote a roster row: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the refusal opened a worker: %v", starts)
		}
	})

	t.Run("flex_extra 0 refuses with the none hint", func(t *testing.T) {
		// (f) flex_extra=0: no temporary panel at all (the lane message with
		// no live temporary workers).
		f := burstFixture(t, nil)
		burstConfig(&f, "pane_mode", "flex", "flex_extra", "0")
		f.roster(t)
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter"})
		if code != 10 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "f")
		if got := burstJSONString(t, obj, "lane", "f"); got != "docs" {
			t.Fatalf("lane=%q, want docs; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "name", "f"); got != "" {
			t.Fatalf("name=%q, want empty; %s", got, stdout)
		}
		if !strings.Contains(stderr, "lane 'docs' only takes a temporary worker (pane_mode=flex) and none is free: flex_extra=0, live temporary workers: none. Raise flex_extra.") {
			t.Fatalf("stderr=%q", stderr)
		}
		if rows := burstRosterRows(t, f); len(rows) != 0 {
			t.Fatalf("the refusal wrote a roster row: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the refusal opened a worker: %v", starts)
		}
	})

	t.Run("two live bursts are listed and the remedy names the first", func(t *testing.T) {
		// (f2) the burst slot is taken by live temporary workers: the lane
		// message lists them (all lanes) and names the first one for release.
		f := burstFixture(t, []burstLive{{"docs", "p-docs", "working"}, {"review-2", "p-review2", "working"}})
		burstConfig(&f, "pane_mode", "flex")
		f.roster(t, burst13Row("docs", "documenter", "docs"), burst13Row("review-2", "reviewer", "review"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter"})
		if code != 10 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "f2")
		if got := burstJSONString(t, obj, "lane", "f2"); got != "docs" {
			t.Fatalf("lane=%q, want docs; %s", got, stdout)
		}
		if got := burstJSONString(t, obj, "name", "f2"); got != "docs" {
			t.Fatalf("name=%q, want docs; %s", got, stdout)
		}
		if !strings.Contains(stderr, "lane 'docs' only takes a temporary worker (pane_mode=flex) and none is free: flex_extra=1, live temporary workers: docs, review-2. Release one (release docs), or raise flex_extra.") {
			t.Fatalf("stderr=%q", stderr)
		}
		if rows := burstRosterRows(t, f); len(rows) != 2 {
			t.Fatalf("the state roster changed on the refusal: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the refusal opened a worker: %v", starts)
		}
	})

	t.Run("an idle burst worker is reused without a burst key or new worker", func(t *testing.T) {
		// (g) an idle temporary worker is reused, not re-burst: the docs lane
		// with an idle burst documenter reuses it (no new pane, no burst key).
		// The row makes the compatible cwd, kind, model, effort and approvals
		// explicit so the reuse gates line up with the request.
		f := burstFixture(t, []burstLive{{"docs", "p-docs", "idle"}})
		burstConfig(&f, "pane_mode", "flex")
		f.roster(t, strings.Join([]string{"docs", "p-docs", "grok", "documenter", "xai", "1", f.cwd, "now", "", "full", "documenter", "docs", "burst", "", ""}, "\t"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter", "--kind", "grok"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "g")
		if got := burstJSONString(t, obj, "name", "g"); got != "docs" {
			t.Fatalf("name=%q, want docs; %s", got, stdout)
		}
		v, ok := obj.Get("reused")
		if !ok || v != true {
			t.Fatalf("the reuse is not marked: %s", stdout)
		}
		if _, present := obj.Get("burst"); present {
			t.Fatalf("a reuse is not a burst: %s", stdout)
		}
		if rows := burstRosterRows(t, f); len(rows) != 1 {
			t.Fatalf("the reuse opened a new worker row: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the reuse started a pane: %v", starts)
		}
	})

	t.Run("a custom ops lane with max_workers 1 refuses 8 via the global cap", func(t *testing.T) {
		// (h) a custom lane whose max_workers was written as the capacity sum
		// (the old lane-file write): the burst hits the global cap (exit 8).
		f := burstFixture(t, []burstLive{{"ops", "p-ops", "working"}})
		burstConfig(&f, "pane_mode", "flex", "lane_ops_roles", "implementer,tasker,documenter", "lane_ops_panes", "1", "max_workers", "1")
		f.roster(t, burst12Row("ops", "implementer", "ops"))
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"documenter"})
		if code != 8 {
			t.Fatalf("exit=%d message=%q", code, message)
		}
		if !strings.Contains(message, "max_workers=1 reached (1 live: ops)") {
			t.Fatalf("message=%q", message)
		}
		if rows := burstRosterRows(t, f); len(rows) != 1 {
			t.Fatalf("the state roster changed on the refusal: %v", rows)
		}
		if starts := burstAgentStarts(calls); len(starts) != 0 {
			t.Fatalf("the refusal opened a worker: %v", starts)
		}
	})

	t.Run("a custom ops lane with max_workers 2 opens the ops-2 burst", func(t *testing.T) {
		// (h2) with the sum + flex_extra the lane file now writes, the burst
		// opens inside the custom lane.
		f := burstFixture(t, []burstLive{{"ops", "p-ops", "working"}})
		burstConfig(&f, "pane_mode", "flex", "lane_ops_roles", "implementer,tasker,documenter", "lane_ops_panes", "1", "max_workers", "2")
		f.roster(t, burst12Row("ops", "implementer", "ops"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"documenter"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		obj := burstJSON(t, stdout, "h2")
		if got := burstJSONString(t, obj, "name", "h2"); got != "ops-2" {
			t.Fatalf("name=%q, want ops-2; %s", got, stdout)
		}
		if v, ok := obj.Get("burst"); !ok || v != true {
			t.Fatalf("the JSON does not mark the temporary worker: %s", stdout)
		}
		rows := burstRosterRows(t, f)
		if len(rows) != 2 {
			t.Fatalf("roster rows=%d, want 2: %v", len(rows), rows)
		}
		fields := strings.Split(rows[1], "\t")
		if fields[0] != "ops-2" || fields[12] != "burst" || fields[11] != "ops" {
			t.Fatalf("the ops-2 roster row lacks the burst marker or lane: %q", rows[1])
		}
		starts := burstAgentStarts(calls)
		if len(starts) != 1 || starts[0] != "ops-2" {
			t.Fatalf("agent start calls=%v, want exactly [ops-2]", starts)
		}
	})
}
