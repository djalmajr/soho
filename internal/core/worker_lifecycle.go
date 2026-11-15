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

// TryLockWorkerLifecycle serializes commands for one roster name. The OS
// drops the lock on process death. An abandoned file is reused, not aged out:
// a slow live holder must never lose its lock. Callers must defer the release.
func TryLockWorkerLifecycle(sd, agent string) (func(), error) {
	path := filepath.Join(sd, fmt.Sprintf("worker-%x.lock", sha256.Sum256([]byte(agent))))
	f, err := openWorkerLifecycleFile(path)
	if err != nil {
		return nil, err
	}
	return tryWorkerLifecycleLockFile(path, f)
}

func tryWorkerLifecycleLockFile(path string, f *os.File) (func(), error) {
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
			if err := os.Remove(path); err != nil {
				_, _ = fmt.Fprintf(platform.Stderr, "soho: could not remove worker lifecycle lock %s: %v\n", path, err)
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
	deadline := time.Now().Add(10 * time.Second)
	for {
		unlock, err := TryLockWorkerLifecycle(sd, agent)
		if !errors.Is(err, ErrWorkerLifecycleBusy) {
			return unlock, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("worker %s lifecycle lock held for too long: %w", agent, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
