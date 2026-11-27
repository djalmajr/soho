//go:build !windows

package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/djalmajr/soho/internal/platform"
)

// sharedLockTarget keeps the Unix lock identity on the validated directory;
// no sidecar file is published.
func sharedLockTarget(dir string) string { return dir }

// writeSharedExcludeAt is the single append write. A fixture overrides it to
// inject a short write and prove rollback under the held common lock.
var writeSharedExcludeAt = func(f *os.File, p []byte, off int64) (int, error) {
	return f.WriteAt(p, off)
}

// writeSharedExclude updates the common exclude under the held directory lock.
// Additions write the existing inode in place, preserving owner, group, mode
// and ACL. A new file gets the parent's real access class and is verified
// before any state is created; an inaccessible result is removed and reported
// with the file and actual permission detail.
func writeSharedExclude(path, before, after string, appendOnly bool) error {
	perm := sharedExcludeLockPerm(path)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if perm == 0o600 {
			return platform.AtomicWrite(path, after)
		}
		parent, statErr := os.Stat(filepath.Dir(path))
		if statErr != nil {
			return statErr
		}
		f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
		if openErr != nil {
			return openErr
		}
		created, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			return statErr
		}
		ok := false
		defer func() {
			if !ok {
				if current, err := os.Lstat(path); err == nil && os.SameFile(created, current) {
					_ = os.Remove(path)
				}
			}
		}()
		if perm == 0o660 {
			parentStat, ok := parent.Sys().(*syscall.Stat_t)
			if !ok {
				_ = f.Close()
				return fmt.Errorf("%s: no POSIX group to assign", path)
			}
			if err := f.Chown(-1, int(parentStat.Gid)); err != nil {
				_ = f.Close()
				return fmt.Errorf("%s: cannot assign parent group %d: %w", path, parentStat.Gid, err)
			}
		}
		// Create with O_EXCL and then apply the exact mode: the process
		// umask must not silently narrow a shared access class.
		if err := f.Chmod(perm); err != nil {
			_ = f.Close()
			return fmt.Errorf("%s: cannot set mode %04o: %w", path, perm, err)
		}
		if err := verifySharedExcludeAccess(path, perm); err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.WriteString(after); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		ok = true
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: shared exclude is not a regular file", path)
	}
	// Private metadata keeps the established atomic replacement, including
	// its permission refusal and retry semantics. Only shared class files
	// use the in-place writer that preserves group and ACL.
	if perm == 0o600 && info.Mode().Perm()&0o022 == 0 {
		return platform.AtomicWrite(path, after)
	}
	if !appendOnly {
		return atomicSharedExclude(path, before, after, info)
	}
	if !strings.HasPrefix(after, before) {
		return fmt.Errorf("%s: the exclude merge changed existing bytes; refusing a partial rewrite", path)
	}
	payload := after[len(before):]
	if payload == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// A directory-writable user may still replace a readable but
		// non-writable exclude. Keep the old rules intact if access
		// cannot be preserved on the replacement.
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return atomicSharedExclude(path, before, after, info)
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return fmt.Errorf("%s: exclude was replaced between inspection and open", path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if string(data) != before {
		return fmt.Errorf("%s: exclude changed during the transaction", path)
	}
	n, err := writeSharedExcludeAt(f, []byte(payload), int64(len(before)))
	if err != nil || n != len(payload) {
		rolledBack := false
		if current, statErr := f.Stat(); statErr == nil && os.SameFile(info, current) {
			if truncateErr := f.Truncate(int64(len(before))); truncateErr == nil {
				rolledBack = true
			}
		}
		cause := err
		if cause == nil {
			cause = fmt.Errorf("short write (%d of %d bytes)", n, len(payload))
		}
		if !rolledBack {
			return fmt.Errorf("%s: appended %d of %d bytes (%v); rollback failed and a partial rule may be active", path, n, len(payload), cause)
		}
		return fmt.Errorf("%s: appended %d of %d bytes (%v); restored the original %d bytes", path, n, len(payload), cause, len(before))
	}
	return nil
}

// atomicSharedExclude prepares the new contents and access mode before
// replacing the old file. Any staging failure leaves the old inode intact.
func atomicSharedExclude(path, before, after string, original os.FileInfo) error {
	staged, err := os.CreateTemp(filepath.Dir(path), ".soho-exclude-*")
	if err != nil {
		return err
	}
	name := staged.Name()
	defer os.Remove(name)
	defer staged.Close()
	if _, err := staged.WriteString(after); err != nil {
		return err
	}

	mode := original.Mode().Perm()
	originalStat, ok := original.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: no POSIX ownership to preserve", path)
	}
	stagedInfo, err := staged.Stat()
	if err != nil {
		return err
	}
	stagedStat, ok := stagedInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: staged exclude has no POSIX ownership", path)
	}
	// Apply only the principals that differ. A writer that cannot assign the
	// live UID/GID must fail here: unlink permission in the directory does not
	// grant permission to transfer ownership of the access policy.
	wantUID, wantGID := int(originalStat.Uid), int(originalStat.Gid)
	if int(stagedStat.Uid) != wantUID || int(stagedStat.Gid) != wantGID {
		uid, gid := -1, -1
		if int(stagedStat.Uid) != wantUID {
			uid = wantUID
		}
		if int(stagedStat.Gid) != wantGID {
			gid = wantGID
		}
		if err := staged.Chown(uid, gid); err != nil {
			return fmt.Errorf("%s: cannot preserve exclude owner %d and group %d while staging: %w", path, wantUID, wantGID, err)
		}
	}
	if err := staged.Chmod(mode); err != nil {
		return err
	}
	stagedInfo, err = staged.Stat()
	if err != nil {
		return err
	}
	stagedStat, ok = stagedInfo.Sys().(*syscall.Stat_t)
	if !ok || int(stagedStat.Uid) != wantUID || int(stagedStat.Gid) != wantGID || stagedInfo.Mode().Perm() != mode {
		return fmt.Errorf("%s: staged exclude does not match the live owner %d, group %d and mode %04o", path, wantUID, wantGID, mode)
	}
	live, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer live.Close()
	liveInfo, err := live.Stat()
	if err != nil || !os.SameFile(original, liveInfo) {
		return fmt.Errorf("%s: exclude was replaced before the access check", path)
	}
	if err := copySharedExcludeAccessPolicy(live, staged); err != nil {
		return fmt.Errorf("%s: cannot preserve the exclude's access policy on replacement: %w", path, err)
	}
	if err := staged.Close(); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) {
		return fmt.Errorf("%s: exclude was replaced during the transaction", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(data) != before {
		return fmt.Errorf("%s: exclude changed during the transaction", path)
	}
	return platform.RenameReplacing(name, path)
}

// verifySharedExcludeAccess checks the class the writers were promised using
// the created file and its parent, not a chmod assumption.
func verifySharedExcludeAccess(path string, want os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if got := info.Mode().Perm(); got&want != want {
		return fmt.Errorf("%s: mode %04o does not grant the required %04o", path, got, want)
	}
	if want == 0o660 {
		fileStat, ok := info.Sys().(*syscall.Stat_t)
		parentStat, parentOK := parent.Sys().(*syscall.Stat_t)
		if !ok || !parentOK {
			return fmt.Errorf("%s: no POSIX ownership to verify", path)
		}
		if fileStat.Gid != parentStat.Gid {
			return fmt.Errorf("%s: group %d does not match the parent group %d", path, fileStat.Gid, parentStat.Gid)
		}
	}
	return nil
}
