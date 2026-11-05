package platform

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// CanonPath normalizes one side of a path comparison the way the dispatch
// comparator normalizes it today (dispatch.SamePath): on win32 backslashes
// become slashes, path.Clean runs and the result folds to lower case (the
// drive letter included); on the other platforms the path is only cleaned,
// case preserved. Pure: it does not touch the filesystem and resolves no
// symlinks, so a table comparison on another platform stays lexical.
func CanonPath(p, goos string) string {
	if goos == "win32" {
		return strings.ToLower(path.Clean(strings.ReplaceAll(p, `\`, "/")))
	}
	return filepath.Clean(p)
}

// PathInside reports whether child is the same path as parent or beneath
// it, for two already-canonical spellings (CanonPath on both sides): equal,
// or child carries parent's spelling plus the separator — a name prefix is
// not containment, so /a/bc is not inside /a/b. A root parent already ends
// with the separator, so one is added only when it is missing and every
// absolute child of the root is inside it.
func PathInside(child, parent, goos string) bool {
	if child == parent {
		return true
	}
	sep := string(os.PathSeparator)
	if goos == "win32" {
		sep = "/"
	}
	prefix := parent
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(child, prefix)
}
