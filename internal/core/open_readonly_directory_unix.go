//go:build unix

package core

import (
	"fmt"
	"os"
	"runtime"

	"github.com/djalmajr/soho/internal/platform"
)

// openReadOnlyDirectory acquires one checked plain child directory without
// ever blocking on a raced-in FIFO: the child is opened nonblocking
// (a FIFO with no writer refuses the open, and one with a writer is
// refused by the kind check), the opened handle is required to be a
// directory, and that descriptor is retained as its own os.Root through
// the descriptor path — never an unchecked pathname reopen of name. The
// new root's identity is verified against the opened handle, and every
// intermediate handle is closed. On a plain directory the nonblocking
// flag changes nothing, and the caller keeps its pre and post identity
// checks against the parent entry.
func openReadOnlyDirectory(parent *os.Root, name string) (*os.Root, error) {
	f, err := parent.OpenFile(name, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("the directory %q is not an opened directory", name)
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
		_ = root.Close()
		return nil, fmt.Errorf("the directory %q handle identity changed", name)
	}
	return root, nil
}
