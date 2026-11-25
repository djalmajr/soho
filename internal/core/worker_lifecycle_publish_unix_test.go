//go:build !windows

package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A shared metadata lock is held on the validated directory itself. There is
// no lock inode whose group could exclude an eligible writer, and no hard
// link to create. A waiter must fail while held and reuse the same directory
// after release or process death.
func TestSharedDirectoryLockLifetime(t *testing.T) {
	dir := t.TempDir()
	holder, err := TryLockSharedPath(dir, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := TryLockSharedPath(dir, 0o600); !errors.Is(err, ErrWorkerLifecycleBusy) {
		if err == nil {
			other()
		}
		t.Fatalf("second owner acquired a live directory lock: %v", err)
	}
	holder()
	for i := 0; i < 2; i++ {
		unlock, err := TryLockSharedPath(dir, 0o600)
		if err != nil {
			t.Fatalf("reacquire %d: %v", i, err)
		}
		unlock()
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory lock published a file: entries=%v err=%v", entries, err)
	}
}

// Private worker locks keep their 0o600 mode and are never shared.
func TestPrivateWorkerLockStaysPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	unlock, err := TryLockPath(filepath.Join(dir, "worker-private.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	info, err := os.Stat(filepath.Join(dir, "worker-private.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private lock mode=%04o, want 0600", got)
	}
}
