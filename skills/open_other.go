//go:build !unix

package skill

import "os"

// readOnlyNonBlocking is plain read-only on systems without filesystem
// FIFOs (Windows), where an open of a path cannot block on one, matching
// platform.ReadOnlyNonBlocking.
const readOnlyNonBlocking = os.O_RDONLY

// Windows has no filesystem FIFO. OpenRoot retains the child handle; the
// shared acquisition checks its identity against both parent-entry reads.
func openBundleDirectory(parent *os.Root, name string) (*os.Root, error) {
	return parent.OpenRoot(name)
}
