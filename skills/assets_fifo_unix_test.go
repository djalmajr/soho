//go:build unix

package skill

// A FIFO planted at a known resource must be refused without blocking the
// verification: the entry's kind is checked before the (nonblocking)
// open, so the lookup can never hang on it.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBundleCachedRefusesFifoResourceWithoutBlocking(t *testing.T) {
	cache := t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.defaults")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(file, 0o600); err != nil {
		t.Skipf("no fifos on this system: %v", err)
	}
	if _, err := Cached(cache); err == nil {
		t.Fatal("a FIFO resource was accepted; the nonregular kind must be refused")
	}
}
