//go:build !unix

package core_test

import "errors"

// roMkfifo refuses to plant a FIFO on systems without FIFOs (Windows),
// where the nonregular refusal is covered by the directory case instead.
func roMkfifo(path string) error {
	return errors.New("no fifos on this system")
}
