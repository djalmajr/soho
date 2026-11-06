package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorSharedCodex(t *testing.T) {
	const id = "66bf36b5-9b48-4b58-88a3-5da128ff288b"
	for _, mode := range []string{"refuse", "warn", "off"} {
		t.Run(mode, func(t *testing.T) {
			herdr.ResetCallerCheck()
			t.Cleanup(herdr.ResetCallerCheck)
			shell, _ := testutil.CallerShellPIDs()
			rules := []fakecli.Rule{
				{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
				{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
				{Argv: []string{"pane", "get", "w0test:p0a"}, Stdout: `{"result":{"pane":{"title":"orchestrator","workspace_id":"ws"}}}`},
				{Argv: []string{"pane", "process-info", "--pane", "w0test:p0a"}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)},
				{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"w2:p8","agent_session":{"agent":"codex","kind":"id","value":"` + id + `"}}]}}`},
			}
			env, root := paneContextFixture(t, rules)
			env["HERDR_PANE_ID"], env["HERDR_WORKSPACE_ID"], env["CODEX_THREAD_ID"] = "w0test:p0a", "ws", id
			env["SOHO_CALLER_CHECK"] = mode
			env["XDG_CONFIG_HOME"] = env["HOME"]
			env["TMPDIR"] = t.TempDir()
			t.Cleanup(platform.OverrideAncestorParents(map[int]int{os.Getpid(): shell, shell: 1}))
			reads := 0
			t.Cleanup(platform.OverrideProcExecutableRunner(func(pid int) (string, error) {
				reads++
				if pid == os.Getpid() || pid == shell {
					return filepath.Join(root, "codex"), nil
				}
				return "", errors.New("synthetic missing executable")
			}))
			out := doctorCheckOutput(t, env, root)
			want := "warn   Herdr context invalid: HERDR_PANE_ID='w0test:p0a' cannot identify this Codex executor: its complete ancestry contains at least two Codex executables, so it may carry another pane's identity. This Codex session is shown in pane 'w2:p8'. Resume this agent in the intended pane with 'codex resume --no-daemon " + id + "'. \n"
			if mode == "off" {
				if strings.Contains(out, "cannot identify this Codex executor") || reads != 0 {
					t.Fatalf("off: reads=%d output=%s", reads, out)
				}
			} else if strings.Count(out, want) != 1 || strings.Contains(out, "does not reach shell pid") {
				t.Fatalf("dedicated warning missing or false: %s", out)
			}
		})
	}
}
