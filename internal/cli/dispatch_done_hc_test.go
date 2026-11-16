package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchHcPreviousReportDone(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Stdout: hcWorkerGet(7, "done")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		{Argv: []string{"send", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":8,"started":true}`},
	}...))
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("first dispatch: code=%d out=%s stderr=%s", code, out, stderr)
	}
	previous := filepath.Join(f.sd, "reports", "worker-"+composedStampOf(f, t)+".md")
	if err := os.WriteFile(previous, []byte("previous report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The composed filename has second precision: advance the fixture clock
	// so this second dispatch has its own immutable brief and report.
	clock := platform.Now
	platform.Now = func() time.Time { return clock().Add(2 * time.Second) }
	code, out, stderr = f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	calls := f.hcCalls(t)
	sends := hcSends(calls)
	if len(sends) != 2 {
		t.Fatalf("sends=%#v", sends)
	}
	seen := 0
	for i, c := range calls {
		if isRealSend(c.Argv) {
			seen++
			if seen == 1 {
				continue
			}
			if i == 0 || len(calls[i-1].Argv) != 3 || calls[i-1].Argv[0] != "get" || calls[i-1].Argv[1] != "worker" {
				t.Fatalf("send without immediate same-target get: %#v", calls)
			}
			if len(c.Argv) != 5 || c.Argv[1] != "--json" || c.Argv[2] != "--" || c.Argv[3] != "worker" {
				t.Fatalf("send argv=%q", c.Argv)
			}
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}
