package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func tm9CommandFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	env, cwd := commandFixture(t)
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}}); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SOHO_DIR", "SOHO_SKILL_DIR", "HERDR_WORKSPACE_ID"} {
		clean[key] = env.Get(key)
	}
	clean["USERPROFILE"] = env.Get("HOME")
	clean["PATH"] = bin
	clean["HERDR_ENV"] = "1"
	clean["HERDR_SOCKET_PATH"] = filepath.Join(t.TempDir(), "missing.sock")
	return clean, cwd
}

func TestTM9ConfigAndSessionCommands(t *testing.T) {
	t.Run("config set writes dotted role/lane/model keys", func(t *testing.T) { // JS: "config set writes dotted role/lane/model keys"
		env, cwd := tm9CommandFixture(t)
		for _, pair := range [][2]string{{"role.reviewer.kind", "grok"}, {"lane.build.kind", "opencode"}, {"model.opencode.worker", "vendor/model"}} {
			code, _, stderr := runIn(t, []string{"config", "set", pair[0], pair[1]}, env, cwd)
			if code != 0 || stderr != "" {
				t.Fatalf("set %s code=%d stderr=%q", pair[0], code, stderr)
			}
		}
		data, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"role.reviewer.kind=grok", "lane.build.kind=opencode", "model.opencode.worker=vendor/model"} {
			if !strings.Contains(string(data), want+"\n") {
				t.Errorf("file %q lacks %q", data, want)
			}
		}
	})
	t.Run("config set writes the role/lane/args keys with dash-led values", func(t *testing.T) { // JS: "config set writes the role/lane/args keys with dash-led values"
		env, cwd := tm9CommandFixture(t)
		code, _, stderr := runIn(t, []string{"config", "set", "role.reviewer.args", "-c", "a=b"}, env, cwd)
		data, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if code != 0 || stderr != "" || err != nil || string(data) != "role.reviewer.args=-c a=b\n" {
			t.Fatalf("code=%d stderr=%q data=%q err=%v", code, stderr, data, err)
		}
	})
	t.Run("unknown key / invalid value / empty value refuse with rc 2 and leave the file", func(t *testing.T) { // JS: "unknown key / invalid value / empty value refuse with rc 2 and leave the file"
		env, cwd := tm9CommandFixture(t)
		path := filepath.Join(cwd, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# keep\nmax_workers=3\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		for _, args := range [][]string{{"config", "set", "unknown", "x"}, {"config", "set", "max_workers", "-1"}, {"config", "set", "layout", ""}} {
			code, out, _ := runIn(t, args, env, cwd)
			if code != 2 || out != "" {
				t.Errorf("args=%v code=%d out=%q", args, code, out)
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
				t.Fatalf("invalid command changed config: %q err=%v", after, err)
			}
		}
	})
	t.Run("config set --user writes the user file, not the project file", func(t *testing.T) { // JS: "config set --user writes the user file, not the project file"
		env, cwd := tm9CommandFixture(t)
		project := filepath.Join(cwd, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(project, []byte("reuse_workers=on\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runIn(t, []string{"config", "set", "reuse_workers", "off", "--user"}, env, cwd)
		user := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
		gotUser, userErr := os.ReadFile(user)
		gotProject, projectErr := os.ReadFile(project)
		if code != 0 || stderr != "" || userErr != nil || projectErr != nil || string(gotUser) != "reuse_workers=off\n" || string(gotProject) != "reuse_workers=on\n" {
			t.Fatalf("code=%d stderr=%q user=%q project=%q errors=%v/%v", code, stderr, gotUser, gotProject, userErr, projectErr)
		}
	})
	t.Run("values reach the file verbatim: no escape processing, no key injection", func(t *testing.T) { // JS: "values reach the file verbatim: no escape processing, no key injection"
		env, cwd := tm9CommandFixture(t)
		value := `ok\nrole.reviewer.kind=grok`
		code, _, stderr := runIn(t, []string{"config", "set", "herd_label", value}, env, cwd)
		data, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if code != 0 || stderr != "" || err != nil || string(data) != "herd_label="+value+"\n" {
			t.Fatalf("code=%d stderr=%q data=%q err=%v", code, stderr, data, err)
		}
	})
	t.Run("config set keeps the file mode and creates new files 0600", func(t *testing.T) { // JS: "config set keeps the file mode and creates new files 0600"
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose POSIX file modes")
		}
		env, cwd := tm9CommandFixture(t)
		code, _, stderr := runIn(t, []string{"config", "set", "max_workers", "5"}, env, cwd)
		path := filepath.Join(cwd, ".agents", "soho.conf")
		info, err := os.Stat(path)
		if code != 0 || stderr != "" || err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("code=%d stderr=%q mode=%v err=%v", code, stderr, info, err)
		}
	})
	t.Run("validation: unknown key / bad value refuse and leave the file", func(t *testing.T) { // JS: "validation: unknown key / bad value refuse and leave the file"
		env, cwd := tm9CommandFixture(t)
		code, _, _ := runIn(t, []string{"session", "set", "lane.build.kind", "pi"}, env, cwd)
		if code != 0 {
			t.Fatalf("seed session code=%d", code)
		}
		path := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"session", "set", "unknown", "x"}, {"session", "set", "max_workers", "-1"}} {
			code, _, _ := runIn(t, args, env, cwd)
			if code != 2 {
				t.Errorf("args=%v exit=%d", args, code)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("session changed after %v: %q err=%v", args, after, err)
			}
		}
	})
	t.Run("unknown subcommand and bad usage refuse with rc 2", func(t *testing.T) { // JS: "unknown subcommand and bad usage refuse with rc 2"
		env, cwd := tm9CommandFixture(t)
		for _, args := range [][]string{{"session", "unknown"}, {"session", "set"}, {"session", "set", "key", "value", "extra"}, {"session", "clear", "a", "b"}} {
			code, out, _ := runIn(t, args, env, cwd)
			if code != 2 || out != "" {
				t.Errorf("args=%v code=%d out=%q", args, code, out)
			}
		}
	})
	t.Run("session set and config set take one key=value argument", func(t *testing.T) { // JS: "session set and config set take one key=value argument"
		env, cwd := tm9CommandFixture(t)
		for _, args := range [][]string{{"session", "set", "lanes=off"}, {"config", "set", "layout=columns"}} {
			code, _, stderr := runIn(t, args, env, cwd)
			if code != 0 || stderr != "" {
				t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr)
			}
		}
		for _, path := range []string{filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf"), filepath.Join(cwd, ".agents", "soho.conf")} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("expected file %s: %v", path, err)
			}
		}
	})
	t.Run("session set with \"key value\" in one argument names the shell mistake", func(t *testing.T) { // JS: "session set with \"key value\" in one argument names the shell mistake"
		env, cwd := tm9CommandFixture(t)
		code, _, stderr := runIn(t, []string{"session", "set", "lanes off"}, env, cwd)
		if code != 2 || !strings.Contains(stderr, "arrived as one argument; pass the key and the value as two arguments or as key=value") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")); !os.IsNotExist(err) {
			t.Fatalf("unsplit input wrote session file: %v", err)
		}
	})
	t.Run("session set writes the args keys with dash-led values", func(t *testing.T) { // JS: "session set writes the args keys with dash-led values"
		env, cwd := tm9CommandFixture(t)
		code, _, stderr := runIn(t, []string{"session", "set", "role.reviewer.args", "-c", "a=b"}, env, cwd)
		data, err := os.ReadFile(filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf"))
		if code != 0 || stderr != "" || err != nil || string(data) != "role.reviewer.args=-c a=b\n" {
			t.Fatalf("code=%d stderr=%q data=%q err=%v", code, stderr, data, err)
		}
	})
	t.Run("config set key=value honors --user and --project", func(t *testing.T) { // JS: "config set key=value honors --user and --project"
		env, cwd := tm9CommandFixture(t)
		for _, args := range [][]string{{"config", "set", "layout=columns", "--user"}, {"config", "set", "--project", "layout=split"}} {
			code, _, stderr := runIn(t, args, env, cwd)
			if code != 0 || stderr != "" {
				t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr)
			}
		}
		user, err := os.ReadFile(filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"))
		if err != nil || string(user) != "layout=columns\n" {
			t.Fatalf("user=%q err=%v", user, err)
		}
		project, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if err != nil || string(project) != "layout=split\n" {
			t.Fatalf("project=%q err=%v", project, err)
		}
	})
	t.Run("roles command: table lists project roles first with the project file as source", func(t *testing.T) { // JS: "roles command: table lists project roles first with the project file as source"
		env, cwd := tm9CommandFixture(t)
		roleFile := filepath.Join(cwd, ".agents", "herdr-roles", "implementer.md")
		if err := os.MkdirAll(filepath.Dir(roleFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(roleFile, []byte("---\nname: implementer\nkind: pi\nmode: edit\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runIn(t, []string{"roles"}, env, cwd)
		if code != 0 || stderr != "" || !strings.Contains(out, "implementer") || !strings.Contains(out, roleFile) {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("role command: JSON shape and values, pretty-printed like jq", func(t *testing.T) { // JS: "role command: JSON shape and values, pretty-printed like jq"
		env, cwd := tm9CommandFixture(t)
		code, out, stderr := runIn(t, []string{"role", "implementer"}, env, cwd)
		var role map[string]any
		if err := json.Unmarshal([]byte(out), &role); err != nil {
			t.Fatalf("role JSON=%q err=%v", out, err)
		}
		if code != 0 || stderr != "" || role["name"] != "implementer" || role["kind"] == "" || !strings.HasPrefix(out, "{\n  \"file\"") {
			t.Fatalf("code=%d stderr=%q role=%v", code, stderr, role)
		}
	})
	t.Run("role command: skill role resolves the skill file and its frontmatter", func(t *testing.T) { // JS: "role command: skill role resolves the skill file and its frontmatter"
		env, cwd := tm9CommandFixture(t)
		code, out, stderr := runIn(t, []string{"role", "implementer"}, env, cwd)
		var role map[string]any
		if err := json.Unmarshal([]byte(out), &role); err != nil {
			t.Fatalf("role JSON=%q err=%v", out, err)
		}
		if code != 0 || stderr != "" || role["name"] != "implementer" || !strings.HasSuffix(role["file"].(string), filepath.Join("roles", "implementer.md")) {
			t.Fatalf("code=%d stderr=%q role=%v", code, stderr, role)
		}
	})
}

func TestTM9StatusUsesExplicitWorkingDirectory(t *testing.T) { // JS: "status resolves state under its explicit cwd"
	env, requested := tm9CommandFixture(t)
	caller := t.TempDir()
	for _, dir := range []string{requested, caller} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env["SOHO_DIR"] = ".status-state"
	state := filepath.Join(requested, ".status-state", "ws")
	if err := os.MkdirAll(filepath.Join(state, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(state, "reports", "build.md")
	marker := filepath.Join(state, "last-report-build")
	if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(marker, time.Unix(1000, 0), time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(report, time.Unix(1042, 0), time.Unix(1042, 0)); err != nil {
		t.Fatal(err)
	}
	// The command is run from a different directory; its state must follow its own cwd.
	code, out, stderr := runIn(t, []string{"status", "build"}, env, requested)
	if code != 0 || stderr != "" || out != "build\tdone\t"+report+"\t42\t-\n" {
		t.Fatalf("code=%d stderr=%q output=%q", code, stderr, out)
	}
	if _, err := os.Stat(filepath.Join(caller, ".status-state", "ws", "last-report-build")); !os.IsNotExist(err) {
		t.Fatalf("caller directory unexpectedly supplied status: %v", err)
	}
}

func TestTM9ParentTitleSubtests(t *testing.T) {
	cases := []struct{ title, test string }{
		{"config shows defaults for unset keys (multi_role on defaults)", "TestConfigCommandRoutesToGo"},
		{"session set writes the session file in the state dir", "TestSessionCommandRoutesToGoAndLegacyEnvIsApplied"},
		{"config shows the session value with source `session`", "TestSessionCommandRoutesToGoAndLegacyEnvIsApplied"},
		{"status resolves state under its explicit cwd", "TestTM9StatusUsesExplicitWorkingDirectory"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.title, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run", "^"+tc.test+"$")
			// The child's TestMain takes its cwd as the package directory:
			// start it there, not in this suite's isolated cwd.
			cmd.Dir = packageDir
			// The child re-runs the race-instrumented test binary and would
			// otherwise pause for one second on exit; set only GORACE.
			childEnv := os.Environ()
			replaced := false
			for i, entry := range childEnv {
				if strings.HasPrefix(entry, "GORACE=") {
					childEnv[i] = "GORACE=" + fakecli.RaceOptions(childEnv)
					replaced = true
				}
			}
			if !replaced {
				childEnv = append(childEnv, "GORACE="+fakecli.RaceOptions(childEnv))
			}
			cmd.Env = childEnv
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("existing Go mirror %s failed: %v\n%s", tc.test, err, output)
			}
		})
	}
}
