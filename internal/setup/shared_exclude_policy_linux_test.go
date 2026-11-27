//go:build linux

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"syscall"
)

// The staged exclude must carry the live file's specific access attributes
// and drop staged-only entries before publication.
func TestSharedExcludeAccessPolicyCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	staged := filepath.Join(dir, "staged")
	for _, path := range []string{source, staged} {
		if err := os.WriteFile(path, []byte("rules\n"), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Setxattr(source, "user.soho.acl", []byte("named-entry"), 0); err != nil {
		t.Skipf("user xattrs unavailable: %v", err)
	}
	if err := syscall.Setxattr(staged, "user.soho.inherited", []byte("broad"), 0); err != nil {
		t.Fatal(err)
	}
	srcFile, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer srcFile.Close()
	stagedFile, err := os.Open(staged)
	if err != nil {
		t.Fatal(err)
	}
	defer stagedFile.Close()
	if err := copySharedExcludeAccessPolicy(srcFile, stagedFile); err != nil {
		t.Fatalf("policy copy: %v", err)
	}
	value := make([]byte, 32)
	n, err := syscall.Getxattr(staged, "user.soho.acl", value)
	if err != nil || string(value[:n]) != "named-entry" {
		t.Fatalf("staged ACL value=%q err=%v", value[:n], err)
	}
	if _, err := syscall.Getxattr(staged, "user.soho.inherited", value); err == nil {
		t.Fatal("staged-only xattr was not removed")
	}
}
