package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The destination uses the fixture's complete working native protocol.
// An alias therefore submits successfully rather than failing on a missing
// fake rule; the old name must still stop before every native call.
func TestDJA659OldNameNeverDispatchesToLiveReplacement(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	sd := filepath.Join(f.state, "ws")
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(dja659RosterHeader+dja659RosterLine("old", "w0test:p0a")), 0o600); err != nil {
		t.Fatal(err)
	}
	core.RosterRename(sd, "old", "worker")
	if core.RosterLine(sd, "old") != "" || core.RosterRenameHint(sd, "old") != "worker" {
		t.Fatal("setup: the actual rename did not bind the live replacement")
	}
	code, out, stderr := f.run(t, "old", f.brief, "--no-wait")
	if code != 3 || out != "" || !strings.Contains(stderr, "renamed to 'worker'") {
		t.Errorf("diagnostic-only old-name refusal: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("old-name dispatch reached the renamed worker: exit=%d stdout=%q calls=%#v", code, out, calls)
	}
}
