package core

import "github.com/djalmajr/soho/internal/platform"

// The queues live in the workspace scratch (scratch_dir.go): the two
// levels <tmp>/soho and <tmp>/soho/<workspace> are checked with the
// scratch rules, and a queue file is read and rewritten without following
// a link. A queue file writable by the group or the others is refused on
// Unix, even the user's own: another user could have changed its bytes in
// place.

// pendingQueueDir prepares and checks <tmp>/soho and <tmp>/soho/<ws> and
// returns the second (privateScratchDir, kind "queue").
func pendingQueueDir(env platform.Env, workspaceID string, mode ScratchMode) (string, error) {
	return privateScratchDir(env, workspaceID, "queue", mode)
}

// readPendingFile reads a queue file through readScratchFile, refusing
// one writable by the group or the others (it is refused, not narrowed,
// and is removed by hand).
func readPendingFile(queue string) (string, error) {
	data, err := readScratchFile(queue, "queue", true)
	if err != nil {
		return "", err
	}
	return platform.DecodeText(data), nil
}
