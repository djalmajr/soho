package wait

// SOH-457: a plain --wake replaces only the wakes this caller recorded here.
// Selection is storage first, then Core's live/parent snapshot; a name is
// never used to decide what to stop. The fake hc and fake terminal.run stand
// in for Core; no real terminal is killed.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func writeWakeRecord(t *testing.T, f *wakeFixture, address, agents string) {
	t.Helper()
	dir := filepath.Join(f.sd, "wakes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, address), []byte(agents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hcCloseRules(address string) []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"kill", address, "--json"}, Stdout: `{"address":"` + address + `","state":"finished","parent":"t1"}`},
		{Argv: []string{"rm", address, "--json"}, Stdout: `{"removed":"` + address + `"}`},
	}
}

func assertNoClose(t *testing.T, f *wakeFixture) {
	t.Helper()
	for _, argv := range f.hcArgvs(t) {
		if argv[0] == "kill" || argv[0] == "rm" {
			t.Fatalf("a terminal was closed: %v", argv)
		}
	}
}

// Acceptance 1: the recorded, live, caller-parented wake is killed and rm'd
// through hc, its record is removed, one wake for the new set is armed and
// replaced names it. The stop happens before the run.
func TestWakeReplacesOnlyTheRecordedWakeOfThisCaller(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	rules := []fakecli.Rule{
		{Argv: []string{"ls", "--json"}, Call: 1, Stdout: `{"terminals":[]}`},
		{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[{"address":"t9","name":"wake-w1","state":"live","parent":"t1"}]}`},
		notFoundRule("wake-w1"), notFoundRule("wake-w2"),
	}
	f := newWakeFixture(t, conn, []string{"w1", "w2"}, append(rules, hcCloseRules("t9")...)...)
	if code, msg, _, stderr := f.cmd(t, "w1", "--wake", "--timeout", "600000"); code != 0 || msg != "" {
		t.Fatalf("first: code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	oldRecord := filepath.Join(f.sd, "wakes", "t9")
	if data, err := os.ReadFile(oldRecord); err != nil || string(data) != "w1\n" {
		t.Fatalf("first record=%q err=%v", data, err)
	}
	callsBefore := len(f.hcArgvs(t))
	runsBefore := len(conn.runs(t))
	conn.response = wakeRPCHello + "\n" + `{"jsonrpc":"2.0","id":2,"result":{"address":"t10"}}` + "\n"
	code, msg, out, stderr := f.cmd(t, "w2", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("second: code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	arm := wakeJSON(t, out)
	if got := stringsOf(arm["replaced"]); !reflect.DeepEqual(got, []string{"t9"}) {
		t.Fatalf("replaced=%v, want [t9] (line %v)", got, arm)
	}
	if _, err := os.Stat(oldRecord); !os.IsNotExist(err) {
		t.Fatalf("old record remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(f.sd, "wakes", "t10")); err != nil || string(data) != "w2\n" {
		t.Fatalf("new record=%q err=%v, want w2", data, err)
	}
	runs := conn.runs(t)
	if len(runs) != runsBefore+1 || runs[len(runs)-1]["name"] != "wake-w2" {
		t.Fatalf("runs=%v, want one new wake-w2", runs)
	}
	wantArgv := []string{platform.LauncherPath(f.env), "wait", "w2", "--timeout", "600000", "--observe"}
	if got := stringsOf(runs[len(runs)-1]["argv"]); !reflect.DeepEqual(got, wantArgv) {
		t.Fatalf("argv=%q want %q", got, wantArgv)
	}
	calls := f.hcArgvs(t)[callsBefore:]
	killAt, rmAt, runAt := -1, -1, -1
	for i, argv := range calls {
		if reflect.DeepEqual(argv, []string{"kill", "t9", "--json"}) && killAt < 0 {
			killAt = i
		}
		if reflect.DeepEqual(argv, []string{"rm", "t9", "--json"}) && rmAt < 0 {
			rmAt = i
		}
		if reflect.DeepEqual(argv, []string{"get", "t1", "--json"}) {
			runAt = i // the run's own caller check is the last one
		}
	}
	if killAt < 0 || rmAt < 0 || killAt >= rmAt || rmAt >= runAt {
		t.Fatalf("kill/rm of t9 must precede the run: calls=%v", calls)
	}
}

// Acceptance 2, identity: a recorded wake Core reports as another terminal's
// child, and a live child of this caller with no record here, are both left
// alone. Only the storage record makes a wake ours.
func TestWakeLeavesOtherOwnersAndUnrecordedChildrenAlone(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	live := `{"terminals":[{"address":"t8","name":"wake-w1","state":"live","parent":"t2"},{"address":"t7","name":"wake-other","state":"live","parent":"t1"}]}`
	rules := []fakecli.Rule{
		{Argv: []string{"ls", "--json"}, Stdout: live},
		notFoundRule("wake-w1"),
	}
	// The unrecorded child of the caller has working kill/rm rules: a bug that
	// closes it must fail on the ownership assertion, not on a missing fake.
	f := newWakeFixture(t, conn, []string{"w1"}, append(rules, hcCloseRules("t7")...)...)
	writeWakeRecord(t, f, "t8", "w1\n")
	code, msg, out, stderr := f.cmd(t, "w1", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	if got := stringsOf(wakeJSON(t, out)["replaced"]); len(got) != 0 {
		t.Fatalf("replaced=%v, want empty", got)
	}
	assertNoClose(t, f)
	if data, err := os.ReadFile(filepath.Join(f.sd, "wakes", "t8")); err != nil || string(data) != "w1\n" {
		t.Fatalf("the other owner's record changed: %q %v", data, err)
	}
	if len(conn.runs(t)) != 1 {
		t.Fatalf("runs=%d, want one", len(conn.runs(t)))
	}
}

// Lifetime: a wake that already fired is not live, so its stale record is
// dropped without a kill, and exactly one new wake is armed for the agents.
func TestWakeDropsAFiredRecordAndArmsOnce(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	f := newWakeFixture(t, conn, []string{"w1"},
		fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`}, notFoundRule("wake-w1"))
	writeWakeRecord(t, f, "t8", "w1\n")
	code, msg, out, stderr := f.cmd(t, "w1", "--wake", "--timeout", "600000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	if got := stringsOf(wakeJSON(t, out)["replaced"]); len(got) != 0 {
		t.Fatalf("replaced=%v, want empty", got)
	}
	assertNoClose(t, f)
	if _, err := os.Stat(filepath.Join(f.sd, "wakes", "t8")); !os.IsNotExist(err) {
		t.Fatalf("stale record remains: %v", err)
	}
	if len(conn.runs(t)) != 1 {
		t.Fatalf("runs=%d, want exactly one", len(conn.runs(t)))
	}
}

// Boundary: an unreachable Core refuses before touching a terminal, like the
// existing check. The listing failure names the new stop path.
func TestWakeRefusesWhenCoreCannotList(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	f := newWakeFixture(t, conn, []string{"w1"},
		fakecli.Rule{Argv: []string{"ls", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"boom"}}`},
		notFoundRule("wake-w1"))
	writeWakeRecord(t, f, "t8", "w1\n")
	code, msg, out, _ := f.cmd(t, "w1", "--wake", "--timeout", "600000")
	if code != 4 || !strings.Contains(msg, "wait: --wake could not stop previous wakes:") || out != "" {
		t.Fatalf("code=%d msg=%q out=%q, want exit 4 from the stop check", code, msg, out)
	}
	assertNoClose(t, f)
	if len(conn.runs(t)) != 0 || conn.connectCount() != 0 {
		t.Fatalf("runs=%d connects=%d, want none", len(conn.runs(t)), conn.connectCount())
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wakes", "t8")); err != nil {
		t.Fatalf("a failed check must not drop the record: %v", err)
	}
}

// --add keeps the old reconciliation: the recorded live wake of this caller
// stays armed and nothing is stopped.
func TestWakeAddKeepsTheRecordedWake(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	live := `{"terminals":[{"address":"t9","name":"wake-w1","state":"live","parent":"t1"}]}`
	f := newWakeFixture(t, conn, []string{"w1"},
		fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: live}, notFoundRule("wake-w1"))
	writeWakeRecord(t, f, "t9", "w1\n")
	code, msg, out, stderr := f.cmd(t, "w1", "--wake", "--add", "--timeout", "600000")
	if code != 0 || msg != "" || out != "" || !strings.Contains(stderr, "already armed (t9)") {
		t.Fatalf("code=%d msg=%q out=%q stderr=%q", code, msg, out, stderr)
	}
	assertNoClose(t, f)
	if len(conn.runs(t)) != 0 {
		t.Fatalf("runs=%d, want none", len(conn.runs(t)))
	}
}

func TestWakeAddNeedsWake(t *testing.T) {
	conn := newWakeRPCConn(wakeRPCRun)
	f := newWakeFixture(t, conn, []string{"w1"}, notFoundRule("wake-w1"))
	code, msg, out, _ := f.cmd(t, "w1", "--add")
	if code != 2 || msg != "wait: --add needs --wake" || out != "" {
		t.Fatalf("code=%d msg=%q out=%q", code, msg, out)
	}
	if conn.connectCount() != 0 {
		t.Fatalf("connects=%d, want none", conn.connectCount())
	}
}
