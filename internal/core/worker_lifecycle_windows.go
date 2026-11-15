package core

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var workerLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func openWorkerLifecycleFile(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Share deletion so the holder can unlink before closing. Other open
	// handles must verify that they still refer to the current pathname.
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return nil, ErrWorkerLifecycleBusy // an unlinked handle is delete-pending
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
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
