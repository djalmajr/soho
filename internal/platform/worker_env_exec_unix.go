//go:build darwin || linux

package platform

import (
	"errors"
	"strings"
	"syscall"
)

const workerEnvExecSupported = true

// WorkerEnvExec filters only the named entries from this process's
// environment and replaces the process with the worker through exec, keeping
// the same PID and TTY. Unrelated variables, including macOS dynamic-loader
// settings, are passed through untouched; no shell, interpreter or external
// utility is involved.
func WorkerEnvExec(args []string) error {
	names, program, workerArgs, err := parseWorkerEnvExecArgs(args)
	if err != nil {
		return err
	}
	drop := make(map[string]bool, len(names))
	for _, name := range names {
		drop[name] = true
	}
	argv := make([]string, 0, len(workerArgs)+1)
	argv = append(argv, program)
	argv = append(argv, workerArgs...)
	if err := syscall.Exec(program, argv, filteredWorkerEnv(drop)); err != nil && errors.Is(err, syscall.ENOENT) {
		return errors.New("worker env exec: program not found")
	} else if err != nil {
		return errors.New("worker env exec: exec failed: " + strings.TrimSpace(err.Error()))
	}
	return nil
}
