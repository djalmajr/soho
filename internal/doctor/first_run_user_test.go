package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// TestFirstRunConsidersProjectUserAndRoster runs the real configuration
// loader over files on disk: a team chosen in the project file or in the
// user's own file, or a roster row, ends first run; none of them leaves it.
// The user-file cases cover the keys setup's notice matches (the shared
// function setup.UserLayerChoosesTeam) in every spelling the loader accepts.
func TestFirstRunConsidersProjectUserAndRoster(t *testing.T) {
	skill, err := filepath.Abs(filepath.Join("..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, user, project string
		roster              bool
		want                bool
	}{
		{"neither file nor roster", "", "", false, true},
		{"user file without a team key", "max_workers=4\nlane.build.model=m\n", "", false, true},
		{"user file with the key commented out", "# lane.build.kind=pi\n", "", false, true},
		{"user lane kind, no roster", "lane.build.kind=pi\n", "", false, false},
		{"user lane kind, underscore spelling", "lane_build_kind=pi\n", "", false, false},
		{"user role kind", "role.reviewer.kind=codex\n", "", false, false},
		{"user multi_role, hyphen spelling", "multi-role=off\n", "", false, false},
		{"project lane kind", "", "lane.build.kind=pi\n", false, false},
		{"roster row", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			project := filepath.Join(root, "project")
			state := filepath.Join(root, "state")
			env := platform.Env{
				"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
				"PATH": filepath.Join(root, "bin"), "SOHO_DIR": state, "SOHO_SKILL_DIR": skill,
			}
			write := func(file, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.user != "" {
				write(core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}), tc.user)
			}
			if tc.project != "" {
				write(filepath.Join(project, ".agents", "soho.conf"), tc.project)
			}
			ctx := core.LoadConfig(env, project)
			if tc.roster {
				write(filepath.Join(core.StateRootPath(&ctx, env, project), "ws", "agents.tsv"), "# header\nworker\tpane-1\tcodex\timplementer\n")
			}
			if got := ProjectIsFirstRun(&ctx, env, project); got != tc.want {
				t.Fatalf("ProjectIsFirstRun = %v, want %v", got, tc.want)
			}
			var out strings.Builder
			DoctorCheck(&ctx, env, project, &out)
			wantLine := "first_run: false"
			if tc.want {
				wantLine = "first_run: true"
			}
			if !strings.Contains(out.String(), wantLine+"\n") {
				t.Fatalf("doctor output lacks %q:\n%s", wantLine, out.String())
			}
		})
	}
}
