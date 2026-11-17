package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestSetupReadFlagRouting(t *testing.T) {
	// Mutation captured: routing setup read flags through JS needs an unavailable runtime instead of Go's parser error.
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	env := setupReadFlagEnv(t)
	code := Run([]string{"setup", "--probe", "--invalid"}, env)
	if code != 2 || out.Len() != 0 || errOut.String() != "soho: setup --probe: unknown option '--invalid'\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestSetupDetectValidatesFullArgv(t *testing.T) {
	for _, tc := range []struct {
		name, arg, want string
	}{
		{name: "unknown option", arg: "--bogus", want: "soho: setup: unknown option '--bogus'\n"},
		{name: "missing value", arg: "--panes", want: "soho: setup: --panes expects a value\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, errOut bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &errOut
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			home := t.TempDir()
			env := platform.Env{"PATH": t.TempDir(), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "SOHO_SKILL_DIR": t.TempDir()}
			code := Run([]string{"setup", "--detect", tc.arg}, env)
			if code != 2 || out.Len() != 0 || errOut.String() != tc.want {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
	t.Run("valid detect still emits JSON", func(t *testing.T) {
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		home := t.TempDir()
		env := platform.Env{"PATH": t.TempDir(), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "SOHO_SKILL_DIR": t.TempDir()}
		code := Run([]string{"setup", "--detect"}, env)
		if code != 0 || errOut.Len() != 0 || !strings.HasPrefix(out.String(), "{\n  \"kinds\"") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

func TestSetupWriteRejectsSetBeforeAnyWrites(t *testing.T) {
	// Mutation captured: performing setup writes before rejecting --set leaves files despite the CLI error.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for setup CLI integration test")
	}
	root := t.TempDir()
	cmd := exec.Command(git, "init", "-q", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	skill, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := platform.Env{"PATH": filepath.Dir(git), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "SOHO_SKILL_DIR": skill, "SOHO_DIR": ".herdr-soho"}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run([]string{"setup", "--target", "GUIDE.md", "--no-hooks", "--panes", "3", "--lane", "build=pi", "--set", "max_workers", "5"}, env)
	if code != 2 || out.Len() != 0 || errOut.String() != "soho: setup: --set is a --plan option (it previews a value without writing); to write it, run 'soho config set <key> <value>'\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, file := range []string{"GUIDE.md", ".agents/herdr-soho.conf", ".gitignore", ".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Errorf("write-mode refusal left %s: %v", file, err)
		}
	}
}

func setupReadFlagEnv(t *testing.T) platform.Env {
	t.Helper()
	return isolatedUserEnv(platform.Env{"PATH": t.TempDir(), "HOME": t.TempDir(), "SOHO_SKILL_DIR": t.TempDir()})
}

func TestSetupLaneDecisionSuppressesMigrationWarning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    bool
		lane    bool
		warning bool
	}{
		{name: "user", user: true}, {name: "argument", lane: true}, {name: "undecided", warning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			conf := filepath.Join(cwd, ".agents", "soho.conf")
			roles := "role.implementer.kind=grok\nrole.designer.kind=agy\n"
			tm6Write(t, conf, roles)
			if tc.user {
				tm6Write(t, core.ConfigFileFor("user", env, cwd), "lane.build.kind=codex\n")
			}
			args := []string{"setup", "--target", "AGENTS.md", "--no-hooks", "--panes", "4"}
			if tc.lane {
				args = append(args, "--lane", "build=codex")
			}
			code, out, stderr := runIn(t, args, env, cwd)
			data, err := os.ReadFile(conf)
			if err != nil {
				t.Fatal(err)
			}
			if code != 0 || strings.Contains(stderr, "lane 'build' kinds differ") != tc.warning || !strings.Contains(string(data), "role.implementer.kind=grok") || !strings.Contains(string(data), "role.designer.kind=agy") || (tc.user && strings.Contains(string(data), "lane.build.kind=")) || (tc.lane && !strings.Contains(string(data), "lane.build.kind=codex")) {
				t.Fatalf("code=%d out=%q stderr=%q config=%q", code, out, stderr, data)
			}
		})
	}
}

func TestSetupPlanLaneMigrationMatchesWrite(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	conf := filepath.Join(cwd, ".agents", "soho.conf")
	roles := []string{"implementer", "designer", "tasker"}
	tm6Write(t, conf, "role.implementer.kind=grok\nrole.designer.kind=grok\nrole.tasker.kind=grok\n")
	args := []string{"setup", "--target", "AGENTS.md", "--no-hooks", "--panes", "4", "--lane", "build=codex"}
	code, plan, stderr := runIn(t, append(append([]string{}, args...), "--plan"), env, cwd)
	if code != 0 {
		t.Fatalf("plan code=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runIn(t, args, env, cwd)
	data, err := os.ReadFile(conf)
	if code != 0 || err != nil {
		t.Fatalf("write code=%d stderr=%s read=%v", code, stderr, err)
	}
	for _, role := range roles {
		key := "role." + role + ".kind"
		removed := strings.Contains(plan, key+" grok → (removed)")
		present := strings.Contains(string(data), key+"=grok")
		if removed == present || !present {
			t.Errorf("%s: plan removed=%v write present=%v\nplan=%s\nconfig=%s", key, removed, present, plan, data)
		}
	}
	if !strings.Contains(string(data), "lane.build.kind=codex") || !strings.Contains(plan, "lane.build.kind") {
		t.Fatalf("lane missing: plan=%s config=%s", plan, data)
	}
}

func TestSetupIgnoredLowerLayerLaneModelWarns(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	conf := filepath.Join(cwd, ".agents", "soho.conf")
	tm6Write(t, conf, "lane.build.kind=codex\nrole.implementer.model=legacy-one\nrole.designer.model=legacy-two\n")
	tm6Write(t, core.ConfigFileFor("user", env, cwd), "lane.build.model=user-model\n")
	ctx := core.LoadConfig(env, cwd)
	if got := core.LaneAttr(&ctx, "build", "model", nil, env); got != "" {
		t.Fatalf("effective model=%q, want empty", got)
	}
	code, _, stderr := runIn(t, []string{"setup", "--target", "AGENTS.md", "--no-hooks", "--panes", "4"}, env, cwd)
	if code != 0 || !strings.Contains(stderr, "lane 'build' models differ") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	data, err := os.ReadFile(conf)
	if err != nil || !strings.Contains(string(data), "role.implementer.model=legacy-one") || !strings.Contains(string(data), "role.designer.model=legacy-two") {
		t.Fatalf("config=%s err=%v", data, err)
	}
}
