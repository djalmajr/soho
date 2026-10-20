package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

func pendingCopy(path string) CopyRow {
	return CopyRow{Path: path, Owner: "worker", Pane: "p1", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "mutation-copy", Task: "t.md", Report: "r.md"}
}

func acceptCopy(row CopyRow) (CopyRow, string) { return row, "" }

func readRaw(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

func TestQueueCopyWritesTheFullLineWithItsStateDir(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	queue, err := QueueCopy(env, "ws", sd, pendingCopy("/x/copy"))
	if err != nil {
		t.Fatal(err)
	}
	if queue != filepath.Join(env.Get("TMPDIR"), "soho", "ws", "copies.pending.tsv") {
		t.Fatalf("queue=%s", queue)
	}
	want := pendingCopiesHeader + "\n" + sd + "\t/x/copy\tworker\tp1\t2026-01-01T00:00:00Z\t-\tmutation-copy\tt.md\tr.md\n"
	if got := readRaw(t, queue); got != want {
		t.Fatalf("queue=%q\nwant %q", got, want)
	}
	if _, err := os.Stat(queue + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("the queue lock was left behind: %v", err)
	}
	if _, err := os.Stat(CopiesFile(sd)); !os.IsNotExist(err) {
		t.Fatalf("queueing wrote the registry: %v", err)
	}
}

func TestIncorporatePendingCopiesMovesOnlyThisStateDir(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	other := t.TempDir()
	if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/mine")); err != nil {
		t.Fatal(err)
	}
	queue, err := QueueCopy(env, "ws", other, pendingCopy("/x/theirs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteCopies(sd, []CopyRow{{Path: "/x/old", Owner: "-", Pane: "-", Created: "c", Source: "-", Origin: "add"}}); err != nil {
		t.Fatal(err)
	}
	res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy)
	if err != nil || res.Moved != 1 || len(res.Dropped) != 0 {
		t.Fatalf("res=%+v err=%v; want one line moved", res, err)
	}
	rows, err := ReadCopies(sd)
	if err != nil || len(rows) != 2 || rows[0].Path != "/x/old" || rows[1] != pendingCopy("/x/mine") {
		t.Fatalf("registry rows=%#v err=%v; want the old line and the queued one, complete", rows, err)
	}
	want := pendingCopiesHeader + "\n" + other + "\t/x/theirs\tworker\tp1\t2026-01-01T00:00:00Z\t-\tmutation-copy\tt.md\tr.md\n"
	if got := readRaw(t, queue); got != want {
		t.Fatalf("queue=%q; want only the other state dir's line, intact", got)
	}
	// The other state dir takes its own line, and the queue goes away.
	res, err = IncorporatePendingCopies(other, "ws", env, acceptCopy)
	if err != nil || res.Moved != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(queue); !os.IsNotExist(err) {
		t.Fatalf("an empty queue stays: %v", err)
	}
	if rows, _ := ReadCopies(other); len(rows) != 1 || rows[0].Path != "/x/theirs" {
		t.Fatalf("other registry=%#v", rows)
	}
}

func TestIncorporatePendingCopiesDropsRefusedLines(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	for _, path := range []string{"/x/good", "/x/refused"} {
		if _, err := QueueCopy(env, "ws", sd, pendingCopy(path)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := IncorporatePendingCopies(sd, "ws", env, func(row CopyRow) (CopyRow, string) {
		if row.Path == "/x/refused" {
			return row, "does not exist"
		}
		row.Path += "-resolved"
		return row, ""
	})
	if err != nil || res.Moved != 1 || len(res.Dropped) != 1 || res.Dropped[0] != (PendingDrop{What: "/x/refused", Cause: "does not exist"}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if rows, _ := ReadCopies(sd); len(rows) != 1 || rows[0].Path != "/x/good-resolved" {
		t.Fatalf("registry rows=%#v; want the checked row only", rows)
	}
	if _, err := os.Stat(res.Queue); !os.IsNotExist(err) {
		t.Fatalf("the refused line stayed in the queue: %v", err)
	}
}

func TestIncorporatePendingProcsMovesOnlyThisStateDir(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	other := t.TempDir()
	mine := ProcRow{Pid: 4242, Started: "s1", Name: "sleep", Owner: "worker", Pane: "p1", Created: "c"}
	if _, err := QueueProc(env, "ws", sd, mine); err != nil {
		t.Fatal(err)
	}
	queue, err := QueueProc(env, "ws", other, ProcRow{Pid: 4343, Started: "s2", Name: "sleep", Owner: "w", Pane: "p", Created: "c"})
	if err != nil {
		t.Fatal(err)
	}
	before := readRaw(t, queue)
	res, err := IncorporatePendingProcs(sd, "ws", env)
	if err != nil || res.Moved != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if rows, _ := ReadProcs(sd); len(rows) != 1 || rows[0] != mine {
		t.Fatalf("registry rows=%v; want the queued line", rows)
	}
	lines := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
	if got := readRaw(t, queue); got != lines[0]+"\n"+lines[2]+"\n" {
		t.Fatalf("queue=%q; want the other state dir's line only", got)
	}
}

func TestIncorporationWithNothingDueTakesNoLock(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	old := CopiesLockTimeout
	CopiesLockTimeout = 100 * time.Millisecond
	t.Cleanup(func() { CopiesLockTimeout = old })
	// A held registry lock: an incorporation that took it would fail.
	if err := os.WriteFile(filepath.Join(sd, copiesLockName), []byte("1 now\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy); err != nil || res.Moved != 0 {
		t.Fatalf("no queue: res=%+v err=%v", res, err)
	}
	if _, err := QueueCopy(env, "ws", t.TempDir(), pendingCopy("/x/theirs")); err != nil {
		t.Fatal(err)
	}
	if res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy); err != nil || res.Moved != 0 {
		t.Fatalf("only another state dir's line: res=%+v err=%v", res, err)
	}
	// With a line for this state dir the held lock is the error, and the
	// queue stays.
	queue, err := QueueCopy(env, "ws", sd, pendingCopy("/x/mine"))
	if err != nil {
		t.Fatal(err)
	}
	before := readRaw(t, queue)
	if _, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy); err == nil || !strings.Contains(err.Error(), "copies: registry is locked") {
		t.Fatalf("err=%v; want the registry lock error", err)
	}
	if got := readRaw(t, queue); got != before {
		t.Fatalf("a failed incorporation changed the queue: %q -> %q", before, got)
	}
}

// A queue writer that arrives while an incorporation sits between its queue
// read and its rewrite waits on the queue lock (it never takes the
// registry's), and its line lands in the queue after the rewrite: the next
// incorporation registers it.
func TestQueueWriteDuringAnIncorporationIsNotLost(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/first")); err != nil {
		t.Fatal(err)
	}
	inside := make(chan struct{})
	release := make(chan struct{})
	pendingIncorporateHook = func(string) {
		close(inside)
		<-release
	}
	t.Cleanup(func() { pendingIncorporateHook = nil })
	type outcome struct {
		res PendingResult
		err error
	}
	incorporated := make(chan outcome, 1)
	go func() {
		res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy)
		incorporated <- outcome{res, err}
	}()
	select {
	case <-inside:
	case <-time.After(10 * time.Second):
		t.Fatal("the incorporation never reached the hook")
	}
	written := make(chan error, 1)
	go func() {
		_, err := QueueCopy(env, "ws", sd, pendingCopy("/x/second"))
		written <- err
	}()
	select {
	case err := <-written:
		close(release)
		t.Fatalf("the writer did not wait for the queue lock (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	got := <-incorporated
	if got.err != nil || got.res.Moved != 1 {
		t.Fatalf("first incorporation res=%+v err=%v", got.res, got.err)
	}
	if err := <-written; err != nil {
		t.Fatalf("the writer failed: %v", err)
	}
	pendingIncorporateHook = nil
	if res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy); err != nil || res.Moved != 1 {
		t.Fatalf("second incorporation res=%+v err=%v", res, err)
	}
	rows, err := ReadCopies(sd)
	if err != nil || len(rows) != 2 || rows[0].Path != "/x/first" || rows[1].Path != "/x/second" {
		t.Fatalf("registry rows=%#v err=%v; want both lines", rows, err)
	}
	if _, err := os.Stat(PendingCopiesFile(env, "ws")); !os.IsNotExist(err) {
		t.Fatalf("the queue stays: %v", err)
	}
}

func TestMeasureFootprintCountsTheQueueReadOnly(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir(), "PATH": os.Getenv("PATH")}
	stateDir := t.TempDir()
	copyDir := filepath.Join(t.TempDir(), "queued")
	seedFile(t, filepath.Join(copyDir, "f.bin"), 2048)
	queue, err := QueueCopy(env, "ws", stateDir, pendingCopy(copyDir))
	if err != nil {
		t.Fatal(err)
	}
	// The same copy queued for another state dir does not count here.
	if _, err := QueueCopy(env, "ws", t.TempDir(), pendingCopy(filepath.Join(t.TempDir(), "x"))); err != nil {
		t.Fatal(err)
	}
	cmd := startFootprintHelper(t, "sleeper")
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	started := waitRunning(t, cmd.Process.Pid, env)
	procQueue, err := QueueProc(env, "ws", stateDir, ProcRow{Pid: cmd.Process.Pid, Started: started, Name: "sleeper", Owner: "a", Pane: "p", Created: "c"})
	if err != nil {
		t.Fatal(err)
	}
	copiesBefore, procsBefore := readRaw(t, queue), readRaw(t, procQueue)
	fp := MeasureFootprint(stateDir, "ws", env)
	if fp.Copies != 1 || fp.CopiesBytes != 2048 || fp.Running != 1 {
		t.Fatalf("footprint=%+v; want the queued copy and process counted", fp)
	}
	if readRaw(t, queue) != copiesBefore || readRaw(t, procQueue) != procsBefore {
		t.Fatal("the footprint changed a queue")
	}
	for _, path := range []string{CopiesFile(stateDir), ProcsFile(stateDir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("the footprint wrote %s: %v", path, err)
		}
	}
	// A copy both registered and queued counts once.
	if err := WriteCopies(stateDir, []CopyRow{pendingCopy(copyDir)}); err != nil {
		t.Fatal(err)
	}
	if fp := MeasureFootprint(stateDir, "ws", env); fp.Copies != 1 {
		t.Fatalf("copies=%d; want the registered and queued copy once", fp.Copies)
	}
}
