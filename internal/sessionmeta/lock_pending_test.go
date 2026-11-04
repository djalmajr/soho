package sessionmeta

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// permissionDenied is the shape of the creation error a Windows
// delete-pending lock directory produces, independent of the host.
func permissionDenied() error {
	return &fs.PathError{Op: "mkdirat", Path: lockName, Err: os.ErrPermission}
}

// TestDeletePendingClassification pins the predicate: only a permission
// error on Windows is a delete-pending collision; an existing lock, any
// other error, and a permission error elsewhere are not.
func TestDeletePendingClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		goos string
		want bool
	}{
		{"windows permission is delete-pending", permissionDenied(), "windows", true},
		{"linux permission is a failure", permissionDenied(), "linux", false},
		{"darwin permission is a failure", permissionDenied(), "darwin", false},
		{"windows existing lock is contention, not delete-pending", &fs.PathError{Op: "mkdirat", Path: lockName, Err: os.ErrExist}, "windows", false},
		{"windows other failure", &fs.PathError{Op: "mkdirat", Path: lockName, Err: errors.New("disk failure")}, "windows", false},
		{"no error", nil, "windows", false},
	}
	for _, c := range cases {
		if got := deletePending(c.err, c.goos); got != c.want {
			t.Errorf("%s: deletePending=%v, want %v", c.name, got, c.want)
		}
	}
}

// openLockSession opens the fixture session root the writers use.
func openLockSession(t *testing.T, f *fsFix) *os.Root {
	t.Helper()
	sessRoot, missing, err := f.st.openSession()
	if err != nil || missing {
		t.Fatalf("session open missing=%v err=%v", missing, err)
	}
	t.Cleanup(func() { _ = sessRoot.Close() })
	return sessRoot
}

// TestAcquireLockRetriesDeletePending makes the first three creations
// fail as a delete-pending lock directory does on Windows. Windows must
// retry and then own a real lock that releases cleanly; every other
// platform must refuse at once, without retrying or creating a lock.
func TestAcquireLockRetriesDeletePending(t *testing.T) {
	f := newFS(t, "s1")
	sessRoot := openLockSession(t, f)
	lockPath := filepath.Join(f.base, f.sess, lockName)
	calls := 0
	mkdir := func(r *os.Root) error {
		calls++
		if calls <= 3 {
			return permissionDenied()
		}
		return mkdirOwnLock(r)
	}
	saved, handle, err := acquireLockWith(sessRoot, mkdir, openOwnLockDir)
	if runtime.GOOS != "windows" {
		if err == nil || errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "the lock cannot be created") {
			t.Fatalf("permission failure on %s: err=%v, want the creation error", runtime.GOOS, err)
		}
		if calls != 1 {
			t.Fatalf("permission failure on %s retried: %d creations, want 1", runtime.GOOS, calls)
		}
		if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a lock appeared after a refused creation: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("delete-pending acquisition: %v", err)
	}
	if calls != 4 {
		t.Fatalf("delete-pending acquisition took %d creations, want 4", calls)
	}
	if info, err := os.Lstat(lockPath); err != nil || !info.IsDir() {
		t.Fatalf("the own lock is not a directory after acquisition: %v %v", info, err)
	}
	if err := releaseLock(sessRoot, saved, handle); err != nil {
		t.Fatalf("own lock release: %v", err)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the own lock remained after release: %v", err)
	}
}

// TestAcquireLockDeletePendingDeadline keeps every creation failing with
// a permission error. Windows retries until the bounded deadline and
// then names the original error, not ErrLocked; every other platform
// refuses at once.
func TestAcquireLockDeletePendingDeadline(t *testing.T) {
	f := newFS(t, "s1")
	sessRoot := openLockSession(t, f)
	calls := 0
	mkdir := func(*os.Root) error {
		calls++
		return permissionDenied()
	}
	start := time.Now()
	_, _, err := acquireLockWith(sessRoot, mkdir, openOwnLockDir)
	elapsed := time.Since(start)
	if err == nil || errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "the lock cannot be created") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("permanent permission failure: err=%v, want the original creation error", err)
	}
	if _, err := os.Lstat(filepath.Join(f.base, f.sess, lockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lock appeared after refused creations: %v", err)
	}
	if runtime.GOOS != "windows" {
		if calls != 1 {
			t.Fatalf("permission failure on %s retried: %d creations, want 1", runtime.GOOS, calls)
		}
		return
	}
	if calls < 2 {
		t.Fatalf("delete-pending failure was not retried: %d creations", calls)
	}
	if elapsed < 4*time.Second || elapsed > 15*time.Second {
		t.Fatalf("the bounded wait took %v, want between 4s and 15s", elapsed)
	}
}
