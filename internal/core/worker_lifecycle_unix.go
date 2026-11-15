//go:build !windows

package core

import (
	"errors"
	"os"
	"syscall"
)

func openWorkerLifecycleFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func tryWorkerLifecycleFileLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrWorkerLifecycleBusy
	}
	return err
}
