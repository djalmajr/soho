//go:build windows

package platform

import (
	"testing"
	"unsafe"
)

// The counters must match PROCESS_MEMORY_COUNTERS byte for byte: two
// DWORDs, then eight SIZE_Ts, with WorkingSetSize the second SIZE_T. A
// short struct makes K32GetProcessMemoryInfo refuse the call, and every
// process would read as unreadable.
func TestWindowsProcMemoryCountersLayout(t *testing.T) {
	var c windowsProcMemoryCounters
	ptr := unsafe.Sizeof(uintptr(0))
	if got, want := unsafe.Sizeof(c), 8+8*ptr; got != want {
		t.Fatalf("sizeof(PROCESS_MEMORY_COUNTERS) = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(c.WorkingSetSize), 8+ptr; got != want {
		t.Fatalf("offsetof(WorkingSetSize) = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(c.PeakPagefileUsage), 8+7*ptr; got != want {
		t.Fatalf("offsetof(PeakPagefileUsage) = %d, want %d", got, want)
	}
}
