//go:build !unix

package core

import "os"

// openReadOnlyDirectory retains the child through os.Root.OpenRoot on
// systems without filesystem FIFOs (Windows), where an open of a path
// cannot block on one; the caller's pre and post identity checks apply
// unchanged.
func openReadOnlyDirectory(parent *os.Root, name string) (*os.Root, error) {
	return parent.OpenRoot(name)
}
