package collaboration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stateLockPermissionDenied is the shape of the creation error a Windows
// delete-pending state.lock entry produces, independent of the host.
func stateLockPermissionDenied() error {
	return &fs.PathError{Op: "creat", Path: "state.lock", Err: os.ErrPermission}
}

// TestStateLockRetriesDeletePending makes the first three creations fail as
// a delete-pending lock file does on Windows. The windows platform must
// retry the permission error as contention and then own a real lock that
// releases cleanly; every other platform must refuse at once, without
// retrying or creating a lock.
func TestStateLockRetriesDeletePending(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			lockPath := filepath.Join(t.TempDir(), "state.lock")
			calls := 0
			unlock, err := acquireStateLock(lockPath, func(name string, flag int, perm os.FileMode) (*os.File, error) {
				calls++
				if calls <= 3 {
					return nil, stateLockPermissionDenied()
				}
				return os.OpenFile(name, flag, perm)
			}, goos, time.Sleep)
			if goos != "windows" {
				if err == nil || !errors.Is(err, os.ErrPermission) {
					t.Fatalf("%s: err=%v, want the original permission error", goos, err)
				}
				if calls != 1 {
					t.Fatalf("%s: the permission failure was retried: %d creations, want 1", goos, calls)
				}
				if _, statErr := os.Lstat(lockPath); !os.IsNotExist(statErr) {
					t.Fatalf("a lock appeared after a refused creation: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("delete-pending acquisition: %v", err)
			}
			if calls != 4 {
				t.Fatalf("delete-pending acquisition took %d creations, want 4", calls)
			}
			if info, statErr := os.Lstat(lockPath); statErr != nil || !info.Mode().IsRegular() {
				t.Fatalf("the own lock is not a regular file after acquisition: %v %v", info, statErr)
			}
			unlock()
			if _, statErr := os.Lstat(lockPath); !os.IsNotExist(statErr) {
				t.Fatalf("the own lock remained after release: %v", statErr)
			}
		})
	}
}

// TestStateLockDeletePendingDeadline keeps every creation failing with a
// permission error. The windows platform retries until the 1-second bound
// and then returns the original creation error, not the held-lock error;
// every other platform refuses at once.
func TestStateLockDeletePendingDeadline(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			lockPath := filepath.Join(t.TempDir(), "state.lock")
			perm := stateLockPermissionDenied()
			calls := 0
			unlock, err := acquireStateLock(lockPath, func(string, int, os.FileMode) (*os.File, error) {
				calls++
				return nil, perm
			}, goos, time.Sleep)
			if goos != "windows" {
				if err != perm {
					t.Fatalf("%s: err=%v, want the original permission error", goos, err)
				}
				if calls != 1 {
					t.Fatalf("%s: the permission failure was retried: %d creations, want 1", goos, calls)
				}
				return
			}
			if err != perm {
				t.Fatalf("permanent permission failure: err=%v, want the original creation error %v", err, perm)
			}
			if calls < 2 {
				t.Fatalf("the permission failure was not retried: %d creations", calls)
			}
			if unlock != nil {
				t.Fatal("a failing acquisition returned an unlock")
			}
			if _, statErr := os.Lstat(lockPath); !os.IsNotExist(statErr) {
				t.Fatalf("a lock appeared after refused creations: %v", statErr)
			}
		})
	}
}

// TestStateLockExistKeepsHeldError keeps the os.ErrExist behavior as it is
// today: the existing lock is retried until the bound, which then reports
// the held-lock error, even on Windows.
func TestStateLockExistKeepsHeldError(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.lock")
	exist := &fs.PathError{Op: "creat", Path: "state.lock", Err: os.ErrExist}
	calls := 0
	unlock, err := acquireStateLock(lockPath, func(string, int, os.FileMode) (*os.File, error) {
		calls++
		return nil, exist
	}, "windows", time.Sleep)
	if unlock != nil {
		t.Fatal("a failing acquisition returned an unlock")
	}
	if err == nil || !strings.Contains(err.Error(), "collaboration lock held") {
		t.Fatalf("permanent existing lock: err=%v, want the held-lock error", err)
	}
	if calls < 2 {
		t.Fatalf("the existing lock was not retried: %d creations", calls)
	}
}
