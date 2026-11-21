package wait

// SOH-465 v2: a plain --wake that replaces this caller's recorded live wakes
// names the replaced agents that the new wake does not cover and that the
// current roster still accepts. It makes no status probe and claims nothing
// about their state; the suggested rearm uses --add, which is safe either
// way. The fake hc and fake terminal.run stand in for Core; no real terminal
// is killed.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// assertStringSet compares membership and multiplicity, never directory
// order: wake records are enumerated in filesystem order.
func assertStringSet(t *testing.T, key string, got []string, want ...string) {
	t.Helper()
	counts := make(map[string]int, len(want))
	for _, value := range want {
		counts[value]++
	}
	for _, value := range got {
		counts[value]--
	}
	for value, count := range counts {
		if count != 0 {
			t.Fatalf("%s=%v, want members %v (difference at %q: %d)", key, got, want, value, count)
		}
	}
}

// wakeReplaceFixture arms two recorded live wakes of the caller (t9 and t10)
// with a roster of a1, a2 and a3. The agents each record holds are the map's
// values. No fake rule is installed for an agent's get/read: any status probe
// during arming would surface in the hc call log.
func wakeReplaceFixture(t *testing.T, records map[string]string) (*wakeFixture, *wakeRPCConn) {
	t.Helper()
	conn := newWakeRPCConn(wakeRPCRun)
	live := `{"terminals":[{"address":"t9","name":"wake-a1","state":"live","parent":"t1"},{"address":"t10","name":"wake-a2","state":"live","parent":"t1"}]}`
	rules := []fakecli.Rule{
		{Argv: []string{"ls", "--json"}, Stdout: live},
		notFoundRule("wake-a1"), notFoundRule("wake-a2"), notFoundRule("wake-a3"),
	}
	rules = append(rules, hcCloseRules("t9")...)
	rules = append(rules, hcCloseRules("t10")...)
	f := newWakeFixture(t, conn, []string{"a1", "a2", "a3"}, rules...)
	for address, agents := range records {
		writeWakeRecord(t, f, address, agents)
	}
	return f, conn
}

// assertNoStatusCalls checks the calls made in one slice of the hc log:
// arming must not get or read any worker named in a wake.
func assertNoStatusCalls(t *testing.T, f *wakeFixture, calls [][]string) {
	t.Helper()
	for _, argv := range calls {
		if len(argv) > 1 && (argv[0] == "get" || argv[0] == "read") {
			switch argv[1] {
			case "a1", "a2", "a3":
				t.Fatalf("arming probed a worker: %v", argv)
			}
		}
	}
}

// Acceptance 1: a2 was released from the roster, a1 is still there. Only a1
// is unwatched, on stdout's JSON and in the exact stderr rearm line. The
// replacement is unchanged: both old wakes are stopped and one child armed.
func TestWakeReplaceNamesOnlyRosterAgents(t *testing.T) {
	f, conn := wakeReplaceFixture(t, map[string]string{"t9": "a1\n", "t10": "a2\n"})
	core.RosterRemove(f.sd, "a2")
	callsBefore := len(f.hcArgvs(t))
	code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	arm := wakeJSON(t, out)
	assertStringSet(t, "unwatched", stringsOf(arm["unwatched"]), "a1")
	assertStringSet(t, "replaced", stringsOf(arm["replaced"]), "t9", "t10")
	want := "soho: wait: this wake replaced the wake(s) of a1; to keep watching them, rearm with: soho wait a1 --wake --add\n"
	if stderr != want {
		t.Fatalf("stderr=%q, want %q", stderr, want)
	}
	assertNoStatusCalls(t, f, f.hcArgvs(t)[callsBefore:])
	if runs := conn.runs(t); len(runs) != 1 || runs[0]["name"] != "wake-a3" {
		t.Fatalf("runs=%v, want one wake-a3", runs)
	}

	// The suggestion is accepted by the CLI and only adds a second wake: the
	// first wake's record stays, nothing is killed or removed by --add.
	conn.response = wakeRPCHello + "\n" + `{"jsonrpc":"2.0","id":2,"result":{"address":"t11"}}` + "\n"
	argv := []string{"a1", "--wake", "--add"}
	if at := strings.LastIndex(stderr, "soho wait "); at >= 0 {
		argv = strings.Fields(strings.TrimSpace(stderr[at+len("soho wait "):]))
	}
	callsBefore = len(f.hcArgvs(t))
	retryCode, retryMsg, retryOut, _ := f.cmd(t, argv...)
	if retryCode != 0 || retryMsg != "" {
		t.Fatalf("suggested rearm argv %v: code=%d msg=%q out=%q, want 0", argv, retryCode, retryMsg, retryOut)
	}
	retry := wakeJSON(t, retryOut)
	if retry["wake"] != "armed" || retry["agents"] == nil {
		t.Fatalf("suggested rearm line=%v, want armed", retry)
	}
	if got := stringsOf(retry["unwatched"]); len(got) != 0 {
		t.Fatalf("suggested rearm claimed unwatched=%v", got)
	}
	for _, argv := range f.hcArgvs(t)[callsBefore:] {
		if argv[0] == "kill" || argv[0] == "rm" {
			t.Fatalf("--add closed a terminal: %v", argv)
		}
	}
	if data, err := os.ReadFile(filepath.Join(f.sd, "wakes", "t9")); err != nil || string(data) != "a3\n" {
		t.Fatalf("first wake record=%q err=%v, want a3", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(f.sd, "wakes", "t11")); err != nil || string(data) != "a1\n" {
		t.Fatalf("added wake record=%q err=%v, want a1", data, err)
	}
}

// Boundary: the arm itself makes no status call, even when the replaced wake
// covered a roster agent and when a new agent is named.
func TestWakeReplaceDoesNotProbeStatus(t *testing.T) {
	f, _ := wakeReplaceFixture(t, map[string]string{"t9": "a1\n", "t10": "a2\n"})
	callsBefore := len(f.hcArgvs(t))
	code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	_ = wakeJSON(t, out)
	assertNoStatusCalls(t, f, f.hcArgvs(t)[callsBefore:])
}

// Boundary: an unreadable regular record contributes no agents and does not
// block the replacement; a nonregular record keeps the baseline refusal.
func TestWakeReplaceUnreadableRecordStillReplaces(t *testing.T) {
	t.Run("mode 000", func(t *testing.T) {
		f, conn := wakeReplaceFixture(t, map[string]string{"t9": "a1\n"})
		path := filepath.Join(f.sd, "wakes", "t9")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if file, err := os.Open(path); err == nil {
			_ = file.Close()
			t.Skip("host allowed reading a mode-000 file")
		}
		code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--timeout", "600000")
		if code != 0 || msg != "" {
			t.Fatalf("code=%d msg=%q stderr=%q, want exit 0 as at the baseline", code, msg, stderr)
		}
		arm := wakeJSON(t, out)
		if _, ok := arm["unwatched"]; ok {
			t.Fatalf("unreadable record claimed an agent (line %v)", arm)
		}
		assertStringSet(t, "replaced", stringsOf(arm["replaced"]), "t9")
		if stderr != "" {
			t.Fatalf("stderr=%q, want empty", stderr)
		}
		if runs := conn.runs(t); len(runs) != 1 {
			t.Fatalf("runs=%d, want one", len(runs))
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "a3\n" {
			t.Fatalf("new record=%q err=%v, want a3", data, err)
		}
	})
	t.Run("nonregular record", func(t *testing.T) {
		f, conn := wakeReplaceFixture(t, map[string]string{"t9": "a1\n"})
		path := filepath.Join(f.sd, "wakes", "t9")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		code, msg, out, _ := f.cmd(t, "a3", "--wake", "--timeout", "600000")
		if code != 4 || !strings.Contains(msg, `invalid wake record "t9"`) || out != "" {
			t.Fatalf("code=%d msg=%q out=%q, want the baseline exit 4", code, msg, out)
		}
		if len(conn.runs(t)) != 0 {
			t.Fatalf("runs=%d, want none", len(conn.runs(t)))
		}
	})
}

// No replaced wake means no claim and no line; the roster still has agents.
func TestWakeNoReplacedWakeHasNoUnwatched(t *testing.T) {
	f, _ := wakeReplaceFixture(t, map[string]string{})
	code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	arm := wakeJSON(t, out)
	if _, ok := arm["unwatched"]; ok {
		t.Fatalf("unwatched must be absent without a replaced wake (line %v)", arm)
	}
	if got := stringsOf(arm["replaced"]); len(got) != 0 {
		t.Fatalf("replaced=%v, want empty", got)
	}
	if stderr != "" {
		t.Fatalf("stderr=%q, want empty", stderr)
	}
}

// --add keeps its baseline reconciliation: it does not stop a wake, reports
// no unwatched field, and answers already-armed when the agent is covered.
func TestWakeAddUnchanged(t *testing.T) {
	f, conn := wakeReplaceFixture(t, map[string]string{"t9": "a1\n", "t10": "a2\n"})
	fresh := wakeRPCHello + "\n" + `{"jsonrpc":"2.0","id":2,"result":{"address":"t11"}}` + "\n"
	conn.response = fresh
	code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--add", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("add code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	arm := wakeJSON(t, out)
	if arm["wake"] != "armed" {
		t.Fatalf("wake=%v, want armed (line %v)", arm["wake"], arm)
	}
	for _, key := range []string{"unwatched", "replaced"} {
		if value, ok := arm[key]; ok && len(stringsOf(value)) != 0 {
			t.Fatalf("%s=%v, want absent or empty (line %v)", key, value, arm)
		}
	}
	assertNoClose(t, f)
	if strings.Contains(stderr, "replaced the wake") {
		t.Fatalf("--add reported replaced wakes: %q", stderr)
	}
	if len(conn.runs(t)) != 1 {
		t.Fatalf("runs=%d, want one", len(conn.runs(t)))
	}

	runsBefore := len(conn.runs(t))
	code, msg, out, stderr = f.cmd(t, "a1", "--wake", "--add", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("covered add code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	if !strings.Contains(stderr, "already armed (t9)") {
		t.Fatalf("covered add stderr=%q, want already armed (t9)", stderr)
	}
	if len(conn.runs(t)) != runsBefore {
		t.Fatalf("covered add armed %d children, want none", len(conn.runs(t))-runsBefore)
	}
}

// A name repeated across records counts once. The expected set is derived
// from the agents actually recorded in the addresses the arm says it stopped,
// so the assertion holds for either directory enumeration order.
func TestWakeReplaceUnwatchedIsDeduplicated(t *testing.T) {
	records := map[string]string{"t9": "a2\na1\n", "t10": "a1\n"}
	f, _ := wakeReplaceFixture(t, records)
	code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	arm := wakeJSON(t, out)
	seen := map[string]bool{}
	for _, address := range stringsOf(arm["replaced"]) {
		for _, agent := range strings.Split(strings.TrimSuffix(records[address], "\n"), "\n") {
			if agent != "" {
				seen[agent] = true
			}
		}
	}
	got := stringsOf(arm["unwatched"])
	assertStringSet(t, "unwatched", got, "a1", "a2")
	if len(got) != len(seen) {
		t.Fatalf("unwatched=%v has duplicates; records hold %v", got, seen)
	}
	if cmd := "soho wait " + strings.Join(got, " ") + " --wake --add"; !strings.Contains(stderr, cmd) {
		t.Fatalf("stderr=%q, want %q", stderr, cmd)
	}
}

// The rearm suggestion excludes the newly armed name, an already-covered
// name, a released name, a blank record and a malformed non-roster name.
func TestWakeReplaceUnwatchedMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
		change func(*wakeFixture)
		want   []string
	}{
		{name: "pending", record: "a1\n", want: []string{"a1"}},
		{name: "covered", record: "a3\n"},
		{name: "released", record: "a2\n", change: func(f *wakeFixture) { core.RosterRemove(f.sd, "a2") }},
		{name: "renamed", record: "a2\n", change: func(f *wakeFixture) { core.RosterRename(f.sd, "a2", "a4") }},
		{name: "blank", record: "\n"},
		{name: "invalid", record: "bad name\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := wakeReplaceFixture(t, map[string]string{"t9": tc.record})
			if tc.change != nil {
				tc.change(f)
			}
			code, msg, out, stderr := f.cmd(t, "a3", "--wake", "--timeout", "600000")
			if code != 0 || msg != "" {
				t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
			}
			arm := wakeJSON(t, out)
			if len(tc.want) == 0 {
				if _, ok := arm["unwatched"]; ok {
					t.Fatalf("unwatched must be absent (line %v)", arm)
				}
				if stderr != "" {
					t.Fatalf("stderr=%q, want empty", stderr)
				}
			} else {
				assertStringSet(t, "unwatched", stringsOf(arm["unwatched"]), tc.want...)
				if cmd := "soho wait " + strings.Join(tc.want, " ") + " --wake --add"; !strings.Contains(stderr, cmd) {
					t.Fatalf("stderr=%q, want %q", stderr, cmd)
				}
			}
		})
	}
}

// The lenient wrapper's two neighbors that a CmdWait fixture cannot
// interleave: a file swapped after Lstat, and one that opens but cannot be
// read. Both mean unknown membership, never an error or a claimed agent.
func TestWakeRecordedAgentsUnknownMembership(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(base, "t9"), []byte("a1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	other, err := os.Lstat(filepath.Join(base, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wakeRecordedAgents(root, "t9", other); len(got) != 0 {
		t.Fatalf("identity mismatch agents=%v, want none", got)
	}
	directory, err := os.Lstat(filepath.Join(base, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wakeRecordedAgents(root, "dir", directory); len(got) != 0 {
		t.Fatalf("unreadable file agents=%v, want none", got)
	}
	regular, err := os.Lstat(filepath.Join(base, "t9"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wakeRecordedAgents(root, "t9", regular); !reflect.DeepEqual(got, []string{"a1"}) {
		t.Fatalf("readable record agents=%v, want [a1]", got)
	}
}
