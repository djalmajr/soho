package platform

import (
	"errors"
	"time"
)

// ErrOrphansNotDetected is what UserOrphanProcesses and ProcCwd return on
// a platform where the working directory of another process cannot be
// read without reading its memory (Windows: the PEB, which also holds the
// command line and the environment, is never read).
var ErrOrphansNotDetected = errors.New("unregistered processes are not detected on this platform")

// ErrStopGuardChanged is what StopProcessTreeGuarded returns when its
// guard reported a change before a signal round: nothing more was sent.
var ErrStopGuardChanged = errors.New("the process changed before a signal; not stopped")

// OrphanProc is one orphan process of the current user: its pid, its start
// identity (the same string ProcStartIdentity reads for that pid), the
// base name of its command, its working directory as the kernel reports
// it, and its start time. Neither the command line nor the environment is
// read.
type OrphanProc struct {
	Pid      int
	Identity string
	Name     string
	Cwd      string
	Started  time.Time
}

// orphanEntry is one process of the system table before its working
// directory is read.
type orphanEntry struct {
	pid      int
	ppid     int
	uid      int
	identity string
	name     string
	started  time.Time
}

// selectOrphans keeps the entries of the user uid whose parent is a
// reaper (isReaper: the init process, or on Linux the user's systemd), in
// table order. A pid that is not positive, pid 1 itself and this process
// never qualify.
func selectOrphans(entries []orphanEntry, uid int, self int, isReaper func(ppid int) bool) []orphanEntry {
	out := []orphanEntry{}
	for _, entry := range entries {
		if entry.pid <= 1 || entry.pid == self {
			continue
		}
		if entry.uid != uid {
			continue
		}
		if !isReaper(entry.ppid) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// UserOrphanProcesses lists the orphan processes of the current user, with
// their working directory: on macOS the processes whose parent is pid 1
// (sysctl kern.proc.uid, then proc_info PROC_PIDVNODEPATHINFO per
// candidate); on Linux the ones whose parent is pid 1 or a process of the
// same uid named systemd (the user manager), by a scan of /proc. A pid
// that exits or cannot be read during the scan is left out; it never
// fails the list. Other platforms return ErrOrphansNotDetected. No cgo,
// and no command line or environment is read.
func UserOrphanProcesses() ([]OrphanProc, error) {
	return userOrphanProcesses()
}

// ProcCwd reads the working directory of one process as the kernel
// reports it (the same read UserOrphanProcesses makes). Other platforms
// return ErrOrphansNotDetected.
func ProcCwd(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("not a process id")
	}
	return procCwd(pid)
}
