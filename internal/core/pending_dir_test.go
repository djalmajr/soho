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
	if !pendingChecksOwner {
		t.Skip("the owner and the mode are checked on Unix only")
	}
}

// ownedByAnother makes PendingOwnerOf report another uid for the files
// whose base name is in names, and the real owner for the others.
func ownedByAnother(t *testing.T, names ...string) {
	t.Helper()
	old := PendingOwnerOf
	PendingOwnerOf = func(info fs.FileInfo) (int, bool) {
		for _, name := range names {
			if info.Name() == name {
				return os.Getuid() + 1, true
			}
		}
		return old(info)
	}
	t.Cleanup(func() { PendingOwnerOf = old })
}

func wantRefused(t *testing.T, err error, path, cause string) {
	t.Helper()
	var refused *PendingRefusedError
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
		if _, err := pendingQueueDir(env, "ws", pendingDirCreate); err != nil {
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
	if _, err := pendingQueueDir(env, "ws", pendingDirCreate); err != nil {
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
