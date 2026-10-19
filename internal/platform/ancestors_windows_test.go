//go:build windows

package platform

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestProcessAncestorsRealChildWindows walks the parent chain of a real
// child of this test process (a ping that runs for about 5 s) and requires
// the test's own pid to be in it. The walk reads one Toolhelp32 process
// snapshot (pid and parent fields only), never a command line. The child
// is not `timeout`: without a console it exits at once, and a child that
// is already gone when the snapshot is taken is not in it. Along the
// chain, every parent was created no later than its child.
func TestProcessAncestorsRealChildWindows(t *testing.T) {
	cmd := exec.Command("ping", "-n", "6", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(200 * time.Millisecond) // let the child start
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
	// Every child -> parent pair of the chain has the parent created at
	// the same instant as the child or before it (no reused pid). Only the
	// last pid may be unreadable: a parent that already exited.
	for i := 0; i+1 < len(chain); i++ {
		child, parent := chain[i], chain[i+1]
		childAt, ok := windowsAncestorCreated(child)
		if !ok {
			t.Fatalf("creation time of %d (chain %v) is unreadable", child, chain)
		}
		parentAt, ok := windowsAncestorCreated(parent)
		if !ok {
			if i+2 == len(chain) {
				continue
			}
			t.Fatalf("creation time of %d (chain %v) is unreadable", parent, chain)
		}
		if parentAt > childAt {
			t.Fatalf("parent %d created at %d after its child %d at %d (chain %v)", parent, parentAt, child, childAt, chain)
		}
	}
}
