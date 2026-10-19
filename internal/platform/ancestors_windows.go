//go:build windows

package platform

// ancestorProvider returns the per-hop parent read: one single Toolhelp32
// process snapshot answers every hop of the walk (the pid and parent
// fields only, in the style of ParentPID: the image name field is never
// read). A snapshot failure is a read failure; a parent missing from the
// snapshot ends the chain, and a parent created after its child (a reused
// pid) or with an unreadable creation time ends it at the child
// (snapshotParentReader, with windowsAncestorCreated).
func ancestorProvider(_ Env) (func(pid int) (int, error), error) {
	parentOf, err := windowsParentSnapshot()
	if err != nil {
		return nil, errAncestorRead
	}
	return snapshotParentReader(parentOf, windowsAncestorCreated), nil
}

// windowsAncestorCreated reads one pid's creation time (FILETIME ticks)
// through a PROCESS_QUERY_LIMITED_INFORMATION handle, closed before it
// returns; false when the open or GetProcessTimes fails. No image name,
// command line or environment is read.
func windowsAncestorCreated(pid int) (int64, bool) {
	handle, err := windowsOpenProcess(windowsProcessQueryLimitedInformation, pid)
	if err != nil {
		return 0, false
	}
	defer windowsProcCloseHandle.Call(handle)
	ticks, err := windowsCreationTicks(handle)
	if err != nil {
		return 0, false
	}
	return ticks, true
}
