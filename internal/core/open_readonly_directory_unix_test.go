//go:build unix

package core

// Directory FIFO regression: a checked plain appended directory that is
// swapped to a FIFO must be refused promptly by the shared nonblocking
// directory acquisition — the actual nil default of openInspectDirectory
// and the readonly config appended-directory open — never by blocking on
// an owned writer. boundedFIFORefuse gives the implementation a short
// deadline and, only if it blocks, opens the owned FIFO writer to
// unblock and join the reader before failing, so a bad implementation
// cannot hang the test.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// boundedFIFORefuse runs fn under a short deadline. fn must return a
// non-nil error promptly; a blocked fn is unblocked with the owned FIFO
// writer, joined, and then fails.
func boundedFIFORefuse(t *testing.T, fifo string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("the raced-in FIFO was accepted")
		}
	case <-time.After(300 * time.Millisecond):
		writer, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatalf("cannot open the owned FIFO writer to unblock the blocked reader: %v", err)
		}
		defer writer.Close()
		if err := <-done; err == nil {
			t.Errorf("the raced-in FIFO was accepted after the owned unblock")
		}
		t.Fatal("the raced-in FIFO blocked the acquisition instead of refusing promptly")
	}
}

// swapToFIFO replaces the plain dir with a FIFO at the same path.
func swapToFIFO(t *testing.T, dir string) {
	t.Helper()
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Fatalf("cannot retain the checked directory: %v", err)
	}
	if err := syscall.Mkfifo(dir, 0o600); err != nil {
		t.Skipf("the test user cannot create FIFOs: %v", err)
	}
}

// TestReadOnlyDirectoryFIFORefusal: the actual default directory
// acquisition (the nil opener of openInspectDirectory) and the shared
// helper itself refuse a raced-in FIFO promptly, without a writer:
// (a) the FIFO stands where the checked directory was before the call —
// the pre-check refuses and no open runs; (b) the helper's own nonblocking
// open of the FIFO refuses (the operation that used to block); (c) the
// real OpenSessionSource through the nil default refuses a FIFO session
// dir; (d) the real openSessionSource through its existing per-call
// opener swaps after the pre-check and still calls the real default
// helper, which refuses the raced-in FIFO.
func TestReadOnlyDirectoryFIFORefusal(t *testing.T) {
	// (a) The actual default helper path with the FIFO in place: the
	// checked entry is a FIFO, the refusal is the pre-check's and no
	// open runs.
	base := t.TempDir()
	child := filepath.Join(base, "session")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	swapToFIFO(t, child)
	parent, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	boundedFIFORefuse(t, child, func() error {
		root, e := openInspectDirectory(parent, "session", nil)
		if root != nil {
			_ = root.Close()
		}
		return e
	})
	// (b) The shared helper's own nonblocking open of the raced-in FIFO.
	boundedFIFORefuse(t, child, func() error {
		root, e := openReadOnlyDirectory(parent, "session")
		if root != nil {
			_ = root.Close()
		}
		return e
	})
	// (c) The real OpenSessionSource, nil default, FIFO session dir.
	base2 := t.TempDir()
	fifo2 := filepath.Join(base2, "s1")
	if err := syscall.Mkfifo(fifo2, 0o600); err != nil {
		t.Skipf("the test user cannot create FIFOs: %v", err)
	}
	boundedFIFORefuse(t, fifo2, func() error {
		src, e := OpenSessionSource(base2, "s1", "")
		if src != nil {
			_ = src.Close()
		}
		return e
	})
	// (d) The real openSessionSource through its existing per-call
	// opener: the swap lands after the pre-check, and the opener calls
	// the real default helper (never a fake that merely errors).
	base3 := t.TempDir()
	child3 := filepath.Join(base3, "s1")
	if err := os.Mkdir(child3, 0o700); err != nil {
		t.Fatal(err)
	}
	boundedFIFORefuse(t, child3, func() error {
		src, e := openSessionSource(base3, "s1", "", func(p *os.Root, name string) (*os.Root, error) {
			if err := os.Rename(child3, child3+"-original"); err != nil {
				return nil, err
			}
			if err := syscall.Mkfifo(child3, 0o600); err != nil {
				return nil, err
			}
			return openReadOnlyDirectory(p, name)
		})
		if src != nil {
			_ = src.Close()
		}
		return e
	})
}

// TestReadOnlyDirectoryValidControls: a plain valid directory is
// acquired by the real default helper with its identity verified against
// the pre-checked parent entry, the retained parent stays usable, and
// the valid OpenSessionSource and readonly config reads keep working.
func TestReadOnlyDirectoryValidControls(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "session")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	pre, err := parent.Lstat("session")
	if err != nil {
		t.Fatal(err)
	}
	childRoot, err := openReadOnlyDirectory(parent, "session")
	if err != nil {
		t.Fatalf("the valid directory was refused: %v", err)
	}
	defer childRoot.Close()
	handle, err := childRoot.Lstat(".")
	if err != nil || !handle.IsDir() || !os.SameFile(pre, handle) {
		t.Fatalf("the acquired handle is not the checked directory: %v", err)
	}
	// The retained parent stays usable after the acquisition.
	after, err := parent.Lstat("session")
	if err != nil || !os.SameFile(pre, after) {
		t.Fatalf("the retained parent no longer sees the checked directory: %v", err)
	}
	// The real OpenSessionSource over the same base.
	src, err := OpenSessionSource(base, "session", "")
	if err != nil || src == nil || src.root == nil {
		t.Fatalf("OpenSessionSource = %v, %v; want the valid source", src, err)
	}
	_ = src.Close()
}

// TestReadOnlyConfigAppendedFIFORefusal: the readonly config confined
// reader refuses an appended directory swapped to a FIFO (a refusal,
// never absence) through the shared helper, and a valid layout keeps
// reading.
func TestReadOnlyConfigAppendedFIFORefusal(t *testing.T) {
	base := t.TempDir()
	mid := filepath.Join(base, "cfg")
	if err := os.Mkdir(mid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mid, "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapToFIFO(t, mid)
	boundedFIFORefuse(t, mid, func() error {
		data, absent, e := readConfinedFile(base, "cfg/config.json", 1<<20, nil)
		if e == nil {
			t.Logf("confined read = %q absent=%v", data, absent)
		}
		if e == nil || absent {
			return nil // the refusal must be an explicit error, never absence
		}
		return e
	})
	// Control: the valid layout reads through the same path.
	base2 := t.TempDir()
	mid2 := filepath.Join(base2, "cfg")
	if err := os.Mkdir(mid2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mid2, "config.json"), []byte("inventory=show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, absent, err := readConfinedFile(base2, "cfg/config.json", 1<<20, nil)
	if err != nil || absent || string(data) != "inventory=show\n" {
		t.Fatalf("valid read = %q, %v, %v; want the file bytes", string(data), absent, err)
	}
}
