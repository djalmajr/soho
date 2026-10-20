package core

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/platform"
)

// The queues live in a directory other users of the machine could reach
// first: without TMPDIR on Linux it is /tmp/soho/<workspace>. Each of the
// two levels, <tmp>/soho and <tmp>/soho/<workspace>, must be a directory
// (os.Lstat: never a link) and, on Unix, the current user's; the queue
// file must be a regular file of that user, read only after the file
// opened is proved to be the one checked, and rewritten through a fresh
// O_EXCL temporary renamed over it, so a link in its place is replaced,
// never followed.

// PendingOwnerOf reads the owner uid of a checked file (Unix: the stat
// uid; ok is false where the owner is not read). Tests swap it, in this
// package and in the commands': a test without root cannot create another
// user's file.
var PendingOwnerOf = fileOwnerUID

// PendingRefusedError is a queue directory or file that is not the
// current user's private one. It never wraps fs.ErrPermission: the
// incorporation reports it instead of taking it for a sandbox.
type PendingRefusedError struct {
	Path  string
	Cause string
}

func (e *PendingRefusedError) Error() string {
	return fmt.Sprintf("queue: refusing %s: %s", e.Path, e.Cause)
}

// pendingOwnerRefusal is the owner check of a checked level or file: ""
// when it is the current user's, or where the owner is not checked
// (Windows, whose %TEMP% is the user's).
func pendingOwnerRefusal(info fs.FileInfo) string {
	if !pendingChecksOwner {
		return ""
	}
	uid, ok := PendingOwnerOf(info)
	if !ok || uid != os.Getuid() {
		return "it belongs to another user"
	}
	return ""
}

// pendingDirMode says how pendingQueueDir treats the two levels.
type pendingDirMode int

const (
	// pendingDirCreate creates a missing level with 0o700 and narrows an
	// own level writable by the group or the others to 0o700 (the queue
	// writers).
	pendingDirCreate pendingDirMode = iota
	// pendingDirFix creates nothing (a missing level is no queue) but
	// narrows an own level to 0o700 (the incorporation).
	pendingDirFix
	// pendingDirRead creates and changes nothing; an own level writable
	// by the group or the others is refused (the read-only footprint).
	pendingDirRead
)

// pendingQueueDir prepares and checks <tmp>/soho and <tmp>/soho/<ws> and
// returns the second. A missing level in pendingDirFix or pendingDirRead
// comes back as an os.ErrNotExist error: there is no queue.
func pendingQueueDir(env platform.Env, workspaceID string, mode pendingDirMode) (string, error) {
	dir := PendingDir(env, workspaceID)
	base := filepath.Dir(dir)
	if mode == pendingDirCreate {
		if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
			return "", err
		}
	}
	for _, level := range []string{base, dir} {
		if err := pendingPrivateLevel(level, mode); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func pendingPrivateLevel(path string, mode pendingDirMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && mode == pendingDirCreate {
		if mkErr := os.Mkdir(path, 0o700); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
			return mkErr
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &PendingRefusedError{Path: path, Cause: "it is a symbolic link"}
	}
	if !info.IsDir() {
		return &PendingRefusedError{Path: path, Cause: "it is not a directory"}
	}
	if cause := pendingOwnerRefusal(info); cause != "" {
		return &PendingRefusedError{Path: path, Cause: cause}
	}
	if pendingChecksOwner && info.Mode().Perm()&0o022 != 0 {
		if mode == pendingDirRead {
			return &PendingRefusedError{Path: path, Cause: "it is writable by other users"}
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// readPendingFile reads a queue file that os.Lstat proves to be a regular
// file of the current user; the file opened must be that same file (a
// link swapped in after the check is refused), so a link is never read.
func readPendingFile(queue string) (string, error) {
	info, err := os.Lstat(queue)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", &PendingRefusedError{Path: queue, Cause: "it is not a regular file"}
	}
	if cause := pendingOwnerRefusal(info); cause != "" {
		return "", &PendingRefusedError{Path: queue, Cause: cause}
	}
	f, err := os.Open(queue)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, opened) {
		return "", &PendingRefusedError{Path: queue, Cause: "it changed after the check"}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return platform.DecodeText(data), nil
}

// writePendingFile replaces a queue file with content: a temporary file
// created with O_EXCL in the same directory, renamed over the queue, so a
// link in the queue's place is replaced and its target is never written.
func writePendingFile(queue, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(queue), "."+filepath.Base(queue)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, queue); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
