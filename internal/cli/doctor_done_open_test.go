package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorDoneOpenCommand(t *testing.T) {
	fixed := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	oldNow := platform.Now
	platform.Now = func() time.Time { return fixed }
	t.Cleanup(func() { platform.Now = oldNow })
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("listingFailure=%v", fail), func(t *testing.T) {
			env, repo := commandFixture(t)
			bin := t.TempDir()
			code := 0
			if fail {
				code = 1
			}
			if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","name":"orchestrator","state":"live","labels":{"soho.session":"ws"}}`},
				{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[{"address":"t2","name":"worker","state":"live","status":{"state":"idle"}}]}`, Code: code},
				{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"t2","name":"worker","state":"live","status":{"state":"idle"}}`},
			}); err != nil {
				t.Fatal(err)
			}
			env = withFakeCLI(env, bin)
			env = withHostGit(t, env, bin)
			env["SOHO_TRANSPORT"], env["HC_TERMINAL_ID"] = "hc", "t1"
			sd := filepath.Join(env["SOHO_DIR"], "ws")
			for _, dir := range []string{filepath.Join(sd, "reports"), filepath.Join(sd, "wait")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(sd, "agents.tsv"), "worker\tt2\tpi\timplementer\n")
			report := filepath.Join(sd, "reports", "worker.md")
			write(report, "report\n")
			at := fixed.Add(-31 * time.Minute)
			if err := os.Chtimes(report, at, at); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(report)
			if err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(sd, "last-report-worker"), report+"\n")
			write(filepath.Join(sd, "wait", "worker.report-working-final"), fmt.Sprintf("%d\t%d\t%q\n", info.Size(), info.ModTime().UnixNano(), report))
			rc, out, stderr := runIn(t, []string{"doctor"}, env, repo)
			if rc != 0 {
				t.Fatalf("doctor exit=%d stderr=%q out=%s", rc, stderr, out)
			}
			want := "warn   worker 'worker' finished its task 31 min ago and is still open: release it (soho release worker --close), or keep it with a reason you state to the user\n"
			if !fail && strings.Count(out, want) != 1 {
				t.Fatalf("missing warning: %s", out)
			}
			if fail && strings.Contains(out, "finished its task") {
				t.Fatalf("warning on failed listing: %s", out)
			}
			if !strings.Contains(out, "first_run:") {
				t.Fatalf("doctor did not finish: %s", out)
			}
		})
	}
}
