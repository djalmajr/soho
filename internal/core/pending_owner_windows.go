//go:build windows

package core

import "io/fs"

// scratchChecksOwner: on Windows %TEMP% is the user's own, so only "a
// directory (or a regular file), not a link" is checked.
const scratchChecksOwner = false

func fileOwnerUID(fs.FileInfo) (int, bool) { return 0, false }
