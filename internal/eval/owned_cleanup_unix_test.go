//go:build !windows

package eval

import (
	"context"
	"errors"
	"io"
	"testing"
)

// A failed proof that the tree ended must not lose the child's exit
// information, and it must reach the caller.
func TestOwnedProcessCleanupFailureKeepsTheExitInformation(t *testing.T) {
	proof := errors.New("group still there")
	real := verifyOwnedGroup
	verifyOwnedGroup = func(int) error { return proof }
	t.Cleanup(func() { verifyOwnedGroup = real })
	cases := []struct {
		name, script string
		exit         int
		signaled     bool
		wantRunErr   bool
	}{
		{"nonzero exit", "exit 3", 3, false, true},
		{"signal", "kill -9 $$", -1, true, true},
		{"success", "exit 0", 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := RunOwnedProcess(context.Background(), "/bin/sh", []string{"-c", c.script}, OwnedProcessOptions{Dir: t.TempDir(), Output: io.Discard})
			if !errors.Is(res.CleanupErr, proof) {
				t.Fatalf("CleanupErr = %v", res.CleanupErr)
			}
			if res.ExitCode != c.exit || res.Signaled != c.signaled {
				t.Fatalf("exit = %d, signaled = %t", res.ExitCode, res.Signaled)
			}
			if (err != nil) != c.wantRunErr {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
