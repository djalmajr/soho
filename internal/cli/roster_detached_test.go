package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// detachedFixture is a project with two workspace state directories, a fake
// herdr that records every call (and fails when one dares to happen) and,
// when asked, a fake hc scripted by the test. Every path lives in one temp
// tree, and the process environment is confined to that tree.
type detachedFixture struct {
	root, project, state, bin string
	env                       platform.Env
}

func newDetachedFixture(t *testing.T, hcRules []fakecli.Rule, withHC bool) *detachedFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &detachedFixture{
		root:    root,
		project: filepath.Join(root, "project"),
		state:   filepath.Join(root, "state"),
		bin:     filepath.Join(root, "bin"),
	}
	skill := filepath.Join(root, "skill")
	for _, dir := range []string{f.project, f.state, f.bin, skill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	captureEnv := fakecli.InstallOptions{CaptureEnv: []string{"_SOHO_LAUNCH_CONFIG_BASE", "_SOHO_LAUNCH_HOME"}}
	if withHC {
		if _, err := fakecli.InstallWithOptions(t, f.bin, "hc", hcRules, captureEnv); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fakecli.InstallWithOptions(t, f.bin, "herdr", []fakecli.Rule{{AnyArgs: true, Code: 91, Stderr: "herdr was called"}}, captureEnv); err != nil {
		t.Fatal(err)
	}
	f.env = platform.Env{
		"HOME":                root,
		"USERPROFILE":         root,
		"XDG_CONFIG_HOME":     filepath.Join(root, "conf"),
		"TMPDIR":              root,
		"PATH":                f.bin,
		"SOHO_FAKECLI_CONFIG": f.bin,
		"SOHO_DIR":            f.state,
		"SOHO_SKILL_DIR":      skill,
		"SOHO_NOWRITE":        "1",
	}
	return f
}

func detachedRow(name, address, kind, role, model, effort string) string {
	return strings.Join([]string{name, address, kind, role, "xai", "0", "/work/" + name, "now", model, "ask", "former-" + role, "build", "", "", effort}, "\t")
}

func (f *detachedFixture) writeRows(t *testing.T, workspace string, rows ...string) string {
	t.Helper()
	dir := filepath.Join(f.state, workspace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"
	for _, row := range rows {
		content += row + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (f *detachedFixture) calls(t *testing.T, name string) [][]string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, name+".calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := make([][]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.Argv)
	}
	return out
}

// detachedSnapshot records every directory and the digest of every file
// under the named roots, so a created directory or a changed byte is visible.
func detachedSnapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			key := filepath.Join(root, rel)
			if entry.IsDir() {
				out[key] = "dir"
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			out[key] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

func detachedParse(t *testing.T, out string) *jsonjs.Object {
	t.Helper()
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("roster JSON is not one line: %q", out)
	}
	value, err := jsonjs.Parse([]byte(out))
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("root = %T, want object", value)
	}
	return obj
}

func detachedAgents(t *testing.T, obj *jsonjs.Object) []any {
	t.Helper()
	value, _ := obj.Get("agents")
	agents, ok := value.([]any)
	if !ok {
		t.Fatalf("agents = %#v", value)
	}
	return agents
}

func detachedField(t *testing.T, row any, key string) any {
	t.Helper()
	obj, ok := row.(*jsonjs.Object)
	if !ok {
		t.Fatalf("row is %T", row)
	}
	value, found := obj.Get(key)
	if !found {
		t.Fatalf("row %v has no %s", obj.Keys(), key)
	}
	return value
}

func detachedHCRecord(state string) string {
	return `{"address":"t7","state":"live","status":{"state":"` + state + `"}}`
}

// The main acceptance fixture: both workspaces, one hc get per hc row, no
// get for herdr pane ids, no herdr call, and a byte-identical state root.
func TestDetachedRosterHCJSONTwoWorkspacesAndNowrite(t *testing.T) {
	hostile := `t9; echo "pwned" $(touch /tmp/pwned-sentinel) &`
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Stdout: detachedHCRecord("working")},
		{Argv: []string{"get", "t8", "--json"}, Stdout: detachedHCRecord("idle")},
		{Argv: []string{"get", "t11", "--json"}, Stdout: detachedHCRecord("blocked")},
		{Argv: []string{"get", hostile, "--json"}, Stdout: detachedHCRecord("working")},
	}
	f := newDetachedFixture(t, rules, true)
	task := `say "hello" \\ and $HOME; keep exactly`
	dirA := f.writeRows(t, "ws-a",
		detachedRow("build", "t7", "pi", "implementer", "lane-model", "high"),
		detachedRow("review", "t8", "codex", "reviewer", "review-model", "medium"),
		detachedRow("legacy", "ws:p1", "pi", "planner", "", ""),
		detachedRow("addressless", "", "pi", "tasker", "", ""),
	)
	f.writeRows(t, "ws-b",
		detachedRow("scout", "t11", "pi", "scout", "scout-model", "low"),
		detachedRow("hostile", hostile, "pi", "tasker", "", ""),
	)
	if err := os.WriteFile(filepath.Join(dirA, "task-build"), []byte(task+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dirA, "reports", "build-report.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "last-report-build"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := detachedSnapshot(t, f.project, f.state)

	env := f.env.Clone()
	env["SOHO_TRANSPORT"] = "hc"
	delete(env, "HC_TERMINAL_ID")
	code, out, stderr := runIn(t, []string{"roster", "--json"}, env, f.project)
	if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
		t.Fatalf("detached roster wrote state:\nbefore %v\nafter  %v", before, after)
	}
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj := detachedParse(t, out)
	wantKeys := "workspace,scope,settings,agents,others,detached,state_root"
	if keys := obj.Keys(); strings.Join(keys, ",") != wantKeys {
		t.Fatalf("top-level keys = %v, want %s", keys, wantKeys)
	}
	if detached, _ := obj.Get("detached"); detached != true {
		t.Fatalf("detached = %#v", detached)
	}
	if root, _ := obj.Get("state_root"); root != f.state {
		t.Fatalf("state_root = %#v, want %q", root, f.state)
	}
	if workspace, _ := obj.Get("workspace"); workspace != "" {
		t.Fatalf("a detached answer printed a caller workspace: %#v", workspace)
	}
	if others, _ := obj.Get("others"); !reflect.DeepEqual(others, []any{}) {
		t.Fatalf("others = %#v, want empty", others)
	}
	agents := detachedAgents(t, obj)
	if len(agents) != 6 {
		t.Fatalf("agents = %d, want 6", len(agents))
	}
	wantRowKeys := "name,role,previous_role,kind,pane,tab,state,report,cwd,task,model,effort,status,report_path,task_s,activity_s,workspace"
	for _, row := range agents {
		if keys := row.(*jsonjs.Object).Keys(); strings.Join(keys, ",") != wantRowKeys {
			t.Fatalf("agent keys = %v, want %s", keys, wantRowKeys)
		}
	}
	byName := map[string]any{}
	for _, row := range agents {
		byName[detachedField(t, row, "name").(string)] = row
	}
	for name, want := range map[string]struct{ workspace, state, status, task string }{
		"build":       {"ws-a", "unknown", "working", task},
		"review":      {"ws-a", "unknown", "idle", ""},
		"legacy":      {"ws-a", "unknown", "unknown", ""},
		"addressless": {"ws-a", "unknown", "unknown", ""},
		"scout":       {"ws-b", "unknown", "blocked", ""},
		"hostile":     {"ws-b", "unknown", "working", ""},
	} {
		row, found := byName[name]
		if !found {
			t.Fatalf("no agent %q", name)
		}
		for key, value := range map[string]any{
			"workspace": want.workspace, "state": want.state, "status": want.status, "task": want.task,
		} {
			if got := detachedField(t, row, key); got != value {
				t.Fatalf("agents[%s].%s = %#v, want %#v", name, key, got, value)
			}
		}
		for _, key := range []string{"task_s", "activity_s"} {
			if got := detachedField(t, row, key); got != nil {
				t.Fatalf("agents[%s].%s = %#v, want null", name, key, got)
			}
		}
		if got := detachedField(t, row, "tab"); got != "" {
			t.Fatalf("agents[%s].tab = %#v, want empty", name, got)
		}
	}
	build := byName["build"]
	if got := detachedField(t, build, "report"); got != "ready" {
		t.Fatalf("build.report = %#v", got)
	}
	if got := detachedField(t, build, "report_path"); got != report {
		t.Fatalf("build.report_path = %#v, want %q", got, report)
	}
	if got := detachedField(t, build, "model"); got != "lane-model" {
		t.Fatalf("build.model = %#v", got)
	}
	if got := detachedField(t, build, "previous_role"); got != "former-implementer" {
		t.Fatalf("build.previous_role = %#v", got)
	}
	if got := detachedField(t, build, "effort"); got != "high" {
		t.Fatalf("build.effort = %#v", got)
	}

	wantCalls := [][]string{
		{"get", "t7", "--json"},
		{"get", "t8", "--json"},
		{"get", "t11", "--json"},
		{"get", hostile, "--json"},
	}
	if got := f.calls(t, "hc"); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("hc calls = %#v, want exactly %#v", got, wantCalls)
	}
	if got := f.calls(t, "herdr"); len(got) != 0 {
		t.Fatalf("herdr calls = %#v, want none", got)
	}

	// --scope is accepted but the detached answer stays state-root-wide.
	second := f.env.Clone()
	second["SOHO_TRANSPORT"] = "hc"
	code, out2, stderr := runIn(t, []string{"roster", "--scope", "server", "--json"}, second, f.project)
	if code != 0 || stderr != "" || out2 != out {
		t.Fatalf("second invocation code=%d stderr=%q output differed: %q", code, stderr, out2)
	}
	wantTwice := append(append([][]string{}, wantCalls...), wantCalls...)
	if got := f.calls(t, "hc"); !reflect.DeepEqual(got, wantTwice) {
		t.Fatalf("hc calls after two invocations = %#v, want %#v", got, wantTwice)
	}
	if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
		t.Fatal("the second detached invocation wrote state")
	}
}

// Outside Herdr with the herdr transport the same read works, calls no herdr
// binary, and leaves a herdr-style row's status unknown even though hc is on
// PATH for the hc rows. SOHO_NOWRITE is absent here: the path is read-only by
// itself, not because a gate forced it.
func TestDetachedRosterHerdrTransportCallsNoHerdr(t *testing.T) {
	f := newDetachedFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Stdout: detachedHCRecord("working")},
	}, true)
	f.writeRows(t, "ws-a",
		detachedRow("build", "t7", "pi", "implementer", "", ""),
		detachedRow("legacy", "ws:p1", "pi", "planner", "", ""),
	)
	delete(f.env, "SOHO_NOWRITE")
	before := detachedSnapshot(t, f.project, f.state)
	code, out, stderr := runIn(t, []string{"roster", "--scope", "workspace", "--json"}, f.env, f.project)
	if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
		t.Fatalf("detached roster wrote state: before %v after %v", before, after)
	}
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
	}
	obj := detachedParse(t, out)
	agents := detachedAgents(t, obj)
	if len(agents) != 2 {
		t.Fatalf("agents = %#v", agents)
	}
	if got := detachedField(t, agents[0], "status"); got != "working" {
		t.Fatalf("build.status = %#v, want working", got)
	}
	if got := detachedField(t, agents[1], "status"); got != "unknown" {
		t.Fatalf("legacy.status = %#v, want unknown", got)
	}
	if got := f.calls(t, "hc"); !reflect.DeepEqual(got, [][]string{{"get", "t7", "--json"}}) {
		t.Fatalf("hc calls = %#v", got)
	}
	if got := f.calls(t, "herdr"); len(got) != 0 {
		t.Fatalf("herdr calls = %#v, want none", got)
	}
}

// No hc binary: every row, including an hc-shaped address, is unknown.
func TestDetachedRosterWithoutHcOnPath(t *testing.T) {
	f := newDetachedFixture(t, nil, false)
	f.writeRows(t, "ws-a",
		detachedRow("build", "t7", "pi", "implementer", "", ""),
		detachedRow("legacy", "ws:p1", "pi", "planner", "", ""),
	)
	env := f.env.Clone()
	env["SOHO_TRANSPORT"] = "hc"
	before := detachedSnapshot(t, f.project, f.state)
	code, out, stderr := runIn(t, []string{"roster", "--json"}, env, f.project)
	if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
		t.Fatal("detached roster without hc wrote state")
	}
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
	}
	for _, row := range detachedAgents(t, detachedParse(t, out)) {
		if got := detachedField(t, row, "status"); got != "unknown" {
			t.Fatalf("%v.status = %#v, want unknown", detachedField(t, row, "name"), got)
		}
	}
	if got := f.calls(t, "herdr"); len(got) != 0 {
		t.Fatalf("herdr calls = %#v", got)
	}
}

// A failed get for another reason and a timed-out get keep the row's status
// unknown; only a successful, parseable get supplies one. A not-found get is
// the finished/missing case and is covered by the next test.
func TestDetachedRosterHCFailureAndTimeoutStayUnknown(t *testing.T) {
	f := newDetachedFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"failed"}}`},
		{Argv: []string{"get", "t8", "--json"}, Delay: 2500, Stdout: detachedHCRecord("idle")},
		{Argv: []string{"get", "t9", "--json"}, Stdout: detachedHCRecord("working")},
	}, true)
	f.writeRows(t, "ws-a",
		detachedRow("failed", "t7", "pi", "tasker", "", ""),
		detachedRow("slow", "t8", "pi", "tasker", "", ""),
		detachedRow("live", "t9", "pi", "tasker", "", ""),
	)
	env := f.env.Clone()
	env["SOHO_TRANSPORT"] = "hc"
	code, out, stderr := runIn(t, []string{"roster", "--json"}, env, f.project)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
	}
	want := map[string]string{"failed": "unknown", "slow": "unknown", "live": "working"}
	for _, row := range detachedAgents(t, detachedParse(t, out)) {
		name := detachedField(t, row, "name").(string)
		if got := detachedField(t, row, "status"); got != want[name] {
			t.Fatalf("%s.status = %#v, want %q", name, got, want[name])
		}
	}
	wantCalls := [][]string{{"get", "t7", "--json"}, {"get", "t8", "--json"}, {"get", "t9", "--json"}}
	if got := f.calls(t, "hc"); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("hc calls = %#v, want %#v", got, wantCalls)
	}
}

// F1: the detached status follows the in-terminal stateOf rule: a finished
// record, with or without a status object, and a not-found get are gone. A
// live record still reports its status.state word.
func TestDetachedRosterFinishedAndMissingAreGone(t *testing.T) {
	f := newDetachedFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"finished","exit_code":0,"parent":"t0"}`},
		{Argv: []string{"get", "t2", "--json"}, Stdout: `{"address":"t2","state":"finished","status":null,"turn":9}`},
		{Argv: []string{"get", "t3", "--json"}, Stdout: `{"address":"t3","state":"finished","status":{"state":"done","app":"pi"}}`},
		{Argv: []string{"get", "t4", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
		{Argv: []string{"get", "t5", "--json"}, Stdout: `{"address":"t5","state":"live","status":{"state":"working"}}`},
	}, true)
	f.writeRows(t, "ws-a",
		detachedRow("finished", "t1", "pi", "tasker", "", ""),
		detachedRow("finished-null", "t2", "pi", "tasker", "", ""),
		detachedRow("finished-done", "t3", "pi", "tasker", "", ""),
		detachedRow("not-found", "t4", "pi", "tasker", "", ""),
		detachedRow("live", "t5", "pi", "tasker", "", ""),
	)
	env := f.env.Clone()
	env["SOHO_TRANSPORT"] = "hc"
	before := detachedSnapshot(t, f.project, f.state)
	code, out, stderr := runIn(t, []string{"roster", "--json"}, env, f.project)
	if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
		t.Fatal("the finished-terminal read wrote state")
	}
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
	}
	want := map[string]string{
		"finished": "gone", "finished-null": "gone", "finished-done": "gone",
		"not-found": "gone", "live": "working",
	}
	rows := detachedAgents(t, detachedParse(t, out))
	if len(rows) != len(want) {
		t.Fatalf("agents = %d, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		name := detachedField(t, row, "name").(string)
		if got := detachedField(t, row, "status"); got != want[name] {
			t.Fatalf("%s.status = %#v, want %q", name, got, want[name])
		}
	}
	wantCalls := [][]string{
		{"get", "t1", "--json"}, {"get", "t2", "--json"}, {"get", "t3", "--json"},
		{"get", "t4", "--json"}, {"get", "t5", "--json"},
	}
	if got := f.calls(t, "hc"); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("hc calls = %#v, want %#v", got, wantCalls)
	}
}

// F2: the detached entry runs after Run drops the job's private launch
// locators, so the hc child receives neither path even when the caller has
// both. The fake hc records the variables it actually inherited.
func TestDetachedRosterDropsLaunchLocatorsBeforeHcChild(t *testing.T) {
	f := newDetachedFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Stdout: detachedHCRecord("working")},
	}, true)
	f.writeRows(t, "ws-a", detachedRow("build", "t7", "pi", "tasker", "", ""))
	env := f.env.Clone()
	env["SOHO_TRANSPORT"] = "hc"
	env["_SOHO_LAUNCH_CONFIG_BASE"] = filepath.Join(f.root, "launch-base")
	env["_SOHO_LAUNCH_HOME"] = filepath.Join(f.root, "launch-home")
	before := detachedSnapshot(t, f.project, f.state)
	code, out, stderr := runIn(t, []string{"roster", "--json"}, env, f.project)
	if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
		t.Fatal("the detached roster with locators wrote state")
	}
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
	}
	if got := detachedField(t, detachedAgents(t, detachedParse(t, out))[0], "status"); got != "working" {
		t.Fatalf("build.status = %#v, want working", got)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("hc calls = %d, want exactly one", len(calls))
	}
	for _, key := range []string{"_SOHO_LAUNCH_CONFIG_BASE", "_SOHO_LAUNCH_HOME"} {
		if value, found := calls[0].Env[key]; found {
			t.Fatalf("the hc child inherited %s=%q from the caller", key, value)
		}
	}
}

// The boundary cases are reads of whatever exists. No missing root is made
// present and no file or directory appears in its place.
func TestDetachedRosterBoundaries(t *testing.T) {
	t.Run("missing state root", func(t *testing.T) {
		f := newDetachedFixture(t, nil, false)
		missing := filepath.Join(f.root, "absent-state")
		f.env["SOHO_DIR"] = missing
		before := detachedSnapshot(t, f.root)
		code, out, stderr := runIn(t, []string{"roster", "--json"}, f.env, f.project)
		if after := detachedSnapshot(t, f.root); !reflect.DeepEqual(before, after) {
			t.Fatalf("missing root was created: before %v after %v", before, after)
		}
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		obj := detachedParse(t, out)
		if root, _ := obj.Get("state_root"); root != missing {
			t.Fatalf("state_root = %#v, want %q", root, missing)
		}
		if agents := detachedAgents(t, obj); len(agents) != 0 {
			t.Fatalf("agents = %#v, want none", agents)
		}
		if _, err := os.Lstat(missing); !os.IsNotExist(err) {
			t.Fatalf("missing state root now exists: %v", err)
		}
	})
	t.Run("empty state root and stray file", func(t *testing.T) {
		f := newDetachedFixture(t, nil, false)
		if err := os.WriteFile(filepath.Join(f.state, "not-a-workspace"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runIn(t, []string{"roster", "--json"}, f.env, f.project)
		if code != 0 || stderr != "" || len(detachedAgents(t, detachedParse(t, out))) != 0 {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("workspace without a roster", func(t *testing.T) {
		f := newDetachedFixture(t, nil, false)
		if err := os.MkdirAll(filepath.Join(f.state, "empty-ws"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runIn(t, []string{"roster", "--json"}, f.env, f.project)
		if code != 0 || stderr != "" || len(detachedAgents(t, detachedParse(t, out))) != 0 {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("SOHO_STATE_DIR at a missing path", func(t *testing.T) {
		f := newDetachedFixture(t, nil, false)
		override := filepath.Join(f.root, "override-state")
		delete(f.env, "SOHO_DIR")
		f.env["SOHO_STATE_DIR"] = override
		before := detachedSnapshot(t, f.root)
		code, out, stderr := runIn(t, []string{"roster", "--json"}, f.env, f.project)
		if after := detachedSnapshot(t, f.root); !reflect.DeepEqual(before, after) {
			t.Fatal("SOHO_STATE_DIR path was created")
		}
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		obj := detachedParse(t, out)
		if root, _ := obj.Get("state_root"); root != override {
			t.Fatalf("state_root = %#v, want %q", root, override)
		}
	})
	t.Run("resolved project state root without SOHO_DIR", func(t *testing.T) {
		f := newDetachedFixture(t, nil, false)
		f.writeRows(t, "ws-a", detachedRow("build", "t7", "pi", "tasker", "", ""))
		// The fixture project is not a Git repository, so StateProjectRoot
		// resolves to cwd and the default state root is <project>/.soho.
		state := filepath.Join(f.project, ".soho")
		dir := filepath.Join(state, "ws-a")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(f.state, "ws-a", "agents.tsv"), filepath.Join(dir, "agents.tsv")); err != nil {
			t.Fatal(err)
		}
		delete(f.env, "SOHO_DIR")
		code, out, stderr := runIn(t, []string{"roster", "--json"}, f.env, f.project)
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
		obj := detachedParse(t, out)
		if root, _ := obj.Get("state_root"); root != state {
			t.Fatalf("state_root = %#v, want %q", root, state)
		}
		agents := detachedAgents(t, obj)
		if len(agents) != 1 || detachedField(t, agents[0], "workspace") != "ws-a" {
			t.Fatalf("agents = %#v", agents)
		}
	})
}

// Only the JSON form changes: the text form still dies with today's literal
// message in both transports, without writing anything.
func TestDetachedRosterTextFormStillRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, transport, want string
	}{
		{
			name: "herdr",
			want: "soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside\n",
		},
		{
			name:      "hc",
			transport: "hc",
			want:      "soho: transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set); run soho from a terminal started with hc\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetachedFixture(t, nil, true)
			f.writeRows(t, "ws-a", detachedRow("build", "t7", "pi", "tasker", "", ""))
			env := f.env.Clone()
			if tc.transport != "" {
				env["SOHO_TRANSPORT"] = tc.transport
			}
			before := detachedSnapshot(t, f.project, f.state)
			code, out, stderr := runIn(t, []string{"roster"}, env, f.project)
			if after := detachedSnapshot(t, f.project, f.state); !reflect.DeepEqual(before, after) {
				t.Fatal("a refused text roster wrote state")
			}
			if code != 2 || out != "" || stderr != tc.want {
				t.Fatalf("code=%d out=%q stderr=%q, want %q", code, out, stderr, tc.want)
			}
		})
	}
}
