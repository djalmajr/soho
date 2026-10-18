//go:build windows

package platform

import (
	"unsafe"
)

// windowsProcK32GetProcessMemoryInfo fills the process memory counters of
// an opened process (the K32 alias takes no dummy first parameter).
var windowsProcK32GetProcessMemoryInfo = windowsKernel32.NewProc("K32GetProcessMemoryInfo")

// windowsProcMemoryCounters is PROCESS_MEMORY_COUNTERS: two DWORDs and
// eight SIZE_Ts (uintptr, so the layout also holds on 32-bit Windows). The
// whole struct is declared (K32GetProcessMemoryInfo validates the passed
// size against sizeof(PROCESS_MEMORY_COUNTERS)); only WorkingSetSize is
// read.
type windowsProcMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// ReadProcessRSS reads the pid's resident working set (bytes) through
// K32GetProcessMemoryInfo's WorkingSetSize, with a
// PROCESS_QUERY_LIMITED_INFORMATION handle (the read-only access the
// identity reads already use). A failed open or a failed read reports
// unreadable, and so does a process that is not STILL_ACTIVE; the process
// command line is never read.
func ReadProcessRSS(pid int, _ Env) (int64, bool) {
	handle, err := windowsOpenProcess(windowsProcessQueryLimitedInformation, pid)
	if err != nil {
		return 0, false
	}
	defer func() { _, _, _ = windowsProcCloseHandle.Call(handle) }()
	// An exited process stays openable while any handle to it is open, and
	// its counters still read: only a STILL_ACTIVE process has an RSS.
	if windowsExitState(handle) != ProcRunning {
		return 0, false
	}
	var counters windowsProcMemoryCounters
	counters.Size = uint32(unsafe.Sizeof(counters))
	done, _, _ := windowsProcK32GetProcessMemoryInfo.Call(handle,
		uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if done == 0 {
		return 0, false
	}
	return int64(counters.WorkingSetSize), true
}
