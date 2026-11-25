//go:build !windows

package core

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openWorkerLifecycleFileWithPerm(path string, perm os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("worker lifecycle lock %s is not a regular file", path)
	}
	return f, nil
}

// tryLockSharedMetadata flocks the validated common Git info directory
// itself. There is no lock inode to create, chown, inherit or recover, so
// every eligible writer of the directory contends on one process-held lock
// even on filesystems without hard links or with differing primary groups.
// The no-follow open and the post-lock identity recheck reject a replaced or
// redirected directory before entering the transaction.
func tryLockSharedMetadata(path string, _ os.FileMode) (func(), error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := tryWorkerLifecycleFileLock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("shared metadata lock %s is not a directory", path)
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(opened, current) {
		_ = f.Close()
		return nil, ErrWorkerLifecycleBusy
	}
	return func() { _ = f.Close() }, nil
}

func tryWorkerLifecycleFileLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrWorkerLifecycleBusy
	}
	return err
}
