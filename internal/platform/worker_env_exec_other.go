//go:build !darwin && !linux

package platform

import "errors"

const workerEnvExecSupported = false

// ErrWorkerEnvExecUnsupported marks a host without an in-process exec, so the
// Core transport refuses environment removal instead of depending on an
// external utility.
var ErrWorkerEnvExecUnsupported = errors.New("worker env exec: unsupported on this platform")

// WorkerEnvExec is unavailable outside macOS and Linux.
func WorkerEnvExec([]string) error { return ErrWorkerEnvExecUnsupported }
