//go:build !windows

package core

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSessionSourceScratchUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the owned fixture's directory permissions")
	}
	base, _ := sessionFixture(t, "s1")
	scratch := t.TempDir()
	dir := filepath.Join(scratch, "soho")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	s := openSource(t, base, "s1", scratch)
	if _, err := s.ObserveScratchReport("evidence"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable scratch must be an error, got %v", err)
	}
}

// TestSessionSourceFIFONeverBlocks: a FIFO planted where a marker or a
// report is expected refuses with no read (the confined open is
// nonblocking) and does not block the bounded reader.
func TestSessionSourceFIFONeverBlocks(t *testing.T) {
	base, sd := sessionFixture(t, "s1")
	marker := filepath.Join(sd, "wait", "w1.marker")
	if err := syscall.Mkfifo(marker, 0o640); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(sd, "reports", "w1-final.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(report, 0o640); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, base, "s1", "")
	done := make(chan error, 1)
	go func() {
		_, err := s.ReadMetadata("wait", "w1.marker")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("the FIFO marker was read")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the FIFO marker read blocked")
	}
	if _, err := s.ObserveStateReport("w1-final.md"); err != nil {
		t.Fatalf("the FIFO report observation failed: %v", err)
	}
}
