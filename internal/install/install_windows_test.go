//go:build windows

// Windows-specific proof for the replacement path. os.Rename on Windows is
// MoveFileEx with MOVEFILE_REPLACE_EXISTING (internal/syscall/windows.
// Rename), so it replaces a normal existing destination; these tests prove
// that replacement succeeds with static payloads, and that a destination
// held open by a running process, which MoveFileEx refuses to replace, is
// moved to <destination>.old (or .old-<n>) so the new bytes take its
// place, with the move undone when the new binary cannot follow. No Go toolchain is needed at test time: the fixture is the
// re-executed native test binary itself (its own bytes are the installed
// artifact), not a program built by the test. The generic tests in
// install_test.go also run on Windows for the shared contracts.
package install

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testBinaryBytes reads the running test binary. On Windows it is a native
// PE executable, so its bytes can be installed as the artifact and then
// launched to hold its file open — without building anything.
func testBinaryBytes(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestMain doubles as the re-executed helper. Launched with -install.hold
// as the installed destination, the process blocks until killed, keeping
// its executable image file open share-read only; that is what makes
// MoveFileEx replacement of the same path fail on Windows.
func TestMain(m *testing.M) {
	for _, arg := range os.Args {
		if arg == "-install.hold" {
			for {
				time.Sleep(time.Hour) // keep a timer live, avoiding a Go deadlock exit
			}
		}
	}
	os.Exit(m.Run())
}

// holdRunning launches the executable at path in hold mode (see TestMain)
// and stops it in the test's cleanup, before the temp dirs are removed.
func holdRunning(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(path, "-install.hold")
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start installed executable: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
}

// Replacement of an existing destination through os.Rename
// (MoveFileEx REPLACE_EXISTING), with static payloads and no toolchain: a
// destination that is not running is replaced directly, with no .old.
func TestInstallWindowsReplacesExistingDestination(t *testing.T) {
	first := testBinaryBytes(t)
	got := runCore(t, fixtureOptions{payload: first})
	if got.err != nil {
		t.Fatalf("first install: %v", got.err)
	}
	second := []byte("static replacement payload v2\n")
	again := runCore(t, fixtureOptions{payload: second, installDir: got.installDir})
	if again.err != nil {
		t.Fatalf("replace install: %v", again.err)
	}
	installed, err := os.ReadFile(again.res.Destination)
	if err != nil || !bytes.Equal(installed, second) {
		t.Fatalf("destination not replaced: len=%d err=%v", len(installed), err)
	}
	if again.res.Previous != "" {
		t.Fatalf("Previous = %q; want empty for a direct replacement", again.res.Previous)
	}
	assertDirExact(t, got.installDir, "soho.exe")
}

// A destination whose bytes are a running executable is locked by the
// loader, so MoveFileEx refuses to replace it; Windows still lets it be
// renamed. The install moves it to .old and puts the new bytes in place:
// the new content is at the destination, the running one at .old, and the
// temp files are removed.
func TestInstallWindowsRunningDestinationIsMovedAside(t *testing.T) {
	exe := testBinaryBytes(t)
	got := runCore(t, fixtureOptions{payload: exe})
	if got.err != nil {
		t.Fatalf("first install: %v", got.err)
	}
	destination := got.res.Destination
	holdRunning(t, destination)
	second := []byte("static replacement payload v2\n")
	again := runCore(t, fixtureOptions{payload: second, installDir: got.installDir})
	if again.err != nil {
		t.Fatalf("replacing the running destination: %v", again.err)
	}
	installed, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(installed, second) {
		t.Fatalf("destination not replaced: len=%d err=%v", len(installed), err)
	}
	moved, err := os.ReadFile(destination + ".old")
	if err != nil || !bytes.Equal(moved, exe) {
		t.Fatalf("%s.old does not hold the running binary: len=%d err=%v", destination, len(moved), err)
	}
	if again.res.Previous != destination+".old" {
		t.Fatalf("Previous = %q; want %q", again.res.Previous, destination+".old")
	}
	assertDirExact(t, got.installDir, "soho.exe", "soho.exe.old")
}

// A .old an earlier install left, whose process has ended, is removed by
// the next install, which then replaces the destination directly.
func TestInstallWindowsRemovesARemovableEarlierOld(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"soho.exe": "previous binary", "soho.exe.old": "older binary"})
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, filepath.Join(dir, "soho.exe"), fakeBinary)
	if got.res.Previous != "" {
		t.Fatalf("Previous = %q; want empty", got.res.Previous)
	}
	assertDirExact(t, dir, "soho.exe")
}

// A .old still running (it cannot be removed) and a running destination:
// the install keeps the .old and moves the destination to .old-1.
func TestInstallWindowsEarlierOldInUseLeadsToASuffix(t *testing.T) {
	exe := testBinaryBytes(t)
	got := runCore(t, fixtureOptions{payload: exe})
	if got.err != nil {
		t.Fatalf("first install: %v", got.err)
	}
	destination := got.res.Destination
	holdRunning(t, destination)
	again := runCore(t, fixtureOptions{payload: exe, installDir: got.installDir})
	if again.err != nil || again.res.Previous != destination+".old" {
		t.Fatalf("second install: %v, Previous %q; want the running binary at .old", again.err, again.res.Previous)
	}
	holdRunning(t, destination)
	third := []byte("static replacement payload v3\n")
	last := runCore(t, fixtureOptions{payload: third, installDir: got.installDir})
	if last.err != nil {
		t.Fatalf("third install: %v", last.err)
	}
	if last.res.Previous != destination+".old-1" {
		t.Fatalf("Previous = %q; want %q", last.res.Previous, destination+".old-1")
	}
	installed, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(installed, third) {
		t.Fatalf("destination not replaced: len=%d err=%v", len(installed), err)
	}
	for _, aside := range []string{destination + ".old", destination + ".old-1"} {
		data, err := os.ReadFile(aside)
		if err != nil || !bytes.Equal(data, exe) {
			t.Fatalf("%s does not hold a running binary: len=%d err=%v", aside, len(data), err)
		}
	}
	assertDirExact(t, got.installDir, "soho.exe", "soho.exe.old", "soho.exe.old-1")
}

// The new binary cannot follow the move aside (the second rename fails,
// simulated by the rename seam): the move aside is undone while the old
// binary runs, so the destination keeps its bytes, no .old is left, the
// error surfaces and the temp files are removed.
func TestInstallWindowsSecondRenameFailureRestoresTheDestination(t *testing.T) {
	exe := testBinaryBytes(t)
	got := runCore(t, fixtureOptions{payload: exe})
	if got.err != nil {
		t.Fatalf("first install: %v", got.err)
	}
	destination := got.res.Destination
	holdRunning(t, destination)
	tempOntoDestination := 0
	useReplaceSeams(t, true, func(src, dst string) error {
		if dst == destination && strings.HasPrefix(filepath.Base(src), ".soho.") {
			tempOntoDestination++
			if tempOntoDestination == 2 {
				return &os.LinkError{Op: "rename", Old: src, New: dst, Err: errors.New("simulated: second rename refused")}
			}
		}
		return os.Rename(src, dst)
	}, nil)
	again := runCore(t, fixtureOptions{payload: []byte("static replacement payload v2\n"), installDir: got.installDir})
	if again.err == nil || tempOntoDestination != 2 {
		t.Fatalf("install err = %v after %d renames onto the destination; want the second one's failure", again.err, tempOntoDestination)
	}
	installed, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(installed, exe) {
		t.Fatalf("running destination changed: len=%d err=%v", len(installed), err)
	}
	assertDirExact(t, got.installDir, "soho.exe")
}
