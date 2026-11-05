package platform

import (
	"path"
	"strings"
)

// CanonPath normalizes one side of a path comparison the way the dispatch
// comparator normalizes it today (dispatch.SamePath): on win32 backslashes
// become slashes, path.Clean runs and the result folds to lower case (the
// drive letter included); on the other platforms the path is cleaned with
// slash semantics, case preserved and a backslash kept as a name character.
// The requested system's lexical rules apply independently of the host —
// the host's own filepath.Clean and os.PathSeparator would fold a POSIX
// table spelling into the host's separators on a win32 host — so a table
// comparison on another platform stays lexical on every OS. Pure: it does
// not touch the filesystem and resolves no symlinks.
func CanonPath(p, goos string) string {
	if goos == "win32" {
		return strings.ToLower(path.Clean(strings.ReplaceAll(p, `\`, "/")))
	}
	return path.Clean(p)
}

// PathInside reports whether child is the same path as parent or beneath
// it, for two already-canonical spellings (CanonPath on both sides): equal,
// or child carries parent's spelling plus the separator — a name prefix is
// not containment, so /a/bc is not inside /a/b. Canonical spellings are
// slash-normalized on every system (win32 backslashes become slashes, the
// other platforms clean with slash semantics), so the containment separator
// is the slash on every host, not the host's own (a win32 host's backslash
// would break the prefix on a POSIX table spelling). A root parent already
// ends with the separator, so one is added only when it is missing and
// every absolute child of the root is inside it.
func PathInside(child, parent, goos string) bool {
	if child == parent {
		return true
	}
	sep := "/"
	prefix := parent
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(child, prefix)
}
