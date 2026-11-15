package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// The "no team choice" notice is for a project where neither the project
// file nor the user's own file chooses a team: a choice in the user's file
// is in effect in every project that does not override it. These cases run
// the public setup command, canonical and --local, over files on disk read
// by the real loader, and look at the warning itself.
func TestSetupTeamNoticeConsidersProjectAndUserFiles(t *testing.T) {
	const notice = "sets neither multi_role"
	for _, tc := range []struct {
		name, user, project string
		warn                bool
	}{
		{"neither file", "", "", true},
		{"user file without a team key", "max_workers=4\nlane.build.model=m\n", "", true},
		{"user file with the key commented out", "# lane.build.kind=pi\n", "", true},
		{"user lane kind", "lane.build.kind=pi\n", "", false},
		{"user lane kind, underscore spelling", "lane_build_kind=pi\n", "", false},
		{"user role kind", "role.reviewer.kind=codex\n", "", false},
		{"user multi_role, hyphen spelling", "multi-role=off\n", "", false},
		{"project lane kind", "", "lane.build.kind=pi\n", false},
		{"both files", "lane.build.kind=pi\n", "lane.review.kind=codex\n", false},
	} {
		for _, args := range [][]string{nil, {"--local"}} {
			root, env, _ := setupWriteFixture(t)
			write := func(file, body string) {
				if body == "" {
					return
				}
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write(core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}), tc.user)
			write(filepath.Join(root, ".agents", "soho.conf"), tc.project)
			ctx := core.LoadConfig(env, root)
			code, _, stderr := runSetupWrite(args, &ctx, env, root)
			if code != 0 {
				t.Fatalf("%s %v: code=%d %s", tc.name, args, code, stderr)
			}
			if got := strings.Contains(stderr, notice); got != tc.warn {
				t.Errorf("%s %v: notice printed=%v, want %v\n%s", tc.name, args, got, tc.warn, stderr)
			}
		}
	}
}
