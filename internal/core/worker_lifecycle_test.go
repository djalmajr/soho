package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func TestWorkerLifecycleCrashHelper(t *testing.T) {
	sd := os.Getenv("TEST_WORKER_LIFECYCLE_CRASH")
	if sd == "" {
		return
	}
	if _, err := LockWorkerLifecycle(sd, "worker"); err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass defers: the next command must recover the file
	// without deleting a live holder's lock or waiting for an age threshold.
	os.Exit(0)
}

func TestWorkerLifecycleLockLifetime(t *testing.T) {
	sd := t.TempDir()
	unlock, err := LockWorkerLifecycle(sd, "worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	if unexpected, err := TryLockWorkerLifecycle(sd, "worker"); !errors.Is(err, ErrWorkerLifecycleBusy) {
		if unexpected != nil {
			unexpected()
		}
		t.Fatalf("second owner acquired a live lock: %v", err)
	}
	other, err := TryLockWorkerLifecycle(sd, "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	files, err := filepath.Glob(filepath.Join(sd, "worker-*.lock"))
	if err != nil || len(files) != 1 {
		t.Fatalf("held lock file: %v %v", files, err)
	}
	stale, err := openWorkerLifecycleFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stale.Close() })
	unlock()
	if runtime.GOOS == "windows" {
		// Windows keeps an unlinked file delete-pending until its last
		// handle closes; no replacement pathname can exist in that window.
		if err := stale.Close(); err != nil {
			t.Fatal(err)
		}
	}
	successor, err := LockWorkerLifecycle(sd, "worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(successor)
	if runtime.GOOS != "windows" {
		if unexpected, err := tryWorkerLifecycleLockFile(files[0], stale); !errors.Is(err, ErrWorkerLifecycleBusy) {
			if unexpected != nil {
				unexpected()
			}
			t.Fatalf("stale inode became a concurrent owner: %v", err)
		}
	}
	successor()
	func() {
		defer func() {
			if _, ok := recover().(*platform.ExitError); !ok {
				t.Error("expected platform.Die")
			}
		}()
		unlock, err := LockWorkerLifecycle(sd, "worker")
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		platform.Die("refusal", 3)
	}()
	assertWorkerLifecycleFilesGone(t, sd)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestWorkerLifecycleCrashHelper$", "-test.count=1")
	cmd.Env = append(testutil.CleanEnv(t), "TEST_WORKER_LIFECYCLE_CRASH="+sd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v %s", err, out)
	}
	unlock, err = TryLockWorkerLifecycle(sd, "worker")
	if err != nil {
		t.Fatalf("dead holder blocked successor: %v", err)
	}
	unlock()
	assertWorkerLifecycleFilesGone(t, sd)
}

func assertWorkerLifecycleFilesGone(t *testing.T, sd string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(sd, "worker-*.lock"))
	if err != nil || len(files) != 0 {
		t.Fatalf("lock files left: %v %v", files, err)
	}
}
