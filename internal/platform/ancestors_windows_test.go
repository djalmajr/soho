//go:build windows

package platform

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestProcessAncestorsRealChildWindows walks the parent chain of a real
// child of this test process (a short-lived cmd.exe) and requires the
// test's own pid to be in it. The walk reads one Toolhelp32 process
// snapshot (pid and parent fields only), never a command line.
func TestProcessAncestorsRealChildWindows(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "timeout", "/t", "2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(200 * time.Millisecond) // let cmd.exe reach its prompt
	chain, err := ProcessAncestors(cmd.Process.Pid, Env{})
	if err != nil {
		t.Fatalf("ProcessAncestors(%d)=%v", cmd.Process.Pid, err)
	}
	if len(chain) == 0 || chain[0] != cmd.Process.Pid {
		t.Fatalf("chain does not start at the child: %v", chain)
	}
	found := false
	for _, pid := range chain {
		if pid == os.Getpid() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the test pid is not among the child's ancestors %v", chain)
	}
}
