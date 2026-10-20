package platform

import (
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// DiskDevice identifies the filesystem by its native volume serial number.
// Opening the directory also supports mounted volumes without drive letters.
var DiskDevice = func(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return "", false
	}
	return strconv.FormatUint(uint64(info.VolumeSerialNumber), 10), true
}

// HardlinkIdentity leaves Windows logical size measurements unchanged.
func HardlinkIdentity(_ fs.FileInfo) ([2]uint64, bool) {
	return [2]uint64{}, false
}
