package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA659 stage 1 (F1): dispatch consumer regression against the roster half
// of a rename that is actually executed: the fixture's old live roster line
// is renamed through the existing core.RosterRename on the fixture's actual
// state directory, and the actual CLI dispatch is then invoked for the
// now-missing name. Scope and limit, precisely: these tests cover the
// roster half of the supported rename operation and the CLI dispatch
// consumer; they do NOT execute a live native rename (no herdr agent rename
// call) and they do not pin any persistence format — no proposed history
// record is injected before or after the rename. The recorded red is the
// generic missing-name diagnostic: the pre-fix consumer has no rename
// history, so the assertion that the message explains the rename and names
// the valid replacement fails.

const dja659RosterHeader = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n"

func dja659RosterLine(name, pane string) string {
	return name + "\t" + pane + "\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
}

func TestDJA659RenamedDispatchExits3ExplainsRename(t *testing.T) {
	// RED on the pre-fix code. The behavior protected: dispatching to the
	// old name of a renamed worker exits 3 before any worker interaction
	// (exactly empty stdout, no prompt, zero native calls to the old name)
	// and the message explains the rename, naming the valid replacement.
	// The believable regression that makes it fail: the pre-fix lookup has
	// no rename history, so the message stays generic; the hostile
	// regression is a fix that lets the old name reach the renamed worker
	// (a delivery alias) or a prompt — the no-call and empty-stdout
	// assertions fail it.
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	sd := filepath.Join(f.state, "ws")
	// The old live roster line, with the pane and started identity the
	// rename must preserve.
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(dja659RosterHeader+dja659RosterLine("old", "w0test:p1")), 0o600); err != nil {
		t.Fatal(err)
	}
	// The supported rename operation's roster half, executed on the
	// fixture's actual state directory.
	core.RosterRename(sd, "old", "new")
	// The roster really changed from old to new, pane and started intact.
	if line := core.RosterLine(sd, "old"); line != "" {
		t.Fatalf("the old name still has a roster line: %q", line)
	}
	fields := strings.Split(core.RosterLine(sd, "new"), "\t")
	if len(fields) < 8 || fields[0] != "new" || fields[1] != "w0test:p1" || fields[7] != "now" {
		t.Fatalf("the rename did not keep the pane and started identity: %#v", fields)
	}
	// The actual CLI dispatch for the now-missing old name.
	code, out, stderr := f.run(t, "old", f.brief, "--no-wait")
	if code != 3 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if out != "" {
		t.Fatalf("stdout=%q want exactly empty", out)
	}
	if !strings.Contains(stderr, "agent 'old' is not in this skill's roster") {
		t.Fatalf("stderr=%q lost the roster-miss message", stderr)
	}
	// Zero native interaction with the missing name: asserted before the
	// red diagnostic, so the red run proves the branch sends nothing.
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("the missing-name branch interacted with a native worker: %#v", calls)
	}
	if !strings.Contains(stderr, "renamed to 'new'") {
		t.Fatalf("stderr=%q does not explain the rename and name the valid replacement", stderr)
	}
}

func TestDJA659RenamedDispatchControls(t *testing.T) {
	t.Run("a name without rename history keeps the current generic exit 3", func(t *testing.T) {
		// The fixture's default roster (a live 'worker'; no rename
		// executed, no history record): the diagnostic must not leak to
		// genuinely unknown names, and the message stays the current one,
		// byte for byte.
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		code, out, stderr := f.run(t, "old", f.brief, "--no-wait")
		want := "soho: agent 'old' is not in this skill's roster (spawn it first, or pass a name you spawned)\n"
		if code != 3 || out != "" || stderr != want {
			t.Fatalf("code=%d out=%q stderr=%q, want code 3, exactly empty stdout, stderr=%q", code, out, stderr, want)
		}
	})

	t.Run("the renamed worker dispatches normally under the current name", func(t *testing.T) {
		// The same actual rename executed before the dispatch: the old
		// line is renamed to the name the fixture's herdr rules address,
		// so the live-name path runs end to end on the renamed roster.
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		// The package's normal-dispatch controls use this override: the
		// prompt-receipt check is not under test here.
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		sd := filepath.Join(f.state, "ws")
		if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(dja659RosterHeader+dja659RosterLine("old", "w0test:p0a")), 0o600); err != nil {
			t.Fatal(err)
		}
		core.RosterRename(sd, "old", "worker")
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the normal dispatch did not submit: %q", out)
		}
	})
}
