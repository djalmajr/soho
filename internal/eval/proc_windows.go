//go:build windows

package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// Native Windows constants and kernel32 entry points for the job-object tree
// kill. The patterns mirror the verified reference in
// internal/platform/process_windows.go: raw CreateProcess started suspended,
// job object with KILL_ON_JOB_CLOSE, assignment before resume, and full
// handle cleanup. Stdlib only: no task scripts or interpreters, no PID-only
// kill races. The extended-startup and handle-list constants match the Go
// stdlib (syscall types_windows.go: _EXTENDED_STARTUPINFO_PRESENT =
// 0x00080000, _PROC_THREAD_ATTRIBUTE_HANDLE_LIST = 0x00020002).
const (
	winCreateSuspended               = 0x00000004
	winCreateUnicodeEnvironment      = 0x00000400
	winExtendedStartupInfoPresent    = 0x00080000
	winStartfUseStdHandles           = 0x00000100
	winJobObjectKillOnClose          = 0x00002000
	winJobObjectExtendedInfo         = 9
	winJobObjectBasicAccountingInfo  = 1
	winProcThreadAttributeHandleList = 0x00020002
)

// winErrorInsufficientBuffer is ERROR_INSUFFICIENT_BUFFER: the documented
// outcome of the NULL-buffer size query of InitializeProcThreadAttributeList
// (Go stdlib syscall types_windows.go: ERROR_INSUFFICIENT_BUFFER Errno = 122).
const winErrorInsufficientBuffer = 122

// Bounded drains: every wait on the owned tree is bounded; the
// KILL_ON_JOB_CLOSE close of the job handle is the backstop, never an
// unbounded wait. winJobDrainTimeout bounds the whole-job accounting drain;
// winQueryPollInterval is the snapshot cadence; winSuspendedRootDrain bounds
// the teardown of a root that was refused before resume.
const (
	winJobDrainTimeout    = 10 * time.Second
	winQueryPollInterval  = 20 * time.Millisecond
	winSuspendedRootDrain = 5 * time.Second
	// winCaptureRemovalRetryWindow bounds the path-based removal retry of
	// the owned output captures after a drained run: the kernel can release
	// the last inherited capture handle asynchronously after the
	// ActiveProcesses==0 drain proof, so the bounded retry stays limited,
	// with the final failure returned and never an unbounded wait.
	winCaptureRemovalRetryWindow = 2 * time.Second
)

// The owned output capture file names: regular files inside the
// runner-owned scratch (dir), created and removed by the runner on every
// path. Hidden names keep the scratch package dir clean for the go tool.
const (
	winStdoutFileName = ".herdr-eval-stdout"
	winStderrFileName = ".herdr-eval-stderr"
)

var (
	winKernel32                          = syscall.NewLazyDLL("kernel32.dll")
	winCreateJobObjectW                  = winKernel32.NewProc("CreateJobObjectW")
	winSetInformationJobObject           = winKernel32.NewProc("SetInformationJobObject")
	winAssignProcessToJob                = winKernel32.NewProc("AssignProcessToJobObject")
	winTerminateJobObject                = winKernel32.NewProc("TerminateJobObject")
	winQueryInformationJobObject         = winKernel32.NewProc("QueryInformationJobObject")
	winResumeThread                      = winKernel32.NewProc("ResumeThread")
	winInitializeProcThreadAttributeList = winKernel32.NewProc("InitializeProcThreadAttributeList")
	winUpdateProcThreadAttribute         = winKernel32.NewProc("UpdateProcThreadAttribute")
	winDeleteProcThreadAttributeList     = winKernel32.NewProc("DeleteProcThreadAttributeList")
)

// winJobObjectBasicLimit mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION for the
// 64-bit ABI; the layout matches internal/platform/jobobject_windows_other.go.
type winJobObjectBasicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

// winJobObjectExtendedLimit mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION:
// the basic limit block followed by JOBOBJECT_IO_COUNTERS and the four
// memory limit fields.
type winJobObjectExtendedLimit struct {
	Basic              winJobObjectBasicLimit
	ReadOperations     uint64
	WriteOperations    uint64
	OtherOperations    uint64
	ReadTransfer       uint64
	WriteTransfer      uint64
	OtherTransfer      uint64
	ProcessMemoryLimit uintptr
	JobMemoryLimit     uintptr
	PeakProcessMemory  uintptr
	PeakJobMemory      uintptr
}

// winCaptureShareMode is the sharing of the owned output captures: the
// stdlib os.OpenFile sharing (FILE_SHARE_READ|FILE_SHARE_WRITE) widened
// with FILE_SHARE_DELETE. The capture handles are inherited by the go root,
// whose teardown is asynchronous after the ActiveProcesses==0 drain proof;
// with FILE_SHARE_DELETE the path-based removal passes as a pending delete
// even while that dying root still holds the inherited handle, so the
// removal no longer loses the race to the kernel's handle release.
const winCaptureShareMode = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE

// openOwnedCapture opens one owned output capture with the same access and
// creation semantics the stdlib os.OpenFile(name, os.O_CREATE|os.O_WRONLY|
// os.O_TRUNC, 0o644) call used to resolve to — GENERIC_WRITE access,
// create-and-truncate, FILE_ATTRIBUTE_NORMAL, no template file — with only
// the sharing widened to winCaptureShareMode. The stdlib itself uses
// OPEN_ALWAYS plus a post-open truncate and avoids CREATE_ALWAYS only for
// the read-only-perm edge case (go.dev/issue/38225), which does not apply
// to a writable 0o644 capture: CREATE_ALWAYS here is the atomic
// create-and-truncate for these flags. The handle is created the way the
// stdlib open creates it (os.OpenFile passes O_CLOEXEC on Windows, a nil
// security attributes); the caller marks it inheritable only across the
// locked CreateProcess window. The path given to CreateFile goes through
// extendedCapturePath, as os.OpenFile's fixLongPath would; the returned
// file keeps the plain path as its name.
func openOwnedCapture(dir, name string) (*os.File, error) {
	path := filepath.Join(dir, name)
	path16, err := syscall.UTF16PtrFromString(extendedCapturePath(path))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(path16, syscall.GENERIC_WRITE, winCaptureShareMode,
		nil, syscall.CREATE_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// winShortPathLimit is the length below which the stdlib leaves a path
// alone (MAX_PATH minus 12, the room for an 8.3 name).
const winShortPathLimit = 248

// extendedCapturePath keeps a capture path under MAX_PATH working when the
// process has no long-path support, mirroring the stdlib's
// addExtendedPrefix: a path whose absolute form reaches winShortPathLimit
// gets the \\?\ prefix (\\?\UNC\ for a UNC share) on its absolute, cleaned
// form. Short paths, already extended paths and device paths are returned
// unchanged, and so is a path whose absolute form cannot be resolved.
func extendedCapturePath(path string) string {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil || len(abs) < winShortPathLimit {
		return path
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}

// runOwnedGoProcess runs the go test command under a native job object that
// kills the entire tree when the job handle closes: the go process is
// created suspended, assigned to the KILL_ON_JOB_CLOSE job before its
// primary thread resumes (so it cannot spawn children outside the job), and
// cancellation or the bounded deadline terminates the whole job. Output is
// captured through two runner-owned scratch files read back synchronously
// after the ActiveProcesses==0 drain proof.
func runOwnedGoProcess(ctx context.Context, goExecutable string, dir string, args []string, env []string, stdout, stderr io.Writer) error {
	commandLine, err := winCommandLineUTF16(goExecutable, args)
	if err != nil {
		return fmt.Errorf("build go command line: %v", err)
	}
	application, err := syscall.UTF16PtrFromString(goExecutable)
	if err != nil {
		return fmt.Errorf("encode go executable: %v", err)
	}
	workingDir, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("encode working directory: %v", err)
	}
	environment, err := windowsEnvBlock(env)
	if err != nil {
		return fmt.Errorf("encode environment block: %v", err)
	}
	// Hold the stdlib ForkLock across the whole inheritable-handle window
	// (output-file creation through CreateProcess) so a concurrent
	// CreateProcess in another goroutine cannot interleave and inherit
	// these handles.
	syscall.ForkLock.Lock()
	// Output capture through TWO runner-owned regular files in the
	// runner-owned scratch (dir): the child's stdio are the inherited
	// handles of these files (explicit handle list only); the runner reads
	// them back synchronously after the ActiveProcesses==0 drain proof. No
	// pipes, no pipe-copy goroutines, no read-Close join. File.Close is
	// idempotent, so every owned end closes exactly once through its
	// owning file: no raw CloseHandle on a borrowed numeric handle, no
	// double close after the OS may have reused the number. The captures
	// are opened with winCaptureShareMode (FILE_SHARE_DELETE included), so
	// the path-based removal passes as a pending delete even while the
	// dying child still holds the inherited handle across the kernel's
	// asynchronous teardown after the drain proof.
	var outF, errF *os.File
	var captureCleaned bool
	// Central owning cleanup: after the first create, every exit path
	// closes the owned ends and removes the files, and the explicit paths
	// JOIN the removal error into the returned error — a removal failure
	// is returned, not a swallowed claim that the scratch is clean. This
	// deferral is the backstop that guarantees the cleanup runs even on an
	// early path; File.Close is idempotent, so the backstop and the
	// explicit tail cannot double-close.
	captureCleanup := func() error {
		if captureCleaned {
			return nil
		}
		captureCleaned = true
		closeOwnedFiles(outF, errF)
		return removeOwnedOutput(outF, errF)
	}
	defer func() {
		_ = captureCleanup()
	}()
	outF, err = openOwnedCapture(dir, winStdoutFileName)
	if err != nil {
		syscall.ForkLock.Unlock()
		return fmt.Errorf("create stdout capture file: %v", err)
	}
	errF, err = openOwnedCapture(dir, winStderrFileName)
	if err != nil {
		syscall.ForkLock.Unlock()
		return errors.Join(fmt.Errorf("create stderr capture file: %v", err), captureCleanup())
	}
	// Inheritable only across the locked CreateProcess / explicit handle
	// list below: mark inheritable now, and the child may inherit exactly
	// these two handles and nothing else.
	for _, f := range []*os.File{outF, errF} {
		if err := syscall.SetHandleInformation(syscall.Handle(f.Fd()), syscall.HANDLE_FLAG_INHERIT, 1); err != nil {
			syscall.ForkLock.Unlock()
			return errors.Join(fmt.Errorf("mark capture file inheritable: %v", err), captureCleanup())
		}
	}

	// The child may inherit exactly the two output-file handles and
	// nothing else: an explicit PROC_THREAD_ATTRIBUTE_HANDLE_LIST on
	// STARTUPINFOEX, no inheritable thread attributes (nil), inherit
	// handles on. The borrowed numeric handles are passed to CreateProcess
	// only.
	inherited := []uintptr{uintptr(outF.Fd()), uintptr(errF.Fd())}
	attrList, listErr := newWinProcAttrList(1)
	if listErr != nil {
		syscall.ForkLock.Unlock()
		return errors.Join(listErr, captureCleanup())
	}
	defer attrList.delete()
	if err := attrList.setHandleList(inherited); err != nil {
		syscall.ForkLock.Unlock()
		return errors.Join(err, captureCleanup())
	}

	startup := winStartupInfoEx{}
	// Cb is the size of the ENTIRE extended block, the way the Go stdlib
	// sets it (syscall exec_windows.go newProcess: si.Cb =
	// uint32(unsafe.Sizeof(*si)) with si a _STARTUPINFOEXW).
	startup.StartupInfo.Cb = uint32(unsafe.Sizeof(startup))
	startup.StartupInfo.Flags = winStartfUseStdHandles
	startup.StartupInfo.StdOutput = syscall.Handle(outF.Fd())
	startup.StartupInfo.StdErr = syscall.Handle(errF.Fd())
	startup.attributeList = attrList.list()

	run := runOwnedJob(ctx, winOwnedJobOptions{
		application: application, commandLine: commandLine, environment: environment,
		workingDir: workingDir, startup: &startup,
		captureFiles: []*os.File{outF, errF}, captureDir: dir,
	})
	// Keep the raw-pointer backing buffers, the handle-list backing array,
	// the working-directory backing pointer and the owned output files
	// alive through the call, the way the stdlib keeps its own: syscall
	// exec_windows.go newProcess does runtime.KeepAlive(fd) on the exact
	// same handle-list slice passed as &fd[0] to
	// PROC_THREAD_ATTRIBUTE_HANDLE_LIST (line 447).
	runtime.KeepAlive(application)
	runtime.KeepAlive(commandLine)
	runtime.KeepAlive(environment)
	runtime.KeepAlive(attrList)
	runtime.KeepAlive(inherited)
	runtime.KeepAlive(workingDir)
	runtime.KeepAlive(outF)
	runtime.KeepAlive(errF)
	if run.createErr != nil {
		return errors.Join(fmt.Errorf("create suspended go process: %v", winCallError(run.createErr)), captureCleanup())
	}
	if run.refusalErr != nil {
		return errors.Join(run.refusalErr, captureCleanup())
	}

	// Synchronous bounded capture: on a successful drain the owned tree
	// is dead and the files are final; on a FAILED drain the bounded read
	// (cap+1) keeps the capture honest instead of assuming drain success.
	// The captured bytes are written into the caller's writers before the
	// return (the consumers are finite local boundedWriters; the buffers
	// are never touched after the return).
	captureErr := captureOwnedOutput(dir, stdout, stderr)
	// Central cleanup on the normal tail: close the owned ends (idempotent
	// after closeRunEnds) and remove the files, RETURNING the removal
	// failure instead of swallowing it.
	cleanErr := captureCleanup()

	// One dominant error, with the non-dominant ones joined, not masked:
	// resume failure dominates, then a failed drain (with the capture
	// error joined, not discarded), then the bounded overflow refusal, then
	// a capture refusal alone.
	var mainErr error
	switch {
	case run.resumeFailed:
		mainErr = fmt.Errorf("resume suspended go process: %v", winCallError(run.resumeErr))
		if run.drainErr != nil {
			mainErr = errors.Join(mainErr, fmt.Errorf("drain owned job: %v", run.drainErr))
		}
		if captureErr != nil {
			mainErr = errors.Join(mainErr, captureErr)
		}
	case run.drainErr != nil:
		mainErr = errors.Join(fmt.Errorf("drain owned job: %v", run.drainErr), captureErr)
	case run.overflow:
		mainErr = errors.Join(fmt.Errorf("output exceeded the bounded disk cap (%d bytes per stream)", 2*boundedOutputCap), captureErr)
	case captureErr != nil:
		mainErr = captureErr
	}
	if mainErr != nil {
		return errors.Join(mainErr, cleanErr)
	}
	// A clean run returns nil ONLY when the owned output files were
	// actually removed: a removal failure is returned, not a claim that
	// the drain and the cleanup both succeeded.
	var resultErr error
	if run.canceled || run.waitErr != nil || run.codeErr != nil {
		resultErr = &ownedProcessResult{killed: true}
	} else if run.exitCode != 0 {
		resultErr = &ownedProcessResult{code: int(run.exitCode)}
	}
	return errors.Join(resultErr, cleanErr)
}

// winResumeThreadFailed matches the reference check: ResumeThread returns
// the previous suspend count, and 0xFFFFFFFF signals failure.
func winResumeThreadFailed(resumed uintptr) bool {
	return uint32(resumed) == ^uint32(0)
}

// winOutputOverflow reports whether either owned output file has grown past
// the tightly documented disk allowance (2 x boundedOutputCap per stream):
// the stdout/stderr consumers can only take boundedOutputCap, so on-disk
// growth is allowed up to that multiple and refused beyond it — bounded,
// never unbounded. The measurement is a stat by path (no handle, no seek
// on the shared file object, so the child's write offset is untouched).
func winOutputOverflow(dir string) bool {
	for _, name := range []string{winStdoutFileName, winStderrFileName} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Size() > 2*boundedOutputCap {
			return true
		}
	}
	return false
}

// captureOwnedOutput performs the synchronous bounded output capture
// AFTER the drain proof: a finite read of each owned file
// (boundedOutputCap+1 bytes), an honest overflow refusal when the limit
// is exceeded (the stdout/stderr consumers are finite local boundedWriters
// that can only take boundedOutputCap per stream — never a silently
// truncated capture), and an explicit io.ErrShortWrite refusal. The
// captured bytes are written into the caller's writers before the function
// returns, and the caller's buffers are never touched after the return.
func captureOwnedOutput(dir string, stdout, stderr io.Writer) error {
	return errors.Join(
		captureOneOutput(filepath.Join(dir, winStdoutFileName), stdout),
		captureOneOutput(filepath.Join(dir, winStderrFileName), stderr))
}

func captureOneOutput(path string, dst io.Writer) error {
	const limit = boundedOutputCap + 1
	base := filepath.Base(path)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("capture %s: %v", base, err)
	}
	defer f.Close()
	// Finite read: at most cap+1 bytes, so an oversized file (including
	// one still growing after a FAILED drain) costs bounded I/O and
	// bounded memory, and the extra byte proves the overflow.
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return fmt.Errorf("capture %s: %v", base, err)
	}
	if len(data) > boundedOutputCap {
		return fmt.Errorf("capture %s: at least %d bytes is over the bounded %d-byte output cap (honest refusal, not a truncated capture)",
			base, len(data), boundedOutputCap)
	}
	n, err := dst.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if errors.Is(err, io.ErrShortWrite) {
			return fmt.Errorf("capture %s: the bounded consumer only took part of the data (short write)", base)
		}
		return fmt.Errorf("capture %s: write to the bounded consumer: %v", base, err)
	}
	return nil
}

// removeOwnedOutput removes the owned output files after the runner has
// captured (or refused) them and RETURNS the removal failure: a removal
// error is an honest cleanup failure (the caller's scratch removal would
// fail too), not a swallowed claim that the scratch is clean. Removing a
// file that was never created (an early path) is not a failure.
func removeOwnedOutput(files ...*os.File) error {
	var errs []error
	for _, file := range files {
		if file == nil {
			continue
		}
		if err := removeDrainedOwnedOutput(file.Name()); err != nil {
			errs = append(errs, fmt.Errorf("remove owned output file %s: %v", filepath.Base(file.Name()), err))
		}
	}
	return errors.Join(errs...)
}

// A drained Windows job can finish before the kernel releases the last
// inherited file handle. Retry only the sharing/lock violation for these
// already-owned output paths; every other error remains immediate and a
// persistent lock remains an explicit cleanup failure. With
// winCaptureShareMode the removal normally passes as a pending delete
// during that window; the bounded retry covers the kernel's asynchronous
// release for any handle that does not carry delete sharing, and the final
// failure is returned, not swallowed.
func removeDrainedOwnedOutput(path string) error {
	deadline := time.Now().Add(winCaptureRemovalRetryWindow)
	for {
		err := os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		if (!errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33))) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// closeOwnedFiles closes every owned file end through its owning file
// (idempotent): no raw CloseHandle, no double close of a number the OS may
// have reused.
func closeOwnedFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// closeRunEnds releases the root process/thread handles exactly once each
// and the owned file ends (idempotent file close).
func closeRunEnds(process, thread syscall.Handle, fileEnds ...*os.File) {
	_ = syscall.CloseHandle(process)
	_ = syscall.CloseHandle(thread)
	closeOwnedFiles(fileEnds...)
}

// createKillOnCloseJob creates a job object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: closing the job handle terminates the
// entire assigned tree. Containment is fail-closed: a creation or
// configuration failure is an error, never a silent uncontained fallback.
func createKillOnCloseJob() (syscall.Handle, error) {
	job, _, callErr := winCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return 0, fmt.Errorf("create job object: %v", winCallError(callErr))
	}
	limits := winJobObjectExtendedLimit{}
	limits.Basic.LimitFlags = winJobObjectKillOnClose
	if ok, _, infoErr := winSetInformationJobObject.Call(job, winJobObjectExtendedInfo,
		uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return 0, fmt.Errorf("configure kill-on-close job: %v", winCallError(infoErr))
	}
	return syscall.Handle(job), nil
}

// The containment seams: production always runs the real job creation and
// assignment through these variables; the native regression tests substitute
// fail-closed fakes with the exact production signatures (a correctly typed
// error seam, not a fault injection through an unrelated DLL procedure).
var (
	winCreateOwnedJob        = createKillOnCloseJob
	winAssignOwnedJobProcess = assignOwnedJobProcess
)

// assignOwnedJobProcess assigns a still-suspended process to the job before
// its primary thread resumes.
func assignOwnedJobProcess(job, process syscall.Handle) error {
	if assigned, _, callErr := winAssignProcessToJob.Call(uintptr(job), uintptr(process)); assigned == 0 {
		return fmt.Errorf("assign process to job: %v", winCallError(callErr))
	}
	return nil
}

// winJobObjectBasicAccountingInformation is the EXACT verified primary
// layout of JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (info class
// JobObjectBasicAccountingInformation = 1), as verified by root from the
// official Microsoft documentation and Microsoft
// hcsshim/internal/winapi/jobobject.go: eight fields in this order, 48
// bytes, with ActiveProcesses (DWORD) at offset 40. Query success with the
// returned size of 48 is the ABI evidence; only ActiveProcesses == 0
// proves that no active process remains in the job — a sleeping live
// process can have unchanged counters, so snapshot stability is NOT a
// drain proof.
type winJobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// The query and terminate seams: production always runs the real query and
// terminate through these variables with the exact production signatures;
// the native regressions substitute typed fakes to prove the drain
// contract — a stable snapshot with ActiveProcesses > 0 is not accepted as
// drained, and query/termination failures are propagated.
var (
	winQueryOwnedJobAccounting = queryOwnedJobAccounting
	winTerminateOwnedJob       = terminateOwnedJob
)

// queryOwnedJobAccounting queries the job's basic accounting information
// (class 1). Both the call success AND the returned size (48, the verified
// primary layout) are ABI evidence; a mismatch or a call failure is
// returned, never swallowed.
func queryOwnedJobAccounting(job syscall.Handle) (winJobObjectBasicAccountingInformation, error) {
	var info winJobObjectBasicAccountingInformation
	var retSize uint32
	ok, _, callErr := winQueryInformationJobObject.Call(uintptr(job), winJobObjectBasicAccountingInfo,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&retSize)))
	if ok == 0 {
		return info, fmt.Errorf("query job object basic accounting: %v", winCallError(callErr))
	}
	if want := uint32(unsafe.Sizeof(info)); retSize != want {
		return info, fmt.Errorf("query job object basic accounting: returned size %d, want %d", retSize, want)
	}
	return info, nil
}

// terminateOwnedJob asks the job to terminate its tree; the
// KILL_ON_JOB_CLOSE close of the handle is the backstop that guarantees the
// kill. A termination failure is a real failure and is returned.
func terminateOwnedJob(job syscall.Handle) error {
	if ok, _, callErr := winTerminateJobObject.Call(uintptr(job), 1); ok == 0 {
		return fmt.Errorf("terminate owned job: %v", winCallError(callErr))
	}
	return nil
}

// finishOwnedJob stops the whole owned job and proves the drain by the
// verified primary contract before the handle is closed and the caller
// removes the scratch: terminate the job, then bounded
// QueryInformationJobObject (class 1) until ActiveProcesses == 0 — the
// only whole-job proof. Termination, query, and deadline failures are
// returned as errors; only a proven drain returns nil. The KILL_ON_JOB_CLOSE
// close of the handle is the backstop; no path waits indefinitely.
func finishOwnedJob(job syscall.Handle) error {
	if job == 0 {
		return nil
	}
	if err := winTerminateOwnedJob(job); err != nil {
		_ = syscall.CloseHandle(job)
		return err
	}
	deadline := time.Now().Add(winJobDrainTimeout)
	for {
		info, err := winQueryOwnedJobAccounting(job)
		if err != nil {
			_ = syscall.CloseHandle(job)
			return err
		}
		if info.ActiveProcesses == 0 {
			_ = syscall.CloseHandle(job)
			return nil
		}
		if !time.Now().Before(deadline) {
			_ = syscall.CloseHandle(job)
			return fmt.Errorf("drain owned job: %d active process(es) remain after %s (timeout)", info.ActiveProcesses, winJobDrainTimeout)
		}
		time.Sleep(winQueryPollInterval)
	}
}

// terminateOwnedSuspendedRoot stops a root that was created suspended and
// must never run (the fail-closed refusal before resume): terminate it and
// bound the drain. The suspended process never executed, so it has no
// children; the bounded wait only guards the OS's own teardown. A
// termination or wait failure is returned, never swallowed.
func terminateOwnedSuspendedRoot(process syscall.Handle) error {
	if err := syscall.TerminateProcess(process, 1); err != nil {
		return fmt.Errorf("terminate suspended root: %v", winCallError(err))
	}
	return winWaitSignaled(process, winSuspendedRootDrain)
}

// winWaitSignaled is a bounded WaitForSingleObject whose result is never
// swallowed: a call failure is a real error, a timeout reports that the
// object did not signal within the bound, and only WAIT_OBJECT_0 is a
// clean success.
func winWaitSignaled(h syscall.Handle, wait time.Duration) error {
	event, err := syscall.WaitForSingleObject(h, uint32(wait.Milliseconds()))
	if err != nil {
		return fmt.Errorf("wait %s for the owned root: %v", wait, winCallError(err))
	}
	switch event {
	case syscall.WAIT_OBJECT_0:
		return nil
	case syscall.WAIT_TIMEOUT:
		return fmt.Errorf("wait %s for the owned root timed out", wait)
	default:
		return fmt.Errorf("wait for the owned root: unexpected event %d", event)
	}
}

// winStartupInfoEx mirrors STARTUPINFOEXW (WinBase.h; the stdlib layout
// syscall types_windows.go _STARTUPINFOEXW is the reference): the full
// STARTUPINFO block followed by the PROC_THREAD_ATTRIBUTE_LIST pointer.
// Cb is the size of the entire extended block, as the stdlib sets it.
type winStartupInfoEx struct {
	syscall.StartupInfo
	attributeList unsafe.Pointer
}

// winProcAttrList owns the opaque PROC_THREAD_ATTRIBUTE_LIST buffer through
// the kernel32 lifecycle calls, mirroring the stdlib usage (syscall
// exec_windows.go: Initialize/Update/DeleteProcThreadAttributeList plus
// PROC_THREAD_ATTRIBUTE_HANDLE_LIST).
type winProcAttrList struct {
	ptr unsafe.Pointer
	buf []byte
}

// newWinProcAttrList allocates and initializes a PROC_THREAD_ATTRIBUTE_LIST
// the way the Go stdlib does (syscall syscall_windows.go
// newProcThreadAttributeList): the first call with a NULL buffer is a size
// query whose documented outcome is failure with ERROR_INSUFFICIENT_BUFFER
// and the required buffer size, and the allocated buffer must then be
// initialized before any attribute update.
func newWinProcAttrList(maxAttrs uint32) (*winProcAttrList, error) {
	var size uintptr
	r1, _, callErr := winInitializeProcThreadAttributeList.Call(0, uintptr(maxAttrs), 0, uintptr(unsafe.Pointer(&size)))
	if r1 != 1 && !errors.Is(callErr, syscall.Errno(winErrorInsufficientBuffer)) {
		if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
			return nil, errors.New("initialize proc thread attribute list: unable to query buffer size")
		}
		return nil, fmt.Errorf("initialize proc thread attribute list: %v", winCallError(callErr))
	}
	if size == 0 {
		return nil, errors.New("initialize proc thread attribute list: zero buffer size")
	}
	buf := make([]byte, size)
	l := &winProcAttrList{ptr: unsafe.Pointer(&buf[0]), buf: buf}
	if r1, _, callErr := winInitializeProcThreadAttributeList.Call(uintptr(l.ptr), uintptr(maxAttrs), 0, uintptr(unsafe.Pointer(&size))); r1 != 1 {
		return nil, fmt.Errorf("initialize proc thread attribute list buffer: %v", winCallError(callErr))
	}
	return l, nil
}

// setHandleList stores the explicit PROC_THREAD_ATTRIBUTE_HANDLE_LIST: the
// only handles the spawned process may inherit.
func (l *winProcAttrList) setHandleList(handles []uintptr) error {
	ok, _, callErr := winUpdateProcThreadAttribute.Call(uintptr(l.ptr), 0, uintptr(winProcThreadAttributeHandleList),
		uintptr(unsafe.Pointer(&handles[0])), uintptr(len(handles))*unsafe.Sizeof(handles[0]), 0, 0)
	if ok == 0 {
		return fmt.Errorf("set proc thread attribute handle list: %v", winCallError(callErr))
	}
	return nil
}

func (l *winProcAttrList) list() unsafe.Pointer { return l.ptr }

func (l *winProcAttrList) delete() {
	if l.ptr == nil {
		return
	}
	_, _, _ = winDeleteProcThreadAttributeList.Call(uintptr(l.ptr))
	l.ptr = nil
}

// winCommandLineUTF16 builds the native command line with the MSVCRT
// quoting of the Go stdlib itself: on Windows, syscall.EscapeArg IS the
// exact CommandLineToArgvW implementation the stdlib exports (syscall
// exec_windows.go EscapeArg), so there is no second local copy of the
// algorithm.
func winCommandLineUTF16(exe string, args []string) (*uint16, error) {
	quoted := syscall.EscapeArg(exe)
	for _, arg := range args {
		quoted += " " + syscall.EscapeArg(arg)
	}
	return syscall.UTF16PtrFromString(quoted)
}

// windowsEnvBlock encodes the environment for CREATE_UNICODE_ENVIRONMENT the
// way the Go stdlib does (syscall exec_windows.go createEnvBlock): entries
// are sorted alphabetically by name, each entry is single-NUL-terminated,
// and the whole block has one final extra NUL. An entry containing a NUL is
// a refusal, and an empty environment is the documented two-NUL block.
func windowsEnvBlock(entries []string) (*uint16, error) {
	if len(entries) == 0 {
		block := []uint16{0, 0}
		return &block[0], nil
	}
	sorted := make([]string, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return envSortKey(sorted[i]) < envSortKey(sorted[j])
	})
	var block []uint16
	for _, entry := range sorted {
		if strings.IndexByte(entry, 0) != -1 {
			return nil, syscall.EINVAL
		}
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return &block[0], nil
}

// envSortKey is the name part of an environment entry (before the first
// '=') with the stdlib envSorted ASCII normalization (lowercase letters
// uppercased, so names order case-insensitively); an entry without a name
// keys as empty, which orders it before every named entry. Ties keep their
// relative order (stable), which is deterministic for the env the runner
// builds (one entry per key).
func envSortKey(entry string) string {
	eq := strings.IndexByte(entry, '=')
	if eq < 0 {
		return ""
	}
	k := entry[:eq]
	var b []byte
	for i := 0; i < len(k); i++ {
		c := k[i]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		b = append(b, c)
	}
	return string(b)
}

// winCallError normalizes a Win32 call error the way the reference does: a
// nil or zero errno is not a usable error.
func winCallError(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return syscall.EINVAL
	}
	return err
}

// processAlive reports whether a process with the given PID is running, via
// a query-limited open: no signal is sent.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(handle)
	return true
}
