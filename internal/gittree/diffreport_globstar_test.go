package gittree

import "testing"

func TestDiffOwnedMatchGlobstar(t *testing.T) {
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"ui/src/**", "ui/src/a.ts", true},
		{"ui/src/**", "ui/src/x/y/z.tsx", true},
		{"ui/src/**", "ui/other.ts", false},
		{"**/foo_test.go", "foo_test.go", true},
		{"**/foo_test.go", "a/b/foo_test.go", true},
		{"a/**/b.go", "a/b.go", true},
		{"a/**/b.go", "a/x/y/b.go", true},
		{"a/**/b.go", "a/x/c.go", false},
		{"a/**/**/b.go", "a/x/y/b.go", true},
		{"a/*.go", "a/x/b.go", false},
		{"a/[ab].go", "a/b.go", true},
		{"a/[ab].go", "a/c.go", false},
		{"a/", "a/b.go", true},
		{"a/", "a/x/b.go", true},
		{"a/", "a", false},
		{"a/**/", "a/b.go", true},
		{"a/**/", "a", false},
	} {
		t.Run(tc.pattern+"_"+tc.file, func(t *testing.T) {
			if got := diffOwnedMatch(tc.pattern, tc.file); got != tc.want {
				t.Errorf("diffOwnedMatch(%q, %q) = %t, want %t", tc.pattern, tc.file, got, tc.want)
			}
		})
	}
}
