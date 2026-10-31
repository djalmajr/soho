//go:build unix

package core_test

// Unix-only consumers for the bounded read-only custom-role read: a FIFO
// or an unreadable file at the selected role location refuses without
// blocking or unbounded reads (filesystem FIFOs do not exist on Windows).

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
)

// reBoundedRefuse runs fn (which must refuse a raced-in special file
// promptly) under a deadline: if the acquisition blocks, the owned FIFO
// writer is opened to unblock and join the reader, and the test fails —
// a blocked acquisition is a refusal failure, never a hang.
func reBoundedRefuse(t *testing.T, fifo string, fn func() bool) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- false
			}
		}()
		done <- fn()
	}()
	select {
	case got := <-done:
		if got {
			t.Fatal("the unsafe role metadata resolved edit")
		}
	case <-time.After(5 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err != nil {
			t.Fatalf("cannot open the owned FIFO writer to unblock the blocked reader: %v", err)
		} else {
			w.Close()
		}
		<-done
		t.Fatal("the unsafe role metadata blocked the read-only inspection instead of refusing promptly")
	}
}

// TestReadOnlyRoleFIFORefused: a FIFO raced into the selected role file
// refuses without blocking or reading the body.
func TestReadOnlyRoleFIFORefused(t *testing.T) {
	env, root := reRoleFixture(t)
	roles := filepath.Join(root, ".agents", "soho-roles")
	fifo := filepath.Join(roles, "pipe-role.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("the test user cannot create FIFOs: %v", err)
	}
	reBoundedRefuse(t, fifo, func() bool { return core.RoleIsEditReadOnly("pipe-role", env, root) })
	if _, err := os.Lstat(fifo); err != nil {
		t.Fatalf("the FIFO must remain in place: %v", err)
	}
}

// TestReadOnlyRoleUnreadableRefused: an unreadable regular role file
// refuses like unavailable metadata.
func TestReadOnlyRoleUnreadableRefused(t *testing.T) {
	env, root := reRoleFixture(t)
	roles := filepath.Join(root, ".agents", "soho-roles")
	file := filepath.Join(roles, "locked.md")
	reRoleWrite(t, roles, "locked", "---\nmode: edit\n---\nbody\n")
	if err := os.Chmod(file, 0); err != nil {
		t.Skipf("the test user cannot drop read permission: %v", err)
	}
	defer func() { _ = os.Chmod(file, 0o600) }()
	if got := core.RoleIsEditReadOnly("locked", env, root); got {
		t.Fatal("an unreadable role file must refuse like unavailable metadata")
	}
}
