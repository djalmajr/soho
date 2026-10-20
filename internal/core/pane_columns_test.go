package core_test

// DJA-393 L1: the two new scalar keys pane_columns (the declared caller-tab
// column shape) and pane_overflow (what happens past the declared slots).
// Empty pane_columns is the default grid; an invalid value is refused by
// `config set` and read as empty by the layout rules.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestPaneColumnsValueValidation(t *testing.T) {
	t.Run(`pane_columns: the row-count list contract (the empty value is valid)`, func(t *testing.T) {
		// The empty value is valid in the value validation (it is the
		// default grid); `config set` still refuses it through the generic
		// empty-value guard, as for every key.
		valid := []string{"", "auto", "1", "9", "1,2,3", "1,2,2", "1,1,2", "9,9,9,9,9,9"}
		for _, value := range valid {
			if !core.ConfigValueOk("pane_columns", value, platform.Env{}, "") {
				t.Fatalf("pane_columns=%q must be accepted", value)
			}
		}
		invalid := []string{"0", "01", "1,,2", "1, 2", "10", "1,2,3,4,5,6,7", "abc", "1,2,", ",1", "1,", "1.5", " 1", "1,2,3\n"}
		for _, value := range invalid {
			if core.ConfigValueOk("pane_columns", value, platform.Env{}, "") {
				t.Fatalf("pane_columns=%q must be refused", value)
			}
		}
	})
	t.Run(`pane_overflow: the four modes and nothing else`, func(t *testing.T) {
		for _, value := range []string{"balance", "last-column", "new-column", "tab"} {
			if !core.ConfigValueOk("pane_overflow", value, platform.Env{}, "") {
				t.Fatalf("pane_overflow=%q must be accepted", value)
			}
		}
		for _, value := range []string{"", "side", "BALANCE", "balance ", "LAST-COLUMN", "tab ", "last column"} {
			if core.ConfigValueOk("pane_overflow", value, platform.Env{}, "") {
				t.Fatalf("pane_overflow=%q must be refused", value)
			}
		}
	})
	t.Run(`the new keys are known scalar keys, listed after split_min_pane`, func(t *testing.T) {
		if !core.ConfigKeyOk("pane_columns") || !core.ConfigKeyOk("pane_overflow") {
			t.Fatal("the new keys must pass ConfigKeyOk")
		}
		idx := -1
		for i, key := range core.ConfigScalarKeys {
			if key == "split_min_pane" {
				idx = i
			}
		}
		if idx < 0 || idx+2 >= len(core.ConfigScalarKeys) ||
			core.ConfigScalarKeys[idx+1] != "pane_columns" || core.ConfigScalarKeys[idx+2] != "pane_overflow" {
			t.Fatalf("ConfigScalarKeys around split_min_pane=%#v, want pane_columns then pane_overflow right after", core.ConfigScalarKeys[max(0, idx-1):min(len(core.ConfigScalarKeys), idx+3)])
		}
	})
}

func TestPaneColumnsConfigSet(t *testing.T) {
	t.Run(`config set pane_columns: the valid values land in the file, the invalid ones refuse and leave it`, func(t *testing.T) {
		root := t.TempDir()
		skill := t.TempDir()
		if err := os.WriteFile(filepath.Join(skill, "config.defaults"), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"SOHO_SKILL_DIR": skill, "SOHO_DIR": filepath.Join(root, "state")}
		project := filepath.Join(root, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(project), 0o777); err != nil {
			t.Fatal(err)
		}
		const original = "lanes=on\n"
		if err := os.WriteFile(project, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		assertExit := func(wantCode int, wantMsgPrefix string, run func()) {
			t.Helper()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				run()
			}()
			exit, ok := recovered.(*platform.ExitError)
			if !ok || exit.Code != wantCode || !strings.HasPrefix(exit.Msg, wantMsgPrefix) {
				t.Fatalf("error=%#v, want exit %d and a message starting %q", recovered, wantCode, wantMsgPrefix)
			}
		}
		for _, value := range []string{"1,2,3", "1", "9", "auto"} {
			old := platform.Stdout
			var stdout strings.Builder
			platform.Stdout = &stdout
			defer func() { platform.Stdout = old }()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("config set pane_columns %s refused: %v", value, r)
					}
				}()
				core.CmdConfigSet([]string{"pane_columns", value}, nil, env, root)
			}()
			if !strings.Contains(stdout.String(), "set pane_columns="+value) {
				t.Fatalf("config set %s: stdout=%q", value, stdout.String())
			}
			got, err := os.ReadFile(project)
			if err != nil || !strings.Contains(string(got), "pane_columns="+value) {
				t.Fatalf("after set %s: config=%q err=%v", value, got, err)
			}
		}
		for _, value := range []string{"0", "1,,2", "1, 2", "10", "1,2,3,4,5,6,7", "abc"} {
			got, _ := os.ReadFile(project)
			assertExit(2, "config set: invalid value", func() {
				core.CmdConfigSet([]string{"pane_columns", value}, nil, env, root)
			})
			after, _ := os.ReadFile(project)
			if string(got) != string(after) {
				t.Fatalf("refused set %s rewrote the file: %q", value, after)
			}
		}
		// The empty value stays refused by the generic guard, like every key.
		assertExit(2, "config set: empty value", func() {
			core.CmdConfigSet([]string{"pane_columns", ""}, nil, env, root)
		})
		// The empty value in the file is the default grid: it parses, and Cfg
		// falls back to empty (the layout rules read it as no declaration).
		if err := os.WriteFile(project, []byte("pane_columns=\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := core.LoadConfig(env, root)
		if got := core.Cfg(&ctx, "pane_columns", "", env); got != "" {
			t.Fatalf("pane_columns= in the file must resolve to empty, got %q", got)
		}
	})
}

func TestAutoPaneColumnsConfigLayers(t *testing.T) {
	root, home, skill, configHome, state := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	env := platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": configHome, "TMPDIR": t.TempDir(), "PATH": t.TempDir(), "SOHO_SKILL_DIR": skill, "SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws"}
	if err := os.WriteFile(filepath.Join(skill, "config.defaults"), []byte("pane_columns=\npane_column_rows=3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := platform.Stdout
	var output strings.Builder
	platform.Stdout = &output
	t.Cleanup(func() { platform.Stdout = old })
	for _, layer := range []struct{ name, rows, path string }{
		{"user", "1", platform.UserConfigPath(platform.Current(), env)},
		{"project", "2", filepath.Join(root, ".agents", "soho.conf")},
		{"session", "3", filepath.Join(state, "ws", "session.conf")},
	} {
		set := func(key, value string) {
			t.Helper()
			if layer.name == "session" {
				core.CmdSessionSet([]string{key, value}, nil, env, root)
			} else {
				args := []string{key, value}
				if layer.name == "user" {
					args = append(args, "--user")
				}
				core.CmdConfigSet(args, nil, env, root)
			}
		}
		set("pane_columns", "auto")
		set("pane_column_rows", layer.rows)
		ctx := core.LoadConfig(env, root)
		if core.Cfg(&ctx, "pane_columns", "", env) != "auto" || core.Cfg(&ctx, "pane_column_rows", "", env) != layer.rows || core.CfgSource(&ctx, "pane_column_rows", env) != layer.name {
			t.Fatalf("layer=%s config=%+v", layer.name, ctx)
		}
		for _, invalid := range []string{"0", "10", "x", "03", "-1"} {
			before, err := os.ReadFile(layer.path)
			if err != nil {
				t.Fatal(err)
			}
			var recovered any
			func() { defer func() { recovered = recover() }(); set("pane_column_rows", invalid) }()
			msg := "config set: invalid value '" + invalid + "' for pane_column_rows"
			if layer.name == "session" {
				msg = "session set: invalid value '" + invalid + "' for pane_column_rows"
			}
			if exit, ok := recovered.(*platform.ExitError); !ok || exit.Code != 2 || exit.Msg != msg {
				t.Fatalf("layer=%s value=%q got=%#v want=%q", layer.name, invalid, recovered, msg)
			}
			after, err := os.ReadFile(layer.path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("invalid write changed layer=%s file=%q err=%v", layer.name, after, err)
			}
		}
	}
	if !core.ConfigKeyOk("pane_column_rows") {
		t.Fatal("pane_column_rows must be a known scalar key")
	}
	ctx := core.LoadConfig(env, root)
	env["SOHO_PANE_COLUMN_ROWS"] = "9"
	if core.Cfg(&ctx, "pane_column_rows", "", env) != "9" || core.CfgSource(&ctx, "pane_column_rows", env) != "env" {
		t.Fatal("environment must override all file layers")
	}
}
