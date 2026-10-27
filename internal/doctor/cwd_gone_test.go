package doctor

import (
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorWorkerCwdGone(t *testing.T) {
	for _, name := range []string{"removed", "present", "file", "permission", "dead"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			sd := filepath.Join(root, "state", "ws")
			for _, dir := range []string{bin, sd} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cwd := filepath.Join(root, "worker tree ; literal")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			if name == "removed" || name == "file" || name == "dead" {
				if err := os.Remove(cwd); err != nil {
					t.Fatal(err)
				}
			}
			if name == "file" {
				if err := os.WriteFile(cwd, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "permission" {
				old := waitpkg.WorkerCwdStat
				waitpkg.WorkerCwdStat = func(path string) (os.FileInfo, error) {
					if path == cwd {
						return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrPermission}
					}
					return os.Stat(path)
				}
				t.Cleanup(func() { waitpkg.WorkerCwdStat = old })
			}
			get := fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}
			if name == "dead" {
				get = fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}
			}
			if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{get, {AnyArgs: true, Stdout: `{"result":{}}`}}); err != nil {
				t.Fatal(err)
			}
			row := "worker\tp-worker\tcodex\timplementer\topenai\t0\t" + cwd + "\tnow\t\tfull\n"
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# header\n"+row+row), 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "HERDR_WORKSPACE_ID": "ws", "HERDR_ENV": "1", "SOHO_CALLER_CHECK": "off", "SOHO_SKILL_DIR": root}
			var out strings.Builder
			DoctorCheck(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root, &out)
			want := "worker 'worker' runs in " + cwd + ", which no longer exists; release it with --close"
			expected := 0
			if name == "removed" || name == "file" {
				expected = 1
			}
			if got := strings.Count(out.String(), want); got != expected {
				t.Fatalf("warnings=%d want=%d output=%s", got, expected, out.String())
			}
			t.Logf("case=%s warning_count=%d", name, expected)
		})
	}
}
