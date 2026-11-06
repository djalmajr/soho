//go:build linux

package platform

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// procExec reads /proc/<pid>/exe (the resolved executable) and
// /proc/<pid>/cmdline (NUL-separated arguments, read bounded).
func procExec(pid int) (string, []string, error) {
	exe, err := procExecutableRaw(pid)
	if err != nil {
		return "", nil, err
	}
	argv, err := procExecReadArguments(uintptr(pid))
	if err != nil {
		return "", nil, err
	}
	return exe, argv, nil
}

func procExecutableRaw(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
}

func procExecutable(pid int) (string, error) {
	exe, err := procExecutableRaw(pid)
	if err != nil || !strings.HasSuffix(exe, " (deleted)") {
		return exe, err
	}
	procPath := "/proc/" + strconv.Itoa(pid) + "/exe"
	info, err := os.Stat(procPath)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("platform: executable link identity unavailable")
	}
	result := exe
	if stat.Nlink == 0 {
		// No filesystem link remains: the last suffix is kernel decoration,
		// even when the original filename itself ended in the same text.
		result = strings.TrimSuffix(exe, " (deleted)")
	} else {
		// Preserve a literal only when that exact pathname still identifies
		// the image. Other hard-link cases are ambiguous: text alone must
		// not invent an executable basename.
		linked, err := os.Stat(exe)
		if err != nil {
			return "", err
		}
		if !os.SameFile(info, linked) {
			return "", errors.New("platform: ambiguous executable suffix")
		}
	}
	// Detect observable changes across metadata reads, without claiming an
	// atomic process/start-identity check or elimination of reuse races.
	again, err := procExecutableRaw(pid)
	if err != nil {
		return "", err
	}
	after, err := os.Stat(procPath)
	if err != nil {
		return "", err
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || again != exe || !os.SameFile(info, after) || afterStat.Nlink != stat.Nlink {
		return "", errors.New("platform: executable changed during read")
	}
	return result, nil
}

func procExecArguments(id uintptr) ([]string, error) {
	dir := "/proc/" + strconv.Itoa(int(id))
	f, err := os.Open(dir + "/cmdline")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, procExecMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > procExecMaxBytes || data[len(data)-1] != 0 {
		return nil, errors.New("platform: malformed process arguments")
	}
	return strings.Split(string(data[:len(data)-1]), "\x00"), nil
}
