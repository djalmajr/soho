// Package gate runs one program under a time bound and leaves the evidence a
// reviewer can trust: the exact command, both output streams as files, and a
// result that says how the started process really ended.
//
// The command is deliberately small. It starts the program directly (no
// shell), waits on the exact process it started, and on expiry terminates
// that process and, on Unix, the process group the command created for it.
// It reads no PID from anywhere and never signals a process it did not start.
package gate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	// MinBound and MaxBound are the inclusive limits of the duration argument.
	MinBound = time.Second
	MaxBound = time.Hour

	exitUsage    = 2
	exitExpired  = 124
	exitNotRun   = 125
	usageMessage = "usage: gate <prefix> <duration> -- <program> [args…]"
)

// termGrace is how long the terminated process may take to exit before it is
// killed. It is a variable only so tests can shorten the wait.
var termGrace = 5 * time.Second

// Files are the four artifacts of one gate run, derived from the prefix.
type Files struct {
	Command, Stdout, Stderr, Result string
}

// FilesFor appends the four suffixes to the prefix exactly as given: the
// prefix is never expanded, cleaned or resolved.
func FilesFor(prefix string) Files {
	return Files{
		Command: prefix + ".command.json",
		Stdout:  prefix + ".stdout",
		Stderr:  prefix + ".stderr",
		Result:  prefix + ".result.json",
	}
}

type commandRecord struct {
	Argv    []string `json:"argv"`
	Cwd     string   `json:"cwd"`
	Bound   string   `json:"bound"`
	Started string   `json:"started"`
}

type resultRecord struct {
	PID            int    `json:"pid"`
	Started        string `json:"started"`
	Completed      string `json:"completed"`
	ExecutionBound string `json:"execution_bound"`
	NativeExit     int    `json:"native_exit"`
	Signal         string `json:"signal"`
	ContextExpired bool   `json:"context_expired"`
	WaitJoined     bool   `json:"wait_joined"`
	WaitError      string `json:"wait_error"`
	StartError     string `json:"start_error"`
}

type request struct {
	prefix string
	bound  time.Duration
	argv   []string
}

func usageError(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, "soho: gate: %s\nsoho: %s\n", fmt.Sprintf(format, args...), usageMessage)
	return exitUsage
}

func parse(args []string) (request, string) {
	if len(args) < 2 {
		return request{}, "a prefix and a duration are required"
	}
	if args[0] == "" {
		return request{}, "the prefix is empty"
	}
	bound, err := time.ParseDuration(args[1])
	if err != nil {
		return request{}, fmt.Sprintf("the duration %q is not a Go duration", args[1])
	}
	if bound < MinBound || bound > MaxBound {
		return request{}, fmt.Sprintf("the duration %q is outside 1s..1h", args[1])
	}
	if len(args) < 3 || args[2] != "--" {
		return request{}, "the program must follow a -- argument"
	}
	if len(args) < 4 || args[3] == "" {
		return request{}, "no program after --"
	}
	return request{prefix: args[0], bound: bound, argv: append([]string(nil), args[3:]...)}, ""
}

// Run executes `gate <prefix> <duration> -- <program> [args…]` (args are what
// follows the command name) and returns the command's exit code. stdout gets
// the one summary line; stderr the diagnostics of the command itself.
func Run(args []string, stdout, stderr io.Writer) int {
	req, problem := parse(args)
	if problem != "" {
		return usageError(stderr, "%s", problem)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return usageError(stderr, "cannot read the working directory: %v", err)
	}
	files := FilesFor(req.prefix)
	opened, problem := createFiles(files)
	if problem != "" {
		return usageError(stderr, "%s", problem)
	}
	defer opened.closeAll()

	started := time.Now().UTC()
	startedText := started.Format(time.RFC3339Nano)
	command, err := marshalLine(commandRecord{Argv: req.argv, Cwd: cwd, Bound: req.bound.String(), Started: startedText})
	if err == nil {
		err = writeFile(opened.command, command)
	}
	if err != nil {
		opened.removeAll()
		return usageError(stderr, "cannot record the command: %v", err)
	}

	// The gate's own interruption must end the child too: the child has its
	// own process group, so a terminal interrupt would never reach it.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, interruptSignals()...)
	defer signal.Stop(interrupts)

	res := resultRecord{Started: startedText, ExecutionBound: req.bound.String(), NativeExit: -1}
	cmd := exec.Command(req.argv[0], req.argv[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = opened.stdout
	cmd.Stderr = opened.stderr
	prepare(cmd)

	if err := cmd.Start(); err != nil {
		res.StartError = err.Error()
	} else {
		res.PID = cmd.Process.Pid
		supervise(cmd, req.bound, interrupts, &res)
	}
	res.Completed = time.Now().UTC().Format(time.RFC3339Nano)

	// The output files are closed before the result is written, so the
	// result never describes a run whose streams are still open.
	code := finalCode(res)
	for _, closeErr := range opened.closeStreams() {
		_, _ = fmt.Fprintf(stderr, "soho: gate: %v\n", closeErr)
		code = exitNotRun
	}
	if err := writeResult(opened.result, res); err != nil {
		_, _ = fmt.Fprintf(stderr, "soho: gate: cannot write %s: %v\n", files.Result, err)
		code = exitNotRun
	}
	_, _ = fmt.Fprintf(stdout, "pid=%d native_exit=%d context_expired=%t wait_joined=%t\n", res.PID, res.NativeExit, res.ContextExpired, res.WaitJoined)
	return code
}

// supervise waits on the exact started handle. It owns the bound, the
// termination sequence and the final classification of the exit.
func supervise(cmd *exec.Cmd, bound time.Duration, interrupts <-chan os.Signal, res *resultRecord) {
	done := make(chan error, 1)
	go func() { done <- observeExit(cmd) }()
	timer := time.NewTimer(bound)
	defer timer.Stop()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-interrupts:
		waitErr = terminate(cmd, done)
	case <-timer.C:
		// A program that finished at the same instant as the bound is a
		// normal exit: decide on what the exit observer already reported.
		select {
		case waitErr = <-done:
		default:
			res.ContextExpired = true
			waitErr = terminate(cmd, done)
		}
	}
	waitErr = reap(cmd, waitErr)
	res.WaitJoined = true
	classify(cmd, waitErr, res)
}

// terminate ends the started process (and its group where the platform has
// one): terminate, wait up to termGrace, then kill, and in every case wait
// for exit observation. The final group kill precedes reaping so the leader
// still reserves the identity even if every group member has already exited.
func terminate(cmd *exec.Cmd, done <-chan error) error {
	signalTerminate(cmd)
	grace := time.NewTimer(termGrace)
	defer grace.Stop()
	var err error
	select {
	case err = <-done:
	case <-grace.C:
		signalKill(cmd)
		err = <-done
	}
	signalKill(cmd)
	return err
}

func classify(cmd *exec.Cmd, waitErr error, res *resultRecord) {
	if cmd.ProcessState != nil {
		res.NativeExit = cmd.ProcessState.ExitCode()
		res.Signal = signalName(cmd.ProcessState)
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		res.WaitError = waitErr.Error()
	}
}

func finalCode(res resultRecord) int {
	switch {
	case res.StartError != "":
		return exitNotRun
	case res.ContextExpired:
		return exitExpired
	case res.Signal != "" || res.NativeExit < 0:
		return exitNotRun
	}
	return res.NativeExit
}

type openedFiles struct {
	paths                           Files
	command, stdout, stderr, result *os.File
	streamsClosed                   bool
}

// createFiles reserves the four artifacts exclusively and refuses, creating
// nothing, when the parent directory is missing or any of them exists.
func createFiles(files Files) (*openedFiles, string) {
	parent := filepath.Dir(files.Command)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, fmt.Sprintf("the parent directory %s is missing", parent)
	}
	for _, path := range []string{files.Command, files.Stdout, files.Stderr, files.Result} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				return nil, fmt.Sprintf("%s already exists", path)
			}
			return nil, fmt.Sprintf("cannot inspect %s: %v", path, err)
		}
	}
	opened := &openedFiles{paths: files}
	slots := []struct {
		path string
		dst  **os.File
	}{{files.Command, &opened.command}, {files.Stdout, &opened.stdout}, {files.Stderr, &opened.stderr}, {files.Result, &opened.result}}
	for _, slot := range slots {
		f, err := os.OpenFile(slot.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			// The process umask can only clear bits; make the mode exact.
			if chmodErr := f.Chmod(0o600); chmodErr != nil && !chmodMayFail {
				err = chmodErr
				_ = f.Close()
				_ = os.Remove(slot.path)
			}
		}
		if err != nil {
			// Only the files this call created are removed.
			opened.removeAll()
			if errors.Is(err, fs.ErrExist) {
				return nil, fmt.Sprintf("%s already exists", slot.path)
			}
			return nil, fmt.Sprintf("cannot create %s: %v", slot.path, err)
		}
		*slot.dst = f
	}
	return opened, ""
}

// removeAll closes and deletes the files this run created; it is used only
// before the program started.
func (o *openedFiles) removeAll() {
	pairs := []struct {
		f    *os.File
		path string
	}{{o.command, o.paths.Command}, {o.stdout, o.paths.Stdout}, {o.stderr, o.paths.Stderr}, {o.result, o.paths.Result}}
	for _, pair := range pairs {
		if pair.f != nil {
			_ = pair.f.Close()
			_ = os.Remove(pair.path)
		}
	}
	o.command, o.stdout, o.stderr, o.result = nil, nil, nil, nil
}

func (o *openedFiles) closeStreams() []error {
	if o.streamsClosed {
		return nil
	}
	o.streamsClosed = true
	var errs []error
	for _, pair := range []struct {
		f    *os.File
		path string
	}{{o.stdout, o.paths.Stdout}, {o.stderr, o.paths.Stderr}} {
		if err := pair.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("cannot close %s: %w", pair.path, err))
		}
	}
	return errs
}

// closeAll is the deferred safety net: it closes whatever is still open.
func (o *openedFiles) closeAll() {
	for _, f := range []*os.File{o.command, o.stdout, o.stderr, o.result} {
		if f != nil {
			_ = f.Close()
		}
	}
}

func marshalLine(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeFile(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func writeResult(f *os.File, res resultRecord) error {
	data, err := marshalLine(res)
	if err != nil {
		return err
	}
	if err := writeFile(f, data); err != nil {
		return err
	}
	return f.Close()
}

func interruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
