package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func TestFootprintWarnGBKeyPositionAndValidation(t *testing.T) {
	// The key sits right after pressure_refuse_swap_percent in the scalar
	// list (the frozen listing anchor of the footprint oracle).
	index := -1
	for i, key := range ConfigScalarKeys {
		if key == "pressure_refuse_swap_percent" {
			index = i
		}
	}
	if index < 0 || index+1 >= len(ConfigScalarKeys) || ConfigScalarKeys[index+1] != "footprint_warn_gb" {
		t.Fatalf("footprint_warn_gb does not follow pressure_refuse_swap_percent: %v", ConfigScalarKeys)
	}
	// The numeric case of ConfigValueOk: non-negative decimal integers only.
	for _, value := range []string{"0", "5", "12"} {
		if !ConfigValueOk("footprint_warn_gb", value, platform.Env{}, "") {
			t.Fatalf("value %q must be accepted", value)
		}
	}
	for _, value := range []string{"-1", "1.5", "abc", ""} {
		if ConfigValueOk("footprint_warn_gb", value, platform.Env{}, "") {
			t.Fatalf("value %q must be refused", value)
		}
	}
}

// footprintConfigSetFixture is a project with a skill dir and a project
// config file the `config set` path writes.
func footprintConfigSetFixture(t *testing.T) (*Config, platform.Env, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	skill := filepath.Join(root, "skill")
	repo := filepath.Join(root, "repo")
	for _, dir := range []string{home, skill, filepath.Join(repo, ".agents")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	base := testutil.CleanEnv(t)
	env := platform.Env{}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["HOME"] = home
	env["USERPROFILE"] = home
	env["SOHO_SKILL_DIR"] = skill
	env["HERDR_WORKSPACE_ID"] = "ws"
	ctx := &Config{Entries: map[string]ConfigEntry{}}
	return ctx, env, repo, filepath.Join(repo, ".agents", "soho.conf")
}

func TestConfigSetFootprintWarnGB(t *testing.T) {
	set := func(t *testing.T, ctx *Config, env platform.Env, cwd, value string) int {
		t.Helper()
		code := 0
		func() {
			defer func() {
				if v := recover(); v != nil {
					if e, ok := v.(*platform.ExitError); ok {
						code = e.Code
						return
					}
					panic(v)
				}
			}()
			CmdConfigSet([]string{"footprint_warn_gb", value}, ctx, env, cwd)
		}()
		return code
	}
	ctx, env, cwd, conf := footprintConfigSetFixture(t)

	for _, value := range []string{"0", "5", "12"} {
		if code := set(t, ctx, env, cwd, value); code != 0 {
			t.Fatalf("config set footprint_warn_gb %s exit=%d, want 0", value, code)
		}
		raw, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "footprint_warn_gb="+value) {
			t.Fatalf("the file lacks footprint_warn_gb=%s after set: %s", value, raw)
		}
	}

	before, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"-1", "1.5", "abc"} {
		if code := set(t, ctx, env, cwd, value); code != 2 {
			t.Fatalf("config set footprint_warn_gb %s exit=%d, want 2", value, code)
		}
		after, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("the refused set changed the file: %s", after)
		}
	}
}
