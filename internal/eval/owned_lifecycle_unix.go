//go:build !windows

package eval

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"
)

type ownedGroupOptions struct {
	dir            string
	env            []string
	stdout, stderr io.Writer
	waitDelay      time.Duration
	verify         func(int) error
}

// runOwnedGroup owns command execution, group cancellation and final group
// cleanup for both runners. Run and verification errors stay separate so
// each wrapper can retain its existing error contract. The verifier is
// selected explicitly: probes use the direct proof, validation its test seam.
func runOwnedGroup(ctx context.Context, executable string, args []string, opts ownedGroupOptions) (OwnedProcessResult, error, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = opts.dir
	cmd.Env = opts.env
	cmd.Stdout = opts.stdout
	cmd.Stderr = opts.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Never signal -1 (all processes) or 0 (the caller's group).
		if cmd.Process == nil || cmd.Process.Pid <= 1 {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = opts.waitDelay
	err := cmd.Run()
	// Capture the child's exit information before cleanup, including when
	// verification will fail. Wait only reaps the direct group leader.
	res := OwnedProcessResult{Started: cmd.Process != nil}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ProcessState.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signaled = true
			res.Signal = int(ws.Signal())
		}
	}
	var verifyErr error
	if cmd.Process != nil {
		// Every started run ends the group, including nonzero/signal exits
		// and green runs with detached grandchildren. This run created the
		// group with its child as leader (PGID == PID). POSIX has no group
		// generation token: reuse after its last member exits remains a
		// best-effort race, and a member calling setsid escapes by design.
		if cmd.Process.Pid > 1 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		// Bounded proof waits for descendants still in the kernel's exit
		// path; an unverified group returns an explicit cleanup failure.
		verifyErr = opts.verify(cmd.Process.Pid)
	}
	return res, err, verifyErr
}
