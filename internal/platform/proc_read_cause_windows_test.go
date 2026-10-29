//go:build windows

package platform

import (
	"errors"
	"syscall"
	"testing"
)

// The access-denied cause is the Windows equivalent of the unix
// permission denial: the read can tell it from the kernel32 error code
// (ERROR_ACCESS_DENIED, 5), and any other code keeps today's text (a nil
// cause).
func TestWindowsAccessDeniedCause(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{syscall.Errno(windowsErrorAccessDenied), true},
		{syscall.Errno(windowsErrorInvalidParameter), false},
		{syscall.Errno(8), false},
		{errors.New("open process: access denied"), false},
		{nil, false},
	} {
		if got := windowsAccessDenied(tc.err); got != tc.want {
			t.Fatalf("windowsAccessDenied(%v) = %v; want %v", tc.err, got, tc.want)
		}
		var cause error
		if tc.want {
			cause = ErrProcReadPermissionDenied
		}
		if got := windowsDeniedCause(tc.err); !errors.Is(got, cause) {
			t.Fatalf("windowsDeniedCause(%v) = %v; want %v", tc.err, got, cause)
		}
	}
}
