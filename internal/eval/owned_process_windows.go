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
	// (marking the output handle inheritable through CreateProcess) so a
	// concurrent CreateProcess in another goroutine cannot interleave and
	// inherit the handle.
	syscall.ForkLock.Lock()
	logHandle := syscall.Handle(out.Fd())
	if err := syscall.SetHandleInformation(logHandle, syscall.HANDLE_FLAG_INHERIT, 1); err != nil {
		syscall.ForkLock.Unlock()
		return OwnedProcessResult{}, fmt.Errorf("mark output file inheritable: %v", err)
	}
	// The inherit flag is a window, not a state: it goes back to
	// non-inheritable before ForkLock is released, on every path, so no other
	// CreateProcess of this runner can pick the handle up. The child got its
	// own copy at CreateProcess, so clearing it never disturbs the child. The
	// platform's .cmd launcher serializes on its own mutex, not on ForkLock,
	// but validate runs its layers one at a time and never that launcher
	// beside them.
	clearInherit := func() error {
		return syscall.SetHandleInformation(logHandle, syscall.HANDLE_FLAG_INHERIT, 0)
	}
	// The child may inherit exactly the output handle and nothing else:
	// an explicit PROC_THREAD_ATTRIBUTE_HANDLE_LIST on STARTUPINFOEX, no
	// inheritable thread attributes (nil), inherit handles on. The handle
	// is listed once and used for BOTH stdio (the merged capture): the
	// child's stdout and stderr share the file's write stream, so the
	// bytes land in the order they are written. The slice is named (not a
	// temporary) and kept alive by name through the native call, the way
	// proc_windows keeps its inherited slice.
	inherited := []uintptr{uintptr(logHandle)}
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
	startup.StartupInfo.StdOutput = logHandle
	startup.StartupInfo.StdErr = logHandle
	startup.attributeList = attrList.list()

	var processInfo syscall.ProcessInformation
	createErr := syscall.CreateProcess(application, commandLine, nil, nil, true,
		winCreateSuspended|winCreateUnicodeEnvironment|winExtendedStartupInfoPresent, environment, workingDir,
		&startup.StartupInfo, &processInfo)
	clearErr := clearInherit()
	syscall.ForkLock.Unlock()
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
	if createErr != nil {
		return OwnedProcessResult{}, fmt.Errorf("create suspended process: %v", winCallError(createErr))
	}
	processHandle := syscall.Handle(processInfo.Process)
	threadHandle := syscall.Handle(processInfo.Thread)

	// Containment is a precondition: if the kill-on-close job cannot be
	// created, configured, or the still-suspended root cannot be assigned
	// to it, fail closed BEFORE resuming — terminate the suspended root
	// (bounded drain), finish the job (it holds no assigned process on the
	// refusal paths) and refuse. Both steps go through the typed seams
	// (the same seams the native regressions substitute fakes into).
	if clearErr != nil {
		// The handle stayed inheritable: refuse before the child runs.
		termErr := terminateOwnedSuspendedRoot(processHandle)
		closeRunEnds(processHandle, threadHandle)
		return OwnedProcessResult{}, errors.Join(fmt.Errorf("clear output file inheritance: %v", clearErr), termErr)
	}
	job, jobErr := winCreateOwnedJob()
	if jobErr != nil {
		termErr := terminateOwnedSuspendedRoot(processHandle)
		closeRunEnds(processHandle, threadHandle)
		if termErr != nil {
			return OwnedProcessResult{}, fmt.Errorf("refusing to run without containment: %v (root teardown: %v)", jobErr, termErr)
		}
		return OwnedProcessResult{}, fmt.Errorf("refusing to run without containment: %v", jobErr)
	}
	if err := winAssignOwnedJobProcess(job, processHandle); err != nil {
		drainErr := finishOwnedJob(job)
		termErr := terminateOwnedSuspendedRoot(processHandle)
		closeRunEnds(processHandle, threadHandle)
		switch {
		case termErr != nil:
			return OwnedProcessResult{}, fmt.Errorf("refusing to run without containment: %v (root teardown: %v)", err, termErr)
		case drainErr != nil:
			return OwnedProcessResult{}, errors.Join(fmt.Errorf("refusing to run without containment: %v", err), fmt.Errorf("finish owned job: %v", drainErr))
		default:
			return OwnedProcessResult{}, fmt.Errorf("refusing to run without containment: %v", err)
		}
	}

	resumed, _, resumeErr := winResumeThread.Call(uintptr(processInfo.Thread))
	resumeFailed := winResumeThreadFailed(resumed)
	if resumeFailed {
		// The root never ran: terminate the owned process so the exit
		// information read below is the truthful termination code; the
		// root handles are released exactly once before the drain proof.
		_ = syscall.TerminateProcess(processHandle, 1)
	}

	// Wait for the process, polling the context so cancellation of ctx
	// (a timeout of the caller) terminates the whole job promptly. A poll
	// failure is not treated as an exit and is not swallowed: it breaks
	// the poll and the kill path below (terminate + drain) keeps the
	// result truthful. There is no on-disk overflow allowance here: the
	// output IS the caller's file and the disk is its bound, so the loop
	// only watches the process and the context.
	canceled := false
	var waitErr error
	for {
		select {
		case <-ctx.Done():
			canceled = true
		default:
		}
		if canceled {
			break
		}
		event, pollErr := syscall.WaitForSingleObject(processHandle, 50)
		if pollErr != nil {
			waitErr = pollErr
			break
		}
		if event != syscall.WAIT_TIMEOUT {
			break
		}
	}

	// Read the exit code BEFORE releasing the process handle; then release
	// the root process/thread handles exactly once. The output file stays
	// open in the runner: the caller owns it and appends the trailing
	// lines after this return.
	var exitCode uint32
	codeErr := syscall.GetExitCodeProcess(processHandle, &exitCode)
	_ = syscall.CloseHandle(processHandle)
	_ = syscall.CloseHandle(threadHandle)

	// Stop the whole owned job and drain it boundedly on every path
	// (cancel, resume failure and normal); the drain is proven by the
	// verified primary contract (ActiveProcesses == 0).
	drainErr := finishOwnedJob(job)

	// One dominant error, with the non-dominant ones joined, not masked:
	// resume failure dominates, then a failed drain, then a canceled run,
	// then a poll failure.
	var mainErr error
	switch {
	case resumeFailed:
		mainErr = fmt.Errorf("resume suspended process: %v", winCallError(resumeErr))
		if drainErr != nil {
			mainErr = errors.Join(mainErr, fmt.Errorf("drain owned job: %v", drainErr))
		}
	case drainErr != nil:
		mainErr = fmt.Errorf("drain owned job: %v", drainErr)
	case canceled:
		mainErr = fmt.Errorf("the run was canceled and the whole owned job was terminated and drained")
	case waitErr != nil:
		mainErr = fmt.Errorf("wait for the owned process: %v", winCallError(waitErr))
	}
	res := OwnedProcessResult{Started: true}
	if !canceled && !resumeFailed && waitErr == nil && codeErr == nil {
		res.ExitCode = int(exitCode)
	}
	if mainErr != nil {
		return res, mainErr
	}
	if codeErr != nil {
		return res, fmt.Errorf("read the owned process exit code: %v", winCallError(codeErr))
	}
	return res, nil
}
