package cli

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/startup"
)

func TestReopenFreshPaneDoesNotClearShellLine(t *testing.T) {
	oldPlatform := startup.CurrentPlatform
	startup.CurrentPlatform = func() string { return "darwin" }
	t.Cleanup(func() { startup.CurrentPlatform = oldPlatform })
	f := newReopenFixture(t, "idle")
	setReopenRules(t, f)
	f.env["SOHO_LAYOUT"] = "split"
	run := f.reopen(t, "worker", "--force")
	if run.code != 0 || !strings.Contains(run.stdout, `"created_pane": true`) {
		t.Fatalf("reopen code=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	closed, created, started := false, false, false
	for _, call := range run.calls {
		if len(call.Argv) < 2 {
			continue
		}
		if call.Argv[0] == "agent" && call.Argv[1] == "start" {
			started = true
			break
		}
		if call.Argv[0] == "pane" && call.Argv[1] == "send-text" {
			t.Fatalf("reopen cleared a newly created pane before its first start: %#v", run.calls)
		}
		if call.Argv[0] == "pane" && call.Argv[1] == "close" {
			closed = true
		}
		if (call.Argv[0] == "pane" && call.Argv[1] == "split") || (call.Argv[0] == "tab" && call.Argv[1] == "create") {
			created = true
		}
	}
	if !closed || !created || !started {
		t.Fatalf("reopen lifecycle: closed=%t created=%t started=%t calls=%#v", closed, created, started, run.calls)
	}
}
