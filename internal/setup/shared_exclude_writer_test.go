//go:build !windows

package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A short append must restore the original byte length under the held lock,
// leaving no active partial ignore rule, and a later real append must work.
// Publishing a transformation must keep the live UID, GID and mode on the
// staged descriptor before rename, even when no ownership change is needed.
func TestAtomicSharedExcludePreservesPrincipals(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "exclude")
	if err := os.WriteFile(path, []byte("old\n"), 0o660); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicSharedExclude(path, "old\n", "old\n/new\n", original); err != nil {
		t.Fatalf("atomic replacement: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if original.Mode().Perm() != after.Mode().Perm() || fileIDs(original) != fileIDs(after) {
		t.Fatalf("principals changed: before mode=%04o ids=%s after mode=%04o ids=%s", original.Mode().Perm(), fileIDs(original), after.Mode().Perm(), fileIDs(after))
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old\n/new\n" {
		t.Fatalf("exclude=%q err=%v", data, err)
	}
}

func fileIDs(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "no-posix-ids"
	}
	return fmt.Sprintf("%d:%d", stat.Uid, stat.Gid)
}

func TestSharedExcludeAppendRollsBackShortWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "exclude")
	before := "# rules\n"
	if err := os.WriteFile(path, []byte(before), 0o660); err != nil {
		t.Fatal(err)
	}
	original := writeSharedExcludeAt
	called := false
	writeSharedExcludeAt = func(f *os.File, p []byte, off int64) (int, error) {
		called = true
		// Actually put a partial rule on disk before returning short, so the
		// rollback assertion proves the truncation removed real bytes.
		return f.WriteAt(p[:2], off) // "/s" from "/state-a"
	}
	t.Cleanup(func() { writeSharedExcludeAt = original })

	after := before + "/state-a\n"
	if err := writeSharedExclude(path, before, after, true); err == nil || !strings.Contains(err.Error(), "restored the original") {
		t.Fatalf("short-write diagnostic = %v", err)
	}
	if !called {
		t.Fatal("the append seam was not used")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != before {
		t.Fatalf("partial rule persisted: %q err=%v", data, err)
	}

	writeSharedExcludeAt = original
	if err := writeSharedExclude(path, before, after, true); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != after {
		t.Fatalf("retry result=%q err=%v", data, err)
	}
}
