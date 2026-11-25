package core

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var workerLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

// perm is ignored on Windows: CreateFile carries no POSIX mode and native
// cross-user ACL behavior is not established here.
func openWorkerLifecycleFileWithPerm(path string, _ os.FileMode) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Share deletion so the holder can unlink before closing. Other open
	// handles must verify that they still refer to the current pathname.
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return nil, ErrWorkerLifecycleBusy // an unlinked handle is delete-pending
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("worker lifecycle lock %s is not a regular file", path)
	}
	return f, nil
}

// tryLockSharedMetadata keeps Windows' native no-hard-link file lock. The
// directory's inherited ACLs supply sharing; publication is serialized and
// every open requests the reparse point itself.
func tryLockSharedMetadata(path string, perm os.FileMode) (func(), error) {
	if err := publishSharedLock(path, perm); err != nil {
		return nil, err
	}
	return tryLockPath(path, perm, false)
}

// publishSharedLock uses a serialized CREATE_NEW instead of a hard link, so
// it works on volumes without hard-link support.
func publishSharedLock(path string, _ os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("shared lock %s is not a regular file", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	for {
		h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			_ = syscall.CloseHandle(h)
			return nil
		}
		if errors.Is(err, syscall.ERROR_FILE_EXISTS) || errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			// Another publisher won the create. Reuse only a regular file.
			info, statErr := os.Lstat(path)
			if statErr == nil && info.Mode().IsRegular() {
				return nil
			}
			return fmt.Errorf("shared lock %s is not a regular file", path)
		}
		return err
	}
}

func tryWorkerLifecycleFileLock(f *os.File) error {
	var overlapped syscall.Overlapped
	r, _, err := workerLockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r != 0 {
		return nil
	}
	if errors.Is(err, syscall.Errno(33)) {
		return ErrWorkerLifecycleBusy
	}
	if err == syscall.Errno(0) {
		return syscall.EINVAL
	}
	return err
}
