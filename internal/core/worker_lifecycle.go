package core

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

var ErrWorkerLifecycleBusy = errors.New("worker lifecycle is busy")

// openWorkerLifecycleFile retains the private 0o600 wrapper used by the
// lifecycle tests and callers. Shared metadata locks use the explicit-perm
// variant below.
func openWorkerLifecycleFile(path string) (*os.File, error) {
	return openWorkerLifecycleFileWithPerm(path, 0o600)
}

// TryLockWorkerLifecycle serializes commands for one roster name. The OS
// drops the lock on process death. An abandoned file is reused, not aged out:
// a slow live holder must never lose its lock. Callers must defer the release.
func TryLockWorkerLifecycle(sd, agent string) (func(), error) {
	return TryLockPath(filepath.Join(sd, fmt.Sprintf("worker-%x.lock", sha256.Sum256([]byte(agent)))))
}

// TryLockPath takes the same process-held exclusive lock as the worker
// lifecycle lock on an explicit path, for serializing shared file edits.
// The OS releases it on process death; an abandoned file is reused, and a
// slow live holder is never stolen from. Callers must defer the release.
func TryLockPath(path string) (func(), error) {
	return tryLockPath(path, 0o600, true)
}

// TryLockSharedPath takes one bounded process-held lock on shared metadata.
// Unix flocks the validated directory itself, so no lock inode, ownership or
// hard-link support is needed. Windows uses its native file lock with the
// directory's inherited ACLs. Release only closes the handle.
func TryLockSharedPath(path string, perm os.FileMode) (func(), error) {
	return tryLockSharedMetadata(path, perm)
}

func tryLockPath(path string, perm os.FileMode, removeOnRelease bool) (func(), error) {
	f, err := openWorkerLifecycleFileWithPerm(path, perm)
	if err != nil {
		return nil, err
	}
	return tryWorkerLifecycleLock(path, f, removeOnRelease)
}

// LockPath retries TryLockPath within the shared bounded window without
// taking over a live holder's lock.
func LockPath(path string) (func(), error) {
	return lockPath(path, "path", 0o600, true)
}

// LockSharedPath retries the shared-metadata lock without stealing a live
// holder's lock.
func LockSharedPath(path, label string, perm os.FileMode) (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		unlock, err := TryLockSharedPath(path, perm)
		if !errors.Is(err, ErrWorkerLifecycleBusy) {
			return unlock, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s lock held for too long: %w", label, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func lockPath(path, label string, perm os.FileMode, removeOnRelease bool) (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		unlock, err := tryLockPath(path, perm, removeOnRelease)
		if !errors.Is(err, ErrWorkerLifecycleBusy) {
			return unlock, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s lock held for too long: %w", label, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func tryWorkerLifecycleLockFile(path string, f *os.File) (func(), error) {
	return tryWorkerLifecycleLock(path, f, true)
}

func tryWorkerLifecycleLock(path string, f *os.File, removeOnRelease bool) (func(), error) {
	if err := tryWorkerLifecycleFileLock(f); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	// A waiter can have opened the inode that the previous holder unlinked.
	// Never enter on that detached inode: retry using the current pathname.
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	current, statErr := os.Stat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, errors.Join(statErr, f.Close())
	}
	if statErr != nil || !os.SameFile(info, current) {
		return nil, errors.Join(ErrWorkerLifecycleBusy, f.Close())
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Unlink while still locked. A stale descriptor cannot become a
			// second owner because acquisition verifies the pathname above.
			if removeOnRelease {
				if err := os.Remove(path); err != nil {
					_, _ = fmt.Fprintf(platform.Stderr, "soho: could not remove worker lifecycle lock %s: %v\n", path, err)
				}
			}
			if err := f.Close(); err != nil {
				_, _ = fmt.Fprintf(platform.Stderr, "soho: could not close worker lifecycle lock %s: %v\n", path, err)
			}
		})
	}, nil
}

// LockWorkerLifecycle bounds contention without stealing a live lock. A
// timeout refuses the command; it never permits an unprotected operation.
func LockWorkerLifecycle(sd, agent string) (func(), error) {
	return lockPath(filepath.Join(sd, fmt.Sprintf("worker-%x.lock", sha256.Sum256([]byte(agent)))), "worker "+agent+" lifecycle", 0o600, true)
}
