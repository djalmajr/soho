//go:build windows

package platform

import (
	"errors"
	"syscall"
	"unsafe"
)

var (
	windowsNtdll                         = syscall.NewLazyDLL("ntdll.dll")
	windowsProcNtQueryInformationProcess = windowsNtdll.NewProc("NtQueryInformationProcess")
)

// windowsProcessCommandLineInformation is PROCESSINFOCLASS 60
// (ProcessCommandLineInformation, Windows 8.1 and later): a UNICODE_STRING
// header followed by the command line, readable with
// PROCESS_QUERY_LIMITED_INFORMATION.
const windowsProcessCommandLineInformation = 60

type windowsUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

// procExec reads the image path (QueryFullProcessImageNameW) and the
// command line (NtQueryInformationProcess), split with the system's own
// CommandLineToArgvW rules.
func procExec(pid int) (string, []string, error) {
	handle, live := windowsOpenProc(pid)
	if live != ProcRunning {
		return "", nil, errors.New("platform: process not readable")
	}
	defer windowsProcCloseHandle.Call(handle)
	exe, err := windowsImageName(handle)
	if err != nil {
		return "", nil, err
	}
	argv, err := procExecReadArguments(handle)
	if err != nil {
		return "", nil, err
	}
	return exe, argv, nil
}

func procExecutable(pid int) (string, error) {
	handle, err := windowsOpenProcess(windowsProcessQueryLimitedInformation, pid)
	if err != nil {
		return "", err
	}
	defer windowsProcCloseHandle.Call(handle)
	return windowsImageName(handle)
}

func procExecArguments(handle uintptr) ([]string, error) {
	size := uint32(64 << 10)
	var buf []byte
	for {
		buf = make([]byte, size)
		var needed uint32
		status, _, _ := windowsProcNtQueryInformationProcess.Call(handle, windowsProcessCommandLineInformation,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&needed)))
		if status == 0 {
			break
		}
		// STATUS_INFO_LENGTH_MISMATCH (0xC0000004): retry once larger.
		if uint32(status) == 0xC0000004 && needed > size && needed <= procExecMaxBytes {
			size = needed
			continue
		}
		return nil, errors.New("platform: process command line not readable")
	}
	us := (*windowsUnicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil || us.Length == 0 || us.Length%2 != 0 {
		return nil, errors.New("platform: empty process command line")
	}
	// The string must lie inside buf, after its header.
	start := uintptr(unsafe.Pointer(&buf[0]))
	offset := uintptr(unsafe.Pointer(us.Buffer)) - start
	if uintptr(unsafe.Pointer(us.Buffer)) < start || offset < unsafe.Sizeof(*us) || offset+uintptr(us.Length) > uintptr(len(buf)) {
		return nil, errors.New("platform: malformed process command line")
	}
	chars := unsafe.Slice(us.Buffer, int(us.Length)/2)
	line := append(append([]uint16(nil), chars...), 0)
	var argc int32
	argvp, err := syscall.CommandLineToArgv(&line[0], &argc)
	if err != nil {
		return nil, err
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(argvp))))
	// syscall.CommandLineToArgv types the result as at most 8192 arguments
	// of at most 8192 units each: more arguments is refused, and a longer
	// argument reads only up to that bound (never a supervise id).
	if argc < 0 || argc > 8192 {
		return nil, errors.New("platform: too many process arguments")
	}
	argv := make([]string, 0, argc)
	for i := int32(0); i < argc; i++ {
		argv = append(argv, syscall.UTF16ToString((*argvp[i])[:]))
	}
	return argv, nil
}
