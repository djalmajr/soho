//go:build !windows

package core

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// Focused read-only companion tests for CompactPendingReadInspect
// (review-2 F4): the bounded nonblocking inspection read shares
// compactPendingParse's validation with the command reader, refuses
// nonregular markers on the opened handle without blocking, and enforces
// the private 64 KiB metadata cap.

const inspectValidMarker = `{"version":1,"token":"tok","pane":"p1","brief":"/tmp/brief.md","pid":4242,"started":"2026-10-31T10:00:00Z","created_at":"2026-10-31T10:00:00Z"}`

func TestCompactPendingReadInspectSharedParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wait", "worker.compact-pending.json")
	if _, err := CompactPendingReadInspect(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent marker err = %v, want os.ErrNotExist", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(inspectValidMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := CompactPendingReadInspect(path)
	if err != nil {
		t.Fatalf("valid marker err = %v", err)
	}
	ref, err := CompactPendingRead(path)
	if err != nil {
		t.Fatalf("the command reader err = %v", err)
	}
	if rec.Version != ref.Version || rec.Token != ref.Token || rec.Pane != ref.Pane || rec.Brief != ref.Brief || rec.PID != ref.PID || rec.Started != ref.Started || rec.CreatedAt != ref.CreatedAt {
		t.Fatalf("inspection read = %+v, want the command reader's record %+v", rec, ref)
	}
	if err := os.WriteFile(path, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactPendingReadInspect(path); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt marker err = %v, want the corrupted-record refusal", err)
	}
}

// TestCompactPendingReadInspectCap: a marker over the private 64 KiB cap is
// refused as unverifiable, and a marker exactly at the cap is not a cap
// refusal (its parse result then stands).
func TestCompactPendingReadInspectCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.compact-pending.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), compactInspectionMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CompactPendingReadInspect(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 65536-byte inspection cap") {
		t.Fatalf("over-cap marker err = %v, want the cap refusal", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), compactInspectionMax), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactPendingReadInspect(path); err != nil && strings.Contains(err.Error(), "inspection cap") {
		t.Fatalf("at-cap marker err = %v, must not be a cap refusal", err)
	}
}

// TestCompactPendingReadInspectRefusesFIFO: a FIFO at the marker path is
// refused on the opened handle (platform.ErrNotRegular) without blocking —
// no writer is present or needed.
func TestCompactPendingReadInspectRefusesFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.compact-pending.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CompactPendingReadInspect(path)
	if !errors.Is(err, platform.ErrNotRegular) {
		t.Fatalf("FIFO marker err = %v, want platform.ErrNotRegular", err)
	}
}

// A linked marker never authorizes reading its target, even when that target
// contains a valid record; the ordinary command reader retains its behavior.
func TestCompactPendingReadInspectRefusesLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.json")
	markerDir := filepath.Join(dir, "wait")
	if err := os.Mkdir(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte(inspectValidMarker + "\n")
	if err := os.WriteFile(target, want, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(markerDir, "worker.compact-pending.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactPendingReadInspect(path); !errors.Is(err, platform.ErrNotRegular) {
		t.Fatalf("linked marker err = %v, want platform.ErrNotRegular", err)
	}
	if _, err := CompactPendingRead(path); err != nil {
		t.Fatalf("ordinary reader changed: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("linked target changed: %q, %v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("linked entry changed: %v, %v", info, err)
	}
}
