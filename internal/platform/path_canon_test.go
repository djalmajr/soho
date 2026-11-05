package platform

// DJA-661 (amendment, decision 2): the shared path comparators live here
// as pure functions — CanonPath folds one side exactly the way
// dispatch.SamePath normalizes it today, and PathInside applies the
// separator and root rule dispatch.PathWithin has — so the dispatch,
// gittree audit and overlap warning compare paths with one rule.

import "testing"

func TestCanonPathBySystem(t *testing.T) {
	cases := []struct {
		p, goos, want string
	}{
		// win32: backslashes become slashes, path.Clean runs, the result
		// folds to lower case (drive letter included).
		{`C:\work\repo`, "win32", `c:/work/repo`},
		{`c:/WORK/REPO/`, "win32", `c:/work/repo`},
		{`C:\WORK\repo\..\repo`, "win32", `c:/work/repo`},
		{`C:\work\repo\src\a.go`, "win32", `c:/work/repo/src/a.go`},
		// The drive letter folds with the rest.
		{`c:\work`, "win32", `c:/work`},
		{`C:`, "win32", `c:`},
		// The drive root cleans to the bare drive; the PathInside prefix
		// rule re-adds the separator it needs.
		{`C:/`, "win32", `c:`},
		// Other systems: only cleaned, case preserved.
		{"/work/repo/", "linux", "/work/repo"},
		{"/work//repo/src", "linux", "/work/repo/src"},
		{"/Work/Repo/src", "darwin", "/Work/Repo/src"},
		{`/work\repo`, "darwin", `/work\repo`},
		// The root stays the root.
		{"/", "linux", "/"},
		{"", "linux", "."},
		{".", "win32", "."},
	}
	for _, tc := range cases {
		if got := CanonPath(tc.p, tc.goos); got != tc.want {
			t.Errorf("CanonPath(%q, %q) = %q; want %q", tc.p, tc.goos, got, tc.want)
		}
	}
}

func TestPathInsideBySystem(t *testing.T) {
	cases := []struct {
		child, parent, goos string
		want                bool
	}{
		// Equal canonical paths.
		{`c:/work/repo`, `c:/work/repo`, "win32", true},
		{"/work/repo", "/work/repo", "linux", true},
		// Beneath, with the separator rule.
		{`c:/work/repo/src/a.go`, `c:/work/repo`, "win32", true},
		{"/work/repo/src/a.go", "/work/repo", "linux", true},
		{"/work/repo/src/a.go", "/work/repo", "darwin", true},
		// A name prefix is not containment: /a/bc is not inside /a/b.
		{"/a/bc", "/a/b", "linux", false},
		{`c:/work/repoX/a.go`, `c:/work/repo`, "win32", false},
		{`c:/work/repoX`, `c:/work/repo`, "win32", false},
		// The reverse never holds.
		{`c:/work/repo`, `c:/work/repo/src`, "win32", false},
		{"/work/repo/src", "/work/repo", "linux", true},
		// The root parent already ends with the separator: every absolute
		// child of the root is inside it — including the cleaned drive root
		// spelling, which the prefix rule gives its separator back.
		{"/x", "/", "linux", true},
		{"c:/x", "c:/", "win32", true},
		{"c:/x", "c:", "win32", true},
		{`c:/x`, `/`, "win32", false},
		// Relative canonical paths keep the same rule.
		{"src/a.go", "src", "linux", true},
		{"src/a.go", "src/x", "linux", false},
		{"src", "src/a.go", "linux", false},
	}
	for _, tc := range cases {
		if got := PathInside(tc.child, tc.parent, tc.goos); got != tc.want {
			t.Errorf("PathInside(%q, %q, %q) = %v; want %v", tc.child, tc.parent, tc.goos, got, tc.want)
		}
	}
}
