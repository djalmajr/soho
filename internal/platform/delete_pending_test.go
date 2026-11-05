package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
)

// TestDeletePending pins the shared lock predicate: only a permission
// error on Windows is the delete-pending collision; an existing entry,
// any other error, and a permission error on another platform are not.
func TestDeletePending(t *testing.T) {
	permission := &fs.PathError{Op: "mkdir", Path: "lock", Err: os.ErrPermission}
	wrapped := fmt.Errorf("mkdir: %w", os.ErrPermission)
	exist := &fs.PathError{Op: "mkdir", Path: "lock", Err: os.ErrExist}
	other := &fs.PathError{Op: "mkdir", Path: "lock", Err: errors.New("disk failure")}
	cases := []struct {
		name string
		err  error
		goos string
		want bool
	}{
		{"windows permission is delete-pending", permission, "windows", true},
		{"windows wrapped permission is delete-pending", wrapped, "windows", true},
		{"linux permission is a failure", permission, "linux", false},
		{"darwin permission is a failure", permission, "darwin", false},
		{"windows existing entry is contention, not delete-pending", exist, "windows", false},
		{"windows other failure is not delete-pending", other, "windows", false},
		{"no error is not delete-pending on windows", nil, "windows", false},
		{"no error is not delete-pending on linux", nil, "linux", false},
	}
	for _, c := range cases {
		if got := DeletePending(c.err, c.goos); got != c.want {
			t.Errorf("%s: DeletePending=%v, want %v", c.name, got, c.want)
		}
	}
}
