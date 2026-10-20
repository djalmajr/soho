//go:build !windows

package platform

import (
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// DiskDevice identifies the filesystem containing an existing path. Tests
// may replace it to exercise more than one volume without mounting disks.
var DiskDevice = func(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return strconv.FormatUint(uint64(st.Dev), 10), true
}

// HardlinkIdentity returns a device/inode key only for multiply linked
// files. Logical size measurements use it to count each inode once.
func HardlinkIdentity(info fs.FileInfo) ([2]uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink <= 1 {
		return [2]uint64{}, false
	}
	return [2]uint64{uint64(st.Dev), uint64(st.Ino)}, true
}
