//go:build unix

package sessionmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// doneBefore runs fn in a goroutine and fails if it has not returned
// before the bound: the confined opens must never block on a FIFO. fn
// must not call Fatalf (it runs outside the test goroutine); it reports
// problems as errors.
func doneBefore(t *testing.T, bound time.Duration, fn func() error) error {
	t.Helper()
	ch := make(chan error, 1)
	go func() {
		ch <- fn()
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(bound):
		t.Fatalf("the operation blocked beyond %v on a FIFO", bound)
		return nil
	}
}

// TestUnixFIFONeverBlocks plants FIFOs at the base, the session and the
// record: every confined open must refuse them without blocking, and a
// FIFO base must not hold a writer either.
func TestUnixFIFONeverBlocks(t *testing.T) {
	f := newFS(t, "s1")

	t.Run("fifo session", func(t *testing.T) {
		real := filepath.Join(f.base, "s1")
		if err := os.Rename(real, real+"-real"); err != nil {
			t.Fatalf("the session cannot be moved aside: %v", err)
		}
		defer func() {
			_ = os.RemoveAll(filepath.Join(f.base, "s1"))
			_ = os.Rename(real+"-real", real)
		}()
		if err := syscall.Mkfifo(filepath.Join(f.base, "s1"), 0o640); err != nil {
			t.Fatalf("the FIFO cannot be made: %v", err)
		}
		err := doneBefore(t, 500*time.Millisecond, func() error {
			_, ok, err := f.st.Read()
			if err == nil || ok {
				return fmt.Errorf("FIFO session read ok=%v, want an error", ok)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("FIFO session read: %v", err)
		}
	})

	t.Run("fifo record", func(t *testing.T) {
		if err := syscall.Mkfifo(f.recPath(), 0o640); err != nil {
			t.Fatalf("the FIFO cannot be made: %v", err)
		}
		if err := doneBefore(t, 500*time.Millisecond, func() error {
			_, ok, err := f.st.Read()
			if err == nil || ok {
				return fmt.Errorf("FIFO record read ok=%v, want an error", ok)
			}
			return nil
		}); err != nil {
			t.Fatalf("FIFO record read: %v", err)
		}
		// A writer over a FIFO record must also refuse without blocking.
		if err := doneBefore(t, 500*time.Millisecond, func() error {
			_, err := f.st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1})
			if err == nil {
				return fmt.Errorf("FIFO record register, want an error")
			}
			return nil
		}); err != nil {
			t.Fatalf("FIFO record register: %v", err)
		}
		if info, err := os.Stat(f.recPath()); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("the FIFO record changed: %v %v", info, err)
		}
	})

	t.Run("fifo base", func(t *testing.T) {
		fifoBase := filepath.Join(f.raw, "fifo-base")
		if err := syscall.Mkfifo(fifoBase, 0o640); err != nil {
			t.Fatalf("the FIFO cannot be made: %v", err)
		}
		st := Store{StateBase: fifoBase, SessionID: "s1"}
		err := doneBefore(t, 500*time.Millisecond, func() error {
			_, ok, err := st.Read()
			if err == nil || ok {
				return fmt.Errorf("FIFO base read ok=%v, want an error", ok)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("FIFO base read: %v", err)
		}
	})
}

// TestTempCleanupFailure: a genuine failure to remove the retained own
// temp — the session directory without write permission — is surfaced by
// the cleanup, not discarded, and the temp stays exactly as found.
func TestTempCleanupFailure(t *testing.T) {
	f := newFS(t, "s1")
	r, _, err := f.st.openSession()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p := "owned.tmp"
	own, err := r.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("the own temp cannot be created: %v", err)
	}
	if _, err := own.Write([]byte("aa")); err != nil {
		t.Fatalf("the own temp cannot be written: %v", err)
	}
	opened, err := own.Stat()
	if err != nil {
		t.Fatalf("the own temp identity cannot be retained: %v", err)
	}
	own.Close()
	if os.Geteuid() == 0 {
		// A root process bypasses the directory permission: the identity
		// path still removes the own temp.
		if err := os.Chmod(filepath.Join(f.base, f.sess), 0o550); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		defer os.Chmod(filepath.Join(f.base, f.sess), 0o750)
		if err := removeOwn(r, p, opened); err != nil {
			t.Fatalf("root removeOwn err=%v, want the identity removal", err)
		}
		if _, err := r.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("the own temp remains: %v", err)
		}
		return
	}
	if err := os.Chmod(filepath.Join(f.base, f.sess), 0o550); err != nil {
		t.Fatalf("the session directory cannot be made read-only: %v", err)
	}
	defer os.Chmod(filepath.Join(f.base, f.sess), 0o750)
	if err := removeOwn(r, p, opened); err == nil {
		t.Fatalf("removeOwn under a read-only session dir, want the genuine removal failure surfaced")
	}
	if info, err := r.Lstat(p); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the own temp was touched although the removal failed: %v", err)
	}
}
