package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestLayoutPlanPaneColumnsNextSpawn(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		workers    int
		want       []int
	}{
		{"auto-0", "auto", 0, []int{1, 1}},
		{"auto-1", "auto", 1, []int{1, 2}},
		{"auto-2", "auto", 2, []int{1, 3}},
		{"auto-3", "auto", 3, []int{1, 1, 3}},
		{"auto-5", "auto", 5, []int{1, 3, 3}},
		{"auto-6", "auto", 6, []int{1, 1, 3, 3}},
		{"list-control", "1,3,3", 3, []int{1, 3, 1}},
		{"empty-control", "", 3, []int{1, 2, 2}},
		{"balance-overflow", "1,2,3", 5, []int{1, 3, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			panes := []string{`{"pane_id":"C","rect":{"width":60,"height":90}}`}
			ids := []string{}
			for i, id := range []string{"A", "B", "D", "E", "F", "G"}[:tc.workers] {
				ids = append(ids, id)
				panes = append(panes, fmt.Sprintf(`{"pane_id":%q,"rect":{"x":%d,"y":%d,"width":60,"height":30}}`, id, 60*(1+i/3), 30*(i%3)))
			}
			file := filepath.Join(dir, "layout.json")
			raw := fmt.Sprintf(`{"result":{"layout":{"area":{"width":%d,"height":90},"panes":[%s]}}}`, 60*(1+(tc.workers+2)/3), strings.Join(panes, ","))
			if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			env := layoutPlanEnv(t)
			env["PATH"], env["TMPDIR"] = t.TempDir(), dir
			env["SOHO_PANE_COLUMNS"], env["SOHO_PANE_COLUMN_ROWS"] = tc.mode, "3"
			if tc.mode == "" {
				env["SOHO_SPLIT_MAX_PANES"] = "6"
			}
			ctx := core.LoadConfig(env, dir)
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			defer func() { platform.Stdout = old }()
			code := cmdLayoutPlan([]string{"--layout", file, "--me", "C", "--mine", strings.Join(ids, " ")}, &ctx, env, dir)
			var got struct {
				Placement string
				Cap       *int
				Grid      struct {
					Cells, Cols int
					Rows        []int `json:"rows_per_col"`
				}
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			t.Logf("mode=%q current_workers=%d code=%d output=%s", tc.mode, tc.workers, code, out.String())
			if code != 0 || got.Placement != "split" || got.Grid.Cells != tc.workers+2 || got.Grid.Cols != len(tc.want) || !reflect.DeepEqual(got.Grid.Rows, tc.want) {
				t.Fatalf("next-spawn plan=%s; want split, cells=%d rows=%v", out.String(), tc.workers+2, tc.want)
			}
			if tc.mode != "" && got.Cap != nil {
				t.Fatalf("uncapped declared plan cap=%d", *got.Cap)
			}
		})
	}
}

func TestPaneOverflowConfigBalance(t *testing.T) {
	env, cwd := commandFixture(t)
	env["USERPROFILE"], env["TMPDIR"], env["PATH"] = env["HOME"], t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		source string
		args   []string
	}{{"defaults", nil}, {"project", []string{"config", "set", "pane_overflow", "balance"}}} {
		if tc.args != nil {
			code, out, errOut := runIn(t, tc.args, env, cwd)
			if code != 0 || errOut != "" || !strings.Contains(out, "set pane_overflow=balance in ") {
				t.Fatalf("config set: code=%d out=%q stderr=%q", code, out, errOut)
			}
		}
		code, out, errOut := runIn(t, []string{"config"}, env, cwd)
		found := false
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "pane_overflow" {
				found = true
				if fields[1] != "balance" || fields[2] != tc.source {
					t.Fatalf("config row=%q want balance %s", line, tc.source)
				}
			}
		}
		if code != 0 || errOut != "" || !found {
			t.Fatalf("config: code=%d out=%q stderr=%q", code, out, errOut)
		}
	}
	file := filepath.Join(cwd, ".agents", "soho.conf")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"BALANCE", "balance ", "side"} {
		code, _, _ := runIn(t, []string{"config", "set", "pane_overflow", value}, env, cwd)
		after, err := os.ReadFile(file)
		if code != 2 || err != nil || !bytes.Equal(before, after) {
			t.Fatalf("invalid overflow=%q code=%d file=%q err=%v", value, code, after, err)
		}
	}
}

// layoutPlanEnv is the Env the layout-plan tests load config with: the
// skill directory plus HOME, USERPROFILE, XDG_CONFIG_HOME and APPDATA each
// pointed at a fresh empty directory under t.TempDir(), so the user-config
// layer resolves inside the test's own tree and a machine user config (a
// live split_max_panes, for example) never overrides the defaults.
func layoutPlanEnv(t *testing.T) platform.Env {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"home", "userprofile", "xdg", "appdata"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return platform.Env{
		"SOHO_SKILL_DIR":  testSkillDir(t),
		"HOME":            filepath.Join(root, "home"),
		"USERPROFILE":     filepath.Join(root, "userprofile"),
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
		"APPDATA":         filepath.Join(root, "appdata"),
	}
}

func TestLayoutPlanEnvIsHermetic(t *testing.T) {
	t.Run(`layout-plan test env: the user config resolves into the temp root, not the machine user`, func(t *testing.T) {
		env := layoutPlanEnv(t)
		root := filepath.Dir(env["XDG_CONFIG_HOME"])
		if got := filepath.Clean(platform.UserConfigPath(platform.Current(), env)); !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Fatalf("user config %q is not inside the temp root %q", got, root)
		}
		ctx := core.LoadConfig(env, t.TempDir())
		entry, ok := ctx.Entries["split_max_panes"]
		if !ok || entry.Value != "4" || entry.Source != "defaults" {
			t.Fatalf("split_max_panes value=%q source=%q; want 4 from the defaults (the live user file must not be read)", entry.Value, entry.Source)
		}
	})
	t.Run(`a split_max_panes planted in the helper's own user config is read`, func(t *testing.T) {
		env := layoutPlanEnv(t)
		userFile := platform.UserConfigPath(platform.Current(), env)
		if err := os.MkdirAll(filepath.Dir(userFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userFile, []byte("split_max_panes=6\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := core.LoadConfig(env, t.TempDir())
		entry, ok := ctx.Entries["split_max_panes"]
		if !ok || entry.Value != "6" || entry.Source != "user" {
			t.Fatalf("split_max_panes value=%q source=%q; want 6 from the planted user file (the helper must control the user layer)", entry.Value, entry.Source)
		}
	})
}

func TestLayoutPlanFixture(t *testing.T) {
	t.Run(`Go: layout plan reports the largest candidate and grid for a fixture`, func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "layout.json")
		raw := `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"C","rect":{"x":0,"y":0,"width":50,"height":100}},{"pane_id":"A","rect":{"x":50,"y":0,"width":50,"height":100}}]}}}`
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		env := layoutPlanEnv(t)
		env["SOHO_SPLIT_MAX_PANES"] = "6"
		ctx := core.LoadConfig(env, dir)
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		defer func() { platform.Stdout = old }()
		if code := cmdLayoutPlan([]string{"--layout", file, "--me", "C", "--mine", "A"}, &ctx, env, dir); code != 0 {
			t.Fatalf("exit code=%d", code)
		}
		want := `{"placement":"split","anchor":"A","direction":"down","reason":"largest-area","cap":6,"min_pane":0.18,"candidates":[{"pane_id":"C","caller":true,"width":0.5,"height":1},{"pane_id":"A","caller":false,"width":0.5,"height":1}],"grid":{"cells":3,"cols":2,"rows_per_col":[1,2]}}` + "\n"
		if out.String() != want {
			t.Fatalf("output=%q, want %q", out.String(), want)
		}
	})
}

func TestLayoutPlanFullFixtureMatrix(t *testing.T) {
	t.Run(`layout-plan over a fixture: default cap full, cap 6 split, full grid`, func(t *testing.T) { // JS: "layout-plan over a fixture: default cap full, cap 6 split, full grid"
		// Mutation captured: changing cap handling or grid overflow changes one of these three fixture plans.
		const grid = `{"result":{"layout":{"area":{"x":0,"y":0,"width":213,"height":57},"panes":[{"pane_id":"C","rect":{"x":0,"y":0,"width":71,"height":57}},{"pane_id":"A","rect":{"x":71,"y":0,"width":71,"height":29}},{"pane_id":"B","rect":{"x":71,"y":29,"width":71,"height":28}},{"pane_id":"D","rect":{"x":142,"y":0,"width":71,"height":29}},{"pane_id":"E","rect":{"x":142,"y":29,"width":71,"height":28}}]}}}`
		const full = `{"result":{"layout":{"area":{"x":0,"y":0,"width":213,"height":57},"panes":[{"pane_id":"C","rect":{"x":0,"y":0,"width":71,"height":29}},{"pane_id":"F","rect":{"x":0,"y":29,"width":71,"height":28}},{"pane_id":"A","rect":{"x":71,"y":0,"width":71,"height":29}},{"pane_id":"B","rect":{"x":71,"y":29,"width":71,"height":28}},{"pane_id":"D","rect":{"x":142,"y":0,"width":71,"height":29}},{"pane_id":"E","rect":{"x":142,"y":29,"width":71,"height":28}}]}}}`
		run := func(raw, cap, mine string) string {
			t.Helper()
			file := filepath.Join(t.TempDir(), "layout.json")
			if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			env := layoutPlanEnv(t)
			if cap != "" {
				env["SOHO_SPLIT_MAX_PANES"] = cap
			}
			ctx := core.LoadConfig(env, filepath.Dir(file))
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			defer func() { platform.Stdout = old }()
			if code := cmdLayoutPlan([]string{"--layout", file, "--me", "C", "--mine", mine}, &ctx, env, filepath.Dir(file)); code != 0 {
				t.Fatalf("exit code=%d", code)
			}
			return out.String()
		}
		if got := run(grid, "", "A B D"); !strings.Contains(got, `"placement":"herd"`) || !strings.Contains(got, `"reason":"full","cap":4`) {
			t.Fatalf("default plan=%q", got)
		}
		if got := run(grid, "6", "A B D E"); !strings.Contains(got, `"placement":"split","anchor":"C","direction":"down","reason":"largest-area","cap":6`) || !strings.Contains(got, `"grid":{"cells":6,"cols":3,"rows_per_col":[2,2,2]}`) {
			t.Fatalf("cap-six plan=%q", got)
		}
		if got := run(full, "6", "A B D E F"); !strings.Contains(got, `"placement":"herd"`) || !strings.Contains(got, `"reason":"full","cap":6`) || !strings.Contains(got, `"grid":null`) {
			t.Fatalf("full-grid plan=%q", got)
		}
	})
}

func TestLayoutPlanRejectsMalformedLayoutDocument(t *testing.T) {
	t.Run(`layout-plan: a --layout document that is not JSON exits 2 with a message`, func(t *testing.T) { // JS: "layout-plan: a --layout document that is not JSON exits 2 with a message"
		dir := t.TempDir()
		file := filepath.Join(dir, "layout.json")
		if err := os.WriteFile(file, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		skill := testSkillDir(t)
		oldCwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chdir(oldCwd); err != nil {
				t.Errorf("restore cwd: %v", err)
			}
		})
		env := platform.Env{"SOHO_SKILL_DIR": skill, "HOME": t.TempDir(), "PATH": t.TempDir()}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		if code := Run([]string{"layout-plan", "--layout", file, "--me", "caller"}, env); code != 2 || out.Len() != 0 || errOut.Len() == 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

func TestLayoutPlanJSBoundaryCases(t *testing.T) {
	// Mutation captured: discarding json.Marshal errors blanks output when inferred dimensions are valid.
	// Mutation captured: using encoding/json escapes '<' differently from JSON.stringify.
	cases := []struct {
		name string
		raw  string
		me   string
		mine string
		want string
	}{
		{name: "infer missing area", raw: `{"result":{"layout":{"panes":[{"pane_id":"caller","rect":{"x":0,"y":0,"width":80,"height":40}}]}}}`, me: "caller", want: `"candidates":[{"pane_id":"caller","caller":true,"width":1,"height":1}]`},
		{name: "skip missing rectangle", raw: `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{}}]}}}`, me: "caller", want: `"candidates":[],"grid":{"cells":1,"cols":1,"rows_per_col":[1]}`},
		{name: "coerce JS numeric strings and skip Infinity", raw: `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"caller","rect":{"width":"50","height":100}},{"pane_id":"worker","rect":{"width":"Infinity","height":100}}]}}}`, me: "caller", mine: "worker", want: `"candidates":[{"pane_id":"caller","caller":true,"width":0.5,"height":1}]`},
		{name: "JSON.stringify leaves angle brackets", raw: `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"a<b","rect":{"x":0,"y":0,"width":100,"height":100}}]}}}`, me: "a<b", want: `"anchor":"a<b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "layout.json")
			if err := os.WriteFile(file, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			env := layoutPlanEnv(t)
			ctx := core.LoadConfig(env, dir)
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			defer func() { platform.Stdout = old }()
			args := []string{"--layout", file, "--me", tc.me}
			if tc.mine != "" {
				args = append(args, "--mine", tc.mine)
			}
			if code := cmdLayoutPlan(args, &ctx, env, dir); code != 0 {
				t.Fatalf("exit code=%d", code)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output=%q, want substring %q", out.String(), tc.want)
			}
		})
	}
}

func TestLayoutPlanFromInsideTheSkillGitCheckout(t *testing.T) {
	// Mutation captured: StateRoot used to append the state dir to the
	// .gitignore of the git checkout the cwd resolved to; run from inside a
	// skill that is a git checkout, layout-plan would create or change the
	// skill's own .gitignore (StateDirPath resolves the state path through
	// StateRoot even though layout-plan writes nothing else).
	// The fake herdr is the project's cross-platform fakecli binary: the old
	// #!/bin/sh script does not run on Windows, where the rc.11 round first
	// ran this test (gowin51: code 4 "pane layout failed").
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# herdr-soho\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(skill); err == nil {
		skill = resolved // git reports the resolved spelling (macOS /tmp)
	}
	if out, gitErr := exec.Command("git", "init", "-q", skill).CombinedOutput(); gitErr != nil {
		t.Fatalf("git init: %s %v", out, gitErr)
	}
	const seeded = "# pre-existing\n"
	if err := os.WriteFile(filepath.Join(skill, ".gitignore"), []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	const layoutJSON = `{"result":{"layout":{"area":{"width":100,"height":100},"panes":[{"pane_id":"C","rect":{"x":0,"y":0,"width":50,"height":100}},{"pane_id":"A","rect":{"x":50,"y":0,"width":50,"height":100}}]}}}`
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "layout", "--current"}, Stdout: layoutJSON},
	}); err != nil {
		t.Fatal(err)
	}
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(skill); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldCwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	entries := fakecli.Env(testutil.CleanEnv(t), bin, fakecli.EnvOptions{IncludeBasePath: true})
	env := platform.Env{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	home := t.TempDir()
	env["HERDR_ENV"], env["HERDR_PANE_ID"], env["HERDR_WORKSPACE_ID"] = "1", "C", "ws"
	env["SOHO_SKILL_DIR"] = skill
	env["HOME"], env["USERPROFILE"] = home, home
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := Run([]string{"layout-plan"}, env); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), `"placement":"split","anchor":"C"`) {
		t.Fatalf("layout-plan did not proceed: %q", out.String())
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatalf("read the herdr call log: %v", err)
	}
	requested := false
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "layout" {
			requested = true
		}
	}
	if !requested {
		t.Fatalf("pane layout was not requested: %#v", calls)
	}
	got, err := os.ReadFile(filepath.Join(skill, ".gitignore"))
	if err != nil || string(got) != seeded {
		t.Fatalf("layout-plan changed the skill .gitignore: %q err=%v", got, err)
	}
}
