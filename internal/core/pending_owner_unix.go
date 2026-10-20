//go:build !windows

package core

import (
	"io/fs"
	"syscall"
)

// scratchChecksOwner: on Unix the queue levels and file must be the
// current user's, with no write for the group or the others.
const scratchChecksOwner = true

func fileOwnerUID(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
