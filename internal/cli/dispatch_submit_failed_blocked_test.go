package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// SOH-301, R389 F1: a retry that stops before sending (the codex hooks
// review screen with hooks_review=block) must keep the failed-submission
// marker, so wait still refuses to settle on the previous report.
func TestDispatchBlockedRetryKeepsTheSubmitFailedMarker(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: dispatchVisibleRead, Stdout: dispatchHooksMenuScreen})
	f.env["SOHO_HOOKS_REVIEW"] = "block"
	sd := filepath.Join(f.state, "ws")
	report := filepath.Join(sd, "reports", "previous.md")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(report, []byte("previous completed report\n"), 0600))
	must(taskreport.WriteTaskReportPointer(sd, "worker", jsonjs.O("version", 1, "task_report", report, "current", report, "history", []any{})))
	must(os.WriteFile(filepath.Join(sd, "last-report-worker"), []byte(report+"\n"), 0600))
	marker := filepath.Join(sd, "wait", "worker.submit-failed")
	must(os.WriteFile(marker, []byte(fmt.Sprintf("123 %s %s\n", strings.Repeat("a", 64), report)), 0600))
	wait := func() (int, string) {
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errBuf bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errBuf
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		code := Run([]string{"wait", "worker", "--timeout", "5s"}, f.env)
		return code, out.String()
	}
	if code, out := wait(); code != 15 {
		t.Fatalf("before the retry: exit=%d stdout=%s, want 15 (not-received)", code, out)
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 || !strings.Contains(out, "codex-hooks-review") {
		t.Fatalf("retry was not blocked by the hooks review: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if calls := dispatchHooksCalls(dispatchCalls(t, f)); len(calls) != 0 {
		t.Fatalf("the blocked retry sent input: %q", calls)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the blocked retry removed the marker: %v", err)
	}
	if code, out := wait(); code != 15 || strings.Contains(out, `"status":"done"`) {
		t.Fatalf("after the blocked retry: exit=%d stdout=%s, want not-received (15)", code, out)
	}
}
