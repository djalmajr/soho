package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-375 A: soho roster --json prints one JSON object with the settings,
// the roster agents (uncut task and tab, raw role plus previous_role) and
// the other live agents, with the same sources and order as the text.

const rosterJSONLongLabel = "team build tab beyond sixteen"
const rosterJSONLongTask = "Implement the team view JSON for the soho picker, with the full task text kept uncut in the JSON output"

func rosterJSONFixture(t *testing.T) *copiesFixture {
	t.Helper()
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"build","pane_id":"ws:p2","agent":"pi","agent_status":"working"},{"name":"orchestrator-4","pane_id":"ws:p1","agent":"claude","agent_status":"idle"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"ws:p1","workspace_id":"ws","tab_id":"ws:t1"},{"pane_id":"ws:p2","workspace_id":"ws","tab_id":"ws:t2"}]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"ws:t1","label":"one"},{"tab_id":"ws:t2","label":"` + rosterJSONLongLabel + `"}]}}`},
	})
	sd := f.stateDir(t)
	// The history column (column 10) differs from the role: previous_role.
	f.writeRoster(t, strings.Join([]string{"build", "ws:p2", "pi", "implementer", "xai", "0", "/tmp/work", "now", "grok-4.7", "ask", "planner", "build"}, "\t"))
	if err := os.WriteFile(filepath.Join(sd, "task-build"), []byte(rosterJSONLongTask+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(sd, "reports", "build-20260101T000000.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("report body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "last-report-build"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func rosterJSONParse(t *testing.T, out string) *jsonjs.Object {
	t.Helper()
	if !strings.HasPrefix(out, `{`) || !strings.HasSuffix(out, "}\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("roster JSON is not one object on one line: %q", out)
	}
	parsed, err := jsonjs.Parse([]byte(out))
	if err != nil {
		t.Fatalf("roster JSON does not parse: %v\n%s", err, out)
	}
	obj, ok := parsed.(*jsonjs.Object)
	if !ok {
		t.Fatalf("roster JSON root is %T, want object", parsed)
	}
	return obj
}

func TestRosterJSONWorkspaceScope(t *testing.T) {
	f := rosterJSONFixture(t)
	env := f.env.Clone()
	env["SOHO_NOWRITE"] = "1"
	code, out, stderr := f.run(t, env, "roster", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("roster --json code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj := rosterJSONParse(t, out)
	if keys := obj.Keys(); strings.Join(keys, ",") != "workspace,scope,settings,agents,others" {
		t.Fatalf("roster JSON key order = %v", keys)
	}
	if v, _ := obj.Get("workspace"); v != "ws" {
		t.Fatalf("workspace = %v, want ws", v)
	}
	if v, _ := obj.Get("scope"); v != "workspace" {
		t.Fatalf("scope = %v, want workspace", v)
	}
	settings, ok := obj.Get("settings")
	if !ok {
		t.Fatalf("no settings key: %s", out)
	}
	sobj, ok := settings.(*jsonjs.Object)
	if !ok || strings.Join(sobj.Keys(), ",") != "layout,reuse_workers,multi_role,auto_approve" {
		t.Fatalf("settings = %#v, want the four text values in order", settings)
	}
	wantSettings := map[string]string{"layout": "split", "reuse_workers": "on", "multi_role": "on", "auto_approve": "off"}
	for key, want := range wantSettings {
		if v, _ := sobj.Get(key); v != want {
			t.Fatalf("settings.%s = %v, want %v (the last text line's values)", key, v, want)
		}
	}
	agents, ok := obj.Get("agents")
	if !ok {
		t.Fatalf("no agents key: %s", out)
	}
	arr, ok := agents.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("agents = %#v, want the one roster agent", agents)
	}
	a, ok := arr[0].(*jsonjs.Object)
	if !ok {
		t.Fatalf("agents[0] is %T, want object", arr[0])
	}
	if keys := a.Keys(); strings.Join(keys, ",") != "name,role,previous_role,kind,pane,tab,state,report,cwd,task" {
		t.Fatalf("agents[0] key order = %v", keys)
	}
	field := func(key string) any {
		v, _ := a.Get(key)
		return v
	}
	if field("name") != "build" || field("role") != "implementer" || field("previous_role") != "planner" ||
		field("kind") != "pi" || field("pane") != "ws:p2" || field("state") != "working" ||
		field("report") != "ready" || field("cwd") != "/tmp/work" {
		t.Fatalf("agents[0] = %#v (the raw role, the history as previous_role, the text row's fields)", a)
	}
	if field("tab") != rosterJSONLongLabel {
		t.Fatalf("agents[0].tab = %v, want the full label without the 16 cut", field("tab"))
	}
	if field("task") != rosterJSONLongTask {
		t.Fatalf("agents[0].task = %v, want the full task without the 40 cut", field("task"))
	}
	others, ok := obj.Get("others")
	if !ok {
		t.Fatalf("no others key: %s", out)
	}
	oarr, ok := others.([]any)
	if !ok || len(oarr) != 1 {
		t.Fatalf("others = %#v, want the one live agent outside the roster", others)
	}
	o, ok := oarr[0].(*jsonjs.Object)
	if !ok {
		t.Fatalf("others[0] is %T, want object", oarr[0])
	}
	if keys := o.Keys(); strings.Join(keys, ",") != "name,kind,pane,tab,state,workspace" {
		t.Fatalf("others[0] key order = %v", keys)
	}
	ofield := func(key string) any {
		v, _ := o.Get(key)
		return v
	}
	if ofield("name") != "orchestrator-4" || ofield("kind") != "claude" || ofield("pane") != "ws:p1" ||
		ofield("tab") != "one" || ofield("state") != "idle" || ofield("workspace") != "ws" {
		t.Fatalf("others[0] = %#v (the text's other live agent, with the missing values as \"\")", o)
	}

	// The text keeps its own shape: the truncated task and the cut label.
	code, textOut, stderr := f.run(t, f.env, "roster")
	if code != 0 || stderr != "" {
		t.Fatalf("roster text code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(textOut, rosterJSONLongTask[:39]+"…") || strings.Contains(textOut, rosterJSONLongLabel) {
		t.Fatalf("roster text lost its cuts:\n%s", textOut)
	}
}

func TestRosterJSONServerScopeBothFlagOrders(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"build","pane_id":"ws:p2","agent":"pi","agent_status":"working"},{"name":"orchestrator-4","pane_id":"ws:p1","agent":"claude","agent_status":"idle"},{"name":"far","pane_id":"other:p2","agent":"grok","agent_status":"done"}]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"ws:p1","workspace_id":"ws","tab_id":"ws:t1"},{"pane_id":"ws:p2","workspace_id":"ws","tab_id":"ws:t2"}]}}`},
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"ws:p1","workspace_id":"ws"},{"pane_id":"ws:p2","workspace_id":"ws"},{"pane_id":"other:p2","workspace_id":"other"}]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
	})
	f.writeRoster(t, strings.Join([]string{"build", "ws:p2", "pi", "implementer", "xai", "0", "/tmp/work", "now", "grok-4.7", "ask", "", "build"}, "\t"))
	first, second := "", ""
	for i, argv := range [][]string{{"roster", "--json", "--scope", "server"}, {"roster", "--scope", "server", "--json"}} {
		code, out, stderr := f.run(t, f.env, argv...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v code=%d stderr=%q out=%q", argv, code, stderr, out)
		}
		obj := rosterJSONParse(t, out)
		if v, _ := obj.Get("scope"); v != "server" {
			t.Fatalf("%v scope = %v, want server", argv, v)
		}
		if v, _ := obj.Get("agents"); v == nil || len(v.([]any)) != 1 {
			t.Fatalf("%v agents = %#v", argv, v)
		}
		agents, _ := obj.Get("agents")
		if prev, _ := agents.([]any)[0].(*jsonjs.Object).Get("previous_role"); prev != "" {
			t.Fatalf("%v previous_role = %v, want \"\" when the history is empty", argv, prev)
		}
		others, _ := obj.Get("others")
		oarr, ok := others.([]any)
		if !ok || len(oarr) != 2 {
			t.Fatalf("%v others = %#v, want the two live agents outside the roster", argv, others)
		}
		var far *jsonjs.Object
		for _, e := range oarr {
			eo := e.(*jsonjs.Object)
			if n, _ := eo.Get("name"); n == "far" {
				far = eo
			}
		}
		if far == nil {
			t.Fatalf("%v the other workspace's agent is missing from others: %s", argv, out)
		}
		if w, _ := far.Get("workspace"); w != "other" {
			t.Fatalf("%v far workspace = %v, want other (the text's grouping)", argv, w)
		}
		if tab, _ := far.Get("tab"); tab != "" {
			t.Fatalf("%v far tab = %v, want \"\" for a pane with no tab", argv, tab)
		}
		if i == 0 {
			first = out
		} else {
			second = out
		}
	}
	if first != second {
		t.Fatalf("the two flag orders disagree:\n%s\n%s", first, second)
	}
}

func TestRosterJSONEmptyRosterAndNoOthers(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
	})
	code, out, stderr := f.run(t, f.env, "roster", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("roster --json code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj := rosterJSONParse(t, out)
	for _, key := range []string{"agents", "others"} {
		v, _ := obj.Get(key)
		if v == nil {
			t.Fatalf("%s key missing: %s", key, out)
		}
		arr, ok := v.([]any)
		if !ok || len(arr) != 0 {
			t.Fatalf("%s = %#v, want an empty array (the JSON has no \"-\" cells)", key, v)
		}
	}
}

// TestRosterJSONUsageKeepsTheRefusals pins the non-NOWRITE arg handling:
// every accepted form runs, and every other shape exits 2 with the new usage.
func TestRosterJSONUsageKeepsTheRefusals(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
	})
	for _, argv := range [][]string{{"roster"}, {"roster", "--json"}, {"roster", "--scope", "workspace", "--json"}, {"roster", "--scope", "server"}} {
		code, _, stderr := f.run(t, f.env, argv...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v code=%d stderr=%q", argv, code, stderr)
		}
	}
	for _, argv := range [][]string{{"roster", "--json", "--json"}, {"roster", "--scope"}, {"roster", "--scope", "all"}, {"roster", "--scope", "server", "extra"}, {"roster", "--json", "extra"}, {"roster", "--close"}} {
		code, out, stderr := f.run(t, f.env, argv...)
		if code != 2 || out != "" || stderr != "soho: usage: roster [--scope workspace|server] [--json]\n" {
			t.Fatalf("%v code=%d out=%q stderr=%q, want exit 2 with the new usage", argv, code, out, stderr)
		}
	}
	// roles: --json is the only new argument; anything else is today's
	// behavior (the table is printed, the arguments ignored).
	f2 := newCopiesFixture(t, nil)
	code, jsonOut, stderr := f2.run(t, f2.env, "roles", "--json")
	if code != 0 || stderr != "" || !strings.HasPrefix(jsonOut, `{"roles":[`) {
		t.Fatalf("roles --json code=%d stderr=%q out=%q", code, stderr, jsonOut)
	}
	code, textOut, stderr := f2.run(t, f2.env, "roles", "--json", "extra")
	if code != 0 || stderr != "" || !strings.Contains(textOut, "ROLE") {
		t.Fatalf("roles with other arguments keeps today's text table: code=%d stderr=%q out=%q", code, stderr, textOut)
	}
}

// TestNowriteJSONForms pins the SOHO_NOWRITE=1 contract for the new forms:
// the four roster/roles shapes run read-only, and the new refusal message
// names them, with the doubled 'soho: soho:' prefix and the old tail kept.
func TestNowriteJSONForms(t *testing.T) {
	nr := newNowriteReads(t, true)
	accepted := [][]string{
		{"roles", "--json"},
		{"roster", "--json"},
		{"roster", "--json", "--scope", "workspace"},
		{"roster", "--scope", "server", "--json"},
	}
	for _, argv := range accepted {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			code, out, errOut := nr.runNowrite(t, argv...)
			if code != 0 || errOut != "" {
				t.Fatalf("%v under NOWRITE code=%d err=%q out=%q", argv, code, errOut, out)
			}
			if !strings.HasPrefix(out, `{`) || !strings.HasSuffix(out, "}\n") {
				t.Fatalf("%v under NOWRITE did not print the JSON object: %q", argv, out)
			}
			parsed, err := jsonjs.Parse([]byte(out))
			if err != nil {
				t.Fatalf("%v JSON does not parse: %v\n%s", argv, err, out)
			}
			if argv[0] == "roster" {
				obj, ok := parsed.(*jsonjs.Object)
				if !ok {
					t.Fatalf("%v root is %T", argv, parsed)
				}
				if v, _ := obj.Get("workspace"); v != "ws" {
					t.Fatalf("%v workspace = %v", argv, v)
				}
			}
		})
	}
	const messagePrefix = "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: "
	const messageTail = " — unset SOHO_NOWRITE to write\n"
	rejected := []struct {
		argv    []string
		refused string
	}{
		{[]string{"roles", "--json", "extra"}, "roles (2 extra arguments not allowed)"},
		{[]string{"roster", "--json", "--json"}, "roster (2 extra arguments not allowed)"},
		{[]string{"roster", "--scope", "server", "extra"}, "roster (3 extra arguments not allowed)"},
	}
	for _, c := range rejected {
		t.Run(strings.Join(c.argv, "_"), func(t *testing.T) {
			code, out, errOut := nr.runNowrite(t, c.argv...)
			if code != 2 || out != "" {
				t.Fatalf("%v under NOWRITE code=%d out=%q err=%q", c.argv, code, out, errOut)
			}
			want := messagePrefix + c.refused + messageTail
			if errOut != want {
				t.Fatalf("stderr:\n got:  %q\nwant: %q", errOut, want)
			}
		})
	}
}

// TestRosterJSONUsageLineShowsJson pins the usage lines that decision 5 of
// DJA-375 changes: the roles line and the roster line show the --json.
func TestRosterJSONUsageLineShowsJson(t *testing.T) {
	env, cwd := commandFixture(t)
	for command, want := range map[string]string{
		"roles":  "  soho roles [--json] | kinds\n",
		"kinds":  "  soho roles [--json] | kinds\n",
		"roster": "  soho roster [--scope workspace|server] [--json]\n",
	} {
		code, out, errOut := runIn(t, []string{command, "--help"}, env, cwd)
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("%s --help: code=%d out=%q want %q err=%q", command, code, out, want, errOut)
		}
	}
	// The full usage carries both lines; no other line changed.
	code, out, errOut := runIn(t, []string{"help"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "  soho roles [--json] | kinds\n") || !strings.Contains(out, "  soho roster [--scope workspace|server] [--json]\n") {
		t.Fatalf("help code=%d err=%q; want both usage lines updated", code, errOut)
	}
}
