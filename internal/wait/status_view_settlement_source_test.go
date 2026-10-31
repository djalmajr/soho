package wait

// Permanent source-bound regressions for the two remaining settlement
// defects: the changed-path cutoff is the source's retained report
// observation (a renamed or replaced raw report path cannot move it),
// and a present-invalid required settlement marker (oversize or linked)
// is the conservative unknown at its own branch with the independently
// observed report still ready — never the absent-marker continuation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestStatusViewSourcePoisonedSettlement: the required settlement
// markers (report-working-final, report-stable-at, report-settled) that
// exist but cannot be read as safe metadata (an oversize or a linked
// marker) render the conservative unknown at their own branch, the
// report stays independently ready, and an absent marker stays ordinary.
func TestStatusViewSourcePoisonedSettlement(t *testing.T) {
	for _, marker := range []string{"report-working-final", "report-stable-at", "report-settled"} {
		marker := marker
		t.Run(marker, func(t *testing.T) {
			terminal, expected := "working", "working"
			if marker == "report-settled" {
				terminal, expected = "idle", "done"
			}
			f := newSourceViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + terminal + `"}}}`},
				{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy 30%\n"},
			})
			report := filepath.Join(f.sd, "reports", "report.md")
			reportFilesWrite(t, report, "report\n")
			reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), report+"\n")
			before := f.inspectWith(t, "worker")
			if before.State != expected || !before.ReportReady {
				t.Fatalf("control=%+v want=%s", before, expected)
			}
			markerPath := filepath.Join(f.sd, "wait", "worker."+marker)
			// Oversize: past the metadata cap the confined read caps at.
			reportFilesWrite(t, markerPath, strings.Repeat("x", (64<<10)+1))
			after := f.inspectWith(t, "worker")
			if after.State != "unknown" || !after.ReportReady {
				t.Errorf("oversize marker view=%+v, want unknown with the report ready", after)
			}
			// Linked: a symlink standing where the marker stands.
			if err := os.Remove(markerPath); err != nil {
				t.Fatal(err)
			}
			decoy := filepath.Join(f.sd, "settlement-decoy.md")
			reportFilesWrite(t, decoy, strings.Repeat("x", 128))
			if err := os.Symlink(decoy, markerPath); err != nil {
				t.Skipf("the test user cannot create symlinks: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(markerPath) })
			after = f.inspectWith(t, "worker")
			if after.State != "unknown" || !after.ReportReady {
				t.Errorf("linked marker view=%+v, want unknown with the report ready", after)
			}
		})
	}
}

// TestStatusViewSourceReportMtimeRetained: the inspection's changed-path
// cutoff is the source's retained report observation. A real owned file
// changed after the retained report renders working; after the session
// dir is renamed and a decoy report with a future mtime stands at the
// old raw path, the cutoff still follows the retained report (working),
// never the raw replacement metadata (which would settle done).
func TestStatusViewSourceReportMtimeRetained(t *testing.T) {
	f := newReportFilesFixture(t, "idle", "implementer", "## Owned files\n- `owned.txt`\n")
	report := filepath.Join(f.sd, "reports", "report.md")
	reportFilesWrite(t, report, "report\n")
	if err := os.Chtimes(report, f.at, f.at); err != nil {
		t.Fatal(err)
	}
	reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), report+"\n")
	f.change(t, "owned.txt", time.Second)
	src, err := core.OpenSessionSource(filepath.Dir(f.sd), "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	inspect := func() AgentStatusView {
		view, out, errOut := silentInspect(t, AgentStatusRequest{
			StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.tree,
			Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: src,
		})
		if out != "" || errOut != "" {
			t.Fatalf("the source inspect printed: stdout=%q stderr=%q", out, errOut)
		}
		return view
	}
	before := inspect()
	if before.State != "working" || !before.ReportReady {
		t.Fatalf("control=%+v, want working with the report ready", before)
	}
	if err := os.Rename(f.sd, f.sd+"-retained"); err != nil {
		t.Fatal(err)
	}
	reportFilesWrite(t, report, "decoy metadata\n")
	if err := os.Chtimes(report, f.at.Add(time.Hour), f.at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after := inspect()
	if after.State != "working" || !after.ReportReady {
		t.Errorf("after replacement=%+v, want the retained cutoff (working), not the raw replacement metadata", after)
	}
}
