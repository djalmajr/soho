package testutil

import "os"

// CallerShellPIDs returns the scripted pids of the caller-pane fixtures:
// the claimed pane's shell (4000) and the forked tree's intermediate pid
// (3000), moved out of the way of this process's own pid and of the root
// 1. The ancestry walk always includes its start pid (the test process,
// internal/platform/ancestors.go), and the check reads verified when the
// claimed shell is anywhere in the chain (internal/herdr/caller_check.go).
// DJA-569: the Windows CI test binary was pid 4000 once, and a scripted
// forked mismatch read as verified.
func CallerShellPIDs() (shell, fork int) {
	self := os.Getpid()
	shell, fork = 4000, 3000
	for shell == self || shell == 1 {
		shell += 1_000_000
	}
	for fork == self || fork == 1 || fork == shell {
		fork += 1_000_000
	}
	return shell, fork
}
