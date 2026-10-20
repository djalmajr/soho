package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func skipLinksOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows without developer mode")
	}
}

func skipOwnerOffUnix(t *testing.T) {
	t.Helper()
	if !scratchChecksOwner {
		t.Skip("the owner and the mode are checked on Unix only")
	}
}

// ownedByAnother makes ScratchOwnerOf report another uid for the files
// whose base name is in names, and the real owner for the others.
func ownedByAnother(t *testing.T, names ...string) {
	t.Helper()
	old := ScratchOwnerOf
	ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		for _, name := range names {
			if info.Name() == name {
				return os.Getuid() + 1, true
			}
		}
		return old(info)
	}
	t.Cleanup(func() { ScratchOwnerOf = old })
}

func wantRefused(t *testing.T, err error, path, cause string) {
	t.Helper()
	var refused *ScratchRefusedError
	if !errors.As(err, &refused) || refused.Path != path || refused.Cause != cause {
		t.Fatalf("err=%v; want the refusal of %s: %s", err, path, cause)
	}
	if errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err=%v wraps fs.ErrPermission: the incorporation would stay silent", err)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestQueueLevelsAreCreatedPrivate(t *testing.T) {
	skipOwnerOffUnix(t)
	env := platform.Env{"TMPDIR": t.TempDir()}
	if _, err := QueueCopy(env, "ws", t.TempDir(), pendingCopy("/x/c")); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{filepath.Join(env.Get("TMPDIR"), "soho"), PendingDir(env, "ws")} {
		if mode := modeOf(t, level); mode != 0o700 {
			t.Fatalf("%s mode=%o; want 0700", level, mode)
		}
	}
}

func TestAnOwnLevelOpenToOthersIsNarrowed(t *testing.T) {
	skipOwnerOffUnix(t)
	env := platform.Env{"TMPDIR": t.TempDir()}
	dir := PendingDir(env, "ws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{filepath.Dir(dir), dir} {
		if err := os.Chmod(level, 0o775); err != nil {
			t.Fatal(err)
		}
	}
	// The footprint changes nothing: it ignores such a queue.
	sd := t.TempDir()
	if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
		t.Fatalf("rows=%v", rows)
	}
	if mode := modeOf(t, dir); mode != 0o775 {
		t.Fatalf("the footprint changed the mode to %o", mode)
	}
	if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c")); err != nil {
		t.Fatalf("the queue does not work in an own 0775 level: %v", err)
	}
	for _, level := range []string{filepath.Dir(dir), dir} {
		if mode := modeOf(t, level); mode != 0o700 {
			t.Fatalf("%s mode=%o; want 0700", level, mode)
		}
	}
	if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 1 {
		t.Fatalf("rows=%v; want the queued copy", rows)
	}
}

func TestALevelOfAnotherOwnerIsRefused(t *testing.T) {
	skipOwnerOffUnix(t)
	for _, name := range []string{"soho", "ws"} {
		t.Run(name, func(t *testing.T) {
			env := platform.Env{"TMPDIR": t.TempDir()}
			sd := t.TempDir()
			if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c")); err != nil {
				t.Fatal(err)
			}
			before := readRaw(t, PendingCopiesFile(env, "ws"))
			ownedByAnother(t, name)
			level := PendingDir(env, "ws")
			if name == "soho" {
				level = filepath.Dir(level)
			}
			_, err := QueueCopy(env, "ws", sd, pendingCopy("/x/d"))
			wantRefused(t, err, level, "it belongs to another user")
			_, err = IncorporatePendingCopies(sd, "ws", env, acceptCopy)
			wantRefused(t, err, level, "it belongs to another user")
			_, err = IncorporatePendingProcs(sd, "ws", env)
			wantRefused(t, err, level, "it belongs to another user")
			if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
				t.Fatalf("the footprint read a refused queue: %v", rows)
			}
			if got := readRaw(t, PendingCopiesFile(env, "ws")); got != before {
				t.Fatalf("a refused level's queue changed: %q -> %q", before, got)
			}
			if _, err := os.Stat(CopiesFile(sd)); !os.IsNotExist(err) {
				t.Fatalf("a refused queue reached the registry: %v", err)
			}
		})
	}
}

func TestALinkInPlaceOfTheWorkspaceLevelIsRefused(t *testing.T) {
	skipLinksOnWindows(t)
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	elsewhere := t.TempDir()
	// The link's target holds a queue for sd: it must never be read.
	theirs := filepath.Join(elsewhere, "copies.pending.tsv")
	content := pendingCopiesHeader + "\n" + sd + "\t/x/planted\tw\tp\tc\t-\tadd\t-\t-\n"
	if err := os.WriteFile(theirs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := PendingDir(env, "ws")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	_, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c"))
	wantRefused(t, err, dir, "it is a symbolic link")
	_, err = IncorporatePendingCopies(sd, "ws", env, acceptCopy)
	wantRefused(t, err, dir, "it is a symbolic link")
	if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
		t.Fatalf("the footprint read through the link: %v", rows)
	}
	if got := readRaw(t, theirs); got != content {
		t.Fatalf("the link's target changed: %q", got)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 1 {
		t.Fatalf("something was written through the link: %v", entries)
	}
	if _, err := os.Stat(CopiesFile(sd)); !os.IsNotExist(err) {
		t.Fatalf("the planted line reached the registry: %v", err)
	}
}

func TestALinkInPlaceOfTheQueueIsNeverFollowed(t *testing.T) {
	skipLinksOnWindows(t)
	setup := func(t *testing.T) (platform.Env, string, string, string, string) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		sd := t.TempDir()
		if _, err := pendingQueueDir(env, "ws", ScratchCreate); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "victim.txt")
		content := pendingCopiesHeader + "\n" + sd + "\t/x/planted\tw\tp\tc\t-\tadd\t-\t-\n"
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		queue := PendingCopiesFile(env, "ws")
		if err := os.Symlink(target, queue); err != nil {
			t.Fatal(err)
		}
		return env, sd, queue, target, content
	}

	t.Run("the read refuses it and reads nothing", func(t *testing.T) {
		env, sd, queue, target, content := setup(t)
		_, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy)
		wantRefused(t, err, queue, "it is not a regular file")
		_, err = QueueCopy(env, "ws", sd, pendingCopy("/x/c"))
		wantRefused(t, err, queue, "it is not a regular file")
		if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
			t.Fatalf("the footprint read through the link: %v", rows)
		}
		if _, err := os.Stat(CopiesFile(sd)); !os.IsNotExist(err) {
			t.Fatalf("the target's line reached the registry: %v", err)
		}
		if got := readRaw(t, target); got != content {
			t.Fatalf("the target changed: %q", got)
		}
		if info, err := os.Lstat(queue); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was not left as it was: %v", err)
		}
	})

	t.Run("the rewrite replaces the link and leaves its target as it was", func(t *testing.T) {
		_, _, queue, target, content := setup(t)
		if err := writePending(queue, pendingCopiesHeader, []string{"/sd\t/x/mine\tw\tp\tc\t-\tadd\t-\t-"}); err != nil {
			t.Fatal(err)
		}
		if got := readRaw(t, target); got != content {
			t.Fatalf("the rewrite went through the link: %q", got)
		}
		info, err := os.Lstat(queue)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("the queue is not a regular file after the rewrite: %v %v", info, err)
		}
		if got := readRaw(t, queue); !strings.Contains(got, "/x/mine") {
			t.Fatalf("queue=%q", got)
		}
		entries, _ := os.ReadDir(filepath.Dir(queue))
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				t.Fatalf("a temporary file was left: %s", entry.Name())
			}
		}
	})
}

func TestAQueueFileOfAnotherOwnerIsRefused(t *testing.T) {
	skipOwnerOffUnix(t)
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	queue, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c"))
	if err != nil {
		t.Fatal(err)
	}
	before := readRaw(t, queue)
	ownedByAnother(t, filepath.Base(queue))
	_, err = IncorporatePendingCopies(sd, "ws", env, acceptCopy)
	wantRefused(t, err, queue, "it belongs to another user")
	_, err = QueueCopy(env, "ws", sd, pendingCopy("/x/d"))
	wantRefused(t, err, queue, "it belongs to another user")
	if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
		t.Fatalf("the footprint read another user's queue: %v", rows)
	}
	if got := readRaw(t, queue); got != before {
		t.Fatalf("the queue changed: %q -> %q", before, got)
	}
}

func TestTheFootprintIgnoresARefusedQueueSilently(t *testing.T) {
	skipLinksOnWindows(t)
	env := platform.Env{"TMPDIR": t.TempDir(), "PATH": os.Getenv("PATH")}
	sd := t.TempDir()
	copyDir := filepath.Join(t.TempDir(), "queued")
	seedFile(t, filepath.Join(copyDir, "f.bin"), 1024)
	if _, err := pendingQueueDir(env, "ws", ScratchCreate); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.tsv")
	if err := os.WriteFile(target, []byte(pendingCopiesHeader+"\n"+sd+"\t"+copyDir+"\tw\tp\tc\t-\tadd\t-\t-\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, PendingCopiesFile(env, "ws")); err != nil {
		t.Fatal(err)
	}
	oldErr := platform.Stderr
	var errOut strings.Builder
	platform.Stderr = &errOut
	fp := MeasureFootprint(sd, "ws", env)
	platform.Stderr = oldErr
	if fp.Copies != 0 || errOut.String() != "" {
		t.Fatalf("copies=%d stderr=%q; want the refused queue ignored, silently", fp.Copies, errOut.String())
	}
}

func TestAQueueFileOpenToOthersIsRefusedNotNarrowed(t *testing.T) {
	skipOwnerOffUnix(t)
	env := platform.Env{"TMPDIR": t.TempDir(), "PATH": os.Getenv("PATH")}
	sd := t.TempDir()
	copyDir := filepath.Join(t.TempDir(), "queued")
	seedFile(t, filepath.Join(copyDir, "f.bin"), 512)
	queue, err := QueueCopy(env, "ws", sd, pendingCopy(copyDir))
	if err != nil {
		t.Fatal(err)
	}
	if mode := modeOf(t, queue); mode != 0o600 {
		t.Fatalf("the queue was created with %o; want 0600", mode)
	}
	if err := os.Chmod(queue, 0o666); err != nil {
		t.Fatal(err)
	}
	before := readRaw(t, queue)
	cause := "it is writable by other users, so its lines cannot be trusted; remove it by hand"
	_, err = IncorporatePendingCopies(sd, "ws", env, acceptCopy)
	wantRefused(t, err, queue, cause)
	_, err = QueueCopy(env, "ws", sd, pendingCopy("/x/d"))
	wantRefused(t, err, queue, cause)
	if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
		t.Fatalf("the footprint read the queue: %v", rows)
	}
	if fp := MeasureFootprint(sd, "ws", env); fp.Copies != 0 {
		t.Fatalf("the footprint counted the queue: %+v", fp)
	}
	if _, err := os.Stat(CopiesFile(sd)); !os.IsNotExist(err) {
		t.Fatalf("the queue reached the registry: %v", err)
	}
	if got := readRaw(t, queue); got != before {
		t.Fatalf("the queue was rewritten: %q -> %q", before, got)
	}
	if mode := modeOf(t, queue); mode != 0o666 {
		t.Fatalf("the queue's mode was changed to %o; it is refused, not narrowed", mode)
	}
}

// The incorporation takes the registry lock before the queue's, always.
func TestIncorporationTakesTheRegistryLockBeforeTheQueueLock(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c")); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueProc(env, "ws", sd, ProcRow{Pid: 4242, Started: "s", Name: "n", Owner: "o", Pane: "p", Created: "c"}); err != nil {
		t.Fatal(err)
	}
	queueLock := PendingCopiesFile(env, "ws") + ".lock"
	procsQueueLock := PendingProcsFile(env, "ws") + ".lock"
	seen := []string{}
	pendingLockHook = func(lock string) {
		// The hook sees the lock files themselves: the registry's is
		// already held when it is told "registry", and the queue's only
		// at "queue".
		held := func(path string) bool { _, err := os.Lstat(path); return err == nil }
		switch lock {
		case "registry":
			if !held(filepath.Join(sd, copiesLockName)) && !held(filepath.Join(sd, procsLockName)) {
				t.Errorf("the registry lock is not held at %q", lock)
			}
			if held(queueLock) || held(procsQueueLock) {
				t.Errorf("the queue lock was taken before the registry's")
			}
		case "queue":
			if !held(queueLock) && !held(procsQueueLock) {
				t.Errorf("the queue lock is not held at %q", lock)
			}
		}
		seen = append(seen, lock)
	}
	t.Cleanup(func() { pendingLockHook = nil })
	if res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy); err != nil || res.Moved != 1 {
		t.Fatalf("copies: res=%+v err=%v", res, err)
	}
	if res, err := IncorporatePendingProcs(sd, "ws", env); err != nil || res.Moved != 1 {
		t.Fatalf("procs: res=%+v err=%v", res, err)
	}
	if strings.Join(seen, ",") != "registry,queue,registry,queue" {
		t.Fatalf("lock order=%v; want the registry lock, then the queue's, for each queue", seen)
	}
}

// The levels are checked from the top: <tmp>/soho before <tmp>/soho/<ws>,
// on every path that uses the queue.
func TestTheQueueLevelsAreCheckedParentFirst(t *testing.T) {
	skipOwnerOffUnix(t)
	env := platform.Env{"TMPDIR": t.TempDir()}
	sd := t.TempDir()
	if err := os.MkdirAll(PendingDir(env, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := ScratchOwnerOf
	seen := []string{}
	ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		if info.IsDir() {
			seen = append(seen, info.Name())
		}
		return old(info)
	}
	t.Cleanup(func() { ScratchOwnerOf = old })
	steps := []struct {
		name string
		run  func()
	}{
		{"the writer", func() { _, _ = QueueCopy(env, "ws", sd, pendingCopy("/x/c")) }},
		{"the incorporation", func() { _, _ = IncorporatePendingCopies(sd, "ws", env, acceptCopy) }},
		{"the footprint", func() { _ = PendingCopiesFor(sd, "ws", env) }},
	}
	for _, step := range steps {
		seen = seen[:0]
		step.run()
		if len(seen) < 2 || seen[0] != "soho" || seen[1] != "ws" {
			t.Fatalf("%s: owner check order=%v; want soho then ws", step.name, seen)
		}
	}
}

// An own level other users can only read (0o755) is narrowed to 0o700 by
// the queue writer and by the incorporation; the read-only footprint reads
// the queue through it and changes nothing (DJA-443, amendment 1).
func TestAnOwnLevelReadableByOthersIsNarrowedExceptByTheFootprint(t *testing.T) {
	skipOwnerOffUnix(t)
	setLevels := func(t *testing.T, env platform.Env, mode os.FileMode) []string {
		t.Helper()
		dir := PendingDir(env, "ws")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		levels := []string{filepath.Dir(dir), dir}
		for _, level := range levels {
			if err := os.Chmod(level, mode); err != nil {
				t.Fatal(err)
			}
		}
		return levels
	}
	wantModes := func(t *testing.T, levels []string, want os.FileMode) {
		t.Helper()
		for _, level := range levels {
			if mode := modeOf(t, level); mode != want {
				t.Fatalf("%s mode=%o; want %o", level, mode, want)
			}
		}
	}

	t.Run("the queue writer", func(t *testing.T) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		levels := setLevels(t, env, 0o755)
		if _, err := QueueCopy(env, "ws", t.TempDir(), pendingCopy("/x/c")); err != nil {
			t.Fatal(err)
		}
		wantModes(t, levels, 0o700)
	})

	t.Run("the incorporation", func(t *testing.T) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		sd := t.TempDir()
		if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c")); err != nil {
			t.Fatal(err)
		}
		levels := setLevels(t, env, 0o755)
		if res, err := IncorporatePendingCopies(sd, "ws", env, acceptCopy); err != nil || res.Moved != 1 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		wantModes(t, levels, 0o700)
	})

	t.Run("the footprint reads a 0755 level and changes nothing", func(t *testing.T) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		sd := t.TempDir()
		if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c")); err != nil {
			t.Fatal(err)
		}
		levels := setLevels(t, env, 0o755)
		if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 1 {
			t.Fatalf("rows=%v; want the queued copy read", rows)
		}
		wantModes(t, levels, 0o755)
	})

	t.Run("the footprint refuses a 0775 level silently and changes nothing", func(t *testing.T) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		sd := t.TempDir()
		if _, err := QueueCopy(env, "ws", sd, pendingCopy("/x/c")); err != nil {
			t.Fatal(err)
		}
		levels := setLevels(t, env, 0o775)
		if rows := PendingCopiesFor(sd, "ws", env); len(rows) != 0 {
			t.Fatalf("rows=%v; want the queue ignored", rows)
		}
		wantModes(t, levels, 0o775)
	})
}

// The reports level follows the same rule in the readers that act: an own
// 0o755 level is narrowed by ScratchFix.
func TestTheReportsReadersNarrowAnOwnLevelReadableByOthers(t *testing.T) {
	skipOwnerOffUnix(t)
	env := platform.Env{"TMPDIR": t.TempDir()}
	dir, err := PrivateScratchReports(env, "ws", ScratchCreate)
	if err != nil {
		t.Fatal(err)
	}
	levels := []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir}
	for _, level := range levels {
		if err := os.Chmod(level, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PrivateScratchReports(env, "ws", ScratchFix); err != nil {
		t.Fatal(err)
	}
	for _, level := range levels {
		if mode := modeOf(t, level); mode != 0o700 {
			t.Fatalf("%s mode=%o; want 0700", level, mode)
		}
	}
}
