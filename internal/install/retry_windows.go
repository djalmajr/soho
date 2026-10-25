package install

import (
	"errors"
	"syscall"
)

func isTransientRenameError(err error) bool {
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}
