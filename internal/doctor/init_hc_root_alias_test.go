package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestInitHcCallerRootVariants(t *testing.T) {
	for _, variant := range []string{"symlink", "subdirectory", "foreign", "unnamed"} {
		t.Run(variant, func(t *testing.T) {
			env, root := initHcTestFixture(t, func(root string) []fakecli.Rule {
				callerCwd := root
				switch variant {
				case "symlink":
					callerCwd = filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(root, callerCwd); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				case "subdirectory":
					callerCwd = filepath.Join(root, "nested")
					if err := os.Mkdir(callerCwd, 0700); err != nil {
						t.Fatal(err)
					}
				case "foreign":
					callerCwd = t.TempDir()
				}
				return []fakecli.Rule{
					{Argv: []string{"get", "t2c", "--json"}, Stdout: nativeCallerGetJSON(callerCwd)},
					{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(callerCwd)},
				}
			})
			cwd := root
			if variant == "subdirectory" {
				cwd = filepath.Join(root, "nested")
			}
			out, stderr, recovered := runCmdInitCapture(t, env, cwd)
			if variant == "foreign" {
				exit, ok := recovered.(*platform.ExitError)
				if !ok || exit.Code != 2 || exit.Msg != "init: the caller terminal's root does not match the project root" {
					t.Fatalf("recovered=%#v stderr=%q", recovered, stderr)
				}
				assertNoStateWrite(t, env, root, "hc-t2c")
				return
			}
			if recovered != nil || stderr != "" {
				t.Fatalf("recovered=%#v stderr=%q stdout=%q", recovered, stderr, out)
			}
			if !strings.Contains(out, `"orchestrator": "t2c"`) || !strings.Contains(out, `"pane_id": "t2c"`) {
				t.Fatalf("stdout=%q", out)
			}
			store := sessionmeta.Store{StateBase: core.StateRootPathForProject(&core.Config{}, env, root), SessionID: "hc-t2c"}
			rec, ok, err := store.Read()
			if err != nil || !ok || rec.Terminal != "t2c" || rec.ProjectRoot != root || rec.Name != "t2c" || rec.Kind != "codex" {
				t.Fatalf("record=%+v ok=%v err=%v", rec, ok, err)
			}
		})
	}
}
