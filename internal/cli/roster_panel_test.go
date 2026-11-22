package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func panelStateJSON(state string) string {
	return `{"result":{"agent":{"agent_status":"` + state + `","state_change_seq":1}}}`
}

func panelTreeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		sum := sha256.Sum256(raw)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func panelFixtureWithWorkers(t *testing.T) *copiesFixture {
	t.Helper()
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"work","pane_id":"ws:p1","agent":"pi","agent_status":"working"},{"name":"done","pane_id":"ws:p2","agent":"pi","agent_status":"idle"},{"name":"quota","pane_id":"ws:p3","agent":"codex","agent_status":"idle"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
		{Argv: []string{"agent", "get", "work"}, Stdout: panelStateJSON("working")},
		{Argv: []string{"agent", "get", "done"}, Stdout: panelStateJSON("idle")},
		{Argv: []string{"agent", "get", "quota"}, Stdout: panelStateJSON("idle")},
		{Argv: []string{"agent", "get", "gone"}, Stdout: panelStateJSON("gone")},
		{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\n"},
		{Argv: []string{"agent", "read", "work", "--source", "visible"}, Stdout: panelSteadyScreen},
	})
	row := func(name, pane, kind, role, model, effort string) string {
		return strings.Join([]string{name, pane, kind, role, "xai", "0", "/tmp/work", "now", model, "ask", "", "build", "", "", effort}, "\t")
	}
	f.writeRoster(t,
		row("work", "ws:p1", "pi", "implementer", "lane-model", "high"),
		row("done", "ws:p2", "pi", "reviewer", "review-model", "medium"),
		row("quota", "ws:p3", "codex", "tester", "gpt-6.2-sol", "max"),
		row("gone", "", "pi", "planner", "", ""),
	)
	sd := f.stateDir(t)
	if err := os.WriteFile(filepath.Join(sd, "last-report-done"), []byte(sd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sd, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "reports", "done-report.md"), []byte("report body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "last-report-done"), []byte(filepath.Join(sd, "reports", "done-report.md")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Deterministic elapsed values (F13): the done task started 42 s before
	// its report, and the working pane's last screen change was 17 s ago.
	pointer := filepath.Join(sd, "last-report-done")
	report := filepath.Join(sd, "reports", "done-report.md")
	start := time.Unix(panelFixedUnix-panelTaskSeconds, 0)
	if err := os.Chtimes(pointer, start, start); err != nil {
		t.Fatal(err)
	}
	done := time.Unix(panelFixedUnix, 0)
	if err := os.Chtimes(report, done, done); err != nil {
		t.Fatal(err)
	}
	panelWriteActivityMarker(t, sd, "work", panelActivitySeconds)
	return f
}

// panelFixedUnix is the pinned clock and the anchor of the deterministic
// elapsed fixtures: every expected task_s/activity_s below is a literal.
const (
	panelFixedUnix       = int64(1_800_000_000)
	panelTaskSeconds     = 42
	panelActivitySeconds = 17
	panelSteadyScreen    = "steady screen\n"
)

// panelWriteActivityMarker records the screen hash and the last-change time
// the activity computation compares, for the pinned clock.
func panelWriteActivityMarker(t *testing.T, sd, agent string, ageSeconds int64) {
	t.Helper()
	waitDir := filepath.Join(sd, "wait")
	if err := os.MkdirAll(waitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hash := strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen(panelSteadyScreen))), 10) + "\n"
	if err := os.WriteFile(filepath.Join(waitDir, agent+".stuck-hash"), []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	at := strconv.FormatInt(panelFixedUnix-ageSeconds, 10) + "\n"
	if err := os.WriteFile(filepath.Join(waitDir, agent+".activity-at"), []byte(at), 0o600); err != nil {
		t.Fatal(err)
	}
}

// panelPinClock pins platform.Now for one test and restores it afterwards.
func panelPinClock(t *testing.T) {
	t.Helper()
	old := platform.Now
	platform.Now = func() time.Time { return time.Unix(panelFixedUnix, 0) }
	t.Cleanup(func() { platform.Now = old })
}

func panelAgentRow(t *testing.T, agents []any, name string) *jsonjs.Object {
	t.Helper()
	for _, value := range agents {
		obj, ok := value.(*jsonjs.Object)
		if !ok {
			t.Fatalf("agent row is %T", value)
		}
		if got, _ := obj.Get("name"); got == name {
			return obj
		}
	}
	t.Fatalf("no roster agent %q in %v", name, agents)
	return nil
}

func TestPanelRosterWorkersFieldsAndNoWrites(t *testing.T) {
	f := panelFixtureWithWorkers(t)
	panelPinClock(t)
	before := panelTreeDigest(t, f.state)
	env := f.env.Clone()
	env["SOHO_NOWRITE"] = "1"
	code, out, stderr := f.run(t, env, "roster", "--json")
	// The no-write check runs first: a mutant that takes CmdStatus's
	// mirroring path must fail on a changed state file, not only on output.
	if after := panelTreeDigest(t, f.state); len(before) != len(after) {
		t.Fatalf("roster wrote state files: before %v, after %v", before, after)
	}
	for path, digest := range before {
		if got := panelTreeDigest(t, f.state)[path]; got != digest {
			t.Fatalf("roster changed %s", path)
		}
	}
	if code != 0 || stderr != "" {
		t.Fatalf("roster --json code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj := rosterJSONParse(t, out)
	agentsValue, _ := obj.Get("agents")
	agents, ok := agentsValue.([]any)
	if !ok || len(agents) != 4 {
		t.Fatalf("agents = %#v, want four rows", agentsValue)
	}
	keys := panelObj(t, agents[0], "agents[0]").Keys()
	wantKeys := "name,role,previous_role,kind,pane,tab,state,report,cwd,task,model,effort,status,report_path,task_s,activity_s"
	if strings.Join(keys, ",") != wantKeys {
		t.Fatalf("agents[0] key order = %v, want %s", keys, wantKeys)
	}
	expected := map[string]struct{ model, effort, status string }{
		"work":  {"lane-model", "high", "working"},
		"done":  {"review-model", "medium", "done"},
		"quota": {"gpt-6.2-sol", "max", "quota"},
		"gone":  {"", "", "gone"},
	}
	for name, want := range expected {
		row := panelAgentRow(t, agents, name)
		for key, value := range map[string]string{"model": want.model, "effort": want.effort, "status": want.status} {
			if got, _ := row.Get(key); got != value {
				t.Fatalf("agents[%s].%s = %#v, want %q", name, key, got, value)
			}
		}
	}
	// F13: literal elapsed values and the null controls, not any number.
	for name, want := range map[string]struct{ taskS, activityS any }{
		"work":  {nil, float64(panelActivitySeconds)},
		"done":  {float64(panelTaskSeconds), nil},
		"quota": {nil, nil},
		"gone":  {nil, nil},
	} {
		row := panelAgentRow(t, agents, name)
		if got, _ := row.Get("task_s"); got != want.taskS {
			t.Fatalf("agents[%s].task_s = %#v, want %#v", name, got, want.taskS)
		}
		if got, _ := row.Get("activity_s"); got != want.activityS {
			t.Fatalf("agents[%s].activity_s = %#v, want %#v", name, got, want.activityS)
		}
	}
	done := panelAgentRow(t, agents, "done")
	if reportState, _ := done.Get("report"); reportState != "ready" {
		t.Fatalf("done report state = %v, want ready", reportState)
	}
	if path, _ := done.Get("report_path"); path != filepath.Join(f.stateDir(t), "reports", "done-report.md") {
		t.Fatalf("done report_path = %v", path)
	}
	if taskS, _ := done.Get("task_s"); taskS != float64(panelTaskSeconds) {
		t.Fatalf("done task_s = %#v, want the literal %d", taskS, panelTaskSeconds)
	}
	gone := panelAgentRow(t, agents, "gone")
	if path, ok := gone.Get("report_path"); !ok || path != nil {
		t.Fatalf("gone report_path = %#v (present %v), want null", path, ok)
	}
	if taskS, _ := gone.Get("task_s"); taskS != nil {
		t.Fatalf("gone task_s = %v, want null", taskS)
	}
	// The legacy fields stay intact beside the new ones.
	if state, _ := panelAgentRow(t, agents, "work").Get("state"); state != "working" {
		t.Fatalf("work state = %v", state)
	}
}

func TestPanelCapabilitiesHelpAndNowrite(t *testing.T) {
	f := newCopiesFixture(t, nil)
	code, out, stderr := f.run(t, platform.Env{"SOHO_NOWRITE": "1"}, "capabilities", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("capabilities code=%d stderr=%q", code, stderr)
	}
	obj := panelParseCaps(t, out)
	for _, key := range []string{"team_json", "config_dry_run", "config_unset", "roster_workers", "roster_detached"} {
		if value, _ := obj.Get(key); value != float64(1) {
			t.Fatalf("capabilities.%s = %#v, want 1", key, value)
		}
	}
	if keys := obj.Keys(); strings.Join(keys, ",") != "schema,worker_collaboration,ephemeral_job,job_events,inventory,report_json,team_json,config_dry_run,config_unset,roster_workers,roster_detached,transport" {
		t.Fatalf("capabilities key order = %v", keys)
	}

	code, helpOut, _ := f.run(t, f.env, "config", "--help")
	if code != 0 || !strings.Contains(helpOut, "unset <key>") || !strings.Contains(helpOut, "--dry-run") {
		t.Fatalf("config help lacks unset/dry-run: code=%d\n%s", code, helpOut)
	}

	// The preview is the only config write-shaped invocation the gate admits.
	panelWrite(t, filepath.Join(f.repo, ".agents", "soho.conf"), "lane.build.effort=high\n")
	for _, tc := range []struct {
		argv []string
		want int
	}{
		{[]string{"config", "set", "lane.build.effort", "xhigh", "--dry-run"}, 0},
		{[]string{"config", "set", "lane.build.effort=xhigh", "--dry-run"}, 0},
		{[]string{"config", "set", "lane.build.effort=xhigh", "--dry-run", "--json"}, 0},
		{[]string{"config", "set", "lane.build.effort", "xhigh", "--dry-run", "--json"}, 0},
		{[]string{"config", "set", "lane.build.effort", "xhigh"}, 2},
		{[]string{"config", "set", "lane.build.effort=xhigh"}, 2},
		{[]string{"config", "set", "lane.build.effort=xhigh", "--json"}, 2},
		{[]string{"config", "unset", "lane.build.effort"}, 2},
	} {
		env := f.env.Clone()
		env["SOHO_NOWRITE"] = "1"
		code, _, stderr := f.run(t, env, tc.argv...)
		if code != tc.want {
			t.Fatalf("%v under NOWRITE code=%d, want %d (stderr=%q)", tc.argv, code, tc.want, stderr)
		}
		if tc.want == 2 && !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") {
			t.Fatalf("%v refusal = %q", tc.argv, stderr)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(f.repo, ".agents", "soho.conf")); string(raw) != "lane.build.effort=high\n" {
		t.Fatalf("NOWRITE probe changed the config: %q", raw)
	}
}

func panelObj(t *testing.T, value any, label string) *jsonjs.Object {
	t.Helper()
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("%s = %T, want object", label, value)
	}
	return obj
}

func panelWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func panelParseCaps(t *testing.T, out string) *jsonjs.Object {
	t.Helper()
	parsed, err := jsonjs.Parse([]byte(out))
	if err != nil {
		t.Fatalf("capabilities parse: %v (%q)", err, out)
	}
	obj, ok := parsed.(*jsonjs.Object)
	if !ok {
		t.Fatalf("capabilities root = %T", parsed)
	}
	return obj
}

func TestPanelRolesJSONFromCLI(t *testing.T) {
	f := newCopiesFixture(t, nil)
	config := "lanes=on\nlane.build.roles=designer,tasker\nlane.build.kind=pi\nlane.review.roles=reviewer\nlane.review.kind=codex\n"
	panelWrite(t, filepath.Join(f.repo, ".agents", "soho.conf"), config)
	code, out, stderr := f.run(t, f.env, "roles", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("roles --json code=%d stderr=%q", code, stderr)
	}
	parsed, err := jsonjs.Parse([]byte(out))
	if err != nil {
		t.Fatalf("roles parse: %v", err)
	}
	obj := parsed.(*jsonjs.Object)
	if keys := obj.Keys(); strings.Join(keys, ",") != "roles,lanes,unassigned_roles,team_exists,config_files,team_files" {
		t.Fatalf("roles --json keys = %v", keys)
	}
	if value, _ := obj.Get("team_exists"); value != true {
		t.Fatalf("team_exists = %v, want true (the project file chose a team)", value)
	}
	lanes, _ := obj.Get("lanes")
	if arr, ok := lanes.([]any); !ok || len(arr) != 2 {
		t.Fatalf("lanes = %#v, want build and review", lanes)
	}

	env := f.env.Clone()
	env["SOHO_LANES"] = "off"
	code, out, stderr = f.run(t, env, "roles", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("roles --json lanes off code=%d stderr=%q", code, stderr)
	}
	parsed, _ = jsonjs.Parse([]byte(out))
	lanes, _ = parsed.(*jsonjs.Object).Get("lanes")
	if jsonjs.Stringify(lanes) != "[]" {
		t.Fatalf("lanes with SOHO_LANES=off = %s", jsonjs.Stringify(lanes))
	}

	// A project with no team choice and no roster row is a first run.
	f2 := newCopiesFixture(t, nil)
	code, out, stderr = f2.run(t, f2.env, "roles", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("empty roles --json code=%d stderr=%q", code, stderr)
	}
	parsed, _ = jsonjs.Parse([]byte(out))
	if value, _ := parsed.(*jsonjs.Object).Get("team_exists"); value != false {
		t.Fatalf("fresh project team_exists = %v, want false", value)
	}
}

func TestPanelRolesJSONUsesPresetGroups(t *testing.T) {
	f := newCopiesFixture(t, nil)
	panelWrite(t, filepath.Join(f.repo, ".agents", "soho.conf"), "panes=4\n")
	code, out, stderr := f.run(t, f.env, "roles", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("roles --json exit=%d out=%q stderr=%q", code, out, stderr)
	}
	parsed, err := jsonjs.Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	obj := parsed.(*jsonjs.Object)
	laneField, _ := obj.Get("lanes")
	var names []string
	for _, lane := range laneField.([]any) {
		name, _ := lane.(*jsonjs.Object).Get("name")
		names = append(names, name.(string))
	}
	if got := strings.Join(names, " "); got != "builders reviewers planners" {
		t.Fatalf("group names=%q", got)
	}
	unassigned, _ := obj.Get("unassigned_roles")
	if roles := unassigned.([]any); len(roles) != 0 {
		t.Fatalf("preset left roles unassigned: %v", roles)
	}
}

func TestPanelRosterEmptyAndLegacyRows(t *testing.T) {
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
	}
	f := newCopiesFixture(t, rules)
	code, out, stderr := f.run(t, f.env, "roster", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("empty roster code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj := rosterJSONParse(t, out)
	if agents, _ := obj.Get("agents"); jsonjs.Stringify(agents) != "[]" {
		t.Fatalf("empty roster agents = %s", jsonjs.Stringify(agents))
	}

	// An older registry row stops before column 8: model and effort are
	// absent and must be "" rather than a shifted task or lane value.
	legacy := strings.Join([]string{"legacy", "ws:p9", "pi", "implementer", "xai", "0", "/tmp/work", "now"}, "\t")
	f.writeRoster(t, legacy)
	code, out, stderr = f.run(t, f.env, "roster", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("legacy roster code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj = rosterJSONParse(t, out)
	agentsValue, _ := obj.Get("agents")
	arr, _ := agentsValue.([]any)
	if len(arr) != 1 {
		t.Fatalf("legacy agents = %#v", agentsValue)
	}
	row := panelObj(t, arr[0], "legacy row")
	for _, field := range []string{"model", "effort"} {
		if got, _ := row.Get(field); got != "" {
			t.Fatalf("legacy %s = %v, want empty", field, got)
		}
	}
	if got, ok := row.Get("report_path"); !ok || got != nil {
		t.Fatalf("legacy report_path = %#v (present %v), want null", got, ok)
	}
	if got, _ := row.Get("task_s"); got != nil {
		t.Fatalf("legacy task_s = %v, want null", got)
	}
}

func TestPanelRosterReusedNameUsesItsOwnRow(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"reused","pane_id":"ws:live","agent":"pi","agent_status":"working"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
		{Argv: []string{"agent", "get", "reused"}, Stdout: panelStateJSON("working")},
		{Argv: []string{"agent", "read", "reused", "--source", "visible"}, Stdout: panelSteadyScreen},
	})
	row := func(pane, model, effort string) string {
		return strings.Join([]string{"reused", pane, "pi", "implementer", "xai", "0", "/tmp/work", "now", model, "ask", "", "build", "", "", effort}, "\t")
	}
	sd := f.stateDir(t)
	pointer := filepath.Join(sd, "last-report-reused")
	panelWrite(t, pointer, filepath.Join(sd, "reports", "reused-report.md")+"\n")
	start := time.Unix(panelFixedUnix-120, 0)
	if err := os.Chtimes(pointer, start, start); err != nil {
		t.Fatal(err)
	}
	panelWriteActivityMarker(t, sd, "reused", panelActivitySeconds)
	panelPinClock(t)
	// The row the caller selects is the row the status resolution uses: the
	// live pane reads working with its elapsed values and the stale pane stays
	// gone with nulls, whatever order the roster stores them in (F3).
	for _, tc := range []struct {
		name string
		rows []string
	}{
		{"stale row first", []string{row("ws:old", "old-model", "low"), row("ws:live", "live-model", "max")}},
		{"live row first", []string{row("ws:live", "live-model", "max"), row("ws:old", "old-model", "low")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.writeRoster(t, tc.rows...)
			code, out, stderr := f.run(t, f.env, "roster", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("reused-name roster code=%d stderr=%q out=%q", code, stderr, out)
			}
			obj := rosterJSONParse(t, out)
			agentsValue, _ := obj.Get("agents")
			arr, ok := agentsValue.([]any)
			if !ok || len(arr) != 2 {
				t.Fatalf("reused-name agents = %#v", agentsValue)
			}
			byPane := map[string]*jsonjs.Object{}
			for _, value := range arr {
				row := panelObj(t, value, "row")
				pane, _ := row.Get("pane")
				text, _ := pane.(string)
				byPane[text] = row
			}
			first, second := byPane["ws:old"], byPane["ws:live"]
			if first == nil || second == nil {
				t.Fatalf("rows by pane = %v", byPane)
			}
			if model, _ := first.Get("model"); model != "old-model" {
				t.Fatalf("stale row model = %v, want old-model", model)
			}
			if effort, _ := first.Get("effort"); effort != "low" {
				t.Fatalf("stale row effort = %v, want low", effort)
			}
			if model, _ := second.Get("model"); model != "live-model" {
				t.Fatalf("live row model = %v, want live-model", model)
			}
			if effort, _ := second.Get("effort"); effort != "max" {
				t.Fatalf("live row effort = %v, want max", effort)
			}
			// The stale pane keeps its own recorded state and never borrows the
			// live pane's status or elapsed values (F10, literal values from F13).
			for key, want := range map[string]any{"pane": "ws:old", "state": "gone", "status": "gone", "task_s": nil, "activity_s": nil} {
				if got, _ := first.Get(key); got != want {
					t.Fatalf("stale row %s = %#v, want %#v", key, got, want)
				}
			}
			if got, _ := second.Get("pane"); got != "ws:live" {
				t.Fatalf("live row pane = %v, want ws:live", got)
			}
			if got, _ := second.Get("status"); got != "working" {
				t.Fatalf("live row status = %#v, want working", got)
			}
			if got, _ := second.Get("task_s"); got != float64(120) {
				t.Fatalf("live row task_s = %#v, want 120", got)
			}
			if got, _ := second.Get("activity_s"); got != float64(panelActivitySeconds) {
				t.Fatalf("live row activity_s = %#v, want %d", got, panelActivitySeconds)
			}
		})
	}
}

// TestPanelRosterReportPathNeedsARegularFile pins F5: the panel's path is
// published only for a regular file, while the legacy report indicator keeps
// its own size rule.
func TestPanelRosterReportPathNeedsARegularFile(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"tester","pane_id":"ws:p1","agent":"codex","agent_status":"idle"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
		{Argv: []string{"agent", "get", "tester"}, Stdout: panelStateJSON("idle")},
	})
	f.writeRoster(t, strings.Join([]string{"tester", "ws:p1", "codex", "tester", "xai", "0", "/tmp/work", "now", "test-model", "ask", "", "build", "", "", "high"}, "\t"))
	sd := f.stateDir(t)
	directory := filepath.Join(sd, "reports", "a-directory")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(sd, "reports", "empty.md")
	panelWrite(t, empty, "")
	// The legacy indicator keeps its size rule, and a directory's size is
	// platform-defined (non-zero on Unix, zero on Windows): the expected
	// indicator follows what os.Stat reports, while the published path is
	// absent on every platform.
	dirInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	directoryReport := "pending"
	if dirInfo.Size() > 0 {
		directoryReport = "ready"
	}
	for _, tc := range []struct {
		name           string
		pointer        string
		wantReport     string
		wantReportPath any
	}{
		{"a directory", directory, directoryReport, nil},
		{"an empty regular file", empty, "pending", empty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panelWrite(t, filepath.Join(sd, "last-report-tester"), tc.pointer+"\n")
			code, out, stderr := f.run(t, f.env, "roster", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("roster code=%d stderr=%q out=%q", code, stderr, out)
			}
			agents, _ := rosterJSONParse(t, out).Get("agents")
			arr, _ := agents.([]any)
			if len(arr) != 1 {
				t.Fatalf("agents = %#v", agents)
			}
			got := panelObj(t, arr[0], "tester row")
			if value, _ := got.Get("report"); value != tc.wantReport {
				t.Fatalf("report = %#v, want %#v", value, tc.wantReport)
			}
			if value, ok := got.Get("report_path"); !ok || value != tc.wantReportPath {
				t.Fatalf("report_path = %#v (present %v), want %#v", value, ok, tc.wantReportPath)
			}
		})
	}
}

// TestPanelConfigEmptyInlineValueAtTheCLIBoundary pins F1 through Run: an
// inline key= supplies the value even when empty, so --project and --dry-run
// stay options, both preview modes refuse with exit 2 and the sentinel bytes
// never change, under SOHO_NOWRITE too.
func TestPanelConfigEmptyInlineValueAtTheCLIBoundary(t *testing.T) {
	f := newCopiesFixture(t, nil)
	file := filepath.Join(f.repo, ".agents", "soho.conf")
	const sentinel = "role.tester.model=sentinel\n"
	panelWrite(t, file, sentinel)
	const want = "soho: usage: config set <key> <value> | <key>=<value> [--project|--user] (default --project; for this session only: soho session set)\n"
	cases := []struct {
		name     string
		argv     []string
		nowrite  bool
		wantJSON bool
	}{
		{"text preview", []string{"config", "set", "role.tester.model=", "--project", "--dry-run"}, false, false},
		{"json preview", []string{"config", "set", "role.tester.model=", "--project", "--dry-run", "--json"}, false, true},
		{"text preview under NOWRITE", []string{"config", "set", "role.tester.model=", "--project", "--dry-run"}, true, false},
		{"json preview under NOWRITE", []string{"config", "set", "role.tester.model=", "--project", "--dry-run", "--json"}, true, true},
		{"real set", []string{"config", "set", "role.tester.model=", "--project"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := f.env.Clone()
			if tc.nowrite {
				env["SOHO_NOWRITE"] = "1"
			}
			code, out, errOut := f.run(t, env, tc.argv...)
			if code != 2 || errOut != want {
				t.Fatalf("%v code=%d stderr=%q, want exit 2 with the empty-value refusal", tc.argv, code, errOut)
			}
			if raw, err := os.ReadFile(file); err != nil || string(raw) != sentinel {
				t.Fatalf("%v changed the config: %q (err=%v)", tc.argv, raw, err)
			}
			if !tc.wantJSON {
				if out != "" {
					t.Fatalf("%v printed %q on stdout, want nothing", tc.argv, out)
				}
				return
			}
			obj := panelObj(t, rosterJSONParse(t, out), "empty-value preview")
			if valid, _ := obj.Get("valid"); valid != false {
				t.Fatalf("%v valid = %#v, want false", tc.argv, valid)
			}
			if got, _ := obj.Get("error"); got != strings.TrimSuffix(strings.TrimPrefix(want, "soho: "), "\n") {
				t.Fatalf("%v error = %#v", tc.argv, got)
			}
		})
	}
}

// TestPanelRosterPendingReportPublishesNoPath pins F9: the pending pointer
// keeps its pending indicator but never publishes a path to a file that does
// not exist, and publishes it once the report does.
func TestPanelRosterPendingReportPublishesNoPath(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"tester","pane_id":"ws:p1","agent":"codex","agent_status":"idle"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
		{Argv: []string{"agent", "get", "tester"}, Stdout: panelStateJSON("idle")},
	})
	row := strings.Join([]string{"tester", "ws:p1", "codex", "tester", "xai", "0", "/tmp/work", "now", "test-model", "ask", "", "build", "", "", "high"}, "\t")
	f.writeRoster(t, row)
	sd := f.stateDir(t)
	pending := filepath.Join(sd, "reports", "pending.md")
	panelWrite(t, filepath.Join(sd, "last-report-tester"), pending+"\n")
	env := f.env.Clone()
	env["SOHO_NOWRITE"] = "1"
	for _, ready := range []bool{false, true} {
		if ready {
			panelWrite(t, pending, "report body\n")
		}
		code, out, stderr := f.run(t, env, "roster", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("pending=%v code=%d stderr=%q out=%q", !ready, code, stderr, out)
		}
		agents, _ := rosterJSONParse(t, out).Get("agents")
		arr, _ := agents.([]any)
		if len(arr) != 1 {
			t.Fatalf("pending=%v agents = %#v", !ready, agents)
		}
		got := panelObj(t, arr[0], "tester row")
		if ready {
			for key, want := range map[string]any{"report": "ready", "status": "done", "report_path": pending} {
				if value, _ := got.Get(key); value != want {
					t.Fatalf("ready %s = %#v, want %#v", key, value, want)
				}
			}
			continue
		}
		for key, want := range map[string]any{"report": "pending", "status": "no-report-yet", "report_path": nil} {
			if value, ok := got.Get(key); !ok || value != want {
				t.Fatalf("pending %s = %#v, want %#v", key, value, want)
			}
		}
	}
}

// TestPanelRosterNeverMirrorsScratchReport is the file-level half of the
// write-free claim: the fixture's report lives exactly where status's
// mirror branch copies from, so taking CmdStatus's path would create a file
// under the state dir and fail the hash check.
func TestPanelRosterNeverMirrorsScratchReport(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"mirror","pane_id":"ws:p1","agent":"pi","agent_status":"done"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
		{Argv: []string{"agent", "get", "mirror"}, Stdout: panelStateJSON("done")},
	})
	f.writeRoster(t, strings.Join([]string{"mirror", "ws:p1", "pi", "implementer", "xai", "0", "/tmp/work", "now", "lane-model", "ask", "", "build", "", "", "high"}, "\t"))
	sd := f.stateDir(t)
	scratchReport := filepath.Join(f.tmp, "soho", "ws", "reports", "mirror-report.md")
	panelWrite(t, scratchReport, "scratch report body\n")
	panelWrite(t, filepath.Join(sd, "last-report-mirror"), scratchReport+"\n")
	before := panelTreeDigest(t, f.state)
	code, out, stderr := f.run(t, f.env, "roster", "--json")
	if after := panelTreeDigest(t, f.state); len(before) != len(after) {
		t.Fatalf("roster mirrored the scratch report: before %v, after %v", before, after)
	}
	if code != 0 || stderr != "" {
		t.Fatalf("roster code=%d stderr=%q out=%q", code, stderr, out)
	}
	if _, err := os.Stat(filepath.Join(sd, "reports", "mirror-report.md")); !os.IsNotExist(err) {
		t.Fatalf("roster wrote the mirrored report (err=%v)", err)
	}
	obj := rosterJSONParse(t, out)
	agentsValue, _ := obj.Get("agents")
	arr, ok := agentsValue.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("agents = %#v", agentsValue)
	}
	row := panelObj(t, arr[0], "mirror row")
	if got, _ := row.Get("report_path"); got != scratchReport {
		t.Fatalf("report_path = %v, want %v", got, scratchReport)
	}
	if got, _ := row.Get("report"); got != "ready" {
		t.Fatalf("report = %v, want ready", got)
	}
}
