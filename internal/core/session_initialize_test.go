package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// listDir is the sorted entry names of one directory, or an error message
// when it is not a directory.
func listDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "not-a-dir"
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestEnsureSessionStateDirectoryCreatesMinimum(t *testing.T) {
	base := filepath.Join(t.TempDir(), "state")
	if err := EnsureSessionStateDirectory(base, "ws"); err != nil {
		t.Fatalf("EnsureSessionStateDirectory: %v", err)
	}
	sess := filepath.Join(base, "ws")
	info, err := os.Stat(sess)
	if err != nil || !info.IsDir() {
		t.Fatalf("the session dir is missing or not a dir: %v", err)
	}
	if got := listDir(t, sess); got != "" {
		t.Fatalf("the session dir holds %q; only the minimum base/session may be created (no report/roster/lock/cache)", got)
	}
}

func TestEnsureSessionStateDirectoryCreatesMissingLevels(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "a", "b", "c")
	if err := EnsureSessionStateDirectory(base, "ws"); err != nil {
		t.Fatalf("EnsureSessionStateDirectory: %v", err)
	}
	for _, dir := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b"), base, filepath.Join(base, "ws")} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("the created level %s is missing: %v", dir, err)
		}
	}
}

func TestEnsureSessionStateDirectoryIdempotent(t *testing.T) {
	base := filepath.Join(t.TempDir(), "state")
	if err := EnsureSessionStateDirectory(base, "ws"); err != nil {
		t.Fatalf("first: %v", err)
	}
	marker := filepath.Join(base, "ws", "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSessionStateDirectory(base, "ws"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "x" {
		t.Fatalf("the existing session content changed: %v %q", err, data)
	}
}

func TestEnsureSessionStateDirectoryRefusesUnsafeExisting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, base, sess string)
	}{
		{
			name: "session is a symlink",
			mutate: func(t *testing.T, base, sess string) {
				other := filepath.Join(base, "other")
				if err := os.Mkdir(other, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, sess); err != nil {
					t.Fatalf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "session is a file",
			mutate: func(t *testing.T, base, sess string) {
				if err := os.WriteFile(sess, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			sess := filepath.Join(base, "ws")
			tc.mutate(t, base, sess)
			if err := EnsureSessionStateDirectory(base, "ws"); err == nil {
				t.Fatalf("EnsureSessionStateDirectory accepted the %s", tc.name)
			}
		})
	}
}

func TestEnsureSessionStateDirectoryRefusesUnsafeSession(t *testing.T) {
	base := t.TempDir()
	for _, session := range []string{"", ".", "..", "a/b", "nul", "bad\u0000ctl"} {
		if err := EnsureSessionStateDirectory(base, session); err == nil {
			t.Fatalf("EnsureSessionStateDirectory accepted the unsafe session %q", session)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "ws")); !os.IsNotExist(err) {
		t.Fatalf("no session dir may be created for an unsafe id: %v", err)
	}
}

func TestEnsureSessionStateDirectoryRefusesUnsafeMissingLevel(t *testing.T) {
	base := filepath.Join(t.TempDir(), "state", "con")
	if err := EnsureSessionStateDirectory(base, "ws"); err == nil {
		t.Fatal("EnsureSessionStateDirectory created a reserved device stem level")
	}
}
