//go:build !windows

package eval

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// runOwnedProcess runs the owned process group (the Unix half of
// RunOwnedProcess). The semantics mirror runOwnedGoProcess: the whole tree
// is in a new process group, cancellation of ctx kills that entire group
// with SIGKILL, and a green run re-kills the group and proves it empty —
// cmd.Wait reaps only the leader, and a detached grandchild (a
// backgrounded worker re-parented out of the tree) would otherwise
// outlive the run.
func runOwnedProcess(ctx context.Context, executable string, args []string, opts OwnedProcessOptions) (OwnedProcessResult, error) {
	if opts.Output == nil {
		return OwnedProcessResult{}, fmt.Errorf("runOwnedProcess: Output is required")
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = opts.Dir
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}
	// The child's stdout and stderr are the same destination: the bytes
	// land there in the order the child writes them, and the runner never
	// buffers them.
	cmd.Stdout = opts.Output
	cmd.Stderr = opts.Output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancellation ends the whole group, not only the direct process: a
	// descendant (a test suite, a backgrounded child) must not outlive the
	// cancellation. The kill sites run only for pid > 1: -1 would be the
	// whole-system signal and -0 the invalid group.
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 1 {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if opts.WaitDelay > 0 {
		cmd.WaitDelay = opts.WaitDelay
	}
	err := cmd.Run()
	res := OwnedProcessResult{Started: cmd.Process != nil}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ProcessState.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signaled = true
			res.Signal = int(ws.Signal())
		}
	}
	// The group kill and the bounded verification run on EVERY started
	// run, not only on a green return (DJA-695 F1): an unsuccessful exit
	// — a nonzero shell exit, a shell killed by a signal — can still
	// leave a detached grandchild behind (a backgrounded `sleep 300` the
	// shell abandoned on `exit 3`), and that descendant would outlive the
	// layer and keep writing into the inherited evidence descriptor.
	// runOwnedGoProcess does the same on every return; the exit code and
	// the signal were read from the wait result above, preserved by this
	// order. A failed verification goes in res.CleanupErr, beside the exit
	// information, so a caller can never take an unproven end for a success.
	// Identity and ownership: this run created the group (Setpgid) with its
	// own child as the leader, so -pid can only refer to the group this run
	// owns. POSIX exposes no generation token for this captured PGID; once its
	// last member exits, reuse before the final signal can target a new group
	// — this best-effort cleanup does not eliminate that race. A member that
	// calls setsid leaves the group by design (the escape declared by this
	// package).
	if cmd.Process != nil {
		if cmd.Process.Pid > 1 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		res.CleanupErr = verifyOwnedGroup(cmd.Process.Pid)
	}
	return res, err
}

// verifyOwnedGroup is the group-gone proof; a test replaces it to make the
// proof fail deterministically.
var verifyOwnedGroup = verifyOwnedGroupGone
