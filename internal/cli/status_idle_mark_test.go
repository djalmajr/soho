package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-565 decision 3: release removes the idle-alert marker with its
// existing cleanup of the wait/<agent>.* files (release.go:112), so a
// released worker leaves no mark behind.
func TestReleaseRemovesIdleAlertMarker(t *testing.T) {
	f := newReleaseFixture(t, "idle", "")
	addReleaseReport(t, f.state, "worker", false)
	marker := filepath.Join(f.state, "wait", "worker.idle-alert")
	if err := os.WriteFile(marker, []byte("\t47\t1700000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := f.run(t, "worker", "--close")
	if run.code != 0 {
		t.Fatalf("code=%d out=%q err=%q, want a clean release", run.code, run.stdout, run.stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker still present (%v), want removed with the other wait/worker.* files", err)
	}
}

// DJA-565 decision 2 through the real command entry: `soho status` shows
// the mark on the row's state field and no other column moves; the fake
// herdr call sequence is the unmarked one.
func TestStatusShowsIdleAlertMarkThroughCLI(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	f := newWaitStatusFixture(t, row, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Busy on the task 30%\n"},
	})
	// Acceptance 5: a fixture that sets HOME also sets USERPROFILE to the
	// same directory.
	f.env["USERPROFILE"] = f.env["HOME"]
	marker := filepath.Join(f.state, "wait", "worker.idle-alert")
	if err := os.WriteFile(marker, []byte("\t47\t1700000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "worker")
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q, want 0", code, out, stderr)
	}
	if out != "worker\tworking\t\tidle: no new output for 47 min\t-\t-\n" {
		t.Fatalf("out=%q, want the marked row only", out)
	}
	// The mark adds no herdr call: get, the alive-in-other-pane list and
	// the working screen read, nothing else.
	joined := []string{}
	for _, c := range f.calls(t) {
		joined = append(joined, strings.Join(c.Argv, "\x00"))
	}
	if want := "agent\x00get\x00worker | agent\x00list | agent\x00read\x00worker\x00--source\x00visible"; strings.Join(joined, " | ") != want {
		t.Fatalf("herdr calls=%q, want %q", strings.Join(joined, " | "), want)
	}
}
