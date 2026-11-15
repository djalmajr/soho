//go:build windows

package gate

import "os/exec"

// chmodMayFail is true on Windows, where file modes are not POSIX modes.
const chmodMayFail = true

// prepare does nothing on Windows: the repository's tree termination for
// bounded children lives inside platform's own runner and is not reusable
// here, so the bound ends the direct child only (its descendants are not
// terminated).
func prepare(_ *exec.Cmd) {}

// Windows process handles retain their identity; observation also reaps.
func observeExit(cmd *exec.Cmd) error { return cmd.Wait() }

func reap(_ *exec.Cmd, waitErr error) error { return waitErr }

func signalTerminate(cmd *exec.Cmd) { killDirect(cmd) }

func signalKill(cmd *exec.Cmd) { killDirect(cmd) }

func killDirect(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// signalName is empty on Windows: there are no signals.
func signalName(_ interface{ Sys() any }) string { return "" }
