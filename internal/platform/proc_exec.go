package platform

import (
	"errors"
	"math"
	"sync"
)

// procExecMaxBytes bounds the argument area ProcExec reads for one pid.
const procExecMaxBytes = 4 << 20

// ProcExecPathMax caps the executable path ProcExec returns: 32768 UTF-16
// units (the Windows limit) at 3 UTF-8 bytes each; unix kernels hand out
// at most 4096 bytes. A longer answer is an error.
const ProcExecPathMax = 3 * 32768

// errProcExecUnsupported is ProcExec on a platform without a single-pid
// executable and argument read.
var errProcExecUnsupported = errors.New("platform: reading a process's executable and arguments is not supported here")

var (
	procExecutableMu     sync.Mutex
	procExecutableRunner = procExecutable
)

// procExecReadArguments is the native argument reader, kept separate so tests
// can make it unavailable without replacing the executable lookup.
var procExecReadArguments = procExecArguments

// OverrideProcExecutableRunner replaces only ProcExecutable's native reader.
// The returned closure restores the previous reader. Overrides must not overlap
// concurrent lookups or other overrides; this seam is intended for serial tests.
func OverrideProcExecutableRunner(run func(int) (string, error)) func() {
	procExecutableMu.Lock()
	old := procExecutableRunner
	procExecutableRunner = run
	procExecutableMu.Unlock()
	return func() {
		procExecutableMu.Lock()
		procExecutableRunner = old
		procExecutableMu.Unlock()
	}
}

// ProcExecutable reads only one PID's resolved kernel executable path, without
// reading arguments, environment, comm names or process listings. Failure returns
// no matchable path. This read is not atomic with process start/liveness reads.
func ProcExecutable(pid int) (string, error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return "", errors.New("platform: invalid pid")
	}
	procExecutableMu.Lock()
	run := procExecutableRunner
	procExecutableMu.Unlock()
	exe, err := run(pid)
	if err != nil {
		return "", err
	}
	if len(exe) == 0 || len(exe) > ProcExecPathMax {
		return "", errors.New("platform: executable path empty or over its cap")
	}
	return exe, nil
}

// ProcExec reads one process's executable path and argument vector — one
// pid only, never a listing. The result is meant for an exact comparison
// by the caller and must not be logged: arguments can carry anything. An
// error (no such read on this platform, a permission or size limit, a
// malformed answer) is never a match. The read is not atomic with a
// liveness or start identity read: callers bracket it with two
// ProcStartIdentity reads.
func ProcExec(pid int) (exe string, argv []string, err error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return "", nil, errors.New("platform: invalid pid")
	}
	exe, argv, err = procExec(pid)
	if err == nil && len(exe) > ProcExecPathMax {
		return "", nil, errors.New("platform: executable path over its cap")
	}
	return exe, argv, err
}
