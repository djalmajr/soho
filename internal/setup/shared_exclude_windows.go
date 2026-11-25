//go:build windows

package setup

import (
	"path/filepath"

	"github.com/djalmajr/soho/internal/platform"
)

// sharedLockTarget gives the Windows file publisher one stable sidecar inside
// the validated info directory. The directory itself cannot be published.
func sharedLockTarget(dir string) string {
	return filepath.Join(dir, ".soho-exclude.lock")
}

// writeSharedExclude keeps the Windows atomic replacement: the native file
// lock is held and directory ACL inheritance supplies group access.
func writeSharedExclude(path, _ string, after string, _ bool) error {
	return platform.AtomicWrite(path, after)
}
