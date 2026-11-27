//go:build darwin

package setup

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	darwinAttrCMNExtendedSecurity = 0x00400000
	darwinFSOPTReportFullsize     = 0x00000004
	darwinMaxACLResponse          = 64 * 1024
)

// copySharedExcludeAccessPolicy compares the file-specific ACL seen through
// the already-opened, no-follow descriptors. Publishing a staged exclude with
// a different ACL — including an unknown measurement — is refused before the
// live file is touched. Copying is deliberately not attempted.
func copySharedExcludeAccessPolicy(source, staged *os.File) error {
	sourceACL, err := darwinFileACL(source)
	if err != nil {
		return fmt.Errorf("cannot measure the exclude ACL: %w", err)
	}
	stagedACL, err := darwinFileACL(staged)
	if err != nil {
		return fmt.Errorf("cannot measure the staged ACL: %w", err)
	}
	if !bytes.Equal(sourceACL, stagedACL) {
		return fmt.Errorf("the staged exclude has a different access ACL (%d bytes live, %d staged)", len(sourceACL), len(stagedACL))
	}
	return nil
}

func darwinFileACL(f *os.File) ([]byte, error) {
	// attrlist: bitmapcount, reserved, then the five attrgroup bitmaps.
	var list [24]byte
	binary.LittleEndian.PutUint16(list[0:2], 5)
	binary.LittleEndian.PutUint32(list[4:8], darwinAttrCMNExtendedSecurity)
	response := make([]byte, darwinMaxACLResponse)
	_, _, errno := syscall.Syscall6(syscall.SYS_FGETATTRLIST, f.Fd(),
		uintptr(unsafe.Pointer(&list[0])), uintptr(unsafe.Pointer(&response[0])),
		uintptr(len(response)), darwinFSOPTReportFullsize, 0)
	if errno != 0 {
		return nil, fmt.Errorf("fgetattrlist: %w", errno)
	}
	return parseDarwinACLResponse(response)
}

// The response contains its length followed by one attrreference. The signed
// offset starts at the reference itself, not at the beginning of the buffer.
func parseDarwinACLResponse(response []byte) ([]byte, error) {
	const referenceStart, headerSize = 4, 12
	if len(response) < headerSize {
		return nil, fmt.Errorf("short getattrlist response (%d bytes)", len(response))
	}
	size := int(binary.LittleEndian.Uint32(response[0:4]))
	if size < headerSize || size > len(response) {
		return nil, fmt.Errorf("invalid getattrlist response size %d (buffer %d bytes)", size, len(response))
	}
	offset := referenceStart + int(int32(binary.LittleEndian.Uint32(response[4:8])))
	length := int(binary.LittleEndian.Uint32(response[8:12]))
	if offset < headerSize || offset > size || length > size-offset || length == 0 && size != headerSize {
		return nil, fmt.Errorf("invalid extended-security reference (offset %d, length %d, response %d)", offset, length, size)
	}
	return response[offset : offset+length], nil
}
