//go:build darwin || linux

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// orphanTestKill stops a process this test started, only while it still
// has the start identity the test read for it.
func orphanTestKill(pid int, identity string) {
	if now, _, live := ProcStartIdentity(pid, EnvFromOS()); live == ProcRunning && now == identity {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// orphanTestParent names the parent of pid for a failure message: its pid
// and, on Linux, its comm (never a command line).
func orphanTestParent(pid int) string {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "unreadable (" + err.Error() + ")"
	}
	ppid := strings.TrimSpace(string(out))
	if runtime.GOOS == "linux" {
		comm, _ := os.ReadFile("/proc/" + ppid + "/comm")
		return ppid + " (" + strings.TrimSpace(string(comm)) + ")"
	}
	return ppid
}

// TestUserOrphanProcessesFindsADoubleForkedSleep runs a real orphan: a
// `sh -c 'sleep 600 &'` that exits leaves the sleep to the reaper, with
// its cwd in a temporary directory. The scan finds it with that cwd and
// its start identity; a child of this test with the same cwd is not an
// orphan and is not listed. Both processes are stopped in the cleanup,
// also when an assertion fails.
func TestUserOrphanProcessesFindsADoubleForkedSleep(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "600")
	child.Dir = dir
	if err := child.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	launcher := exec.Command("sh", "-c", "sleep 600 >/dev/null 2>&1 & echo $!")
	launcher.Dir = dir
	out, err := launcher.Output()
	if err != nil {
		t.Fatalf("double fork: %v", err)
	}
	orphan, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || orphan <= 0 {
		t.Fatalf("double fork printed %q", out)
	}
	identity, _, live := ProcStartIdentity(orphan, EnvFromOS())
	if live != ProcRunning {
		t.Fatalf("the orphan %d is not running", orphan)
	}
	t.Cleanup(func() { orphanTestKill(orphan, identity) })

	// The scan is repeated until the orphan shows with the name sleep: the
	// shell forks it first and the exec of sleep comes after, so an early
	// scan can see the pid under the shell's name (or not yet reparented).
	var found *OrphanProc
	var list []OrphanProc
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err = UserOrphanProcesses()
		if err != nil {
			t.Fatalf("UserOrphanProcesses: %v", err)
		}
		found = nil
		for i := range list {
			if list[i].Pid == orphan {
				found = &list[i]
			}
		}
		if (found != nil && found.Name == "sleep") || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("the double-forked sleep %d is not listed; its parent is %s (the scan takes pid 1 and, on Linux, a systemd of this uid)", orphan, orphanTestParent(orphan))
	}
	if found.Cwd != dir {
		t.Fatalf("cwd of %d = %q; want %q", orphan, found.Cwd, dir)
	}
	if found.Identity != identity || found.Name != "sleep" {
		t.Fatalf("orphan = %+v; want identity %q and name sleep", *found, identity)
	}
	if found.Started.IsZero() || time.Since(found.Started) > time.Minute || time.Until(found.Started) > time.Second {
		t.Fatalf("started = %v; want about now", found.Started)
	}
	if cwd, err := ProcCwd(orphan); err != nil || cwd != dir {
		t.Fatalf("ProcCwd(%d) = %q, %v; want %q", orphan, cwd, err, dir)
	}
	for _, entry := range list {
		if entry.Pid == child.Process.Pid {
			t.Fatalf("the child %d of this test is listed as an orphan: %+v", child.Process.Pid, entry)
		}
	}
}

// TestStopProcessTreeGuardedChecksBeforeEachRound runs a real shell that
// ignores TERM: the guard passes before the TERM round and fails before
// the KILL round, so the call returns ErrStopGuardChanged and the shell
// is never killed. A nil guard on a second shell keeps StopProcessTree's
// behavior: TERM ignored, KILL sent, the tree gone.
func TestStopProcessTreeGuardedChecksBeforeEachRound(t *testing.T) {
	env := EnvFromOS()
	start := func(t *testing.T) (int, string) {
		t.Helper()
		ready := filepath.Join(t.TempDir(), "ready")
		cmd := exec.Command("sh", "-c", `trap '' TERM; : > "$1"; while :; do sleep 0.1; done`, "sh", ready)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the shell did not install its TERM trap")
			}
			time.Sleep(10 * time.Millisecond)
		}
		started, live := ReadProc(cmd.Process.Pid, env)
		if live != ProcRunning {
			t.Fatalf("the shell %d is not running", cmd.Process.Pid)
		}
		return cmd.Process.Pid, started
	}
	t.Run("a guard that fails before the KILL round sends no KILL", func(t *testing.T) {
		pid, started := start(t)
		calls := 0
		err := StopProcessTreeGuarded(pid, started, env, func() bool { calls++; return calls == 1 })
		if !errors.Is(err, ErrStopGuardChanged) || calls != 2 {
			t.Fatalf("StopProcessTreeGuarded = %v after %d guard calls; want ErrStopGuardChanged after 2", err, calls)
		}
		if _, live := ReadProc(pid, env); live != ProcRunning {
			t.Fatalf("the shell %d was killed although the guard failed before the KILL round", pid)
		}
	})
	t.Run("a nil guard is StopProcessTree", func(t *testing.T) {
		pid, started := start(t)
		if err := StopProcessTreeGuarded(pid, started, env, nil); err != nil {
			t.Fatalf("StopProcessTreeGuarded(nil guard) = %v", err)
		}
		if _, live := ReadProc(pid, env); live == ProcRunning {
			t.Fatalf("the shell %d survived the KILL round", pid)
		}
	})
}
