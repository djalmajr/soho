package doctor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorBootNoticeWithoutWorkspace(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": root, "SOHO_SKILL_DIR": root, "SOHO_DIR": root}
	old := platform.BootTime
	platform.BootTime = func() (time.Time, error) { return time.Time{}, errors.New("unknown") }
	t.Cleanup(func() { platform.BootTime = old })
	var out strings.Builder
	DoctorCheck(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root, &out)
	if strings.Contains(out.String(), "machine restarted") {
		t.Fatalf("unknown workspace/boot emitted notice: %s", out.String())
	}
	t.Log("doctor outside a resolvable Herdr workspace stays advisory and emits no restart notice")
}

func TestDoctorBootNoticeCheapConditions(t *testing.T) {
	for _, name := range []string{"no-task", "reported", "after-boot", "interrupted", "empty-report"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			sd := filepath.Join(root, "state", "ws")
			bin := filepath.Join(root, "bin")
			if err := os.MkdirAll(filepath.Join(sd, "reports"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp1\tclaude\timplementer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}}); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
				if k, v, ok := strings.Cut(item, "="); ok {
					env[k] = v
				}
			}
			env["PATH"], env["SOHO_DIR"], env["HERDR_WORKSPACE_ID"] = bin, filepath.Join(root, "state"), "ws"
			stamp := "20261003T100000"
			if name == "after-boot" {
				stamp = "20261003T130000"
			}
			report := filepath.Join(sd, "reports", "worker-"+stamp+".md")
			if name != "no-task" {
				pointer, err := json.Marshal(map[string]any{"version": 1, "current": report, "task_report": strings.TrimSuffix(report, ".md") + ".current.md", "history": []any{}})
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(taskreport.TaskReportPointerPath(sd, "worker"), pointer, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "reported" {
				if err := os.WriteFile(report, []byte("done\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "empty-report" {
				if err := os.WriteFile(report, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			saved := platform.BootTime
			platform.BootTime = func() (time.Time, error) { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local), nil }
			t.Cleanup(func() { platform.BootTime = saved })
			var out strings.Builder
			doctorRestartNotices(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root, &out)
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(bin, "herdr.json"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			want := 0
			if name == "interrupted" || name == "empty-report" {
				want = 1
			}
			if len(calls) != want || strings.Contains(out.String(), "run soho resume worker") != (want == 1) {
				t.Fatalf("case=%s calls=%v notice=%s", name, calls, out.String())
			}
		})
	}
}
