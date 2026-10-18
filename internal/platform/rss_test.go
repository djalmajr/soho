package platform

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// rssHelperRoleEnv is the role of the re-executed test binary that stands
// in for a process whose RSS the reader measures.
const rssHelperRoleEnv = "SOHO_RSS_HELPER_ROLE"

func TestRSSHelper(t *testing.T) {
	if os.Getenv(rssHelperRoleEnv) == "" {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(1)
	}
	_ = w
	buf := make([]byte, 1)
	_, _ = r.Read(buf)
	os.Exit(1) // unreachable: the read blocks until a signal stops the process
}

func TestReadProcessRSS(t *testing.T) {
	env := Env{"PATH": os.Getenv("PATH")}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRSSHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), rssHelperRoleEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// A just started process can miss the first by-pid read; poll.
	var rss int64
	var ok bool
	var liveness ProcLiveness
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, _, liveness = ReadProcFull(pid, env)
		if liveness == ProcRunning {
			rss, ok = ReadProcessRSS(pid, env)
			if ok {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the helper child never read as running (liveness %v, rss %d ok %t)", liveness, rss, ok)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rss <= 0 {
		t.Fatalf("rss=%d for the running child, want above zero", rss)
	}

	// A gone pid is unreadable, never a size: use a child that has exited.
	gone := exec.Command(os.Args[0], "-test.run=^TestRSSHelper$", "-test.count=1")
	// no role env: the child runs the helper with the role unset and exits.
	if err := gone.Start(); err != nil {
		t.Fatal(err)
	}
	_ = gone.Wait()
	if _, ok := ReadProcessRSS(gone.Process.Pid, env); ok {
		t.Fatalf("a gone pid read as having an RSS")
	}
}
