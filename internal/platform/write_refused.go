package platform

import (
	"errors"
	"io/fs"
	"syscall"
)

// WriteRefused reports whether err is the refusal of a sandbox that cannot
// write a path: a permission error (EACCES, EPERM; how macOS's sandbox
// refuses) or a read-only file system (EROFS; how a Linux sandbox that
// binds the path read-only refuses). Go's fs.ErrPermission does not match
// EROFS, so a check on fs.ErrPermission alone misses the Linux case.
func WriteRefused(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}
