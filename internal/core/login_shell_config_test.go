package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// login_shell is an on/off key like multi_role, reuse_workers, lanes and
// trust_cwd: known to config, valid in every layer, default off.
func TestLoginShellKeyAcceptsOnOffOnly(t *testing.T) {
	if !ConfigKeyOk("login_shell") {
		t.Fatal("login_shell is not a known configuration key")
	}
	for _, value := range []string{"on", "off"} {
		if !ConfigValueOk("login_shell", value, platform.Env{}, "") {
			t.Fatalf("login_shell=%q must be accepted", value)
		}
	}
	for _, value := range []string{"", "ON", "Off", "yes", "true", "1", "auto", "zsh", "on ", "on#x"} {
		if ConfigValueOk("login_shell", value, platform.Env{}, "") {
			t.Fatalf("login_shell=%q must be refused", value)
		}
	}
}

func TestLoginShellDefaultIsOffInTheBundledDefaults(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "config.defaults"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "login_shell=") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 || lines[0] != "login_shell=off" {
		t.Fatalf("config.defaults login_shell lines=%q, want exactly login_shell=off", lines)
	}
	found := false
	for _, key := range ConfigScalarKeys {
		found = found || key == "login_shell"
	}
	if !found {
		t.Fatalf("login_shell is missing from ConfigScalarKeys (soho config would not list it): %v", ConfigScalarKeys)
	}
}

func TestConfigSetLoginShell(t *testing.T) {
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
			CmdConfigSet([]string{"login_shell", value}, ctx, env, cwd)
		}()
		return code
	}
	ctx, env, cwd, conf := footprintConfigSetFixture(t)

	for _, value := range []string{"on", "off"} {
		if code := set(t, ctx, env, cwd, value); code != 0 {
			t.Fatalf("config set login_shell %s exit=%d, want 0", value, code)
		}
		raw, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "login_shell="+value) {
			t.Fatalf("the file lacks login_shell=%s after set: %s", value, raw)
		}
	}

	before, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"yes", "ON", "true", "zsh"} {
		if code := set(t, ctx, env, cwd, value); code != 2 {
			t.Fatalf("config set login_shell %s exit=%d, want 2", value, code)
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
