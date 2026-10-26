package spawn

import "os"

// Windows has no Unix uid; repository layout and path checks still apply.
func trustOwned(info os.FileInfo) bool { return true }
