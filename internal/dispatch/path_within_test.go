package dispatch

import (
	"runtime"
	"testing"
)

// DJA-379: PathWithin is the containment check the dispatch report route
// uses (the state dir must be inside the worker's cwd). The table covers
// the normalization contract: equal paths, children, the common-prefix
// sibling trap, `..` folds, and the Windows rules under goos == "win32"
// (case-insensitive, backslash and slash equivalent, the drive letter
// folded), mirroring how SamePath normalizes its inputs.
func TestPathWithinTable(t *testing.T) {
	cases := []struct {
		child, parent, goos string
		want                bool
	}{
		// Equal paths (both platforms).
		{"/a/b", "/a/b", "linux", true},
		{"/a/b", "/a/b", "darwin", true},
		{"C:\\work\\repo", "c:/work/repo", "win32", true},
		// Children.
		{"/a/b/c", "/a/b", "linux", true},
		{"/a/b/c/d", "/a/b", "darwin", true},
		{`C:\work\repo\sub`, `C:\work\repo`, "win32", true},
		{`C:\work\repo\sub`, `c:/work`, "win32", true},
		// Common-prefix siblings are not inside each other.
		{"/a/bc", "/a/b", "linux", false},
		{"/a/b", "/a/bc", "darwin", false},
		{`C:\work\repx`, `C:\work\repo`, "win32", false},
		// `..` folds before the comparison.
		{"/a/b/../b", "/a/b", "linux", true},
		{"/a/b/c", "/a/b/c/..", "linux", true},
		{"/a/b/..", "/a/b", "linux", false},
		{`C:\a\b\..\c`, `C:/a/c`, "win32", true},
		// Windows case and separator folding, the drive letter included.
		{`c:/WORK/REPO/sub`, `C:\work\repo`, "win32", true},
		{`C:\Work\Repo\Sub`, `c:/work/repo`, "win32", true},
		{`D:\work\repo`, `C:\work\repo`, "win32", false},
		// POSIX stays case-sensitive.
		{"/A/b/c", "/a/b", "linux", false},
		{"/a/b/c", "/A/b", "darwin", false},
		// A trailing slash normalizes away.
		{"/a/b/c", "/a/b/", "linux", true},
		// A drive root contains every path on that drive.
		{`C:\work\repo\state`, `c:\`, "win32", true},
		{`C:\work\repo\state`, `c:/work/REPO/`, "win32", true},
		{`D:\state`, `c:\`, "win32", false},
	}
	for _, tc := range cases {
		if got := PathWithin(tc.child, tc.parent, tc.goos); got != tc.want {
			t.Fatalf("PathWithin(%q, %q, %q) = %v; want %v", tc.child, tc.parent, tc.goos, got, tc.want)
		}
	}
}

// The POSIX root contains every absolute path: "/" plus the separator must
// not become the prefix "//" (DJA-379 review). On a Windows host "/" resolves
// to the current drive's root, so these POSIX cases run elsewhere only.
func TestPathWithinPOSIXRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX root semantics")
	}
	for _, tc := range []struct {
		child, parent, goos string
		want                bool
	}{
		{"/a/b", "/", "linux", true},
		{"/a/b", "/", "darwin", true},
		{"/", "/", "linux", true},
	} {
		if got := PathWithin(tc.child, tc.parent, tc.goos); got != tc.want {
			t.Fatalf("PathWithin(%q, %q, %q) = %v; want %v", tc.child, tc.parent, tc.goos, got, tc.want)
		}
	}
}
