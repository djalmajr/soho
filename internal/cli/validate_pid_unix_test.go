//go:build !windows

package cli

import (
	"syscall"
	"testing"
)

// pidAlive reports whether a process with the given PID still exists, via
// the signal-0 probe (the standard Unix liveness check): a zombie or a
// process owned by another user (EPERM) is not dead for this purpose, only
// ESRCH proves the exit.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// requireTreeProbe does not skip on Unix: the PID the layer command writes
// is a real PID and the signal-0 probe proves its death.
func requireTreeProbe(t *testing.T) {}
