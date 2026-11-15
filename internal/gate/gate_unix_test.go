//go:build !windows

package gate

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s (pid %d) outlived the gate", what, pid)
}

func assertReaped(t *testing.T, pid int) {
	t.Helper()
	// A started process whose Wait returned leaves no zombie behind.
	if !processGone(pid) {
		t.Fatalf("pid %d is still present after the gate returned", pid)
	}
}

func assertEndedBySignal(t *testing.T, res resultRecord, name string) {
	t.Helper()
	if res.Signal != name || res.NativeExit != -1 {
		t.Fatalf("want signal %s and native_exit -1, got %+v", name, res)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the program never recorded its grandchild: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("pid file %q", data)
	}
	return pid
}

func TestBoundExpiryEndsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	pidFile := filepath.Join(dir, "grandchild.pid")
	got := runGate(t, prefix, "2s", helperArgs("tree", pidFile)...)
	if got.code != 124 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	res := readResult(t, prefix)
	if !res.ContextExpired || !res.WaitJoined {
		t.Fatalf("result = %+v", res)
	}
	assertEndedBySignal(t, res, "SIGTERM")
	grandchild := readPID(t, pidFile)
	// The grandchild shares the group the gate created: gone by the time the
	// started handle was waited.
	waitGone(t, grandchild, "grandchild")
	assertReaped(t, res.PID)
}

func TestExpiryKillsAGroupMemberThatIgnoresTerminationAfterTheLeaderExited(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	pidFile := filepath.Join(dir, "grandchild.pid")
	got := runGate(t, prefix, "2s", helperArgs("tree-leader-exits", pidFile)...)
	if got.code != 124 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	waitGone(t, readPID(t, pidFile), "grandchild that ignores SIGTERM")
}

func TestExpiryKillsAProgramThatIgnoresTermination(t *testing.T) {
	old := termGrace
	termGrace = 400 * time.Millisecond
	defer func() { termGrace = old }()
	prefix := filepath.Join(t.TempDir(), "run")
	began := time.Now()
	got := runGate(t, prefix, "1s", helperArgs("ignoreterm")...)
	if got.code != 124 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	res := readResult(t, prefix)
	if !res.ContextExpired || !res.WaitJoined {
		t.Fatalf("result = %+v", res)
	}
	assertEndedBySignal(t, res, "SIGKILL")
	if elapsed := time.Since(began); elapsed < 1400*time.Millisecond || elapsed > 20*time.Second {
		t.Fatalf("elapsed %v: the grace was not waited before the kill", elapsed)
	}
	assertReaped(t, res.PID)
}

func TestProgramKilledBySignalExits125(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "run")
	got := runGate(t, prefix, "30s", helperArgs("self-kill")...)
	res := readResult(t, prefix)
	if got.code != 125 || res.ContextExpired || !res.WaitJoined {
		t.Fatalf("code=%d result=%+v", got.code, res)
	}
	assertEndedBySignal(t, res, "SIGKILL")
	if want := "pid=" + strconv.Itoa(res.PID) + " native_exit=-1 context_expired=false wait_joined=true\n"; got.out != want {
		t.Fatalf("stdout %q, want %q", got.out, want)
	}
}

func TestArtifactModesAreExactly0600UnderAnyUmask(t *testing.T) {
	for _, mask := range []int{0o000, 0o077, 0o277} {
		prefix := filepath.Join(t.TempDir(), "run")
		old := syscall.Umask(mask)
		got := runGate(t, prefix, "30s", helperArgs("echo")...)
		syscall.Umask(old)
		if got.code != 0 {
			t.Fatalf("umask %04o: code=%d stderr=%q", mask, got.code, got.errOut)
		}
		for _, suffix := range []string{".command.json", ".stdout", ".stderr", ".result.json"} {
			info, err := os.Stat(prefix + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("umask %04o: %s mode %v, want 0600", mask, suffix, info.Mode().Perm())
			}
		}
	}
}

func TestExistingDanglingSymlinkIsRefusedAndNotFollowed(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	target := filepath.Join(dir, "elsewhere")
	if err := os.Symlink(target, prefix+".stdout"); err != nil {
		t.Fatal(err)
	}
	got := runGate(t, prefix, "30s", helperArgs("echo")...)
	if got.code != 2 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Fatal("the link target was created")
	}
	if names := listDir(t, dir); len(names) != 1 || names[0] != "run.stdout" {
		t.Fatalf("directory = %v", names)
	}
}

// An interruption of the gate itself must end the child's own group too: the
// child does not share the terminal's group, so nothing else would.
func TestInterruptingTheGateEndsTheChildGroup(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	pidFile := filepath.Join(dir, "grandchild.pid")
	bound := "1h"
	if os.Getenv("GATE_INTERRUPT_FAILURE") != "" {
		// A removed cleanup must fail the parent test, but still converge
		// without leaving a live supervisor behind during mutation runs.
		bound = "10s"
	}
	cmd := exec.Command(os.Args[0], helperFlag+"gate", prefix, bound, "--", os.Args[0], helperFlag+"tree", pidFile)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			// Let the gate end its owned group before reaping it. If it
			// stalls, killing only this exact supervisor activates the
			// fixture leader's parent-loss fallback for the other group.
			if os.Getenv("GATE_INTERRUPT_KILL") == "1" {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			if data, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(string(data)); err == nil {
					waitGone(t, pid, "failure-cleanup grandchild")
				}
			}
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the program never published readiness")
		}
		time.Sleep(20 * time.Millisecond)
	}
	grandchild := readPID(t, pidFile)
	if path := os.Getenv("GATE_INTERRUPT_FAILURE"); path != "" {
		if err := publishPID(path, grandchild); err != nil {
			t.Fatal(err)
		}
		if err := publishPID(path+".supervisor", cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		if err := publishPID(path+".leader", readPID(t, pidFile+".leader")); err != nil {
			t.Fatal(err)
		}
		t.Fatal("forced interrupt-test failure after readiness")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := <-done
	waited = true
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 125 {
		t.Fatalf("gate ended with %v stdout=%q stderr=%q, want exit 125", err, out.String(), errOut.String())
	}
	res := readResult(t, prefix)
	if res.ContextExpired || !res.WaitJoined || res.Signal != "SIGTERM" {
		t.Fatalf("result = %+v", res)
	}
	waitGone(t, grandchild, "grandchild")
	assertReaped(t, res.PID)
}

func TestInterruptFailureCleanup(t *testing.T) {
	for _, kill := range []string{"0", "1"} {
		t.Run(kill, func(t *testing.T) { checkInterruptFailureCleanup(t, kill) })
	}
}

func checkInterruptFailureCleanup(t *testing.T, kill string) {
	path := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestInterruptingTheGateEndsTheChildGroup$")
	cmd.Env = append(os.Environ(), "GATE_INTERRUPT_FAILURE="+path, "GATE_INTERRUPT_KILL="+kill)
	output, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("forced interrupt-test failure after readiness")) {
		t.Fatalf("failure fixture: err=%v output=%s", err, output)
	}
	for _, path := range []string{path, path + ".leader", path + ".supervisor"} {
		pid := readPID(t, path)
		if !processGone(pid) {
			t.Errorf("failure cleanup left pid %d present at test exit", pid)
		}
		// Do not signal an identity saved by another process. The fixture's
		// own gate bound/parent-loss fallback cleans even a removed defer.
		deadline := time.Now().Add(18 * time.Second)
		for !processGone(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !processGone(pid) {
			t.Fatalf("fixture pid %d did not end within its own bound", pid)
		}
	}
}

// waitid can observe an exited child repeatedly until its parent reaps it.
// At every group signal the gate must still own that waitable identity.
func TestGroupSignalsKeepTheLeaderWaitable(t *testing.T) {
	old := groupSignal
	defer func() { groupSignal = old }()
	for _, alreadyExited := range []bool{false, true} {
		t.Run(strconv.FormatBool(alreadyExited), func(t *testing.T) {
			cmd := exec.Command(helperArgs("sleep")[0], helperArgs("sleep")[1:]...)
			prepare(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			if alreadyExited {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := observeExitOptions(cmd, 0); err != nil {
					t.Fatal(err)
				}
			}
			kills := 0
			groupSignal = func(pid int, sig syscall.Signal) error {
				// WNOHANG makes this an ownership observation even at the
				// initial SIGTERM, when the process can still be running.
				if err := observeExitOptions(cmd, syscall.WNOHANG); err != nil {
					t.Errorf("signal %v released leader identity: %v", sig, err)
					return err // A mutant must not actually signal a released identity.
				}
				if sig == syscall.SIGKILL {
					kills++
				}
				return old(pid, sig)
			}
			done := make(chan error, 1)
			go func() { done <- observeExit(cmd) }()
			_ = reap(cmd, terminate(cmd, done))
			waited = true
			if kills != 1 {
				t.Fatalf("kills=%d", kills)
			}
			if err := observeExitOptions(cmd, syscall.WNOHANG); !errors.Is(err, syscall.ECHILD) {
				t.Fatalf("leader was not reaped exactly once: %v", err)
			}
		})
	}
}
