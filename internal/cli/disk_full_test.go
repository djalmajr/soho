package cli

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchNoWaitDiskFullWarning(t *testing.T) {
	line := "soho: gate: cannot write /tmp/soho/hc-t1/reports/crit-kas283c2-unit1.result.json: disk quota exceeded"
	f := newDispatchArrivalFixture(t, "blocked", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Call: 1, Stdout: line})
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	warning := "soho: warning: worker 'worker' hit a disk error: " + line + "; free space (soho gc removes registered copies) and dispatch again"
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" || strings.Count(stderr, warning) != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q, want submitted and exactly one %q", code, out, stderr, warning)
	}
}

func TestDispatchNoWaitQuotaBeatsDiskWarning(t *testing.T) {
	screen := "Error: disk quota exceeded\nYou've hit your usage limit"
	f := newDispatchArrivalFixture(t, "blocked", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Call: 1, Stdout: screen})
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 11 || dispatchOutputStatus(t, out) != "quota" || !strings.Contains(out, `"match":"You've hit your usage limit"`) || strings.Contains(stderr, "hit a disk error") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestStatusDiskFullWarning(t *testing.T) {
	line := "write failed: ENOSPC token=sk_live_abcdefghij"
	row := "worker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n"
	f := newWaitStatusFixture(t, row, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: line},
	})
	code, out, stderr := f.run(t, "worker")
	warning := "soho: warning: worker 'worker' hit a disk error: write failed: ENOSPC token=[redacted]; free space (soho gc removes registered copies) and dispatch again"
	if code != 0 || strings.Contains(out, `"status":"quota"`) || strings.Count(stderr, warning) != 1 || strings.Contains(stderr, "sk_live_") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
}
