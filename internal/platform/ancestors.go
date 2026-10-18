package platform

import "errors"

// ancestorHopLimit caps the walk (decision: a chain longer than 64 hops
// yields unknown instead of an unbounded loop).
const ancestorHopLimit = 64

var (
	// errAncestorRead: a parent read failed midway (a pid that is no
	// longer running, a ps failure). The caller maps it to unknown.
	errAncestorRead = errors.New("ancestor read failed")
	// errAncestorCycle: the parent chain repeats a pid (a snapshot
	// inconsistency); the walk stops.
	errAncestorCycle = errors.New("ancestor cycle")
	// errAncestorLimit: more than ancestorHopLimit hops without reaching
	// the root.
	errAncestorLimit = errors.New("ancestor hop limit exceeded")
)

// ProcessAncestors walks the parent chain of pid, from pid up to the
// root (pid first, the root last). The chain includes the root when it is
// pid 1, and ends at the last read pid when its parent is 0 (a
// parentless pid). A failed read midway, a cycle, or a chain longer than
// ancestorHopLimit hops is an error (the partial chain is returned so a
// caller can still see how far it got). Only parent pids are read —
// never a command line or an environment of any process. The platform
// supplies the parent provider: POSIX reads one ps per hop, Windows
// builds one Toolhelp32 snapshot that answers every hop.
func ProcessAncestors(pid int, env Env) ([]int, error) {
	if pid <= 0 {
		return nil, errAncestorRead
	}
	next, err := ancestorProvider(env)
	if err != nil {
		return nil, err
	}
	chain := []int{pid}
	seen := map[int]bool{pid: true}
	cur := pid
	for hops := 0; ; hops++ {
		if hops >= ancestorHopLimit {
			return chain, errAncestorLimit
		}
		parent, err := next(cur)
		if err != nil {
			return chain, err
		}
		if parent == 0 {
			return chain, nil
		}
		if seen[parent] {
			return chain, errAncestorCycle
		}
		chain = append(chain, parent)
		if parent == 1 {
			return chain, nil
		}
		seen[parent] = true
		cur = parent
	}
}
