//go:build windows

package eval

import (
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// TestCreateOwnedOutputHandleKeepsWriteData pins the access the MSYS and
// Cygwin runtimes look for on an inherited standard handle: without
// FILE_WRITE_DATA they treat it as read-only, so every write of a shell fails
// and the run exits 1 with an empty log (DJA-695). It needs no sh, so it also
// runs on a machine where Git's usr\bin is not on the PATH.
func TestCreateOwnedOutputHandleKeepsWriteData(t *testing.T) {
	f, err := CreateOwnedOutput(filepath.Join(t.TempDir(), "out.log"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const fileAccessInformation, fileWriteData = 8, 0x2
	var iosb [2]uintptr
	var access uint32
	status, _, _ := syscall.NewLazyDLL("ntdll.dll").NewProc("NtQueryInformationFile").Call(
		f.Fd(), uintptr(unsafe.Pointer(&iosb)), uintptr(unsafe.Pointer(&access)), unsafe.Sizeof(access), fileAccessInformation)
	if status != 0 {
		t.Fatalf("NtQueryInformationFile status = %#x", status)
	}
	if access&fileWriteData == 0 {
		t.Fatalf("handle access = %#x, want FILE_WRITE_DATA", access)
	}
}
