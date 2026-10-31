package inventory

import (
	"errors"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// layer builds one recorded config layer. Its File name points at a path
// that does not exist, so any test that passes also proves the resolver
// reads the loaded entries, not the file system.
func layer(source string, entries ...[2]string) core.ConfigLayer {
	m := make(map[string]core.ConfigEntry, len(entries))
	for _, e := range entries {
		m[e[0]] = core.ConfigEntry{Value: e[1]}
	}
	return core.ConfigLayer{Source: source, File: "/nonexistent/inventory/" + source + ".conf", Entries: m}
}

func layerErr(source string, err error) core.ConfigLayer {
	return core.ConfigLayer{Source: source, File: "/nonexistent/inventory/" + source + ".conf", ReadError: err, Entries: map[string]core.ConfigEntry{}}
}

func cfg(layers ...core.ConfigLayer) *core.Config {
	return &core.Config{Layers: layers}
}

func TestResolveModeCombinations(t *testing.T) {
	readErr := errors.New("configuration file cannot be read")
	cases := []struct {
		name string
		cfg  *core.Config
		env  platform.Env
		want Mode
	}{
		{"nil config closes", nil, nil, ModeOff},
		{"empty config closes", cfg(), nil, ModeOff},
		{"defaults never opt in", cfg(layer("defaults", [2]string{"inventory", "show"})), nil, ModeOff},
		{"defaults never lower", cfg(layer("project", [2]string{"inventory", "show"}), layer("defaults", [2]string{"inventory", "hidden"})), nil, ModeShow},
		{"no project layer: user and session cannot open", cfg(layer("user", [2]string{"inventory", "show"}), layer("session", [2]string{"inventory", "show"})), nil, ModeOff},
		{"project show alone", cfg(layer("project", [2]string{"inventory", "show"})), nil, ModeShow},
		{"project hidden alone", cfg(layer("project", [2]string{"inventory", "hidden"})), nil, ModeHidden},
		{"project off alone", cfg(layer("project", [2]string{"inventory", "off"})), nil, ModeOff},
		{"project with no inventory key closes", cfg(layer("project", [2]string{"state_dir", ".soho"})), nil, ModeOff},
		{"invalid project value closes", cfg(layer("project", [2]string{"inventory", "loud"})), nil, ModeOff},
		{"empty project value closes", cfg(layer("project", [2]string{"inventory", ""})), nil, ModeOff},
		{"project read error closes", cfg(layerErr("project", readErr), layer("user", [2]string{"inventory", "show"}), layer("session", [2]string{"inventory", "show"})), nil, ModeOff},
		{"later project error stays closed", cfg(layer("project", [2]string{"inventory", "show"}), layerErr("project", readErr)), nil, ModeOff},
		{"later project invalid value stays closed", cfg(layer("project", [2]string{"inventory", "show"}), layer("project", [2]string{"inventory", "loud"})), nil, ModeOff},
		{"later project value replaces the earlier one", cfg(layer("project", [2]string{"inventory", "hidden"}), layer("project", [2]string{"inventory", "show"})), nil, ModeShow},
		{"project show lowered by env hidden", cfg(layer("project", [2]string{"inventory", "show"})), platform.Env{"SOHO_INVENTORY": "hidden"}, ModeHidden},
		{"project show closed by env off", cfg(layer("project", [2]string{"inventory", "show"})), platform.Env{"SOHO_INVENTORY": "off"}, ModeOff},
		{"project hidden closed by env off", cfg(layer("project", [2]string{"inventory", "hidden"})), platform.Env{"SOHO_INVENTORY": "off"}, ModeOff},
		{"show cannot raise hidden", cfg(layer("project", [2]string{"inventory", "hidden"})), platform.Env{"SOHO_INVENTORY": "show"}, ModeHidden},
		{"show cannot raise off", cfg(layer("project", [2]string{"inventory", "off"})), platform.Env{"SOHO_INVENTORY": "show"}, ModeOff},
		{"env show alone does nothing", cfg(), platform.Env{"SOHO_INVENTORY": "show"}, ModeOff},
		{"missing env entry does nothing", cfg(layer("project", [2]string{"inventory", "show"})), platform.Env{"SOHO_MAX_WORKERS": "3"}, ModeShow},
		{"invalid env value closes", cfg(layer("project", [2]string{"inventory", "show"})), platform.Env{"SOHO_INVENTORY": "loud"}, ModeOff},
		{"empty env value closes", cfg(layer("project", [2]string{"inventory", "show"})), platform.Env{"SOHO_INVENTORY": ""}, ModeOff},
		{"user hidden lowers show", cfg(layer("project", [2]string{"inventory", "show"}), layer("user", [2]string{"inventory", "hidden"}), layer("session", [2]string{"inventory", "show"})), nil, ModeHidden},
		{"user show alone cannot raise", cfg(layer("project", [2]string{"inventory", "hidden"}), layer("user", [2]string{"inventory", "show"})), nil, ModeHidden},
		{"off wins over show at any restrictive layer", cfg(layer("project", [2]string{"inventory", "show"}), layer("session", [2]string{"inventory", "off"})), platform.Env{"SOHO_INVENTORY": "show"}, ModeOff},
		{"user off wins over session hidden", cfg(layer("project", [2]string{"inventory", "show"}), layer("user", [2]string{"inventory", "off"}), layer("session", [2]string{"inventory", "hidden"})), nil, ModeOff},
		{"invalid user value closes", cfg(layer("project", [2]string{"inventory", "show"}), layer("user", [2]string{"inventory", "loud"})), nil, ModeOff},
		{"user read error closes", cfg(layer("project", [2]string{"inventory", "show"}), layerErr("user", readErr)), nil, ModeOff},
		{"session read error closes", cfg(layer("project", [2]string{"inventory", "show"}), layer("session", [2]string{"inventory", "show"}), layerErr("session", readErr)), nil, ModeOff},
		{"unrelated keys and layers are ignored", cfg(layer("defaults", [2]string{"worker_messages", "1"}), layer("project", [2]string{"inventory", "show"}), layer("user", [2]string{"state_dir", ".soho"})), platform.Env{"SOHO_MAX_WORKERS": "3", "HERDR_PANE_ID": "p1"}, ModeShow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveMode(tc.cfg, tc.env); got != tc.want {
				t.Fatalf("ResolveMode = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveModeIgnoresTheProcessEnvironment sets the variable in the
// test process's own environment and asserts the passed env map alone
// decides: a resolver that fell back to the global environment would
// close the project here.
func TestResolveModeIgnoresTheProcessEnvironment(t *testing.T) {
	t.Setenv("SOHO_INVENTORY", "off")
	cfg := cfg(layer("project", [2]string{"inventory", "show"}))
	if got := ResolveMode(cfg, nil); got != ModeShow {
		t.Fatalf("ResolveMode = %q, want show: the process environment leaked into the resolution", got)
	}
}

// TestResolveModePerformsNoReadsBeyondTheLayers names the no-I/O claim:
// the project layer's file does not exist on disk, yet the loaded
// entries decide, so the resolver reads neither the file system nor a
// state directory.
func TestResolveModePerformsNoReadsBeyondTheLayers(t *testing.T) {
	cfg := cfg(layer("project", [2]string{"inventory", "show"}))
	if got := ResolveMode(cfg, platform.Env{"SOHO_INVENTORY": "hidden"}); got != ModeHidden {
		t.Fatalf("ResolveMode = %q, want hidden from the loaded layers alone", got)
	}
}
