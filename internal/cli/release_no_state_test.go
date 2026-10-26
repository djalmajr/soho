package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Port of the DJA-363 review probe through Run, with an empty home and
// assertions on every fixture state root, rather than only agents.tsv.
func TestReleaseReopenUnknownLeavesNoState(t *testing.T) {
	for _, command := range []string{"release", "reopen"} {
		for _, explicitRoot := range []bool{false, true} {
			name := command + "/default-root"
			if explicitRoot {
				name = command + "/explicit-root"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				roots := map[string]string{}
				for _, name := range []string{"cwd", "home", "config", "state", "tmp"} {
					roots[name] = filepath.Join(root, name)
					if err := os.Mkdir(roots[name], 0o700); err != nil {
						t.Fatal(err)
					}
				}
				bin := t.TempDir()
				if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
					{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"workspace_id":"unrelated"}}}`},
				}); err != nil {
					t.Fatal(err)
				}
				env := withFakeCLI(platform.Env{
					"HOME": roots["home"], "USERPROFILE": roots["home"], "XDG_CONFIG_HOME": roots["config"],
					"TMPDIR": roots["tmp"], "PATH": bin, "HERDR_ENV": "1", "SOHO_SKILL_DIR": testSkillDir(t),
				}, bin)
				stateRoot := filepath.Join(roots["cwd"], ".soho")
				if explicitRoot {
					env["SOHO_STATE_DIR"] = roots["state"]
					env["HERDR_WORKSPACE_ID"] = "unrelated"
					stateRoot = roots["state"]
				}
				for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
					t.Setenv(key, env[key])
				}
				t.Chdir(roots["cwd"])
				oldOut, oldErr := platform.Stdout, platform.Stderr
				var out, stderr bytes.Buffer
				platform.Stdout, platform.Stderr = &out, &stderr
				t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
				code := Run([]string{command, "unknown"}, env)
				want := "soho: agent 'unknown' is not in the roster"
				if command == "release" {
					want += " (state dir: " + filepath.Join(stateRoot, "unrelated") + ")"
				}
				want += "\n"
				if code != 3 || out.String() != "" || stderr.String() != want {
					t.Fatalf("%s: code=%d stdout=%q stderr=%q; want exit 3, empty stdout, stderr=%q; env=%v cwd=%s", command, code, out.String(), stderr.String(), want, env, roots["cwd"])
				}
				for name, dir := range roots {
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Errorf("%s created entries under %s (%s): %v; err=%v", command, name, dir, entries, err)
					}
				}
			})
		}
	}
}
