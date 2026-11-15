//go:build windows

package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	createSuspended            = 0x00000004
	createUnicodeEnvironment   = 0x00000400
	startfUseStdHandles        = 0x00000100
	jobObjectLimitKillOnClose  = 0x00002000
	jobObjectExtendedLimitInfo = 9
)

var (
	windowsCreateProcessMu   sync.Mutex
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	terminateJobObject       = kernel32.NewProc("TerminateJobObject")
	resumeThread             = kernel32.NewProc("ResumeThread")
)

type jobObjectIoCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                jobObjectIoCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func processSignal(_ *exec.ExitError) string { return "" }

func errnoName(errno syscall.Errno) string {
	switch errno {
	case syscall.ENOENT:
		return "ENOENT"
	case syscall.ENOEXEC:
		return "ENOEXEC"
	case syscall.EACCES:
		return "EACCES"
	case syscall.E2BIG:
		return "E2BIG"
	case syscall.EINVAL:
		return "EINVAL"
	case syscall.ETIMEDOUT:
		return "ETIMEDOUT"
	default:
		return ""
	}
}

func setWindowsInvocation(cmd *exec.Cmd, invocation CmdInvocationResult) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: invocation.Command + " " + strings.Join(invocation.Args, " ")}
}

func runWindowsCmdTreeKill(resolved string, invocation CmdInvocationResult, opts RunOptions) (RunResult, bool) {
	if !strings.HasSuffix(strings.ToLower(resolved), ".cmd") && !strings.HasSuffix(strings.ToLower(resolved), ".bat") {
		return RunResult{}, false
	}
	result := RunResult{Resolved: resolved}
	windowsCreateProcessMu.Lock()
	createProcessLocked := true
	defer func() {
		if createProcessLocked {
			windowsCreateProcessMu.Unlock()
		}
	}()
	var err error
	fileOutput := opts.MergeOutput || opts.OutputFiles
	var outputTemp, errorTemp *os.File
	if fileOutput {
		tempDir, code := tempDirFor(opts.Env)
		if code != "" {
			return tempFileError(result, code), true
		}
		outputTemp, err = os.CreateTemp(tempDir, ".soho-out-*")
		if err != nil {
			return tempFileError(result, windowsErrorCode(err)), true
		}
		_ = outputTemp.Chmod(0o600)
		defer func() {
			_ = outputTemp.Close()
			_ = os.Remove(outputTemp.Name())
		}()
		if opts.OutputFiles && !opts.MergeOutput {
			errorTemp, err = os.CreateTemp(tempDir, ".soho-out-*")
			if err != nil {
				return tempFileError(result, windowsErrorCode(err)), true
			}
			_ = errorTemp.Chmod(0o600)
			defer func() {
				_ = errorTemp.Close()
				_ = os.Remove(errorTemp.Name())
			}()
		}
	}
	stdinRead, stdinWrite, err := createWindowsInputPipe()
	if err != nil {
		result.Error = windowsErrorCode(err)
		return result, true
	}
	var stdoutRead, stdoutWrite, stderrRead, stderrWrite syscall.Handle
	if fileOutput {
		stdoutWrite = syscall.Handle(outputTemp.Fd())
		if err = syscall.SetHandleInformation(stdoutWrite, syscall.HANDLE_FLAG_INHERIT, 1); err != nil {
			closeWindowsHandles(stdinRead, stdinWrite)
			result.Error = windowsErrorCode(err)
			return result, true
		}
		if opts.MergeOutput {
			stderrWrite = stdoutWrite
		} else {
			stderrWrite = syscall.Handle(errorTemp.Fd())
			if err = syscall.SetHandleInformation(stderrWrite, syscall.HANDLE_FLAG_INHERIT, 1); err != nil {
				_ = syscall.SetHandleInformation(stdoutWrite, syscall.HANDLE_FLAG_INHERIT, 0)
				closeWindowsHandles(stdinRead, stdinWrite)
				result.Error = windowsErrorCode(err)
				return result, true
			}
		}
	} else {
		stdoutRead, stdoutWrite, err = createWindowsPipe()
		if err != nil {
			closeWindowsHandles(stdinRead, stdinWrite)
			result.Error = windowsErrorCode(err)
			return result, true
		}
		stderrRead, stderrWrite, err = createWindowsPipe()
		if err != nil {
			closeWindowsHandles(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
			result.Error = windowsErrorCode(err)
			return result, true
		}
	}

	attrs := &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), InheritHandle: 1}
	startup := syscall.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(syscall.StartupInfo{})),
		Flags:     startfUseStdHandles,
		StdInput:  stdinRead,
		StdOutput: stdoutWrite,
		StdErr:    stderrWrite,
	}
	commandLine, err := syscall.UTF16PtrFromString(invocation.Command + " " + strings.Join(invocation.Args, " "))
	if err != nil {
		closeWindowsHandles(stdinRead, stdinWrite)
		closeWindowsOutputPipes(fileOutput, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		resetWindowsFileInheritance(fileOutput, opts.MergeOutput, outputTemp, errorTemp)
		result.Error = windowsErrorCode(err)
		return result, true
	}
	application, err := syscall.UTF16PtrFromString(invocation.Command)
	if err != nil {
		closeWindowsHandles(stdinRead, stdinWrite)
		closeWindowsOutputPipes(fileOutput, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		resetWindowsFileInheritance(fileOutput, opts.MergeOutput, outputTemp, errorTemp)
		result.Error = windowsErrorCode(err)
		return result, true
	}
	var currentDir *uint16
	if opts.Cwd != "" {
		currentDir, err = syscall.UTF16PtrFromString(opts.Cwd)
		if err != nil {
			closeWindowsHandles(stdinRead, stdinWrite)
			closeWindowsOutputPipes(fileOutput, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
			resetWindowsFileInheritance(fileOutput, opts.MergeOutput, outputTemp, errorTemp)
			result.Error = windowsErrorCode(err)
			return result, true
		}
	}
	environment, err := windowsEnvironmentBlock(opts.Env.List())
	if err != nil {
		closeWindowsHandles(stdinRead, stdinWrite)
		closeWindowsOutputPipes(fileOutput, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		resetWindowsFileInheritance(fileOutput, opts.MergeOutput, outputTemp, errorTemp)
		result.Error = windowsErrorCode(err)
		return result, true
	}
	var process syscall.ProcessInformation
	if err = syscall.CreateProcess(application, commandLine, nil, attrs, true, createSuspended|createUnicodeEnvironment, pointerToUTF16(environment), currentDir, &startup, &process); err != nil {
		closeWindowsHandles(stdinRead, stdinWrite)
		closeWindowsOutputPipes(fileOutput, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		resetWindowsFileInheritance(fileOutput, opts.MergeOutput, outputTemp, errorTemp)
		result.Error = windowsErrorCode(err)
		return result, true
	}
	resetWindowsFileInheritance(fileOutput, opts.MergeOutput, outputTemp, errorTemp)
	defer syscall.CloseHandle(process.Thread)
	defer syscall.CloseHandle(process.Process)
	_ = syscall.CloseHandle(stdinRead)
	if !fileOutput {
		_ = syscall.CloseHandle(stdoutWrite)
		_ = syscall.CloseHandle(stderrWrite)
	}
	windowsCreateProcessMu.Unlock()
	createProcessLocked = false
	if opts.OnStart != nil {
		opts.OnStart(int(process.ProcessId))
	}
	stdinFile := os.NewFile(uintptr(stdinWrite), "soho-stdin")
	var stdout, stderr bytes.Buffer
	var stdoutDone chan struct{}
	var stderrDone chan struct{}
	if !fileOutput {
		stdoutFile := os.NewFile(uintptr(stdoutRead), "soho-stdout")
		stdoutDone = make(chan struct{})
		go func() {
			_, _ = io.Copy(&stdout, stdoutFile)
			_ = stdoutFile.Close()
			close(stdoutDone)
		}()
		stderrFile := os.NewFile(uintptr(stderrRead), "soho-stderr")
		stderrDone = make(chan struct{})
		go func() {
			_, _ = io.Copy(&stderr, stderrFile)
			_ = stderrFile.Close()
			close(stderrDone)
		}()
	}
	if opts.Input != "" {
		go func() {
			_, _ = io.WriteString(stdinFile, opts.Input)
			_ = stdinFile.Close()
		}()
	} else {
		_ = stdinFile.Close()
	}

	var job syscall.Handle
	jobCreated := !forceWindowsTreeKillFallback(opts.Env)
	if jobCreated {
		job, jobCreated = createWindowsKillJob()
	}
	jobReady, resumeErr := assignThenResume(func() error {
		if !jobCreated {
			return syscall.EINVAL
		}
		if assigned, _, callErr := assignProcessToJobObject.Call(uintptr(job), uintptr(process.Process)); assigned == 0 {
			return windowsCallError(callErr)
		}
		return nil
	}, func() error {
		resumed, _, callErr := resumeThread.Call(uintptr(process.Thread))
		if windowsResumeThreadFailed(resumed) {
			return windowsCallError(callErr)
		}
		return nil
	})
	if resumeErr != nil {
		if jobCreated {
			_ = syscall.CloseHandle(job)
		}
		_ = syscall.TerminateProcess(process.Process, 1)
		_, _ = syscall.WaitForSingleObject(process.Process, syscall.INFINITE)
		result.Error = windowsErrorCode(resumeErr)
		captureWindowsOutput(&result, fileOutput, opts.MergeOutput, opts.StrictOutputUTF8, outputTemp, errorTemp, &stdout, &stderr, stdoutDone, stderrDone)
		return result, true
	}
	if jobCreated && !jobReady {
		_ = syscall.CloseHandle(job)
		jobCreated = false
	}

	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(time.Duration(opts.TimeoutMs) * time.Millisecond)
	cancelled := false
	var waitEvent uint32
	var waitErr error
	for {
		if ctx.Err() != nil {
			cancelled = !errors.Is(ctx.Err(), context.DeadlineExceeded)
			result.TimedOut = !cancelled
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			result.TimedOut = true
			break
		}
		waitFor := remaining
		if waitFor > 50*time.Millisecond {
			waitFor = 50 * time.Millisecond
		}
		waitMs := uint32((waitFor + time.Millisecond - 1) / time.Millisecond)
		waitEvent, waitErr = syscall.WaitForSingleObject(process.Process, waitMs)
		if waitErr != nil || waitEvent != syscall.WAIT_TIMEOUT {
			break
		}
	}
	if waitErr != nil {
		_ = syscall.TerminateProcess(process.Process, 1)
		_, _ = syscall.WaitForSingleObject(process.Process, syscall.INFINITE)
		result.Error = windowsErrorCode(waitErr)
	} else if result.TimedOut || cancelled {
		if jobReady {
			if terminated, _, _ := terminateJobObject.Call(uintptr(job), 1); terminated == 0 {
				killWindowsProcessTree(process.ProcessId, opts.Env)
			}
			_ = syscall.CloseHandle(job)
			jobCreated = false
		} else {
			killWindowsProcessTree(process.ProcessId, opts.Env)
			if event, _ := syscall.WaitForSingleObject(process.Process, 1000); event == syscall.WAIT_TIMEOUT {
				_ = syscall.TerminateProcess(process.Process, 1)
			}
		}
		_, _ = syscall.WaitForSingleObject(process.Process, syscall.INFINITE)
	}
	if jobCreated {
		_ = syscall.CloseHandle(job)
	}
	var exitCode uint32
	if err = syscall.GetExitCodeProcess(process.Process, &exitCode); err == nil && !result.TimedOut && !cancelled {
		status := int(exitCode)
		result.Status = &status
	}
	if result.TimedOut {
		setRunTimeoutResult(&result)
	} else if cancelled {
		result.Signal = "SIGTERM"
	}
	_ = stdinFile.Close()
	captureWindowsOutput(&result, fileOutput, opts.MergeOutput, opts.StrictOutputUTF8, outputTemp, errorTemp, &stdout, &stderr, stdoutDone, stderrDone)
	return result, true
}

func windowsEnvironmentBlock(entries []string) ([]uint16, error) {
	deduplicated := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	// Env.List sorts keys; the last case-insensitive duplicate in that order wins.
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		separator := strings.IndexByte(entry, '=')
		if separator == 0 {
			separator = strings.IndexByte(entry[1:], '=') + 1
		}
		if separator < 0 {
			if entry != "" {
				deduplicated = append(deduplicated, entry)
			}
			continue
		}
		key := strings.ToLower(entry[:separator])
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduplicated = append(deduplicated, entry)
	}
	for i := 0; i < len(deduplicated)/2; i++ {
		j := len(deduplicated) - i - 1
		deduplicated[i], deduplicated[j] = deduplicated[j], deduplicated[i]
	}
	keyForSort := func(entry string) string {
		separator := strings.IndexByte(entry, '=')
		if separator < 0 {
			return ""
		}
		key := []byte(entry[:separator])
		for i, b := range key {
			if 'a' <= b && b <= 'z' {
				key[i] -= 'a' - 'A'
			}
		}
		return string(key)
	}
	sort.SliceStable(deduplicated, func(i, j int) bool {
		return keyForSort(deduplicated[i]) < keyForSort(deduplicated[j])
	})
	// The block holds NUL separators, so it is encoded entry by entry:
	// syscall.UTF16FromString preserves WTF-8 code units and rejects NUL (EINVAL).
	var block []uint16
	for _, entry := range deduplicated {
		encoded, err := syscall.UTF16FromString(entry)
		if err != nil {
			return nil, err
		}
		block = append(block, encoded...)
	}
	if len(deduplicated) == 0 {
		return []uint16{0, 0}, nil
	}
	return append(block, 0), nil
}

func windowsResumeThreadFailed(resumed uintptr) bool {
	return uint32(resumed) == ^uint32(0)
}

func createWindowsPipe() (parent, child syscall.Handle, err error) {
	attrs := &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), InheritHandle: 1}
	if err = syscall.CreatePipe(&parent, &child, attrs, 0); err != nil {
		return 0, 0, err
	}
	if err = syscall.SetHandleInformation(parent, syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = syscall.CloseHandle(parent)
		_ = syscall.CloseHandle(child)
		return 0, 0, err
	}
	return parent, child, nil
}

func createWindowsInputPipe() (childRead, parentWrite syscall.Handle, err error) {
	attrs := &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), InheritHandle: 1}
	if err = syscall.CreatePipe(&childRead, &parentWrite, attrs, 0); err != nil {
		return 0, 0, err
	}
	if err = syscall.SetHandleInformation(parentWrite, syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = syscall.CloseHandle(childRead)
		_ = syscall.CloseHandle(parentWrite)
		return 0, 0, err
	}
	return childRead, parentWrite, nil
}

func closeWindowsHandles(handles ...syscall.Handle) {
	seen := make(map[syscall.Handle]bool)
	for _, handle := range handles {
		if handle != 0 && !seen[handle] {
			seen[handle] = true
			_ = syscall.CloseHandle(handle)
		}
	}
}

func closeWindowsOutputPipes(fileOutput bool, handles ...syscall.Handle) {
	if !fileOutput {
		closeWindowsHandles(handles...)
	}
}

func resetWindowsFileInheritance(fileOutput, mergeOutput bool, stdout, stderr *os.File) {
	if !fileOutput || stdout == nil {
		return
	}
	_ = syscall.SetHandleInformation(syscall.Handle(stdout.Fd()), syscall.HANDLE_FLAG_INHERIT, 0)
	if !mergeOutput && stderr != nil {
		_ = syscall.SetHandleInformation(syscall.Handle(stderr.Fd()), syscall.HANDLE_FLAG_INHERIT, 0)
	}
}

func captureWindowsOutput(result *RunResult, fileOutput, mergeOutput, strictOutputUTF8 bool, stdoutFile, stderrFile *os.File, stdout, stderr *bytes.Buffer, stdoutDone, stderrDone <-chan struct{}) {
	var out, errOut []byte
	if fileOutput {
		read := func(file *os.File) []byte {
			if file == nil {
				return nil
			}
			_ = file.Sync()
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return nil
			}
			data, err := io.ReadAll(file)
			if err != nil {
				return nil
			}
			return data
		}
		out = read(stdoutFile)
		if !mergeOutput {
			errOut = read(stderrFile)
		}
	} else {
		_ = waitWindowsOutput(stdoutDone, stderrDone)
		out, errOut = stdout.Bytes(), stderr.Bytes()
	}
	setRunOutput(result, out, errOut, strictOutputUTF8)
}

func pointerToUTF16(value []uint16) *uint16 {
	if len(value) == 0 {
		return nil
	}
	return &value[0]
}

func createWindowsKillJob() (syscall.Handle, bool) {
	job, _, _ := createJobObjectW.Call(0, 0)
	if job == 0 {
		return 0, false
	}
	limits := jobObjectExtendedLimitInformation{}
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnClose
	if ok, _, _ := setInformationJobObject.Call(job, jobObjectExtendedLimitInfo, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return 0, false
	}
	return syscall.Handle(job), true
}

func windowsCallError(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return syscall.EINVAL
	}
	return err
}

func killWindowsProcessTree(pid uint32, env Env) {
	tempDir := env.Get("TMPDIR")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if runTreeKillTestKiller(env, tempDir, int(pid), func(exe string, args []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, exe, args...).Run()
	}) {
		return
	}
	systemRoot := env.Get("SystemRoot")
	if systemRoot == "" {
		systemRoot = env.Get("WINDIR")
	}
	if systemRoot == "" {
		systemRoot = os.Getenv("SystemRoot")
	}
	if systemRoot == "" {
		systemRoot = os.Getenv("WINDIR")
	}
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := filepath.Join(systemRoot, "System32", "taskkill.exe")
	_ = exec.CommandContext(ctx, command, "/PID", strconv.FormatUint(uint64(pid), 10), "/T", "/F").Run()
}

func waitWindowsOutput(stdoutDone, stderrDone <-chan struct{}) error {
	if stdoutDone != nil {
		<-stdoutDone
	}
	if stderrDone != nil {
		<-stderrDone
	}
	return nil
}

func windowsErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		return "ENOENT"
	}
	return errorCode(err)
}
