//go:build windows

package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// ownedOutputAppendFlag is empty on Windows. Go maps os.O_APPEND to a handle
// holding FILE_APPEND_DATA without FILE_WRITE_DATA, and the MSYS and Cygwin
// runtimes (the sh of Git for Windows) read FILE_WRITE_DATA to decide whether
// an inherited standard handle is writable: on an append-only handle every
// write of the child fails, so a shell exits 1 and leaves nothing in the file.
// Without the flag the child inherits the same file object as the runner, so
// both share one file position and the bytes still land in order.
const ownedOutputAppendFlag = 0

// runOwnedProcess runs the owned job object (the Windows half of
// RunOwnedProcess). The semantics mirror runOwnedGoProcess: the child is
// created suspended, assigned to the kill-on-close job before resume
// (containment is fail-closed — a refusal terminates the suspended root
// and returns), cancellation of ctx terminates the whole job, and the
// bounded drain proves ActiveProcesses == 0 before the return. The
// difference from the go-test capture: the child's stdout and stderr are
// the RUNNER'S OUTPUT FILE HANDLE ITSELF (the caller's evidence log, in
// the runner-owned directory the caller chose), so the bytes land where
// the log is — never in the working directory — and are never buffered or
// read back in the runner.
func runOwnedProcess(ctx context.Context, executable string, args []string, opts OwnedProcessOptions) (OwnedProcessResult, error) {
	out, ok := opts.Output.(*os.File)
	if !ok {
		return OwnedProcessResult{}, fmt.Errorf("runOwnedProcess: on Windows, Output must be an *os.File (the child writes the capture into that file, in the runner-owned directory)")
	}
	if len(opts.Env) == 0 {
		// An empty environment inherits the runner's, the way the Unix
		// half does (the Windows create call takes an explicit block).
		opts.Env = os.Environ()
	}
	// The child reads the null device, as on Unix: a command that reads its
	// input sees the end of it, where a missing handle fails in an MSYS tool
	// ("Bad file descriptor"). Nothing reads from the runner's own input.
	input, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return OwnedProcessResult{}, fmt.Errorf("open the null device: %v", err)
	}
	defer input.Close()
	commandLine, err := winCommandLineUTF16(executable, args)
	if err != nil {
		return OwnedProcessResult{}, fmt.Errorf("build command line: %v", err)
	}
	application, err := syscall.UTF16PtrFromString(executable)
	if err != nil {
		return OwnedProcessResult{}, fmt.Errorf("encode executable: %v", err)
	}
	workingDir, err := syscall.UTF16PtrFromString(opts.Dir)
	if err != nil {
		return OwnedProcessResult{}, fmt.Errorf("encode working directory: %v", err)
	}
	environment, err := windowsEnvBlock(opts.Env)
	if err != nil {
		return OwnedProcessResult{}, fmt.Errorf("encode environment block: %v", err)
	}
	// Hold the stdlib ForkLock across the inheritable-handle window
	// (marking the output and input handles inheritable through
	// CreateProcess) so a concurrent CreateProcess in another goroutine
	// cannot interleave and inherit them.
	syscall.ForkLock.Lock()
	logHandle := syscall.Handle(out.Fd())
	inputHandle := syscall.Handle(input.Fd())
	// The inherit flag is a window, not a state: it goes back to
	// non-inheritable before ForkLock is released, on every path, so no other
	// CreateProcess of this runner can pick the handles up. The child got its
	// own copies at CreateProcess, so clearing them never disturbs the child.
	// The platform's .cmd launcher serializes on its own mutex, not on
	// ForkLock, but validate runs its layers one at a time and never that
	// launcher beside them.
	clearInherit := func() error {
		return errors.Join(
			syscall.SetHandleInformation(logHandle, syscall.HANDLE_FLAG_INHERIT, 0),
			syscall.SetHandleInformation(inputHandle, syscall.HANDLE_FLAG_INHERIT, 0),
		)
	}
	if err := syscall.SetHandleInformation(logHandle, syscall.HANDLE_FLAG_INHERIT, 1); err != nil {
		syscall.ForkLock.Unlock()
		return OwnedProcessResult{}, fmt.Errorf("mark output file inheritable: %v", err)
	}
	if err := syscall.SetHandleInformation(inputHandle, syscall.HANDLE_FLAG_INHERIT, 1); err != nil {
		_ = clearInherit()
		syscall.ForkLock.Unlock()
		return OwnedProcessResult{}, fmt.Errorf("mark null input inheritable: %v", err)
	}
	// The child may inherit exactly the output and input handles and nothing
	// else: an explicit PROC_THREAD_ATTRIBUTE_HANDLE_LIST on STARTUPINFOEX, no
	// inheritable thread attributes (nil), inherit handles on. The output
	// handle is listed once and used for BOTH stdout and stderr (the merged
	// capture): they share the file's write stream, so the bytes land in the
	// order they are written. The slice is named (not a temporary) and kept
	// alive by name through the native call, the way proc_windows keeps its
	// inherited slice.
	inherited := []uintptr{uintptr(logHandle), uintptr(inputHandle)}
	attrList, listErr := newWinProcAttrList(1)
	if listErr != nil {
		_ = clearInherit()
		syscall.ForkLock.Unlock()
		return OwnedProcessResult{}, listErr
	}
	// Registered before setHandleList (DJA-695 Q1): the allocation is
	// released on every later path, including the setHandleList refusal.
	defer attrList.delete()
	if err := attrList.setHandleList(inherited); err != nil {
		_ = clearInherit()
		syscall.ForkLock.Unlock()
		return OwnedProcessResult{}, err
	}

	startup := winStartupInfoEx{}
	// Cb is the size of the ENTIRE extended block, the way the Go stdlib
	// sets it (syscall exec_windows.go newProcess: si.Cb =
	// uint32(unsafe.Sizeof(*si)) with si a _STARTUPINFOEXW).
	startup.StartupInfo.Cb = uint32(unsafe.Sizeof(startup))
	startup.StartupInfo.Flags = winStartfUseStdHandles
	startup.StartupInfo.StdInput = inputHandle
	startup.StartupInfo.StdOutput = logHandle
	startup.StartupInfo.StdErr = logHandle
	startup.attributeList = attrList.list()

	run := runOwnedJob(ctx, winOwnedJobOptions{
		application: application, commandLine: commandLine, environment: environment,
		workingDir: workingDir, startup: &startup,
		restoreInherit: true, finishUnassignedJob: true,
	})
	// Keep the raw-pointer backing buffers, the handle-list backing array
	// (kept by name, through the retained slice, and through attrList),
	// the working-directory backing pointer, the environment backing block
	// and the owned output file alive through the call, the way the stdlib
	// keeps its own.
	runtime.KeepAlive(application)
	runtime.KeepAlive(commandLine)
	runtime.KeepAlive(environment)
	runtime.KeepAlive(attrList)
	runtime.KeepAlive(inherited)
	runtime.KeepAlive(workingDir)
	runtime.KeepAlive(out)
	runtime.KeepAlive(input)
	if run.createErr != nil {
		return OwnedProcessResult{}, fmt.Errorf("create suspended process: %v", winCallError(run.createErr))
	}
	if run.refusalErr != nil {
		return OwnedProcessResult{}, run.refusalErr
	}

	// One dominant error, with the non-dominant ones joined, not masked:
	// resume failure dominates, then a failed drain, then a canceled run,
	// then a poll failure.
	var mainErr error
	switch {
	case run.resumeFailed:
		mainErr = fmt.Errorf("resume suspended process: %v", winCallError(run.resumeErr))
		if run.drainErr != nil {
			mainErr = errors.Join(mainErr, fmt.Errorf("drain owned job: %v", run.drainErr))
		}
	case run.drainErr != nil:
		mainErr = fmt.Errorf("drain owned job: %v", run.drainErr)
	case run.canceled:
		mainErr = fmt.Errorf("the run was canceled and the whole owned job was terminated and drained")
	case run.waitErr != nil:
		mainErr = fmt.Errorf("wait for the owned process: %v", winCallError(run.waitErr))
	}
	res := OwnedProcessResult{Started: run.started}
	if !run.canceled && !run.resumeFailed && run.waitErr == nil && run.codeErr == nil {
		res.ExitCode = int(run.exitCode)
	}
	if mainErr != nil {
		return res, mainErr
	}
	if run.codeErr != nil {
		return res, fmt.Errorf("read the owned process exit code: %v", winCallError(run.codeErr))
	}
	return res, nil
}
