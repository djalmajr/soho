package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func stubRename(t *testing.T, wait time.Duration, rename func() error) {
	t.Helper()
	realRename, realWait := renameFile, renameWait
	renameFile, renameWait = func(string, string) error { return rename() }, wait
	t.Cleanup(func() { renameFile, renameWait = realRename, realWait })
}

func TestRenameReplacingWaitsForAReaderOnWindows(t *testing.T) {
	refusals := map[string]error{
		"access denied":     &os.LinkError{Op: "rename", Err: os.ErrPermission},
		"sharing violation": &os.LinkError{Op: "rename", Err: syscall.Errno(32)},
	}
	for name, refusal := range refusals {
		t.Run(name, func(t *testing.T) {
			calls := 0
			stubRename(t, time.Second, func() error {
				calls++
				if calls < 3 {
					return refusal
				}
				return nil
			})
			if err := renameReplacing("tmp", "target", "windows"); err != nil {
				t.Fatal(err)
			}
			if calls != 3 {
				t.Fatalf("calls = %d, want 3", calls)
			}
		})
	}
}

func TestRenameReplacingGivesUpAtTheDeadline(t *testing.T) {
	refusal := &os.LinkError{Op: "rename", Err: os.ErrPermission}
	calls := 0
	stubRename(t, 20*time.Millisecond, func() error { calls++; return refusal })
	if err := renameReplacing("tmp", "target", "windows"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want a retry before giving up", calls)
	}
}

func TestRenameReplacingDoesNotRetryOtherFailures(t *testing.T) {
	cases := []struct {
		name, goos string
		err        error
	}{
		{"other error on Windows", "windows", errors.New("injected EIO")},
		{"permission elsewhere", "linux", &os.LinkError{Op: "rename", Err: os.ErrPermission}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			stubRename(t, time.Second, func() error { calls++; return c.err })
			if err := renameReplacing("tmp", "target", c.goos); !errors.Is(err, c.err) {
				t.Fatalf("err = %v", err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestAtomicWriteReplacesAFileHeldOpenOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to replace an open file")
	}
	path := filepath.Join(t.TempDir(), "pointer")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(100 * time.Millisecond)
		_ = reader.Close()
	}()
	err = AtomicWrite(path, "new\n")
	<-released
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
}
