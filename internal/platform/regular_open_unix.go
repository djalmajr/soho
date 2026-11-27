//go:build unix

package platform

import "syscall"

// ReadOnlyNonBlocking is the open mode of a read that must never block on
// a FIFO: read-only and non-blocking. On a regular file the flag changes
// nothing.
const ReadOnlyNonBlocking = syscall.O_RDONLY | syscall.O_NONBLOCK

const regularNoFollowFlag = syscall.O_NOFOLLOW

// ReadWriteNonBlocking is the open mode of an update that must never block
// on a FIFO: read-write and non-blocking.
const ReadWriteNonBlocking = syscall.O_RDWR | syscall.O_NONBLOCK
