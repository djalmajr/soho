package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func laneModelFixture(t *testing.T, capValue, models string, extraRules ...fakecli.Rule) spawnFixture {
	t.Helper()
	return laneModelFixtureWithCapacity(t, capValue, models, "2", extraRules...)
}

func laneModelFixtureWithCapacity(t *testing.T, capValue, models, panes string, extraRules ...fakecli.Rule) spawnFixture {
	t.Helper()
	rules := append(append([]fakecli.Rule{}, extraRules...), freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries = map[string]core.ConfigEntry{
		"lane_build_roles":  {Value: "scouter", Source: "project"},
		"lane_build_panes":  {Value: panes, Source: "project"},
		"lane_build_kind":   {Value: "grok", Source: "project"},
		"lane_build_models": {Value: models, Source: "project"},
		"model_cap":         {Value: capValue, Source: "project"},
	}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_panes", "lane_build_kind", "lane_build_models", "model_cap"}
	f.roster(t)
	return f
}

func appendRosterRow(t *testing.T, f spawnFixture, name, model, lane string) {
	t.Helper()
	path := filepath.Join(f.state, "ws", "agents.tsv")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{name, "p-" + name, "grok", "implementer", "xai", "1", "/tmp/work", "now", model, "ask", "implementer", lane}, "\t")
	if err := os.WriteFile(path, append(body, []byte(row+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLaneIdleRow(t *testing.T, f spawnFixture, name, model string) {
	t.Helper()
	appendLaneIdleRowWith(t, f, name, model, f.cwd, "high", "auto", "", "")
}

// appendLaneIdleRowWith writes one idle lane row with every field the reuse
// predicate checks, so a table case can mismatch exactly one of them.
func appendLaneIdleRowWith(t *testing.T, f spawnFixture, name, model, cwd, effort, approvals, mcp, nativeArgs string) {
	t.Helper()
	path := filepath.Join(f.state, "ws", "agents.tsv")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{name, "p-" + name, "grok", "scouter", "xai", "1", cwd, "now", model, approvals, "scouter", "build", "", nativeArgs, effort, mcp}, "\t")
	if err := os.WriteFile(path, append(body, []byte(row+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func addWorkspaceRow(t *testing.T, f spawnFixture, name, model string) {
	t.Helper()
	dir := filepath.Join(f.state, "ws2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{name, "p-" + name, "grok", "implementer", "xai", "1", "/tmp/work", "now", model, "ask", "implementer", "build"}, "\t")
	body := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + row + "\n"
	if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runLaneModelSpawn(t *testing.T, f spawnFixture, args []string) (code int, stdout, stderr, refusal string, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	func() {
		defer func() {
			if value := recover(); value != nil {
				if exit, ok := value.(*platform.ExitError); ok {
					code, refusal = exit.Code, exit.Msg
					return
				}
				panic(value)
			}
		}()
		CmdSpawn(args, f.ctx, f.env, f.cwd)
	}()
	calls, _ = fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	return code, out.String(), errOut.String(), refusal, calls
}

func rosterModels(t *testing.T, f spawnFixture) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) > 8 {
			out = append(out, fields[8])
		}
	}
	return out
}

func TestSpawnRotatesLaneModelsByLiveCount(t *testing.T) {
	f := laneModelFixture(t, "2", "m1,m2")
	appendRosterRow(t, f, "a1", "m1", "other")
	addWorkspaceRow(t, f, "a2", "m1")
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
	}
	if !strings.Contains(stdout, `"model": "m2"`) || !strings.Contains(stdout, `"model_choice": "list"`) {
		t.Fatalf("model choice missing from stdout=%q", stdout)
	}
	models := rosterModels(t, f)
	if len(models) != 2 || models[1] != "m2" {
		t.Fatalf("roster models = %#v, want the new worker on m2\n%s", models, rosterBytes(t, f))
	}
	if acts := herdrActs(calls); len(acts) == 0 {
		t.Fatalf("no worker was opened: %#v", calls)
	}
}

func TestSpawnRefusesWhenEveryListedModelIsFull(t *testing.T) {
	f := laneModelFixture(t, "2", "m1,m2")
	for _, item := range []struct{ name, model string }{{"a1", "m1"}, {"a2", "m1"}, {"b1", "m2"}, {"b2", "m2"}} {
		appendRosterRow(t, f, item.name, item.model, "other")
	}
	before := rosterBytes(t, f)
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 13 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	want := "spawn: every model of lane 'build' is at the cap of 2 live workers: m1 (2), m2 (2); release a worker or raise model_cap"
	if refusal != want {
		t.Fatalf("refusal=%q\nwant    %q", refusal, want)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("the refusal opened something: %v", acts)
	}
	if after := rosterBytes(t, f); after != before {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestSpawnModelCapDoesNotCountGoneRows(t *testing.T) {
	rules := append([]fakecli.Rule{{Argv: []string{"agent", "get", "dead"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}}, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries = map[string]core.ConfigEntry{
		"lane_build_roles":  {Value: "scouter", Source: "project"},
		"lane_build_panes":  {Value: "2", Source: "project"},
		"lane_build_kind":   {Value: "grok", Source: "project"},
		"lane_build_models": {Value: "m1,m2", Source: "project"},
		"model_cap":         {Value: "2", Source: "project"},
	}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_panes", "lane_build_kind", "lane_build_models", "model_cap"}
	f.roster(t, strings.Join([]string{"dead", "p-dead", "grok", "implementer", "xai", "1", "/tmp/work", "now", "m1", "ask", "implementer", "other"}, "\t"))
	appendRosterRow(t, f, "a1", "m1", "other")
	code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
	}
	if !strings.Contains(stdout, `"model": "m1"`) {
		t.Fatalf("a gone row consumed a slot: %q", stdout)
	}
}

func TestSpawnExplicitModelWarnsAndSkipsRotation(t *testing.T) {
	for _, tc := range []struct {
		name, lanes string
		args        []string
	}{
		{"flag with lanes on", "on", []string{"scouter", "--model", "m1"}},
		{"lanes off", "off", []string{"scouter", "--model", "m1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixture(t, "2", "m1,m2")
			f.env["SOHO_LANES"] = tc.lanes
			appendRosterRow(t, f, "a1", "m1", "other")
			appendRosterRow(t, f, "a2", "m1", "other")
			code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, tc.args)
			if code != 0 || refusal != "" {
				t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
			}
			if !strings.Contains(stdout, `"model": "m1"`) {
				t.Fatalf("explicit model lost: %q", stdout)
			}
			want := "soho: warning: spawn: model 'm1' already has 2 live workers (model_cap 2)\n"
			if strings.Count(stderr, want) != 1 {
				t.Fatalf("stderr=%q, want exactly one %q", stderr, want)
			}
			choice := "flag"
			if !strings.Contains(stdout, `"model_choice": "`+choice+`"`) {
				t.Fatalf("model_choice=%q", stdout)
			}
			if len(herdrActs(calls)) == 0 {
				t.Fatalf("no worker was opened: %#v", calls)
			}
		})
	}
}

func TestSpawnModelCapOffUsesFirstListedModel(t *testing.T) {
	f := laneModelFixture(t, "off", "m1,m2")
	for _, item := range []struct{ name, model string }{{"a1", "m1"}, {"a2", "m1"}, {"b1", "m2"}, {"b2", "m2"}} {
		appendRosterRow(t, f, item.name, item.model, "other")
	}
	code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
	}
	if !strings.Contains(stdout, `"model": "m1"`) || !strings.Contains(stdout, `"model_choice": "list"`) {
		t.Fatalf("cap off did not use the first model: %q", stdout)
	}
	if strings.Contains(stderr, "already has") {
		t.Fatalf("cap off warned: %q", stderr)
	}
}

func TestSpawnModelChoiceLaneAndDefault(t *testing.T) {
	t.Run("lane model", func(t *testing.T) {
		f := laneModelFixture(t, "off", "")
		f.ctx.Entries["lane_build_model"] = core.ConfigEntry{Value: "m1", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "lane_build_model")
		code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
		if code != 0 || refusal != "" || !strings.Contains(stdout, `"model_choice": "lane"`) {
			t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
		}
	})
	t.Run("default model", func(t *testing.T) {
		f := laneModelFixture(t, "off", "")
		f.ctx.Entries["role_scouter_model"] = core.ConfigEntry{Value: "m1", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "role_scouter_model")
		code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
		if code != 0 || refusal != "" || !strings.Contains(stdout, `"model_choice": "default"`) {
			t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
		}
	})
}

// Reuse adds no live worker, so a capped model must still reuse its idle lane
// worker. The equivalent single-model lane must make the same reuse decision.
func TestSpawnReusesCappedModelBeforeRotating(t *testing.T) {
	for _, tc := range []struct {
		name, models, choice string
		list                 bool
	}{
		{"models list", "m1,m2", "list", true},
		{"single model", "", "lane", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixture(t, "2", tc.models, idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
			if !tc.list {
				f.ctx.Entries["lane_build_model"] = core.ConfigEntry{Value: "m1", Source: "project"}
				f.ctx.Order = append(f.ctx.Order, "lane_build_model")
			}
			appendLaneIdleRow(t, f, "m1-a", "m1")
			appendLaneIdleRow(t, f, "m1-b", "m1")
			before := rosterBytes(t, f)

			code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
			if code != 0 || refusal != "" {
				t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
			}
			if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"model": "m1"`) {
				t.Fatalf("the capped idle worker was not reused: %q", stdout)
			}
			if !strings.Contains(stdout, `"model_choice": "`+tc.choice+`"`) {
				t.Fatalf("model_choice=%q, want %s", stdout, tc.choice)
			}
			if acts := herdrActs(calls); len(acts) != 0 {
				t.Fatalf("reuse opened something: %v", acts)
			}
			if after := rosterBytes(t, f); after != before {
				t.Fatalf("reuse changed the roster\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestSpawnListReuseRequiresAModelFromTheList(t *testing.T) {
	f := laneModelFixture(t, "off", "m1,m2", idleHerdrRule("other"))
	appendLaneIdleRow(t, f, "other", "m3")
	code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
	}
	if !strings.Contains(stdout, `"model": "m1"`) || !strings.Contains(stdout, `"model_choice": "list"`) {
		t.Fatalf("rotation did not pick the first listed model: %q", stdout)
	}
	if strings.Contains(stdout, `"reused": true`) {
		t.Fatalf("an idle worker on m3 was reused through the list path: %q", stdout)
	}
}

// Ported from lanemod-r2's executed probe: a listed model on an idle worker is
// only a reuse candidate when every filter the reuse branch applies passes.
// A filter mismatch must leave ChooseSpawnModel's rotation in charge.
func TestListedReuseCandidateMustPassTheReuseFilters(t *testing.T) {
	for _, tc := range []struct {
		name, cwd, effort, approvals, mcp, nativeArgs, wantSpec string
		otherCwd                                                bool
	}{
		{name: "cwd", effort: "high", approvals: "auto", otherCwd: true, wantSpec: "'/elsewhere'"},
		{name: "effort", cwd: "default", effort: "low", approvals: "auto", wantSpec: "'low'"},
		{name: "approvals", cwd: "default", effort: "high", approvals: "ask", wantSpec: "'ask'"},
		{name: "mcp", cwd: "default", effort: "high", approvals: "auto", mcp: "inherit", wantSpec: "'inherit'"},
		{name: "native args", cwd: "default", effort: "high", approvals: "auto", nativeArgs: "--old", wantSpec: "'--old'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "4", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
			cwd := f.cwd
			if tc.otherCwd {
				cwd = filepath.Join(f.root, "elsewhere")
			}
			appendLaneIdleRowWith(t, f, "m1-a", "m1", cwd, tc.effort, tc.approvals, tc.mcp, tc.nativeArgs)
			appendLaneIdleRowWith(t, f, "m1-b", "m1", cwd, tc.effort, tc.approvals, tc.mcp, tc.nativeArgs)
			code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
			if code != 0 || refusal != "" {
				t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
			}
			if !strings.Contains(stdout, `"model": "m2"`) || !strings.Contains(stdout, `"model_choice": "list"`) {
				t.Fatalf("rotation to m2 missing; %s mismatch leaked into the model choice: %q (cwd %q)", tc.name, stdout, tc.wantSpec)
			}
			if strings.Contains(stdout, `"reused": true`) {
				t.Fatalf("a filter-mismatched worker was reused: %q", stdout)
			}
			models := rosterModels(t, f)
			if len(models) != 3 || models[2] != "m2" {
				t.Fatalf("roster models = %#v, want a third row on m2", models)
			}
			if acts := herdrActs(calls); len(acts) == 0 {
				t.Fatalf("no fresh worker was opened: %#v", calls)
			}
		})
	}
}

func TestFullLaneWithMismatchedListedWorkersRefuses(t *testing.T) {
	f := laneModelFixture(t, "2", "m1,m2", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
	elsewhere := filepath.Join(f.root, "elsewhere")
	appendLaneIdleRowWith(t, f, "m1-a", "m1", elsewhere, "high", "auto", "", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", elsewhere, "high", "auto", "", "")
	before := rosterBytes(t, f)
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 13 {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	want := "spawn: lane 'build' has no idle worker matching kind 'grok', model 'm2' and cwd '" + f.cwd + "' (idle: 'm1-a' runs grok m1 in " + elsewhere + "); release it or raise the lane's panes"
	if refusal != want {
		t.Fatalf("refusal=%q\nwant    %q", refusal, want)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("the refusal opened something: %v", acts)
	}
	if after := rosterBytes(t, f); after != before {
		t.Fatalf("the refusal changed the roster\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestOneBelowLaneCapacityWithMismatchedListedWorkersRotates(t *testing.T) {
	f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "3", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
	elsewhere := filepath.Join(f.root, "elsewhere")
	appendLaneIdleRowWith(t, f, "m1-a", "m1", elsewhere, "high", "auto", "", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", elsewhere, "high", "auto", "", "")
	code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	if !strings.Contains(stdout, `"model": "m2"`) {
		t.Fatalf("rotation to m2 missing: %q", stdout)
	}
	models := rosterModels(t, f)
	if len(models) != 3 || models[2] != "m2" {
		t.Fatalf("roster models = %#v, want a third row on m2", models)
	}
}

func TestSkillExitCodeListNamesModelCap(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "13 lane `kind-mismatch` (the live session runs another CLI: `release` the lane, and set `lane.<name>.kind` so it cannot recur) or every model of a lane's models list at model_cap"
	if !strings.Contains(string(body), want) {
		t.Fatalf("SKILL.md exit-code list lacks %q", want)
	}
}

func TestListedReuseCandidateGoneBetweenProbeAndReuse(t *testing.T) {
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "m1-a"}, Call: 1, Stdout: `{"result":{"agent":{"name":"m1-a","agent_status":"idle"}}}`},
		{Argv: []string{"agent", "get", "m1-a"}, Call: 2, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
	}
	f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "4", append(rules, idleHerdrRule("m1-b"))...)
	appendLaneIdleRow(t, f, "m1-a", "m1")
	appendLaneIdleRow(t, f, "m1-b", "m1")
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	if strings.Contains(stdout, `"name": "m1-a"`) {
		t.Fatalf("the worker gone between the probe and reuse was reused: %q", stdout)
	}
	if !strings.Contains(stdout, `"model": "m1"`) {
		t.Fatalf("the remaining listed model was lost: %q", stdout)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("a fresh worker opened after a reuse became available: %v", acts)
	}
}

// Ported from lanemod-r3's executed probe: the shared reuse predicate must be
// fed the same normalized MCP and effort the reuse branch uses. A non-canonical
// lane MCP value or a clamped lane effort must not cost the spawn its reuse.
func TestProbeListedReuseMcpCanonicalDrift(t *testing.T) {
	for _, tc := range []struct {
		name, laneMcp, rowMcp string
	}{
		{"non-canonical lane value", "a, b", "a,b"},
		{"order and duplicates", "b, a, b", "b,a"},
		{"canonical control", "a,b", "a,b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "4", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
			f.ctx.Entries["lane_build_mcp"] = core.ConfigEntry{Value: tc.laneMcp, Source: "project"}
			f.ctx.Order = append(f.ctx.Order, "lane_build_mcp")
			appendLaneIdleRowWith(t, f, "m1-a", "m1", f.cwd, "high", "auto", tc.rowMcp, "")
			appendLaneIdleRowWith(t, f, "m1-b", "m1", f.cwd, "high", "auto", tc.rowMcp, "")
			before := rosterBytes(t, f)
			code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
			if code != 0 || refusal != "" {
				t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
			}
			if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"model": "m1"`) {
				t.Fatalf("the fully-matching idle m1 worker was not reused: stdout=%q", stdout)
			}
			if acts := herdrActs(calls); len(acts) != 0 {
				t.Fatalf("reuse opened something: %v", acts)
			}
			if after := rosterBytes(t, f); after != before {
				t.Fatalf("reuse changed the roster\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestProbeListedReuseMcpCanonicalDriftFullLane(t *testing.T) {
	f := laneModelFixture(t, "2", "m1,m2", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
	f.ctx.Entries["lane_build_mcp"] = core.ConfigEntry{Value: "a, b", Source: "project"}
	f.ctx.Order = append(f.ctx.Order, "lane_build_mcp")
	appendLaneIdleRowWith(t, f, "m1-a", "m1", f.cwd, "high", "auto", "a,b", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", f.cwd, "high", "auto", "a,b", "")
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	if !strings.Contains(stdout, `"reused": true`) {
		t.Fatalf("the fully-matching idle m1 worker was not reused: stdout=%q", stdout)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("reuse opened something: %v", acts)
	}
}

func TestProbeListedReuseEffortClampDrift(t *testing.T) {
	for _, tc := range []struct {
		name, laneEffort, maxEffort string
	}{
		{"clamped lane effort", "xhigh", "high"},
		{"above kind ceiling", "max", "high"},
		{"unclamped control", "high", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "4", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
			f.ctx.Entries["lane_build_effort"] = core.ConfigEntry{Value: tc.laneEffort, Source: "project"}
			f.ctx.Order = append(f.ctx.Order, "lane_build_effort")
			f.ctx.Entries["max_effort"] = core.ConfigEntry{Value: tc.maxEffort, Source: "project"}
			f.ctx.Order = append(f.ctx.Order, "max_effort")
			appendLaneIdleRowWith(t, f, "m1-a", "m1", f.cwd, "high", "auto", "", "")
			appendLaneIdleRowWith(t, f, "m1-b", "m1", f.cwd, "high", "auto", "", "")
			before := rosterBytes(t, f)
			code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
			if code != 0 || refusal != "" {
				t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
			}
			if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"model": "m1"`) {
				t.Fatalf("the fully-matching idle m1 worker was not reused: stdout=%q", stdout)
			}
			if acts := herdrActs(calls); len(acts) != 0 {
				t.Fatalf("reuse opened something: %v", acts)
			}
			if after := rosterBytes(t, f); after != before {
				t.Fatalf("reuse changed the roster\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestListedReuseAcceptsRequestedCwdWithTrailingSlash(t *testing.T) {
	f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "4", idleHerdrRule("m1-a"), idleHerdrRule("m1-b"))
	appendLaneIdleRowWith(t, f, "m1-a", "m1", f.cwd, "high", "auto", "", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", f.cwd, "high", "auto", "", "")
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--cwd", f.cwd + string(os.PathSeparator)})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	if !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"model": "m1"`) {
		t.Fatalf("a trailing slash lost the reuse: %q", stdout)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("reuse opened something: %v", acts)
	}
}
