package wait

// SOH-460/SOH-458: a missing-roster refusal from wait names why the name was
// never opened, or (under hc) the live child terminal release left outside
// the roster. The command entry point is the oracle: CmdWait. The close hint
// additionally needs a released-panes row for that exact name and terminal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestWaitRosterInvalidNameReason(t *testing.T) {
	long := strings.Repeat("a", 33)
	for _, tc := range []struct {
		name, reason string
	}{
		{long, "; '" + long + "' is not a valid agent name (longer than 32 characters), so spawn never opened it"},
		{"Review", "; 'Review' is not a valid agent name (must start with a lowercase letter), so spawn never opened it"},
		{"rev.iew", "; 'rev.iew' is not a valid agent name (only a-z, 0-9, _ and - are allowed), so spawn never opened it"},
		{"rev\niew", "; 'rev iew' is not a valid agent name (only a-z, 0-9, _ and - are allowed), so spawn never opened it"},
		{"worker-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHcWakeFixture(t, "1", "none")
			f.installHc(t)
			code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, tc.name)
			want := "agent '" + tc.name + "' is not in the roster (state dir: " + f.sd + ")" + tc.reason
			if code != 3 || msg != want {
				t.Fatalf("code=%d msg=%q want 3 %q", code, msg, want)
			}
		})
	}
}

func TestWaitRosterReleasedChildHint(t *testing.T) {
	const released = "; terminal t7 named 'caller-child' is still open outside the roster (released without --close); close it with: soho release caller-child --close"
	for _, tc := range []struct {
		caseName, name, pane, parent, record, registry, hint string
	}{
		{
			caseName: "released child keeps the hint",
			name:     "caller-child", pane: "t7", parent: "t1",
			record:   `"status":{"state":"idle","app":"claude-code"}`,
			registry: "caller-child\tt7\tcodex\tnow\n",
			hint:     released,
		},
		{
			caseName: "wake child has no hint",
			name:     "wake-w1", pane: "t8", parent: "t1",
			record: `"argv":["/bin/sh","-c","soho wait w1 --wake"]`,
		},
		{
			caseName: "unregistered assistant child has no hint",
			name:     "assistant-1", pane: "t9", parent: "t1",
			record: `"status":{"state":"idle","app":"claude-code"}`,
		},
		{
			caseName: "registry row with another pane has no hint",
			name:     "caller-child", pane: "t7", parent: "t1",
			registry: "caller-child\tt8\tcodex\tnow\n",
		},
		{
			caseName: "registry row with another name has no hint",
			name:     "caller-child", pane: "t7", parent: "t1",
			registry: "someone-else\tt7\tcodex\tnow\n",
		},
		{
			caseName: "foreign parent has no hint",
			name:     "caller-child", pane: "t7", parent: "t9",
			registry: "caller-child\tt7\tcodex\tnow\n",
		},
	} {
		t.Run(tc.caseName, func(t *testing.T) {
			f := newHcWakeFixture(t, "1", "none")
			if tc.registry != "" {
				if err := os.WriteFile(filepath.Join(f.sd, "released-panes.tsv"), []byte("# name\tpane\tkind\treleased\n"+tc.registry), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			record := `"name":"` + tc.name + `","state":"live","parent":"` + tc.parent + `"`
			if tc.record != "" {
				record += "," + tc.record
			}
			f.installHc(t, fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[{"address":"` + tc.pane + `",` + record + `,"labels":{"soho.session":"sW"}}]}`})
			code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, tc.name)
			want := "agent '" + tc.name + "' is not in the roster (state dir: " + f.sd + ")" + tc.hint
			if code != 3 || msg != want {
				t.Fatalf("code=%d msg=%q want 3 %q", code, msg, want)
			}
		})
	}
}

func TestWaitRosterUnreadableRegistryKeepsMessage(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	if err := os.Mkdir(filepath.Join(f.sd, "released-panes.tsv"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.installHc(t, fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[{"address":"t7","name":"caller-child","state":"live","parent":"t1","labels":{"soho.session":"sW"}}]}`})
	code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, "caller-child")
	want := "agent 'caller-child' is not in the roster (state dir: " + f.sd + ")"
	if code != 3 || msg != want {
		t.Fatalf("code=%d msg=%q want 3 %q", code, msg, want)
	}
}

func TestWaitRosterUnreachableCoreKeepsMessage(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t, fakecli.Rule{Argv: []string{"ls", "--json"}, Stderr: `{"error":{"kind":"unavailable"}}`, Code: 1})
	code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, "absent")
	want := "agent 'absent' is not in the roster (state dir: " + f.sd + ")"
	if code != 3 || msg != want {
		t.Fatalf("code=%d msg=%q want 3 %q", code, msg, want)
	}
}

func TestWaitRosterValidNameWithoutTerminalIsUnchanged(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t, fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`})
	code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, "absent")
	want := "agent 'absent' is not in the roster (state dir: " + f.sd + ")"
	if code != 3 || msg != want {
		t.Fatalf("code=%d msg=%q want 3 %q", code, msg, want)
	}
}
