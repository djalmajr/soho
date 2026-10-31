package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/platform"
)

// EnsureSessionStateDirectory safely initializes the minimal session state:
// the configured state base (its missing plain levels included) and the
// plain session directory. The deepest existing ancestor of the base is
// the trusted acquisition root; every level created after it is a
// handle-relative Mkdir verified by the shared checked child-directory
// acquisition, so a replaced, unsafe or nonplain component fails the
// initialization and is left as found.
//
// Only the minimum base/session is created: no report, roster, lock or
// cache, no .gitignore update. An existing base/session is verified and
// left alone; an unsafe or nonplain existing level, a non-UTF-8 or unsafe
// component, or any I/O failure is an error. It writes nothing outside the
// base's tree and never repairs anything.
func EnsureSessionStateDirectory(stateBase, sessionID string) error {
	if !platform.ValidStateComponent(sessionID) {
		return fmt.Errorf("the session %q is not a safe state component", sessionID)
	}
	anchor, missing, err := existingStateAncestor(stateBase)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for i := len(missing) - 1; i >= 0; i-- {
		next, err := ensureCheckedDirectory(root, missing[i])
		if err != nil {
			return err
		}
		_ = root.Close()
		root = next
	}
	sessionRoot, err := ensureCheckedDirectory(root, sessionID)
	if err != nil {
		return err
	}
	return sessionRoot.Close()
}

// existingStateAncestor returns the deepest existing ancestor of base
// (base itself when it exists) and the base components still to create,
// in top-down order. Only true absence walks up; any other Lstat failure
// is an error, and reaching the filesystem root without an existing
// ancestor is a refusal.
func existingStateAncestor(base string) (string, []string, error) {
	base = filepath.Clean(base)
	anchor := base
	var missing []string
	for {
		if _, err := os.Lstat(anchor); err == nil {
			return anchor, missing, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		missing = append(missing, filepath.Base(anchor))
		parent := filepath.Dir(anchor)
		if parent == anchor {
			return "", nil, fmt.Errorf("the state base %s has no existing ancestor", base)
		}
		anchor = parent
	}
}

// ensureCheckedDirectory creates comp under the retained parent when it
// is absent, then acquires it through the shared checked child-directory
// helper: a nonplain or replaced component is a refusal, an existing
// plain directory is verified and retained, and the Mkdir is
// handle-relative, never a path-based mkdirall.
func ensureCheckedDirectory(parent *os.Root, comp string) (*os.Root, error) {
	if !platform.ValidStateComponent(comp) {
		return nil, fmt.Errorf("the directory component %q is not a safe state component", comp)
	}
	if err := parent.Mkdir(comp, 0o777); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return openInspectDirectory(parent, comp, nil)
}
