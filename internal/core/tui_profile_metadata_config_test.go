package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestOpenCodeTUIMetadataConfigSelection(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "ambient"), "PATH": filepath.Join(root, "empty-bin"), "HERDR_WORKSPACE_ID": "fixture-session", "SOHO_DIR": filepath.Join(root, "state")}
	sel := UserSelection{Base: filepath.Join(root, "selected"), Home: filepath.Join(root, "selected-home")}
	write := func(file, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(UserConfigFiles(platform.Current(), env, UserSelection{}), "opencode_tui_profile=legacy\ntransport=herdr\n")
	write(UserConfigFiles(platform.Current(), env, sel), "opencode_tui_profile=v2\ntransport=hc\n")
	before := env.Clone()
	ctx := LoadMetadataConfigFor(env, root, sel)
	if got, err := OpenCodeTUIProfile(&ctx, env); got != "v2" || err != nil || ctx.User != sel {
		t.Fatalf("selected: %q %v user=%v", got, err, ctx.User)
	}
	if TransportMode(&ctx, env) != "hc" {
		t.Fatal("selected transport lost")
	}
	if len(ctx.Layers) != 2 || ctx.Layers[0].Source != "user" || ctx.Layers[1].Source != "session" {
		t.Fatalf("layers=%v", ctx.Layers)
	}
	if TransportModeAt(env, root) != "herdr" {
		t.Fatal("zero selection behavior changed")
	}
	write(filepath.Join(root, ".agents", "soho.conf"), "opencode_tui_profile=legacy\ntransport=herdr\n")
	ctx = LoadMetadataConfigFor(env, root, sel)
	if got, _ := OpenCodeTUIProfile(&ctx, env); got != "legacy" {
		t.Fatal("project lost precedence")
	}
	write(filepath.Join(root, "state", "fixture-session", "session.conf"), "opencode_tui_profile=v2\ntransport=hc\n")
	ctx = LoadMetadataConfigFor(env, root, sel)
	if got, _ := OpenCodeTUIProfile(&ctx, env); got != "v2" || TransportMode(&ctx, env) != "hc" || TransportModeAt(env, root) != "hc" {
		t.Fatal("session lost precedence")
	}
	env["SOHO_OPENCODE_TUI_PROFILE"] = "legacy"
	if got, _ := OpenCodeTUIProfile(&ctx, env); got != "legacy" {
		t.Fatal("environment lost precedence")
	}
	delete(env, "SOHO_OPENCODE_TUI_PROFILE")
	if !reflect.DeepEqual(env, before) {
		t.Fatal("loader changed environment")
	}
	for _, layer := range ctx.Layers {
		if layer.Source == "defaults" {
			t.Fatal("metadata materialized defaults")
		}
	}
}
