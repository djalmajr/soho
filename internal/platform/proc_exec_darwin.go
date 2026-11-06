//go:build darwin

package platform

import (
	"bytes"
	"encoding/binary"
	"errors"
	"syscall"
	"unsafe"
)

// darwinPidPathMax is PROC_PIDPATHINFO_MAXSIZE (4 * MAXPATHLEN).
const darwinPidPathMax = 4096

// procExec reads the executable's kernel path with proc_pidpath
// (proc_info PROC_PIDPATHINFO: the file the process runs, not the path
// string given to exec) and the arguments from sysctl kern.procargs2.<pid>:
// argc (int32), the exec path string, NUL padding, then argc
// NUL-terminated arguments (the environment that follows is never parsed).
func procExec(pid int) (string, []string, error) {
	exe, err := procExecutable(pid)
	if err != nil {
		return "", nil, err
	}
	argv, err := procExecReadArguments(uintptr(pid))
	if err != nil {
		return "", nil, err
	}
	return exe, argv, nil
}

// procExecutable uses proc_pidpath's PROC_INFO_CALL_PIDINFO/PROC_PIDPATHINFO.
func procExecutable(pid int) (string, error) {
	path := make([]byte, darwinPidPathMax)
	// PROC_INFO_CALL_PIDINFO (2), PROC_PIDPATHINFO (11).
	_, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, 2, uintptr(pid), 11, 0, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)))
	if errno != 0 {
		return "", errno
	}
	// The kernel answers 0 and a NUL-terminated path (libproc's
	// proc_pidpath returns its strlen).
	end := bytes.IndexByte(path, 0)
	if end <= 0 || path[0] != '/' {
		return "", errors.New("platform: process path not readable")
	}
	return string(path[:end]), nil
}

func procExecArguments(id uintptr) ([]string, error) {
	pid := int(id)
	size := uint32(1 << 20)
	if argmax, err := syscall.SysctlUint32("kern.argmax"); err == nil && argmax > 0 && argmax <= procExecMaxBytes {
		size = argmax
	}
	mib := [3]int32{1, 49, int32(pid)} // CTL_KERN, KERN_PROCARGS2
	buf := make([]byte, size)
	n := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	data := buf[:n]
	if len(data) < 4 {
		return nil, errors.New("platform: short process arguments")
	}
	argc := int(int32(binary.LittleEndian.Uint32(data[:4])))
	rest := data[4:]
	end := bytes.IndexByte(rest, 0)
	if argc < 0 || argc > 4096 || end <= 0 {
		return nil, errors.New("platform: malformed process arguments")
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argv := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			return nil, errors.New("platform: malformed process arguments")
		}
		argv = append(argv, string(rest[:j]))
		rest = rest[j+1:]
	}
	return argv, nil
}
