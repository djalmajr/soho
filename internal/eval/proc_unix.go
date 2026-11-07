//go:build !windows

package eval

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// probeWaitDelay bounds how long the runner waits for the go test process to
// release inherited pipes after the owned process tree has terminated; it is
// the runner-side counterpart of the go toolchain's own WaitDelay.
const probeWaitDelay = 5 * time.Second

// runOwnedGoProcess runs the go test command as an owned process group: the
// whole tree (go, the compiled test binary and any worker children) is in a
// new process group, cancellation or the bounded deadline kills that entire
// group with SIGKILL, and WaitDelay bounds the pipe release wait. The
// temporary work directories (TMPDIR/TEMP/TMP) are expected to point inside
// the runner-owned temporary copy so cancelled go-build work is removed with
// it.
func runOwnedGoProcess(ctx context.Context, goExecutable string, dir string, args []string, env []string, stdout, stderr interface{ Write([]byte) (int, error) }) error {
	_, err, verifyErr := runOwnedGroup(ctx, goExecutable, args, ownedGroupOptions{
		dir: dir, env: env, stdout: stdout, stderr: stderr,
		waitDelay: probeWaitDelay, verify: verifyOwnedGroupGone,
	})
	if verifyErr != nil {
		if err != nil {
			return errors.Join(err, verifyErr)
		}
		return verifyErr
	}
	return err
}

// processAlive reports whether a process with the given PID is running; the
// signal-0 probe is the standard Unix liveness check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to another user.
	return err == syscall.EPERM
}
