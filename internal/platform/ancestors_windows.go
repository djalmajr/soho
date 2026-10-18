//go:build windows

package platform

// ancestorProvider returns the per-hop parent read: one single Toolhelp32
// process snapshot answers every hop of the walk (the pid and parent
// fields only, in the style of ParentPID: the image name field is never
// read). A snapshot failure is a read failure; a parent missing from the
// snapshot ends the chain (snapshotParentReader).
func ancestorProvider(_ Env) (func(pid int) (int, error), error) {
	parentOf, err := windowsParentSnapshot()
	if err != nil {
		return nil, errAncestorRead
	}
	return snapshotParentReader(parentOf), nil
}
