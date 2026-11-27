package platform

import (
	"errors"
	"io"
	"os"
	"strings"
)

// ErrNotRegular is the refusal of OpenRegularFile for a path that is not a
// regular file (a FIFO, a socket, a device or a directory).
var ErrNotRegular = errors.New("not a regular file")

// OpenRegularFile opens file for reading only when the opened handle is a
// regular file: the open itself is non-blocking where the system has
// FIFOs (ReadOnlyNonBlocking), so a FIFO planted at the path can never
// hold the caller, and the type is checked on the opened handle, so a
// replacement after a path check cannot slip a different kind of file in.
// Any other kind of file is closed and refused with ErrNotRegular.
func OpenRegularFile(file string) (*os.File, error) {
	return openRegularFile(file, ReadOnlyNonBlocking)
}

// OpenRegularFileNoFollow refuses a symlink at the last path component.
// Platforms without an atomic no-follow open return errors.ErrUnsupported.
func OpenRegularFileNoFollow(file string) (*os.File, error) {
	if regularNoFollowFlag == 0 {
		return nil, errors.ErrUnsupported
	}
	return openRegularFile(file, ReadOnlyNonBlocking|regularNoFollowFlag)
}

func openRegularFile(file string, flags int) (*os.File, error) {
	f, err := os.OpenFile(file, flags, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrNotRegular
	}
	return f, nil
}

// ReadRegularFile is os.ReadFile restricted to a regular file, opened
// through OpenRegularFile.
func ReadRegularFile(file string) ([]byte, error) {
	f, err := OpenRegularFile(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// ReadRegularTextFile is ReadTextFile restricted to a regular file, opened
// through OpenRegularFile: the same decoding and line endings.
func ReadRegularTextFile(file string) (string, error) {
	data, err := ReadRegularFile(file)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(decodeWHATWGUTF8(data), "\r\n", "\n"), nil
}
