//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
)

func TestEnsureSessionStateDirectoryClosesRoots(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the own-descriptor counter requires /dev/fd")
	}
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	count := func() int {
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	base := t.TempDir()
	before := count()
	control, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	during := count()
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if during <= before || count() != before {
		t.Fatal("descriptor counter control failed")
	}
	for _, tc := range []struct {
		name, path string
		wantErr    bool
	}{
		{"existing base", base, false},
		{"missing levels", filepath.Join(base, "new", "nested"), false},
		{"refused child", filepath.Join(base, "refused", "con"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := count()
			err := EnsureSessionStateDirectory(tc.path, "ws")
			if (err != nil) != tc.wantErr {
				t.Fatalf("EnsureSessionStateDirectory(%q)=%v", tc.path, err)
			}
			if after := count(); after != before {
				t.Fatalf("path=%q wantErr=%v descriptor delta=%d", tc.path, tc.wantErr, after-before)
			}
		})
	}
}
