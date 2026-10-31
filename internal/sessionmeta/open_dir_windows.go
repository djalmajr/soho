//go:build windows

package sessionmeta

import (
	"errors"
	"fmt"
	"os"
)

// openDirRoot opens an existing directory as a handle-relative os.Root.
// Windows has no FIFO, so the handle open cannot block; the os.Root
// resolution refuses any escape outside the directory, and a reparse
// point that retargets outside the tree is refused by the handle
// resolution.
func openDirRoot(path string) (*os.Root, error) {
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("sessionmeta: the state directory cannot be opened: %v", err)
	}
	return r, nil
}

// openSessionDir opens one session component through the base root
// handle, binding the opened root to the entry identity the caller
// already checked as a plain directory: a swap of the checked entry
// between the check and the open is refused by identity, and the nofollow
// root resolution refuses a reparse point at the component.
func openSessionDir(baseRoot *os.Root, session string, checked os.FileInfo) (*os.Root, error) {
	r, err := baseRoot.OpenRoot(session)
	if err != nil {
		return nil, fmt.Errorf("sessionmeta: the session directory cannot be opened: %v", err)
	}
	info, err := r.Lstat(".")
	if err != nil || !info.IsDir() || !os.SameFile(checked, info) {
		_ = r.Close()
		return nil, errors.New("sessionmeta: the session identity changed between the check and the open")
	}
	return r, nil
}
