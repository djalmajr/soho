package platform

import (
	"errors"
	"os"
)

// DeletePending reports whether a failed lock creation is the Windows
// delete-pending collision: a lock entry another writer has just removed
// stays delete-pending while any handle on it is open, and creating the
// name again fails with ERROR_ACCESS_DENIED (os.ErrPermission) instead of
// "exists". That is contention, retried within the lock's own bound, not
// a failure. On every other platform a permission error is a genuine
// failure. goos is a parameter so each caller keeps the rule testable on
// every host.
func DeletePending(err error, goos string) bool {
	return goos == "windows" && errors.Is(err, os.ErrPermission)
}
