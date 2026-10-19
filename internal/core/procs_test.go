//go:build !windows

package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The two review probes of review-2-20261003T173031 (findings 1 and 2),
// now durable: a fake ps (the shared fakecli re-execution) and real sleeper
// processes owned by the test (killed in the cleanup), StopProcRow as the
// entry point. The fake ps sits first on PATH; ps resolves from the system
// directories and nothing else (no shell, interpreter or sleep on the
// restricted env).

// The sleeper is the test binary itself, re-executed in a fixture role:
// no external sleep, no shell. The role rides in test-private environment
// (argv carries only the test flag).
const (
	procsSleeperRoleEnv = "HERDR_PROCS_SLEEPER_ROLE"
	procsSleeperTest    = "TestProcsSleeperHelper"
)

// TestProcsSleeperHelper is the re-executed helper entry: when the role env
// is set the fixture role runs and the process exits; in the main test run
// the env is unset and the function returns without running anything.
func TestProcsSleeperHelper(t *testing.T) {
	if os.Getenv(procsSleeperRoleEnv) != "sleeper" {
		return
	}
	// A long-lived process that dies from the default SIGTERM: the owned
	// stand-in for the old `sleep 300`. It blocks inside a real blocking
	// syscall (a read on a pipe whose write end this process keeps open):
	// a parked select is a self-declared deadlock, and the default
	// SIGTERM/SIGKILL still stop the process while it is blocked.
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture sleeper: pipe: %v\n", err)
		os.Exit(1)
	}
	_ = w // held open for the life of the process: the read never returns.
	buf := make([]byte, 1)
	_, _ = r.Read(buf)
	os.Exit(1) // unreachable: the read blocks until a signal stops the process
}

// sleeperExe places a copy of the test binary under dir/sleep (a hard link
// when supported, a copy otherwise) so the process table prints sleep as
// the command base name, exactly like the old fixture did.
func sleeperExe(t *testing.T, dir string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	target := filepath.Join(dir, "sleep")
	if err := os.Link(exe, target); err != nil {
		data, readErr := os.ReadFile(exe)
		if readErr != nil {
			t.Fatalf("reading the test binary: %v", readErr)
		}
		if writeErr := os.WriteFile(target, data, 0o700); writeErr != nil {
			t.Fatalf("writing the fixture copy: %v", writeErr)
		}
	}
	return target
}

// spawnSleeper starts the sleeper and owns it for the whole test (killed
// and reaped in the cleanup, also when an assertion failed).
func spawnSleeper(t *testing.T, exe string) int {
	t.Helper()
	cmd := exec.Command(exe, "-test.run=^TestProcsSleeperHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", procsSleeperRoleEnv + "=sleeper"}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the sleeper fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// sleeperGonePid is a sleeper the test killed and reaped: the pid is gone.
func sleeperGonePid(t *testing.T, exe string) int {
	t.Helper()
	cmd := exec.Command(exe, "-test.run=^TestProcsSleeperHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", procsSleeperRoleEnv + "=sleeper"}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the sleeper fixture: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // reap: the entry leaves the table (the kill's exit error is expected)
	return pid
}

// psOnlyDir returns a directory holding only a link to the system ps: the
// tail of the restricted PATH the fixture env carries (no shell,
// interpreter or sleep resolvable; ps is the only native command the
// production reads use).
func psOnlyDir(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ps")
	if err != nil {
		t.Fatalf("locate the system ps: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(path, filepath.Join(dir, "ps")); err != nil {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading the system ps: %v", readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(dir, "ps"), data, 0o755); writeErr != nil {
			t.Fatalf("copying the system ps: %v", writeErr)
		}
	}
	return dir
}

// installFakePs places the shared fakecli ps in dir with the given rules
// and returns the env the consumer must run with: the fake dir first on
// PATH (a ps-only directory after: the restricted env resolves ps and
// nothing else) and the fakecli config marker the re-execution needs to
// dispatch as the fake.
func installFakePs(t *testing.T, dir string, rules []fakecli.Rule) platform.Env {
	t.Helper()
	if _, err := fakecli.Install(t, dir, "ps", rules); err != nil {
		t.Fatal(err)
	}
	return platform.Env{"PATH": dir + ":" + psOnlyDir(t), "SOHO_FAKECLI_CONFIG": dir}
}

// TestReview92RootStartChangedDuringCollection: the consumer's registered
// identity read (lstart=,state=,comm=) gives the registered start, but
// the collection read (lstart=,ppid=,state=) gives another start. The
// root must not be signalled, StopProcRow must keep the line, and the
// stop must not be claimed.
func TestReview92RootStartChangedDuringCollection(t *testing.T) {
	dir := t.TempDir()
	pid := spawnSleeper(t, sleeperExe(t, dir))
	env := installFakePs(t, dir, []fakecli.Rule{
		{Argv: []string{"-A", "-o", "pid=,ppid="}, Stdout: fmt.Sprintf("%d %d\n", pid, os.Getpid())},
		{Argv: []string{"-o", "lstart=,state=,comm="}, ArgvPrefix: true, Stdout: "Sat Oct  3 00:00:00 2026 S sleep\n"},
		{Argv: []string{"-o", "lstart=,ppid=,state="}, ArgvPrefix: true, Stdout: fmt.Sprintf("Sat Oct  3 00:00:01 2026 %d S\n", os.Getpid())},
		{Argv: []string{"-o", "pid=,state="}, ArgvPrefix: true, Stdout: fmt.Sprintf("%d S\n", pid)},
	})
	row := ProcRow{Pid: pid, Started: "Sat Oct  3 00:00:00 2026", Name: "sleep", Owner: "ghost"}
	if err := WriteProcs(dir, []ProcRow{row}); err != nil {
		t.Fatal(err)
	}
	result := StopProcRow(dir, row, env)
	t.Logf("owned pid=%d result=%+v", pid, result)
	if err := syscall.Kill(pid, 0); err != nil || strings.Contains(result.Line, "stopped process") {
		t.Fatal("root was stopped despite changed start between registered identity check and collection")
	}
	if result.Dropped {
		t.Fatalf("StopProcRow = %+v; the line must stay on a divergent root", result)
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), fmt.Sprintf("process %d changed after the check", pid)) {
		t.Fatalf("StopProcRow = %+v; want the changed-after-the-check error", result)
	}
	rows, err := ReadProcs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Pid != pid {
		t.Fatalf("registry rows = %v; the line must stay registered", rows)
	}
}

// TestReview92UnreadableIdentityKeepsRegistration: a valid snapshot and a
// failed identity read (ps exits non-zero; the process is alive, so the
// kill(pid, 0) confirmation is not ESRCH). The read failure is not an
// absence: StopProcRow must report the error and keep the line, no
// signal, no drop.
func TestReview92UnreadableIdentityKeepsRegistration(t *testing.T) {
	dir := t.TempDir()
	pid := spawnSleeper(t, sleeperExe(t, dir))
	env := installFakePs(t, dir, []fakecli.Rule{
		{Argv: []string{"-A", "-o", "pid=,ppid="}, Stdout: fmt.Sprintf("%d %d\n", pid, os.Getpid())},
		{Argv: []string{"-o", "lstart=,state=,comm="}, ArgvPrefix: true, Stdout: "Sat Oct  3 00:00:00 2026 S sleep\n"},
		{Argv: []string{"-o", "lstart=,ppid=,state="}, ArgvPrefix: true, Stderr: "identity query failed\n", Code: 1},
		{Argv: []string{"-o", "pid=,state="}, ArgvPrefix: true, Stdout: fmt.Sprintf("%d S\n", pid)},
	})
	row := ProcRow{Pid: pid, Started: "Sat Oct  3 00:00:00 2026", Name: "sleep", Owner: "ghost"}
	if err := WriteProcs(dir, []ProcRow{row}); err != nil {
		t.Fatal(err)
	}
	result := StopProcRow(dir, row, env)
	t.Logf("owned pid=%d result=%+v", pid, result)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("fixture not alive")
	}
	if result.Err == nil || result.Dropped {
		t.Fatal("unreadable root identity reported stopped and dropped while owned process is alive")
	}
	if !strings.Contains(result.Err.Error(), fmt.Sprintf("process %d is unreadable; not stopped", pid)) {
		t.Fatalf("StopProcRow = %+v; want the unreadable error", result)
	}
	rows, err := ReadProcs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Pid != pid {
		t.Fatalf("registry rows = %v; the line must stay registered", rows)
	}
}

// TestProcRowStateUnknownWhenTheReadFails: a ps that fails every query
// (the process is alive) reads as unknown, never as gone — a failed read
// is not an absence.
func TestProcRowStateUnknownWhenTheReadFails(t *testing.T) {
	dir := t.TempDir()
	exe := sleeperExe(t, dir)
	pid := spawnSleeper(t, exe)
	env := installFakePs(t, dir, []fakecli.Rule{{AnyArgs: true, Code: 9}})
	row := ProcRow{Pid: pid, Started: "Sat Oct  3 00:00:00 2026", Name: "sleep", Owner: "ghost"}
	if st := ProcRowState(row, env); st != "unknown" {
		t.Fatalf("ProcRowState = %q; want unknown (a failed read is not an absence)", st)
	}
	// The proven absence reads as gone: a dead, reaped pid.
	deadPid := sleeperGonePid(t, exe)
	deadRow := ProcRow{Pid: deadPid, Started: "Sat Oct  3 00:00:00 2026", Name: "sleep", Owner: "ghost"}
	envDead := platform.Env{"PATH": psOnlyDir(t)}
	if st := ProcRowState(deadRow, envDead); st != "gone" {
		t.Fatalf("ProcRowState(dead) = %q; want gone", st)
	}
}

// TestStopUnregisteredProc pins the stop of a process outside the
// registry: still reporting a change keeps the process with no signal and
// before any stop; still reporting no change stops it through the
// registered stop (the tree is gone afterwards); a gone pid is a note, not
// an error; and procs.tsv is never written.
func TestStopUnregisteredProc(t *testing.T) {
	dir := t.TempDir()
	exe := sleeperExe(t, dir)
	env := platform.Env{"PATH": "/usr/bin:/bin"}
	t.Run("a change since the listing keeps the process", func(t *testing.T) {
		pid := spawnSleeper(t, exe)
		asked := 0
		res := StopUnregisteredProc(pid, "sleep", func() bool { asked++; return false }, env)
		if asked != 1 || res.Err != nil || res.Stopped || res.Line != fmt.Sprintf("kept process %d (it changed since the listing)", pid) {
			t.Fatalf("StopUnregisteredProc = %+v (still asked %d times); want the kept line", res, asked)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("the process %d was signalled although it changed: %v", pid, err)
		}
	})
	t.Run("an unchanged process is stopped", func(t *testing.T) {
		pid := spawnSleeper(t, exe)
		res := StopUnregisteredProc(pid, "sleep", func() bool { return true }, env)
		if res.Err != nil || !res.Stopped || res.Line != fmt.Sprintf("stopped process %d (sleep)", pid) {
			t.Fatalf("StopUnregisteredProc = %+v; want the stopped line", res)
		}
		if _, live := platform.ReadProc(pid, env); live == platform.ProcRunning {
			t.Fatalf("the process %d survived the stop", pid)
		}
	})
	t.Run("a gone pid is a note", func(t *testing.T) {
		pid := sleeperGonePid(t, exe)
		res := StopUnregisteredProc(pid, "sleep", func() bool { t.Fatal("still must not run for a gone pid"); return true }, env)
		if res.Err != nil || res.Line != fmt.Sprintf("process %d already gone", pid) {
			t.Fatalf("StopUnregisteredProc = %+v; want the gone note", res)
		}
	})
	if _, err := os.Stat(ProcsFile(dir)); !os.IsNotExist(err) {
		t.Fatalf("procs.tsv was written (%v); the stop of an unregistered process never touches the registry", err)
	}
}
