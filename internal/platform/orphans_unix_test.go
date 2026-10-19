//go:build darwin || linux

package platform

import (
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

	var found *OrphanProc
	var list []OrphanProc
	deadline := time.Now().Add(3 * time.Second)
	for found == nil && time.Now().Before(deadline) {
		list, err = UserOrphanProcesses()
		if err != nil {
			t.Fatalf("UserOrphanProcesses: %v", err)
		}
		for i := range list {
			if list[i].Pid == orphan {
				found = &list[i]
			}
		}
		if found == nil {
			time.Sleep(50 * time.Millisecond)
		}
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
