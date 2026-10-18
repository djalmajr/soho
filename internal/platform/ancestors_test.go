//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// realAncestorEnv returns the host environment (its PATH holds the real
// ps) as an Env.
func realAncestorEnv() Env {
	env := Env{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

// installFakePSTable installs a fake ps in dir that answers
// `ps -o ppid= -p <pid>` for every pid in the table (an unscripted pid is
// a non-zero exit, like a real ps for a pid that is not running).
func installFakePSTable(t *testing.T, dir string, table map[string]string) error {
	t.Helper()
	rules := make([]fakecli.Rule, 0, len(table))
	for pid, out := range table {
		rules = append(rules, fakecli.Rule{Argv: []string{"-o", "ppid=", "-p", pid}, Stdout: out})
	}
	_, err := fakecli.Install(t, dir, "ps", rules)
	return err
}

// fakeAncestorEnv points PATH at dir (the fake ps) and nothing else.
func fakeAncestorEnv(t *testing.T, dir string) Env {
	t.Helper()
	env := Env{}
	for _, item := range fakecli.Env(nil, dir) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["TMPDIR"] = t.TempDir()
	return env
}

// TestProcessAncestorsRealChild walks the parent chain of a real child of
// this test process and requires the test's own pid to be in it: the child
// is a short-lived /bin/sh, so the chain is [child, this process, ...].
// Only parent pids are read (ps -o ppid=), never a command line.
func TestProcessAncestorsRealChild(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(100 * time.Millisecond) // let the shell reach its prompt
	chain, err := ProcessAncestors(cmd.Process.Pid, realAncestorEnv())
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
		t.Fatalf("the test pid %d is not among the child's ancestors %v", os.Getpid(), chain)
	}
}

// TestProcessAncestorsGonePidIsAnError: a pid whose parent cannot be read
// (the process is gone) fails the walk instead of silently ending it.
func TestProcessAncestorsGonePidIsAnError(t *testing.T) {
	// Start and reap a child, then query the dead pid: ps exits non-zero
	// for a pid that is not running, so the read is a failure, not a root.
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	gone := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reap child: %v", err)
	}
	if _, err := ProcessAncestors(gone, realAncestorEnv()); err == nil {
		t.Fatalf("ProcessAncestors(%d) succeeded for a reaped pid", gone)
	}
}

// TestProcessAncestorsFakePSChain exercises the per-hop ps reads through a
// scripted ps (the same fake pattern codexenv uses): the chain is exactly
// the scripted ppid sequence, root included, with no read of any command
// line.
func TestProcessAncestorsFakePSChain(t *testing.T) {
	t.Run("POSIX: the chain follows the ppid reads to the root", func(t *testing.T) {
		dir := t.TempDir()
		self := strconv.Itoa(os.Getpid())
		table := map[string]string{
			self:   "4000\n",
			"4000": "3000\n",
			"3000": "1\n",
		}
		if err := installFakePSTable(t, dir, table); err != nil {
			t.Fatal(err)
		}
		chain, err := ProcessAncestors(os.Getpid(), fakeAncestorEnv(t, dir))
		if err != nil {
			t.Fatalf("ProcessAncestors=%v", err)
		}
		want := []int{os.Getpid(), 4000, 3000, 1}
		if len(chain) != len(want) {
			t.Fatalf("chain=%v want %v", chain, want)
		}
		for i := range want {
			if chain[i] != want[i] {
				t.Fatalf("chain=%v want %v", chain, want)
			}
		}
	})
	t.Run("POSIX: a parent of 0 ends the chain at the parentless pid", func(t *testing.T) {
		dir := t.TempDir()
		self := strconv.Itoa(os.Getpid())
		table := map[string]string{self: "4000\n", "4000": "0\n"}
		if err := installFakePSTable(t, dir, table); err != nil {
			t.Fatal(err)
		}
		chain, err := ProcessAncestors(os.Getpid(), fakeAncestorEnv(t, dir))
		if err != nil {
			t.Fatalf("ProcessAncestors=%v", err)
		}
		want := []int{os.Getpid(), 4000}
		if len(chain) != len(want) || chain[0] != want[0] || chain[1] != want[1] {
			t.Fatalf("chain=%v want %v", chain, want)
		}
	})
}
