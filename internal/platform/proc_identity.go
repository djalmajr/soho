package platform

import (
	"errors"
	"math"
)

// ErrProcReadPermissionDenied is the cause ReadProcFullCause attaches to
// an unreadable read (ProcUnknown) the platform can attribute to a
// permission denial: the process table is not readable from this shell,
// as when a sandbox denies ps. The liveness is unchanged — the denial
// names the failure, not the liveness.
var ErrProcReadPermissionDenied = errors.New("the process table is not readable from this shell")

// ProcStartIdentity reads, in one snapshot, a process's start identity and
// its executable base name, with the three-way liveness of ReadProcFull.
// The identity is an opaque string meant only for equality with another
// ProcStartIdentity read of the same platform; its precision is the
// platform's: Windows the creation FILETIME (100 ns), Linux the start time
// in clock ticks since boot (/proc/<pid>/stat) with the boot id, macOS the kernel start time
// in microseconds (sysctl kern.proc.pid), any other unix the ps lstart
// text (one second). Both strings are empty unless the pid reads as
// running. A reused pid reads as another identity at that precision; it is
// not proof against a reuse inside one precision step.
//
// A pid outside 1..math.MaxInt32 is no process id of any platform here
// and reads as unknown.
func ProcStartIdentity(pid int, env Env) (identity, name string, live ProcLiveness) {
	if pid <= 0 || pid > math.MaxInt32 {
		return "", "", ProcUnknown
	}
	return procStartIdentity(pid, env)
}
