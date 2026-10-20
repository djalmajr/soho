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

// The workspace scratch, <TMPDIR (or os.TempDir())>/soho/<workspace>, is a
// directory other users of the machine could reach first: without TMPDIR
// on Linux it is /tmp/soho/<workspace>. It holds the registration queues
// (DJA-323) and, in its reports level, the composed briefs, the attempt
// sidecars and the reports of the workers that cannot reach the state dir
// (DJA-443). Each level, <tmp>/soho, <tmp>/soho/<workspace> and any level
// below it, must be a directory (os.Lstat: never a link) and, on Unix, the
// current user's; a file is read only after os.Lstat proves it a regular
// file of that user and the file opened is the one checked, and written
// through a fresh O_EXCL temporary renamed over it, so a link in its place
// is replaced, never followed.

// ScratchOwnerOf reads the owner uid of a checked file (Unix: the stat
// uid; ok is false where the owner is not read). Tests swap it, in this
// package and in the commands': a test without root cannot create another
// user's file.
var ScratchOwnerOf = fileOwnerUID

// ScratchRefusedError is a scratch directory or file that is not the
// current user's private one. Kind names what it holds ("queue" or
// "reports"). It never wraps fs.ErrPermission: the queue incorporation
// reports it instead of taking it for a sandbox.
type ScratchRefusedError struct {
	Kind  string
	Path  string
	Cause string
}

func (e *ScratchRefusedError) Error() string {
	return fmt.Sprintf("%s: refusing %s: %s", e.Kind, e.Path, e.Cause)
}

// scratchOwnerRefusal is the owner check of a checked level or file: ""
// when it is the current user's, or where the owner is not checked
// (Windows, whose %TEMP% is the user's).
func scratchOwnerRefusal(info fs.FileInfo) string {
	if !scratchChecksOwner {
		return ""
	}
	uid, ok := ScratchOwnerOf(info)
	if !ok || uid != os.Getuid() {
		return "it belongs to another user"
	}
	return ""
}

// ScratchMode says how the scratch check treats its levels.
type ScratchMode int

const (
	// ScratchCreate creates a missing level with 0o700 and narrows an own
	// level with any permission for the group or the others to 0o700 (the
	// writers: the queue writers, the dispatch).
	ScratchCreate ScratchMode = iota
	// ScratchFix creates nothing (a missing level is nothing to read) but
	// narrows an own level the same way (the queue incorporation, the readers
	// of the reports level).
	ScratchFix
	// ScratchRead creates and changes nothing; an own level writable by
	// the group or the others is refused (the read-only footprint).
	ScratchRead
)

// ScratchDir is the workspace scratch dir.
func ScratchDir(env platform.Env, workspaceID string) string {
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	return filepath.Join(tmp, "soho", workspaceID)
}

// ScratchReportsDir is the reports level of the workspace scratch: where
// the dispatch puts the composed brief, the sidecar and the report path of
// a worker whose cwd does not hold the state dir.
func ScratchReportsDir(env platform.Env, workspaceID string) string {
	return filepath.Join(ScratchDir(env, workspaceID), "reports")
}

// privateScratchDir prepares and checks, from the top, <tmp>/soho,
// <tmp>/soho/<ws> and each of sub below it, and returns the last one. A
// missing level in ScratchFix or ScratchRead comes back as an
// os.ErrNotExist error: there is nothing to read.
func privateScratchDir(env platform.Env, workspaceID, kind string, mode ScratchMode, sub ...string) (string, error) {
	dir := ScratchDir(env, workspaceID)
	base := filepath.Dir(dir)
	if mode == ScratchCreate {
		if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
			return "", err
		}
	}
	levels := []string{base, dir}
	for _, name := range sub {
		dir = filepath.Join(dir, name)
		levels = append(levels, dir)
	}
	for _, level := range levels {
		if err := privateScratchLevel(level, kind, mode); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// PrivateScratchReports checks the three levels of the scratch reports
// dir in mode and returns it (see privateScratchDir).
func PrivateScratchReports(env platform.Env, workspaceID string, mode ScratchMode) (string, error) {
	return privateScratchDir(env, workspaceID, "reports", mode, "reports")
}

func privateScratchLevel(path, kind string, mode ScratchMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && mode == ScratchCreate {
		if mkErr := os.Mkdir(path, 0o700); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
			return mkErr
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &ScratchRefusedError{Kind: kind, Path: path, Cause: "it is a symbolic link"}
	}
	if !info.IsDir() {
		return &ScratchRefusedError{Kind: kind, Path: path, Cause: "it is not a directory"}
	}
	if cause := scratchOwnerRefusal(info); cause != "" {
		return &ScratchRefusedError{Kind: kind, Path: path, Cause: cause}
	}
	if !scratchChecksOwner {
		return nil
	}
	// The read-only footprint changes nothing: it refuses, silently for its
	// caller, only a level other users can write (a level they can only
	// read does not change the trust in what it holds). The writers and
	// the readers that act narrow any permission for the group or the
	// others to 0o700: a level other users can read exposes the briefs and
	// the reports.
	if mode == ScratchRead {
		if info.Mode().Perm()&0o022 != 0 {
			return &ScratchRefusedError{Kind: kind, Path: path, Cause: "it is writable by other users"}
		}
		return nil
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// readScratchFile reads a file that os.Lstat proves to be a regular file
// of the current user (on Unix); with refuseOpen, one writable by the group
// or the others is refused too. The file opened must be that same file (a
// link swapped in after the check is refused), so a link is never read.
func readScratchFile(path, kind string, refuseOpen bool) ([]byte, error) {
	info, err := statScratchFile(path, kind)
	if err != nil {
		return nil, err
	}
	if refuseOpen && scratchChecksOwner && info.Mode().Perm()&0o022 != 0 {
		return nil, &ScratchRefusedError{Kind: kind, Path: path, Cause: "it is writable by other users, so its lines cannot be trusted; remove it by hand"}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, &ScratchRefusedError{Kind: kind, Path: path, Cause: "it changed after the check"}
	}
	return io.ReadAll(f)
}

// statScratchFile is the os.Lstat check of a scratch file: a regular file
// (not a link) of the current user on Unix.
func statScratchFile(path, kind string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &ScratchRefusedError{Kind: kind, Path: path, Cause: "it is not a regular file"}
	}
	if cause := scratchOwnerRefusal(info); cause != "" {
		return nil, &ScratchRefusedError{Kind: kind, Path: path, Cause: cause}
	}
	return info, nil
}

// StatScratchReport is the os.Lstat check of a file of the scratch reports
// level, for a reader that only needs its size or its time.
func StatScratchReport(path string) (fs.FileInfo, error) {
	return statScratchFile(path, "reports")
}

// ReadScratchReport reads a file of the scratch reports level (a report,
// a composed brief, a sidecar) without following a link: a regular file
// of the current user on Unix. Its mode is not checked: the directory is
// private, and a worker's umask may give 0o664.
func ReadScratchReport(path string) ([]byte, error) {
	return readScratchFile(path, "reports", false)
}

// WriteScratchFile replaces path with content: a temporary file created
// with O_EXCL (0o600) in the same directory, renamed over path, so a link
// in its place is replaced and its target is never written.
func WriteScratchFile(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
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
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
