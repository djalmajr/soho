package platform

import "sync"

// snapshotParentReader answers each hop of the walk from one process
// snapshot (pid -> parent pid). The first pid asked is the start of the
// chain: a start pid missing from the snapshot is a read failure. Any later
// pid missing from it is an ancestor that already exited: a snapshot keeps
// the parent pid of a dead parent and Windows never reassigns it, so a real
// chain often reaches one. That pid ends the chain, like a parent 0 on
// POSIX: it is treated as parentless.
//
// A pid that Windows reused for another process can divert the chain into
// another tree. That is accepted: it can only turn a refusal into
// verified or unknown, never add one.
func snapshotParentReader(parentOf map[int]int) func(pid int) (int, error) {
	started := false
	return func(pid int) (int, error) {
		first := !started
		started = true
		parent, ok := parentOf[pid]
		if !ok {
			if first {
				return 0, errAncestorRead
			}
			return 0, nil
		}
		return parent, nil
	}
}

var (
	ancestorOverrideMu sync.Mutex
	ancestorOverride   map[int]int
)

// OverrideAncestorParents is the test point of the caller-pane check: while
// it is active, ProcessAncestors reads the parents from parents on every
// system, with no ps and no snapshot. A pid missing from the map is a read
// failure, like a ps that exits non-zero for a pid with no rule. The
// returned restore puts the platform provider back. Tests that use it must
// not call t.Parallel.
func OverrideAncestorParents(parents map[int]int) (restore func()) {
	copied := make(map[int]int, len(parents))
	for pid, parent := range parents {
		copied[pid] = parent
	}
	ancestorOverrideMu.Lock()
	previous := ancestorOverride
	ancestorOverride = copied
	ancestorOverrideMu.Unlock()
	return func() {
		ancestorOverrideMu.Lock()
		ancestorOverride = previous
		ancestorOverrideMu.Unlock()
	}
}

// overriddenAncestorReader is the per-hop read of the override, or nil when
// none is active.
func overriddenAncestorReader() func(pid int) (int, error) {
	ancestorOverrideMu.Lock()
	parents := ancestorOverride
	ancestorOverrideMu.Unlock()
	if parents == nil {
		return nil
	}
	return func(pid int) (int, error) {
		parent, ok := parents[pid]
		if !ok {
			return 0, errAncestorRead
		}
		return parent, nil
	}
}
