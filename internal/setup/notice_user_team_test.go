package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// The "no team choice" notice is for a project where neither the project
// file nor the user's file chooses a team: a choice in the user's file is in
// effect in every project that does not override it.
func TestUserLayerChoosesTeam(t *testing.T) {
	layer := func(source string, keys ...string) core.ConfigLayer {
		return core.ConfigLayer{Source: source, OriginalKeys: keys}
	}
	for _, tc := range []struct {
		name string
		ctx  *core.Config
		want bool
	}{
		{"nil config", nil, false},
		{"no layers", &core.Config{}, false},
		{"user lane kind", &core.Config{Layers: []core.ConfigLayer{layer("user", "lane.build.kind")}}, true},
		{"user role kind", &core.Config{Layers: []core.ConfigLayer{layer("user", "role.reviewer.kind")}}, true},
		{"user multi_role", &core.Config{Layers: []core.ConfigLayer{layer("user", "multi_role")}}, true},
		{"user max_workers alone", &core.Config{Layers: []core.ConfigLayer{layer("user", "max_workers", "lane.build.model")}}, false},
		{"session layer does not count", &core.Config{Layers: []core.ConfigLayer{layer("session", "lane.build.kind")}}, false},
		{"defaults layer does not count", &core.Config{Layers: []core.ConfigLayer{layer("defaults", "lane.build.kind")}}, false},
	} {
		if got := userLayerChoosesTeam(tc.ctx); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The same answer through the real loader: a user file read from disk.
func TestUserLayerChoosesTeamFromLoadedUserFile(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"lane.build.kind=pi\nlane.review.kind=codex\n", true},
		{"max_workers=4\n", false},
		{"# lane.build.kind=pi\n", false},
	} {
		home, project := t.TempDir(), t.TempDir()
		env := platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "TMPDIR": t.TempDir(), "SOHO_DIR": filepath.Join(project, "state")}
		file := core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{})
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx := core.LoadConfigFor(env, project, core.UserSelection{})
		if got := userLayerChoosesTeam(&ctx); got != tc.want {
			t.Errorf("user file %q: got %v, want %v", tc.body, got, tc.want)
		}
	}
}
