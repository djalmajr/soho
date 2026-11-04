package eval

import (
	"context"
	"io"
	"os"
	"time"
)

// OwnedProcessOptions configures RunOwnedProcess.
type OwnedProcessOptions struct {
	// Dir is the child's working directory.
	Dir string
	// Env is the child's full environment (KEY=VALUE entries); an empty
	// value inherits the runner's environment.
	Env []string
	// Output receives the child's stdout and stderr merged in the order
	// the child writes them. The runner never buffers the output: the
	// child inherits the destination's descriptor (file descriptor on
	// Unix, file handle on Windows), so the bytes go straight there. When
	// the runner appends to the same destination after the run (the
	// caller's trailing lines), create Output with CreateOwnedOutput, so
	// the runner's lines land after the child's on every platform.
	Output io.Writer
	// WaitDelay bounds how long the run waits for the tree to release
	// inherited handles after the run ends; zero leaves the platform
	// default. It is unnecessary when Output is a file (no pipe is
	// involved) and is only the belt for a pipe-based caller.
	WaitDelay time.Duration
}

// OwnedProcessResult reports how the owned process ended.
type OwnedProcessResult struct {
	// Started is false when the process could not be created.
	Started bool
	// ExitCode is the child's exit code (meaningful when Started and
	// !Signaled; 0 for a green run).
	ExitCode int
	// Signaled reports that the process ended by a signal (Unix); on
	// Windows there are no signals and the flag stays false.
	Signaled bool
	// Signal is the signal number (Unix, when Signaled).
	Signal int
	// CleanupErr is set when the run could not prove that the process tree
	// ended. The exit information above is still the child's, but the caller
	// must not report success.
	CleanupErr error
}

// CreateOwnedOutput creates the file a RunOwnedProcess child writes into:
// read and write, refusing a path that already exists (never following a
// link in its place), with the append behavior the platform allows
// (ownedOutputAppendFlag). The runner's own writes before and after the run
// land in order around the child's.
func CreateOwnedOutput(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL|ownedOutputAppendFlag, perm)
}

// RunOwnedProcess runs executable with args as an owned process tree and
// ends that tree before the call returns, so no member outlives the run.
//
// On Unix the tree is a new process group: cancellation of ctx SIGKILLs
// the whole group, and a green run ends the group again (plus the bounded
// group-gone proof), because the reaped leader can leave detached
// grandchildren. A member that calls setsid leaves the group by design
// (the escape this package declares for the go toolchain); the final group
// signal after the group's last member exited can hit a reused group id —
// POSIX exposes no generation token — so that cleanup is best-effort, the
// same as runOwnedGoProcess.
//
// On Windows the tree is a kill-on-close job object: the child is created
// suspended and assigned to the job before resume (a containment refusal
// terminates the suspended root and returns — there is no silent
// uncontained fallback), and the job is terminated and drain-proven
// (ActiveProcesses == 0) before the return. The child's stdout and stderr
// are the Output file handle itself, so the bytes land where the file is —
// the runner-owned directory the caller chose — and never in the working
// directory.
//
// The returned error is the run's own error (a start refusal, a
// containment or drain refusal, or the group-gone proof failure); the
// result carries the exit information when the process ran. A canceled
// ctx is the caller's to interpret (its deadline text, not this
// package's).
func RunOwnedProcess(ctx context.Context, executable string, args []string, opts OwnedProcessOptions) (OwnedProcessResult, error) {
	return runOwnedProcess(ctx, executable, args, opts)
}
