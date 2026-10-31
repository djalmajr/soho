//go:build !windows

package wait

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestStatusViewSourceFIFOSettlement: a FIFO standing where a required
// settlement marker stands is refused without being read (no block, no
// writer): the conservative unknown at the marker's own branch, the
// report still ready, and the FIFO left untouched.
func TestStatusViewSourceFIFOSettlement(t *testing.T) {
	for _, marker := range []string{"report-working-final", "report-stable-at", "report-settled"} {
		marker := marker
		t.Run(marker, func(t *testing.T) {
			terminal := "working"
			if marker == "report-settled" {
				terminal = "idle"
			}
			f := newSourceViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + terminal + `"}}}`},
				{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy 30%\n"},
			})
			report := filepath.Join(f.sd, "reports", "report.md")
			reportFilesWrite(t, report, "report\n")
			reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), report+"\n")
			before := f.inspectWith(t, "worker")
			if !before.ReportReady {
				t.Fatalf("control=%+v, want the report ready", before)
			}
			p := filepath.Join(f.sd, "wait", "worker."+marker)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skipf("the test user cannot create FIFOs: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(p) })
			done := make(chan AgentStatusView, 1)
			go func() {
				done <- InspectAgentStatus(AgentStatusRequest{
					StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
					Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: f.src,
				})
			}()
			select {
			case after := <-done:
				if after.State != "unknown" || !after.ReportReady {
					t.Errorf("FIFO marker view=%+v, want unknown with the report ready", after)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("the inspection blocked on the FIFO marker (it must refuse the nonregular entry without reading it)")
			}
			info, err := os.Lstat(p)
			if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("the FIFO marker is gone or no longer a FIFO after the inspection: %v %v", info, err)
			}
		})
	}
}
