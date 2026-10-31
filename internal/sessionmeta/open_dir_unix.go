//go:build unix

package sessionmeta

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/djalmajr/soho/internal/platform"
)

// openDirRoot opens an existing directory as a handle-relative os.Root
// without ever blocking: the nonblocking open refuses a FIFO in the
// directory's place, the opened handle must be a real directory, and the
// root is opened from the open descriptor itself, so a replacement of the
// path after the open cannot retarget the root.
func openDirRoot(path string) (*os.Root, error) {
	f, err := os.OpenFile(path, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil, fmt.Errorf("sessionmeta: the state directory cannot be opened: %v", err)
	}
	return rootFromDirHandle(f, nil)
}

// openSessionDir opens one session component through the base root's
// handle without ever blocking, binding the opened handle to the entry
// identity the caller already checked as a plain directory: a swap of the
// checked entry between the check and the open is refused, and the root is
// opened from the descriptor, not from a path that can be replaced.
func openSessionDir(baseRoot *os.Root, session string, checked os.FileInfo) (*os.Root, error) {
	f, err := baseRoot.OpenFile(session, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil, fmt.Errorf("sessionmeta: the session directory cannot be opened: %v", err)
	}
	return rootFromDirHandle(f, checked)
}

// rootFromDirHandle turns a handle opened in the read-only nonblocking
// mode into an os.Root only when the handle is a real directory, when the
// checked identity is nil or the handle keeps it, and when the root's own
// identity is the handle's. f is always closed.
func rootFromDirHandle(f *os.File, checked os.FileInfo) (*os.Root, error) {
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return nil, errors.New("sessionmeta: the state path is not a plain directory")
	}
	if checked != nil && !os.SameFile(checked, info) {
		return nil, errors.New("sessionmeta: the session identity changed between the check and the open")
	}
	fdPath := fmt.Sprintf("/dev/fd/%d", f.Fd())
	if runtime.GOOS == "linux" {
		fdPath = fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	}
	r, err := os.OpenRoot(fdPath)
	if err != nil {
		return nil, fmt.Errorf("sessionmeta: the state directory cannot be rooted: %v", err)
	}
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(opened, info) {
		_ = r.Close()
		return nil, errors.New("sessionmeta: the state directory identity changed")
	}
	return r, nil
}
