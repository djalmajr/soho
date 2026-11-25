package setup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// Three writers with distinct entries share one common Git info/exclude: two
// ordinary local excludes and one real role-migration exclusion. They must
// serialize under the same directory lock so every entry survives. The hook
// checks an independent nonblocking lock attempt inside the locked section;
// deleting the lock while keeping the hook cannot pass.
func TestEnsureLocalExcludesSerializesCommonFile(t *testing.T) {
	root, env, _ := localPlanFixture(t)
	exclude := excludePath(root, env)
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := []byte("# pre-existing exclude\n/.agents/herdr-roles/\n")
	if err := os.WriteFile(exclude, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	lockDir := excludeLockDir(root, env)
	lock := sharedLockTarget(lockDir)
	holder, err := core.TryLockSharedPath(lock, 0o600)
	if err != nil {
		t.Fatalf("hold the shared exclude lock: %v", err)
	}

	acquired := make(chan bool, 3)
	previous := excludeLockAcquired
	excludeLockAcquired = func() {
		other, err := core.TryLockSharedPath(lock, 0o600)
		held := errors.Is(err, core.ErrWorkerLifecycleBusy)
		if !held && err == nil {
			other()
		}
		acquired <- held
	}
	t.Cleanup(func() { excludeLockAcquired = previous })

	var wg sync.WaitGroup
	done := make(chan string, 3)
	for _, rel := range []string{"state-a", "state-b"} {
		wg.Add(1)
		go func(rel string) {
			defer wg.Done()
			ensureLocalExcludes(root, []string{rel}, env, "init")
			done <- rel
		}(rel)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		migrateRolesExclude(root, env, false)
		done <- "migration"
	}()
	t.Cleanup(func() {
		holder()
		wg.Wait()
	})

	select {
	case name := <-done:
		t.Fatalf("writer %s finished while the shared lock was held", name)
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case held := <-acquired:
		if held {
			t.Fatal("a writer reached the callback while the holder owned the lock")
		}
	default:
	}

	holder()
	for i := 0; i < 3; i++ {
		select {
		case held := <-acquired:
			if !held {
				t.Fatal("the shared exclude lock was not held inside a writer's callback")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a writer did not reach the locked section")
		}
	}
	wg.Wait()
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		default:
			t.Fatal("a writer did not finish")
		}
	}

	data, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/state-a\n", "/state-b\n", "/.agents/soho-roles/\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("common exclude=%q; missing %q", data, want)
		}
	}
}

// The native lock is reusable and creates a single sidecar only on Windows.
func TestSharedExcludeLockLifetime(t *testing.T) {
	root, env, _ := localPlanFixture(t)
	lockDir := excludeLockDir(root, env)
	before, err := os.ReadDir(lockDir)
	if err != nil {
		t.Fatal(err)
	}
	lock := sharedLockTarget(lockDir)
	holder, err := core.TryLockSharedPath(lock, 0o600)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if other, err := core.TryLockSharedPath(lock, 0o600); !errors.Is(err, core.ErrWorkerLifecycleBusy) {
		if err == nil {
			other()
		}
		t.Fatalf("second owner acquired while held: %v", err)
	}
	holder()
	for i := 0; i < 2; i++ {
		unlock, err := core.TryLockSharedPath(lock, 0o600)
		if err != nil {
			t.Fatalf("reacquire %d: %v", i, err)
		}
		unlock()
	}
	after, err := os.ReadDir(lockDir)
	want := len(before)
	if runtime.GOOS == "windows" {
		want++
		info, err := os.Lstat(lock)
		if err != nil || !info.Mode().IsRegular() || filepath.Dir(lock) != lockDir {
			t.Fatalf("lock must be a regular sidecar in %s: %v", lockDir, err)
		}
	}
	if err != nil || len(after) != want {
		t.Fatalf("lock publication directory entries=%d, want %d: %v", len(after), want, err)
	}
}

func TestSharedExcludeLockAccessClass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission classes are not reported on Windows")
	}
	root, env, _ := localPlanFixture(t)
	path := excludePath(root, env)
	parent := filepath.Dir(path)
	for _, tc := range []struct {
		name string
		mode os.FileMode
		want os.FileMode
	}{
		{"private", 0o700, 0o600},
		{"group search only", 0o750, 0o600},
		{"group writable", 0o770, 0o660},
		{"other writable only", 0o702, 0o666},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Chmod(parent, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
			if got := sharedExcludeLockPerm(path); got != tc.want {
				t.Fatalf("perm=%04o, want %04o", got, tc.want)
			}
		})
	}
}

func TestExcludeRedirectCannotCreateTarget(t *testing.T) {
	root, env, ctx := localPlanFixture(t)
	env["SOHO_DIR"] = "state cache"
	ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
	exclude := excludePath(root, env)
	if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside-exclude")
	if err := os.Symlink(target, exclude); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	func() {
		defer func() {
			value := recover()
			exit, ok := value.(*platform.ExitError)
			if !ok || exit.Code != 4 || !strings.Contains(exit.Msg, "is a symlink") {
				t.Fatalf("refusal=%#v, want exit 4 naming the symlink redirect", value)
			}
		}()
		EnsureLocalStateExclude(root, ctx, env, root, "init")
		t.Fatal("a redirected exclude was accepted")
	}()
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("dangling exclude redirect created its target: %v", err)
	}
}
