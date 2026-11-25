package spawn

import (
	"os"
	"path/filepath"
	"slices"
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
	appendLaneIdleRowWith(t, f, name, model, f.cwd, "high", "auto", "none", "")
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
	if !spawnJSONHasField(t, stdout, `"model": "m2"`) || !spawnJSONHasField(t, stdout, `"model_choice": "list"`) {
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
	if !spawnJSONHasField(t, stdout, `"model": "m1"`) {
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
			if !spawnJSONHasField(t, stdout, `"model": "m1"`) {
				t.Fatalf("explicit model lost: %q", stdout)
			}
			want := "soho: warning: spawn: model 'm1' already has 2 live workers (model_cap 2)\n"
			if strings.Count(stderr, want) != 1 {
				t.Fatalf("stderr=%q, want exactly one %q", stderr, want)
			}
			choice := "flag"
			if !spawnJSONHasField(t, stdout, `"model_choice": "`+choice+`"`) {
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
	if !spawnJSONHasField(t, stdout, `"model": "m1"`) || !spawnJSONHasField(t, stdout, `"model_choice": "list"`) {
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
		if code != 0 || refusal != "" || !spawnJSONHasField(t, stdout, `"model_choice": "lane"`) {
			t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
		}
	})
	t.Run("default model", func(t *testing.T) {
		f := laneModelFixture(t, "off", "")
		f.ctx.Entries["role_scouter_model"] = core.ConfigEntry{Value: "m1", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "role_scouter_model")
		code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
		if code != 0 || refusal != "" || !spawnJSONHasField(t, stdout, `"model_choice": "default"`) {
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
			if !spawnJSONHasField(t, stdout, `"reused": true`) || !spawnJSONHasField(t, stdout, `"model": "m1"`) {
				t.Fatalf("the capped idle worker was not reused: %q", stdout)
			}
			if !spawnJSONHasField(t, stdout, `"model_choice": "`+tc.choice+`"`) {
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
	if !spawnJSONHasField(t, stdout, `"model": "m1"`) || !spawnJSONHasField(t, stdout, `"model_choice": "list"`) {
		t.Fatalf("rotation did not pick the first listed model: %q", stdout)
	}
	if spawnJSONHasField(t, stdout, `"reused": true`) {
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
		{name: "cwd", effort: "high", approvals: "auto", mcp: "none", otherCwd: true, wantSpec: "'/elsewhere'"},
		{name: "effort", cwd: "default", effort: "low", approvals: "auto", mcp: "none", wantSpec: "'low'"},
		{name: "approvals", cwd: "default", effort: "high", approvals: "ask", mcp: "none", wantSpec: "'ask'"},
		{name: "mcp", cwd: "default", effort: "high", approvals: "auto", mcp: "inherit", wantSpec: "'inherit'"},
		{name: "native args", cwd: "default", effort: "high", approvals: "auto", mcp: "none", nativeArgs: "--old", wantSpec: "'--old'"},
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
			if !spawnJSONHasField(t, stdout, `"model": "m2"`) || !spawnJSONHasField(t, stdout, `"model_choice": "list"`) {
				t.Fatalf("rotation to m2 missing; %s mismatch leaked into the model choice: %q (cwd %q)", tc.name, stdout, tc.wantSpec)
			}
			if spawnJSONHasField(t, stdout, `"reused": true`) {
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
	appendLaneIdleRowWith(t, f, "m1-a", "m1", elsewhere, "high", "auto", "none", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", elsewhere, "high", "auto", "none", "")
	before := rosterBytes(t, f)
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 13 {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	assertGroupMismatch(t, code, stdout, stderr, "cwd differs ("+elsewhere+" vs "+f.cwd+")")
	obj := parseSpawnJSONLineText(t, strings.SplitN(stdout, "\n", 2)[0])
	spawnOneLineField(t, obj, "session_cwd", elsewhere)
	spawnOneLineField(t, obj, "requested_cwd", f.cwd)
	if spawnJSONHasField(t, stdout, `"requested_model": "m2"`) {
		t.Fatalf("full lane rotated the inherited list: %q", stdout)
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
	appendLaneIdleRowWith(t, f, "m1-a", "m1", elsewhere, "high", "auto", "none", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", elsewhere, "high", "auto", "none", "")
	code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, []string{"scouter"})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	if !spawnJSONHasField(t, stdout, `"model": "m2"`) {
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
	want := "13 `kind-mismatch` for incompatible idle workers in a full lane, or when opening on an inherited `lane.<name>.models` list whose entries are all at `model_cap`"
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
	if spawnJSONHasField(t, stdout, `"name": "m1-a"`) {
		t.Fatalf("the worker gone between the probe and reuse was reused: %q", stdout)
	}
	if !spawnJSONHasField(t, stdout, `"model": "m1"`) {
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
			if !spawnJSONHasField(t, stdout, `"reused": true`) || !spawnJSONHasField(t, stdout, `"model": "m1"`) {
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
	if !spawnJSONHasField(t, stdout, `"reused": true`) {
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
			appendLaneIdleRowWith(t, f, "m1-a", "m1", f.cwd, "high", "auto", "none", "")
			appendLaneIdleRowWith(t, f, "m1-b", "m1", f.cwd, "high", "auto", "none", "")
			before := rosterBytes(t, f)
			code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
			if code != 0 || refusal != "" {
				t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
			}
			if !spawnJSONHasField(t, stdout, `"reused": true`) || !spawnJSONHasField(t, stdout, `"model": "m1"`) {
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
	appendLaneIdleRowWith(t, f, "m1-a", "m1", f.cwd, "high", "auto", "none", "")
	appendLaneIdleRowWith(t, f, "m1-b", "m1", f.cwd, "high", "auto", "none", "")
	code, stdout, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--cwd", f.cwd + string(os.PathSeparator)})
	if code != 0 || refusal != "" {
		t.Fatalf("exit=%d refusal=%q stderr=%q stdout=%q", code, refusal, stderr, stdout)
	}
	if !spawnJSONHasField(t, stdout, `"reused": true`) || !spawnJSONHasField(t, stdout, `"model": "m1"`) {
		t.Fatalf("a trailing slash lost the reuse: %q", stdout)
	}
	if acts := herdrActs(calls); len(acts) != 0 {
		t.Fatalf("reuse opened something: %v", acts)
	}
}

func TestSpawnGroupModelListRequiresInheritedModel(t *testing.T) {
	for _, tc := range []struct {
		name, roleKind, roleModel, wantKind, wantModel string
		args                                           []string
	}{
		{name: "own model", roleModel: "role-model", wantKind: "grok", wantModel: "role-model"},
		{name: "own kind", roleKind: "pi", wantKind: "pi", wantModel: "pi-worker"},
		{name: "kind flag", args: []string{"--kind", "grok"}, wantKind: "grok", wantModel: "grok-worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rules []fakecli.Rule
			if tc.wantKind == "pi" {
				rules = []fakecli.Rule{piHerdrIdentityRule("scouter", "p2", "/sessions/scouter.jsonl")}
			}
			f := laneModelFixture(t, "off", "m1,m2", rules...)
			for key, value := range map[string]string{
				"role_scouter_kind": tc.roleKind, "role_scouter_model": tc.roleModel,
				"model_pi_worker": "pi-worker", "model_grok_worker": "grok-worker",
			} {
				if value != "" {
					f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "user"}
				}
			}
			code, stdout, stderr, refusal, _ := runLaneModelSpawn(t, f, append([]string{"scouter"}, tc.args...))
			if code != 0 || !spawnJSONHasField(t, stdout, `"kind": "`+tc.wantKind+`"`) || !spawnJSONHasField(t, stdout, `"model": "`+tc.wantModel+`"`) || spawnJSONHasField(t, stdout, `"model_choice": "list"`) {
				t.Fatalf("exit=%d refusal=%q stdout=%q stderr=%q", code, refusal, stdout, stderr)
			}
		})
	}
}

func TestGroupReuseNativeArgsCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, panes, oldArgs string
		otherKindFirst       bool
		wantCode             int
	}{
		{"equal", "2", "--current", false, 0},
		{"different with room", "2", "--old", false, 0},
		{"different full", "1", "--old", false, 13},
		{"another kind before matching args", "2", "--current", true, 0},
		{"another kind before differing args", "3", "--old", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", "", tc.panes, idleHerdrRule("existing"), idleHerdrRule("other"))
			if tc.otherKindFirst {
				f.roster(t, strings.Join([]string{"other", "p-other", "pi", "scouter", "pi", "1", f.cwd, "now", "m1", "auto", "scouter", "build", "", "", "high", "none"}, "\t"))
			}
			f.ctx.Entries["lane_build_model"] = core.ConfigEntry{Value: "m1", Source: "project"}
			f.ctx.Entries["lane_build_args"] = core.ConfigEntry{Value: "--current", Source: "project"}
			appendLaneIdleRowWith(t, f, "existing", "m1", f.cwd, "high", "auto", "none", tc.oldArgs)
			before := rosterBytes(t, f)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter"})
			if code != tc.wantCode {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q refusal=%q", code, tc.wantCode, out, stderr, refusal)
			}
			fresh := false
			switch {
			case tc.oldArgs == "--current":
				if !spawnJSONHasField(t, out, `"name": "existing"`) || !spawnJSONHasField(t, out, `"reused": true`) {
					t.Fatalf("equal args not reused: stdout=%q stderr=%q", out, stderr)
				}
			case code == 13:
				assertGroupMismatch(t, code, out, stderr, "native args differs (--old vs --current)", `"session_args": "--old"`, `"requested_args": "--current"`)
			default:
				worker, detail := "existing", "native args differs (--old vs --current)"
				if tc.otherKindFirst {
					worker, detail = "other", "kind differs (pi vs grok)"
				}
				assertFreshForMismatch(t, out, stderr, worker, detail)
				fresh = true
			}
			if fresh {
				if len(herdrActs(calls)) == 0 || !strings.HasPrefix(rosterBytes(t, f), before) {
					t.Fatalf("expected a new worker and retained roster: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
				}
			} else if len(herdrActs(calls)) != 0 || rosterBytes(t, f) != before {
				t.Fatalf("worker set changed: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
			}
			t.Logf("exit=%d stdout=%s stderr=%s", code, out, stderr)
		})
	}
}

func TestGroupReuseOpeningEffortCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, laneKind, panes, request string
		code, workers                  int
	}{
		{"matching", "", "2", "medium", 0, 1},
		{"untyped spare", "", "2", "low", 0, 2},
		{"untyped full", "", "1", "low", 13, 1},
		{"typed spare", "grok", "2", "low", 0, 2},
		{"typed full", "grok", "1", "low", 13, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", "", tc.panes, idleHerdrRule("existing"))
			for key, value := range map[string]string{"lane_build_kind": tc.laneKind, "role_scouter_kind": "grok", "role_scouter_model": "m1", "role_scouter_effort": "high"} {
				f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
			}
			appendLaneIdleRowWith(t, f, "existing", "m1", f.cwd, "medium", "auto", "none", "")
			before := rosterBytes(t, f)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--effort", tc.request})
			if code != tc.code || len(rosterModels(t, f)) != tc.workers {
				t.Fatalf("code=%d stdout=%q stderr=%q refusal=%q roster=%q", code, out, stderr, refusal, rosterBytes(t, f))
			}
			switch {
			case tc.code == 13:
				assertGroupMismatch(t, code, out, stderr, "effort differs (medium vs low)", `"session_effort": "medium"`, `"requested_effort": "low"`)
			case tc.workers == 2:
				assertFreshForMismatch(t, out, stderr, "existing", "effort differs (medium vs low)")
				if !strings.Contains(stderr, "effort from flag") || !strings.HasPrefix(rosterBytes(t, f), before) || len(herdrActs(calls)) == 0 {
					t.Fatalf("stderr=%q actions=%v roster=%q", stderr, herdrActs(calls), rosterBytes(t, f))
				}
			case !spawnJSONHasField(t, out, `"reused": true`):
				t.Fatalf("matching opening effort not reused: stdout=%q stderr=%q", out, stderr)
			}
			if tc.workers == 1 && (rosterBytes(t, f) != before || len(herdrActs(calls)) != 0) {
				t.Fatalf("worker set changed: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
			}
			t.Logf("exit=%d stdout=%s stderr=%s", code, out, stderr)
		})
	}
}

func TestGroupReuseOtherKindAndAdditionalDifferences(t *testing.T) {
	for _, tc := range []struct {
		name, laneKind, panes, effort, approvals, mcp, args string
		otherCwd                                            bool
		code                                                int
	}{
		{"typed spare", "grok", "2", "high", "auto", "inherit", "", false, 0},
		{"untyped spare", "", "2", "high", "auto", "inherit", "", false, 0},
		{"typed full", "grok", "1", "high", "auto", "inherit", "", false, 13},
		{"untyped full", "", "1", "high", "auto", "inherit", "", false, 13},
		{"effort", "grok", "2", "low", "auto", "inherit", "", false, 0},
		{"approvals", "grok", "2", "high", "ask", "inherit", "", false, 0},
		{"mcp", "grok", "2", "high", "auto", "none", "", false, 0},
		{"native args", "grok", "2", "high", "auto", "inherit", "--old", false, 0},
		{"cwd", "grok", "2", "high", "auto", "inherit", "", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", "", tc.panes, idleHerdrRule("existing"))
			for key, value := range map[string]string{
				"lane_build_kind": tc.laneKind, "role_scouter_kind": "grok", "role_scouter_model": "m1", "role_scouter_effort": "high",
			} {
				f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
			}
			workerCwd := f.cwd
			if tc.otherCwd {
				workerCwd = f.root
			}
			f.roster(t, strings.Join([]string{"existing", "p-existing", "pi", "scouter", "pi", "1", workerCwd, "now", "pi-model", tc.approvals, "scouter", "build", "", tc.args, tc.effort, tc.mcp}, "\t"))
			before := rosterBytes(t, f)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--mcp", "inherit"})
			if code != tc.code {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q refusal=%q", code, tc.code, out, stderr, refusal)
			}
			detail := "kind differs (pi vs grok), model differs (pi-model vs m1)"
			if code == 0 {
				assertFreshForMismatch(t, out, stderr, "existing", detail)
				if len(rosterModels(t, f)) != 2 || !strings.HasPrefix(rosterBytes(t, f), before) || len(herdrActs(calls)) == 0 {
					t.Fatalf("fresh spawn did not retain worker and add a row: roster=%q actions=%v", rosterBytes(t, f), herdrActs(calls))
				}
				return
			}
			assertGroupMismatch(t, code, out, stderr, detail, `"session_kind": "pi"`, `"requested_kind": "grok"`, `"session_model": "pi-model"`, `"requested_model": "m1"`)
			if tc.laneKind == "" && (!spawnJSONHasField(t, out, `"session_effort": "high"`) || !spawnJSONHasField(t, out, `"requested_effort": "high"`)) {
				t.Fatalf("legacy effort pairs missing: %s", out)
			}
			if len(herdrActs(calls)) != 0 || rosterBytes(t, f) != before {
				t.Fatalf("refusal changed worker set: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
			}
		})
	}
}

func TestCappedListedReuseWithStaleRequestedName(t *testing.T) {
	for _, tc := range []struct {
		name, panes, state string
		named              bool
		code               int
		reused             string
	}{
		{"gone named in full lane", "1", "gone", true, 0, "stale"},
		{"gone unnamed", "1", "gone", false, 0, "compatible"},
		{"live unnamed", "1", "idle", false, 0, "compatible"},
		{"live named keeps its slot", "1", "idle", true, 13, ""},
		{"unavailable named keeps its slot", "1", "unavailable", true, 10, ""},
		{"gone named with room still checks cap", "2", "gone", true, 13, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rename := []string{"agent", "rename", "p-compatible", "stale"}
			rules := []fakecli.Rule{
				idleHerdrRule("compatible"),
				{Argv: rename, Stdout: `{"result":{}}`},
			}
			switch tc.state {
			case "gone":
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "stale"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`})
			case "unavailable":
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "stale"}, Code: 1, Stderr: `{"error":{"code":"permission_denied","message":"unavailable"}}`})
			default:
				rules = append(rules, idleHerdrRule("stale"))
			}
			f := laneModelFixtureWithCapacity(t, "1", "m1", tc.panes, rules...)
			appendLaneIdleRowWith(t, f, "stale", "m1", f.cwd, "low", "auto", "none", "")
			appendLaneIdleRow(t, f, "compatible", "m1")
			before := rosterBytes(t, f)
			args := []string{"scouter", "--mcp", "none"}
			if tc.named {
				args = append(args, "--name", "stale")
			}
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, args)
			t.Logf("exit=%d stdout=%s stderr=%s refusal=%s", code, out, stderr, refusal)
			if code != tc.code {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q refusal=%q", code, tc.code, out, stderr, refusal)
			}
			var renames int
			for _, call := range calls {
				if slices.Equal(call.Argv, rename) {
					renames++
				}
			}
			if len(herdrActs(calls)) != 0 {
				t.Fatalf("unexpected terminal actions: %v", herdrActs(calls))
			}
			if tc.reused == "" {
				switch tc.state {
				case "idle":
					assertGroupMismatch(t, code, out, stderr, "effort differs (low vs high)", `"name": "stale"`)
				case "unavailable":
					if !spawnJSONHasField(t, out, `"status": "busy"`) || !spawnJSONHasField(t, out, `"name": "stale"`) {
						t.Fatalf("wrong unavailable name refusal: %q", out)
					}
				default:
					if !strings.Contains(refusal, "every model of lane 'build' is at the cap") {
						t.Fatalf("wrong creation refusal: %q", refusal)
					}
				}
				if rosterBytes(t, f) != before || renames != 0 {
					t.Fatalf("refusal changed worker state: stdout=%q refusal=%q roster=%q", out, refusal, rosterBytes(t, f))
				}
				return
			}
			if !spawnJSONHasField(t, out, `"name": "`+tc.reused+`"`) || !spawnJSONHasField(t, out, `"pane_id": "p-compatible"`) ||
				!spawnJSONHasField(t, out, `"reused": true`) || !spawnJSONHasField(t, out, `"model_choice": "list"`) {
				t.Fatalf("compatible worker was not reused on its pane: %s", out)
			}
			if tc.state == "gone" && (len(rosterModels(t, f)) != 1 || !strings.Contains(rosterBytes(t, f), tc.reused+"\tp-compatible\t")) {
				t.Fatalf("stale row was not retired: %q", rosterBytes(t, f))
			}
			if tc.named && renames != 1 {
				t.Fatalf("expected exactly one legacy rename: %#v", calls)
			}
		})
	}
}

func TestGroupMismatchShowsConfiguredModelKey(t *testing.T) {
	for _, tc := range []struct {
		name, list, scalar, role, flag, panes, model, requestSource, groupLabel string
	}{
		{"inherited list", "m1,m2", "", "", "", "1", "m1", "lane.build.models", "lane.build.models=m1,m2"},
		{"role override with shadowed scalar", "m1,m2", "shadow", "m3", "", "1", "m3", "role.scouter.model", "lane.build.models=m1,m2"},
		{"flag override with shadowed scalar", "m1,m2", "shadow", "", "m3", "2", "m3", "flag", "lane.build.models=m1,m2"},
		{"scalar default", "", "m1", "m3", "", "2", "m3", "role.scouter.model", "lane.build.model=m1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", tc.list, tc.panes, idleHerdrRule("existing"))
			f.ctx.Entries["lane_build_model"] = core.ConfigEntry{Value: tc.scalar, Source: "project"}
			f.ctx.Entries["role_scouter_model"] = core.ConfigEntry{Value: tc.role, Source: "user"}
			appendLaneIdleRow(t, f, "existing", "old")
			args := []string{"scouter", "--mcp", "none"}
			if tc.flag != "" {
				args = append(args, "--model", tc.flag)
			}
			code, out, stderr, refusal, _ := runLaneModelSpawn(t, f, args)
			t.Logf("exit=%d stdout=%s stderr=%s refusal=%s", code, out, stderr, refusal)
			if tc.panes == "1" {
				assertGroupMismatch(t, code, out, stderr, "model differs (old vs "+tc.model+")", `"session_model": "old"`, `"requested_model": "`+tc.model+`"`)
			} else {
				if code != 0 || !spawnJSONHasField(t, out, `"model": "`+tc.model+`"`) {
					t.Fatalf("did not open the requested model: exit=%d stdout=%q", code, out)
				}
				assertFreshForMismatch(t, out, stderr, "existing", "model differs (old vs "+tc.model+")")
			}
			if !strings.Contains(stderr, "model from "+tc.requestSource) || !strings.Contains(stderr, "group config: "+tc.groupLabel) || strings.Contains(stderr, "lane.build.model=shadow") {
				t.Fatalf("diagnostic did not distinguish the requested source and configured model key: %s", stderr)
			}
		})
	}
}

func TestFullWorkingLaneStaysBusy(t *testing.T) {
	working := fakecli.Rule{Argv: []string{"agent", "get", "existing"}, Stdout: `{"result":{"agent":{"name":"existing","agent_status":"working"}}}`}
	f := laneModelFixtureWithCapacity(t, "1", "m1", "1", working)
	appendLaneIdleRow(t, f, "existing", "m1")
	before := rosterBytes(t, f)
	code, out, stderr, _, calls := runLaneModelSpawn(t, f, []string{"scouter", "--mcp", "none"})
	t.Logf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	if code != 10 || !spawnJSONHasField(t, out, `"status": "busy"`) || !spawnJSONHasField(t, out, `"name": "existing"`) ||
		strings.Contains(stderr, "not reused") || len(herdrActs(calls)) != 0 || rosterBytes(t, f) != before {
		t.Fatalf("expected full working lane to stay busy: exit=%d stdout=%q stderr=%q actions=%v", code, out, stderr, herdrActs(calls))
	}
}

func TestMissingExecutableRemedyUsesKindSource(t *testing.T) {
	for _, tc := range []struct {
		name, group, role, flag, wantKind, want string
	}{
		{"role controls a pi lane", "pi", "codex", "", "codex", "role.scouter.kind (user)"},
		{"role still controls after lane edit", "grok", "codex", "", "codex", "role.scouter.kind (user)"},
		{"inherited lane controls", "codex", "", "", "codex", "lane.build.kind (project)"},
		{"flag controls", "grok", "", "codex", "codex", "--kind"},
		{"available inherited kind", "grok", "", "", "grok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", "", "2")
			f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: tc.group, Source: "project"}
			f.ctx.Entries["role_scouter_kind"] = core.ConfigEntry{Value: tc.role, Source: "user"}
			args := []string{"scouter"}
			if tc.flag != "" {
				args = append(args, "--kind", tc.flag)
			}
			code, out, stderr, refusal, _ := runLaneModelSpawn(t, f, args)
			t.Logf("exit=%d stdout=%s stderr=%s refusal=%s", code, out, stderr, refusal)
			if code != 0 || !spawnJSONHasField(t, out, `"kind": "`+tc.wantKind+`"`) {
				t.Fatalf("wrong selected kind: exit=%d stdout=%s", code, out)
			}
			if tc.want == "" {
				if strings.Contains(stderr, "not found in PATH") {
					t.Fatalf("unexpected executable warning: %s", stderr)
				}
				return
			}
			if !strings.Contains(stderr, "executable 'codex' not found") || !strings.Contains(stderr, "change "+tc.want) ||
				strings.Contains(stderr, "choose another with 'soho setup --lane") {
				t.Fatalf("missing actionable source %q: %s", tc.want, stderr)
			}
		})
	}
}

func TestAbsentNameWithRoomChecksModelCap(t *testing.T) {
	f := laneModelFixtureWithCapacity(t, "1", "m1", "2", idleHerdrRule("compatible"))
	appendLaneIdleRow(t, f, "compatible", "m1")
	before := rosterBytes(t, f)
	code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--name", "wanted"})
	if code != 13 || !strings.Contains(refusal, "at the cap") || len(herdrActs(calls)) != 0 || rosterBytes(t, f) != before {
		t.Fatalf("new name must pass model cap: exit=%d stdout=%s stderr=%s refusal=%s actions=%v", code, out, stderr, refusal, herdrActs(calls))
	}
}

func TestReuseGuidanceDistinguishesPaths(t *testing.T) {
	skill := filepath.Join("..", "..", "skills", "soho")
	for _, tc := range []struct {
		path, want string
	}{
		{filepath.Join(skill, "config.defaults"), "When opening with an inherited lane.<name>.models"},
		{filepath.Join(skill, "config.defaults"), "A role model or --model skips the list"},
		{filepath.Join(skill, "references", "troubleshooting.md"), "With `lanes=off` and `multi_role=on`, cross-role reuse"},
		{filepath.Join(skill, "references", "troubleshooting.md"), "Grouped reuse requires exact approvals."},
	} {
		body, err := os.ReadFile(tc.path)
		if err != nil || !strings.Contains(string(body), tc.want) {
			t.Errorf("%s lacks %q: %v", tc.path, tc.want, err)
		}
	}
}
