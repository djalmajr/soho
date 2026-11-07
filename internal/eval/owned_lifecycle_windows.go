//go:build windows

package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

type winOwnedJobOptions struct {
	application, commandLine, environment, workingDir *uint16
	startup                                           *winStartupInfoEx
	restoreInherit                                    bool
	finishUnassignedJob                               bool
	// Only the probe runner owns capture files. A nil slice leaves the
	// caller's log open and disables on-disk overflow monitoring.
	captureFiles []*os.File
	captureDir   string
}

type winOwnedJobResult struct {
	started, resumeFailed, canceled, overflow bool
	exitCode                                  uint32
	createErr, refusalErr, resumeErr          error
	waitErr, codeErr, drainErr                error
}

// runOwnedJob takes over with ForkLock held and releases it immediately
// after suspended creation and optional inheritance restoration. It owns
// containment, resume, wait, exit capture, root/owned-file handle release
// and job termination/drain. Wrappers retain output setup/readback and
// public error composition. Their native-call backing storage must stay
// alive through this call.
func runOwnedJob(ctx context.Context, opts winOwnedJobOptions) (res winOwnedJobResult) {
	var processInfo syscall.ProcessInformation
	res.createErr = syscall.CreateProcess(opts.application, opts.commandLine, nil, nil, true,
		winCreateSuspended|winCreateUnicodeEnvironment|winExtendedStartupInfoPresent, opts.environment, opts.workingDir,
		&opts.startup.StartupInfo, &processInfo)
	var clearErr error
	if opts.restoreInherit {
		clearErr = errors.Join(
			syscall.SetHandleInformation(opts.startup.StartupInfo.StdOutput, syscall.HANDLE_FLAG_INHERIT, 0),
			syscall.SetHandleInformation(opts.startup.StartupInfo.StdInput, syscall.HANDLE_FLAG_INHERIT, 0),
		)
	}
	syscall.ForkLock.Unlock()
	if res.createErr != nil {
		return res
	}
	processHandle := syscall.Handle(processInfo.Process)
	threadHandle := syscall.Handle(processInfo.Thread)
	if clearErr != nil {
		// A borrowed log must become non-inheritable before execution.
		termErr := terminateOwnedSuspendedRoot(processHandle)
		closeRunEnds(processHandle, threadHandle, opts.captureFiles...)
		res.refusalErr = errors.Join(fmt.Errorf("clear output file inheritance: %v", clearErr), termErr)
		return res
	}
	job, jobErr := winCreateOwnedJob()
	if jobErr != nil {
		termErr := terminateOwnedSuspendedRoot(processHandle)
		closeRunEnds(processHandle, threadHandle, opts.captureFiles...)
		res.refusalErr = fmt.Errorf("refusing to run without containment: %v", jobErr)
		if termErr != nil {
			res.refusalErr = fmt.Errorf("refusing to run without containment: %v (root teardown: %v)", jobErr, termErr)
		}
		return res
	}
	if err := winAssignOwnedJobProcess(job, processHandle); err != nil {
		var drainErr error
		if opts.finishUnassignedJob {
			drainErr = finishOwnedJob(job)
		} else {
			_ = syscall.CloseHandle(job)
		}
		termErr := terminateOwnedSuspendedRoot(processHandle)
		closeRunEnds(processHandle, threadHandle, opts.captureFiles...)
		switch {
		case termErr != nil:
			res.refusalErr = fmt.Errorf("refusing to run without containment: %v (root teardown: %v)", err, termErr)
		case drainErr != nil:
			res.refusalErr = errors.Join(fmt.Errorf("refusing to run without containment: %v", err), fmt.Errorf("finish owned job: %v", drainErr))
		default:
			res.refusalErr = fmt.Errorf("refusing to run without containment: %v", err)
		}
		return res
	}
	// Containment refusals above never count as Started. Assignment must
	// precede resume so even a child spawned immediately stays in the job.
	res.started = true
	resumed, _, resumeErr := winResumeThread.Call(uintptr(processInfo.Thread))
	res.resumeErr = resumeErr
	res.resumeFailed = winResumeThreadFailed(resumed)
	if res.resumeFailed {
		_ = syscall.TerminateProcess(processHandle, 1)
	}
	for {
		select {
		case <-ctx.Done():
			res.canceled = true
		default:
		}
		if res.canceled {
			break
		}
		event, pollErr := syscall.WaitForSingleObject(processHandle, 50)
		if pollErr != nil {
			res.waitErr = pollErr
			break
		}
		if event != syscall.WAIT_TIMEOUT {
			break
		}
		// Live monitoring belongs inside the wait: Background callers
		// must also terminate promptly on excessive capture growth.
		if len(opts.captureFiles) != 0 && winOutputOverflow(opts.captureDir) {
			res.overflow = true
			break
		}
	}
	// Preserve exit information before closing root handles. Release the
	// owned capture ends before normal drain, but never the borrowed log.
	res.codeErr = syscall.GetExitCodeProcess(processHandle, &res.exitCode)
	closeRunEnds(processHandle, threadHandle, opts.captureFiles...)
	res.drainErr = finishOwnedJob(job)
	return res
}
