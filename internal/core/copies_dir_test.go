package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestConfigCopiesDirValidation(t *testing.T) {
	env := platform.Env{}
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", true}, {filepath.Join(t.TempDir(), "copies"), true}, {"~/copies", true}, {"relative/path", false}, {"~other/path", false}, {"../copies", false}} {
		if got := ConfigValueOk("copies_dir", tc.value, env, ""); got != tc.want {
			t.Errorf("copies_dir=%q got %t want %t", tc.value, got, tc.want)
		}
	}
	if !ConfigKeyOk("copies_dir") {
		t.Fatal("copies_dir must be known")
	}
}

func TestConfigCopiesDirLayers(t *testing.T) {
	root, home, skill, configHome, state := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	env := platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": configHome, "TMPDIR": t.TempDir(), "PATH": t.TempDir(), "SOHO_SKILL_DIR": skill, "SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws"}
	old := platform.Stdout
	var output strings.Builder
	platform.Stdout = &output
	t.Cleanup(func() { platform.Stdout = old })
	for _, layer := range []struct{ name, path string }{
		{"user", platform.UserConfigPath(platform.Current(), env)},
		{"project", filepath.Join(root, ".agents", "soho.conf")},
		{"session", filepath.Join(state, "ws", "session.conf")},
	} {
		value := filepath.Join(root, layer.name)
		if err := os.MkdirAll(filepath.Dir(layer.path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(layer.path, []byte("copies_dir="+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx := LoadConfig(env, root)
		if got := Cfg(&ctx, "copies_dir", "", env); got != value || CfgSource(&ctx, "copies_dir", env) != layer.name {
			t.Fatalf("layer %s got %q source %q", layer.name, got, CfgSource(&ctx, "copies_dir", env))
		}
	}
	ctx := LoadConfig(env, root)
	env["SOHO_COPIES_DIR"] = filepath.Join(root, "env")
	if got := Cfg(&ctx, "copies_dir", "", env); got != env["SOHO_COPIES_DIR"] || CfgSource(&ctx, "copies_dir", env) != "env" {
		t.Fatalf("env got %q source %q", got, CfgSource(&ctx, "copies_dir", env))
	}
	CmdConfig(&ctx, env, root)
	if !strings.Contains(output.String(), "copies_dir") || !strings.Contains(output.String(), env["SOHO_COPIES_DIR"]) {
		t.Fatalf("config listing %q", output.String())
	}
}
