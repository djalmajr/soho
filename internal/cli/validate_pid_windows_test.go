//go:build windows

package cli

import "testing"

// pidAlive is the Windows stub: the process-tree tests of soho validate
// skip on Windows (requireSh — there is no sh) before the probe is used.
func pidAlive(pid int) bool { return false }

// requireTreeProbe skips the process-tree tests on Windows (DJA-695
// security amendment): pidAlive always returns false here, so a liveness
// assertion through it proves nothing, and the $! of Git Bash's sh is not
// a Windows PID either — there is no trustworthy way to watch the
// grandchild die.
func requireTreeProbe(t *testing.T) {
	t.Skip("Windows: the tree tests need a real PID probe (pidAlive is a false-returning stub here, and Git Bash's $! is not a Windows PID)")
}
