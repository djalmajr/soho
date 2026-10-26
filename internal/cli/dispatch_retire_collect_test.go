package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/taskreport"
)

// TestDispatchRetiredPointerKeepsStableReport: collect after a release must
// not delete the stable copy of a completed task's report (the retired
// pointer has an empty current, which is not a missing report).
func TestDispatchRetiredPointerKeepsStableReport(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	_, report := firstDispatch(t, f)
	reopenDispatchWrite(t, report, "completed report\n")
	sd := filepath.Join(f.state, "ws")
	if code, out, stderr := reopenDispatchRun(t, f.env, "collect", "worker"); code != 0 {
		t.Fatalf("first collect: %d %s %s", code, out, stderr)
	}
	pointer := taskreport.ReadTaskReportPointer(sd, "worker")
	value, _ := pointer.Get("task_report")
	stable := value.(string)
	before, err := os.ReadFile(stable)
	if err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := reopenDispatchRun(t, f.env, "release", "worker"); code != 0 {
		t.Fatalf("release: %d %s %s", code, out, stderr)
	}
	_, _, _ = reopenDispatchRun(t, f.env, "collect", "worker")
	after, err := os.ReadFile(stable)
	if err != nil || string(after) != string(before) {
		t.Fatalf("collect after release changed the stable report: %q err=%v; want %q", after, err, before)
	}
}
