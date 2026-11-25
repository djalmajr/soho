//go:build !windows

package setup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A shared addition must keep the existing exclude inode, group, mode and
// CRLF-aware bytes rather than replacing it with a file owned by the writer's
// primary group.
func TestSharedExcludeAppendPreservesInode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "exclude")
	before := "# rules\r\n/state-a\r\n"
	if err := os.WriteFile(path, []byte(before), 0o660); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstStat, ok := first.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no POSIX ownership")
	}
	after := before + "/state-b\r\n"
	if err := writeSharedExclude(path, before, after, true); err != nil {
		t.Fatalf("append: %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	secondStat, ok := second.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no POSIX ownership")
	}
	if !os.SameFile(first, second) || firstStat.Gid != secondStat.Gid || first.Mode() != second.Mode() {
		t.Fatalf("append replaced or re-owned the exclude: first=%v second=%v", first, second)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != after {
		t.Fatalf("exclude=%q err=%v", data, err)
	}
}

// First creation uses the parent's real access class and is verified before
// returning; an inaccessible result is reported rather than silently shared.
// Private metadata keeps the atomic replacement contract; a same-inode
// rewrite there would mutate a file whose permission refusal must stand.
func TestPrivateExcludeRewriteStaysAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exclude")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSharedExclude(path, "old\n", "new\n", false); err != nil {
		t.Fatalf("private rewrite: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("private exclude was mutated in place; expected atomic replacement")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("exclude=%q err=%v", data, err)
	}
}

func TestSharedExcludeWriterRejectsRedirect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exclude")
	target := filepath.Join(dir, "outside")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeSharedExclude(path, "", "/state-a\n", true); err == nil {
		t.Fatal("a symlinked exclude was accepted")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep\n" {
		t.Fatalf("redirect target changed: %q err=%v", data, err)
	}
}

func TestSharedExcludeFirstCreationAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "exclude")
	content := "/state-a\n"
	if err := writeSharedExclude(path, "", content, true); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := verifySharedExcludeAccess(path, 0o660); err != nil {
		t.Fatalf("created exclude is not accessible: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("exclude=%q err=%v", data, err)
	}
}
