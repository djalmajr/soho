//go:build windows

package core

import "io/fs"

// pendingChecksOwner: on Windows %TEMP% is the user's own, so only "a
// directory (or a regular file), not a link" is checked.
const pendingChecksOwner = false

func fileOwnerUID(fs.FileInfo) (int, bool) { return 0, false }
