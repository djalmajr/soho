package spawn

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// e2eLockPermissionDenied is the shape of the creation error a Windows
// delete-pending e2e-ports.lock entry produces, independent of the host.
func e2eLockPermissionDenied() error {
	return &fs.PathError{Op: "mkdir", Path: "e2e-ports.lock", Err: os.ErrPermission}
}

// TestLockE2EPortsRetriesDeletePending makes the first three creations
// fail as a delete-pending lock directory does on Windows. The windows
// platform must retry the permission error as contention and then own a
// real lock that releases cleanly; every other platform must refuse at
// once, without retrying or creating a lock.
func TestLockE2EPortsRetriesDeletePending(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			sd := t.TempDir()
			lockPath := filepath.Join(sd, "e2e-ports.lock")
			calls := 0
			unlock, err := acquireE2EPortLock(sd, func(path string, perm os.FileMode) error {
				calls++
				if calls <= 3 {
					return e2eLockPermissionDenied()
				}
				return os.Mkdir(path, perm)
			}, goos, func(time.Duration) {})
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
			if info, statErr := os.Lstat(lockPath); statErr != nil || !info.IsDir() {
				t.Fatalf("the own lock is not a directory after acquisition: %v %v", info, statErr)
			}
			unlock()
			if _, statErr := os.Lstat(lockPath); !os.IsNotExist(statErr) {
				t.Fatalf("the own lock remained after release: %v", statErr)
			}
		})
	}
}

// TestLockE2EPortsDeletePendingDeadline keeps every creation failing with
// a permission error. The windows platform retries until the 199-try
// bound and then returns the original creation error, not a new one; every
// other platform refuses at once.
func TestLockE2EPortsDeletePendingDeadline(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			sd := t.TempDir()
			perm := e2eLockPermissionDenied()
			calls := 0
			unlock, err := acquireE2EPortLock(sd, func(string, os.FileMode) error {
				calls++
				return perm
			}, goos, func(time.Duration) {})
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
			if calls != 200 {
				t.Fatalf("the permission failure was not retried to the bound: %d creations, want 200", calls)
			}
			if unlock != nil {
				t.Fatal("a failing acquisition returned an unlock")
			}
			if _, statErr := os.Lstat(filepath.Join(sd, "e2e-ports.lock")); !os.IsNotExist(statErr) {
				t.Fatalf("a lock appeared after refused creations: %v", statErr)
			}
		})
	}
}

// TestLockE2EPortsDiesWithOriginalError keeps the production entry
// contract: a creation error the lock cannot classify as contention exits
// with the friction exit 4 naming the original error. A regular file where
// the lock path expects a directory is a real, host-portable failure.
func TestLockE2EPortsDiesWithOriginalError(t *testing.T) {
	root := t.TempDir()
	sd := filepath.Join(root, "notadir")
	if err := os.WriteFile(sd, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var exit *platform.ExitError
	func() {
		defer func() {
			if r := recover(); r != nil {
				if e, ok := r.(*platform.ExitError); ok {
					exit = e
				}
			}
		}()
		lockE2EPorts(sd)
	}()
	if exit == nil {
		t.Fatal("lockE2EPorts did not die on an uncreatable lock path")
	}
	if exit.Code != 4 || !exit.Friction || !strings.HasPrefix(exit.Msg, "spawn: ") {
		t.Fatalf("exit={code:%d friction:%v msg:%q}, want the friction exit 4 naming the original error", exit.Code, exit.Friction, exit.Msg)
	}
	if info, err := os.Stat(sd); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the lock path was altered: %v", err)
	}
}
