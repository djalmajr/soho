//go:build !((darwin && !ios) || (linux && !android))

package platform

import "errors"

const workerEnvExecSupported = false

// ErrWorkerEnvExecUnsupported marks a non-desktop-Unix host without an
// in-process exec, so the Core transport refuses environment removal instead
// of depending on an external utility. This stub also covers Windows, iOS and
// Android.
var ErrWorkerEnvExecUnsupported = errors.New("worker env exec: unsupported on this platform")

// WorkerEnvExec is unavailable outside macOS and Linux.
func WorkerEnvExec([]string) error { return ErrWorkerEnvExecUnsupported }
