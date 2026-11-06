//go:build unix

package transcript

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The rescue writer is owned by this test and always joined. Removing the
// nonblocking-open flag must yield an ordinary assertion failure, not hang
// the package until its watchdog fires.
func TestCodexReceiptFIFOReplacementIsNonblocking(t *testing.T) {
	for _, kind := range []string{"candidate", "directory", "sessions"} {
		t.Run(kind, func(t *testing.T) {
			home, path := receiptFixture(t, receiptHeader())
			control := NewCodexReceipt(home, receiptUUID, receiptClosing)
			if control == nil {
				t.Fatal("valid FIFO-test baseline did not arm")
			}
			control.Close()
			rel := filepath.Join("2026", "11", "05", filepath.Base(path))
			if kind == "directory" {
				path = filepath.Join(home, "sessions", "2026")
				rel = "2026"
			}
			if kind == "sessions" {
				path = filepath.Join(home, "sessions")
				rel = "sessions"
			}
			var setupErr error
			replaced := false
			done := make(chan *CodexReceipt, 1)
			ready := make(chan struct{})
			go func() {
				fired := false
				done <- newCodexReceipt(home, receiptUUID, receiptClosing, codexReceiptLimits, func(stage string) {
					if stage != "before-open:"+rel || fired {
						return
					}
					fired = true
					replaced = true
					if setupErr = os.Rename(path, path+".owned-original"); setupErr == nil {
						setupErr = syscall.Mkfifo(path, 0600)
					}
					close(ready)
				})
			}()
			var r *CodexReceipt
			select {
			case r = <-done:
			case <-ready:
				select {
				case r = <-done:
				case <-time.After(time.Second):
					// O_RDWR|O_NONBLOCK opens even without a peer, and releases a
					// deliberately mutated blocking reader before we join it.
					rescue, e := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0600)
					if e != nil {
						t.Fatalf("cannot release owned FIFO reader: %v", e)
					}
					r = <-done
					_ = rescue.Close()
					t.Error("root-relative replacement open blocked on an owned FIFO")
				}
			}
			if r != nil {
				t.Cleanup(r.Close)
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			if !replaced {
				t.Fatal("fixture did not reach the inspection/open replacement window")
			}
			if r != nil {
				r.Close()
				t.Error("FIFO replacement armed a regular-file reader")
			}
		})
	}
}
