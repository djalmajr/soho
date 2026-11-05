//go:build windows

package platform

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestDeletePendingErrorMapping pins, on real Windows, that the
// ERROR_ACCESS_DENIED a delete-pending lock entry reports for a same-name
// creation is mapped by the os package to os.ErrPermission: DeletePending
// relies on that mapping to classify the collision as contention. Opening
// an existing directory for truncation is the minimal real trigger: the
// directory opens read-only, and truncating the directory handle is
// refused with ERROR_ACCESS_DENIED.
func TestDeletePendingErrorMapping(t *testing.T) {
	dir := t.TempDir()
	h, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_TRUNC, 0)
	if err != syscall.ERROR_ACCESS_DENIED {
		t.Fatalf("open the directory for truncation: err=%v, want ERROR_ACCESS_DENIED", err)
	}
	if h != syscall.InvalidHandle {
		_ = syscall.CloseHandle(h)
	}
	if _, oerr := os.OpenFile(dir, os.O_RDONLY|os.O_TRUNC, 0); oerr == nil {
		t.Fatal("the os-level open succeeded; want the access-denied refusal")
	} else if !errors.Is(oerr, os.ErrPermission) {
		t.Fatalf("the os-level error %v is not mapped to os.ErrPermission", oerr)
	}
}
