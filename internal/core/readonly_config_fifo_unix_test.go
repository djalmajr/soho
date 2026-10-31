//go:build unix

package core_test

import "syscall"

// roMkfifo creates a FIFO the read-only seam must refuse (a nonregular
// kind planted at the config path); unix systems have FIFOs.
func roMkfifo(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
