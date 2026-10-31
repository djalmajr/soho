//go:build unix

package skill

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// readOnlyNonBlocking is the open mode of a published-bundle read that must
// never block on a planted FIFO: read-only and non-blocking. On a regular
// file the flag changes nothing. skills/soho cannot import internal/platform
// (import cycle), so the flag is mirrored here from the standard library
// only, matching platform.ReadOnlyNonBlocking.
const readOnlyNonBlocking = syscall.O_RDONLY | syscall.O_NONBLOCK

// openBundleDirectory refuses a raced-in FIFO without blocking, checks the
// opened kind, then roots the descriptor itself. It never reopens name.
func openBundleDirectory(parent *os.Root, name string) (*os.Root, error) {
	f, err := parent.OpenFile(name, readOnlyNonBlocking, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("bundled skill: not an opened directory: %s", name)
	}
	fdPath := fmt.Sprintf("/dev/fd/%d", f.Fd())
	if runtime.GOOS == "linux" {
		fdPath = fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	}
	root, err := os.OpenRoot(fdPath)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("bundled skill: directory handle identity changed: %s", name)
	}
	return root, nil
}
