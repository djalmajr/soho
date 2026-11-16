package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"testing"
)

func TestWriteRefused(t *testing.T) {
	for name, err := range map[string]error{
		"EACCES":        &fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES},
		"EPERM":         &fs.PathError{Op: "open", Path: "/x", Err: syscall.EPERM},
		"EROFS":         &fs.PathError{Op: "open", Path: "/x", Err: syscall.EROFS},
		"wrapped EROFS": fmt.Errorf("copies: lock: %w", &fs.PathError{Op: "open", Path: "/x", Err: syscall.EROFS}),
		"ErrPermission": os.ErrPermission,
	} {
		if !WriteRefused(err) {
			t.Fatalf("%s: want refused", name)
		}
	}
	for name, err := range map[string]error{
		"nil":    nil,
		"ENOENT": &fs.PathError{Op: "open", Path: "/x", Err: syscall.ENOENT},
		"ENOSPC": &fs.PathError{Op: "write", Path: "/x", Err: syscall.ENOSPC},
		"other":  errors.New("held lock"),
	} {
		if WriteRefused(err) {
			t.Fatalf("%s: want not refused", name)
		}
	}
}
