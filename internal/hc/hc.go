// Package hc is the soho client for the herdr-core CLI. It runs `hc` with
// --json through platform.RunCli, decodes stdout on exit 0, and maps the
// stderr error object and the exit codes onto typed errors. transport=hc
// (DJA-603 stage 2a) is read by doctor only so far.
package hc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// The named exit codes of hc 0.1.14. Other non-zero exits are generic
// failures: their Kind is whatever the stderr error object carries, or ""
// when the stderr is not that JSON.
const (
	ExitNotFound        = 3
	ExitNeedsTarget     = 7
	ExitBlocked         = 9
	ExitStatusMismatch  = 10
	ExitFinished        = 11
	ExitRunStillRunning = 124 // `hc run` without --bg while the program runs
)

// MinVersion is the oldest hc version this soho supports with transport=hc.
const MinVersion = "0.1.14"

// ErrNotInstalled is the sentinel for a missing hc binary. It is never an
// *Error: the client cannot read what the absent CLI did not print.
var ErrNotInstalled = errors.New("hc CLI not found in PATH")

// Error is one non-zero hc exit with its decoded stderr error object,
// {"error":{"kind":"...","message":"...","hint":"..."}} (hint may be
// missing). When the stderr is not that JSON, Kind is "" and Message is
// the trimmed stderr.
type Error struct {
	Kind    string
	Message string
	Hint    string
	Exit    int
}

func (e *Error) Error() string {
	if e.Kind == "" {
		return fmt.Sprintf("hc exited %d: %s", e.Exit, e.Message)
	}
	if e.Hint != "" {
		return fmt.Sprintf("hc exited %d: %s: %s (%s)", e.Exit, e.Kind, e.Message, e.Hint)
	}
	return fmt.Sprintf("hc exited %d: %s: %s", e.Exit, e.Kind, e.Message)
}

// Run runs hc with the given sub-arguments and --json appended, without
// stdin input. It returns the stdout text on exit 0; a missing binary is
// ErrNotInstalled; every other non-zero exit is an *Error decoded from the
// stderr.
func Run(ctx context.Context, env platform.Env, args ...string) (string, error) {
	return RunInput(ctx, env, "", args...)
}

// RunInput is Run with one line written to hc's stdin before it is closed.
func RunInput(ctx context.Context, env platform.Env, input string, args ...string) (string, error) {
	argv := append(append([]string{}, args...), "--json")
	res := platform.RunCli("hc", argv, platform.RunOptions{Context: ctx, Env: env, Input: input})
	if res.NotFound {
		return "", ErrNotInstalled
	}
	if res.Status != nil && *res.Status == 0 {
		return res.Stdout, nil
	}
	exit := 0
	if res.Status != nil {
		exit = *res.Status
	}
	err := decodeError(res.Stderr, exit)
	if err.Message == "" && res.Error != "" {
		err.Message = strings.TrimSpace(res.Error)
	}
	return "", err
}

// decodeError maps one non-zero exit onto an *Error. When the stderr (after
// trimming) is the {"error":{...}} JSON object, its kind, message and hint
// are carried over; otherwise Kind stays "" and Message is the trimmed
// stderr.
func decodeError(stderr string, exit int) *Error {
	e := &Error{Exit: exit}
	trimmed := strings.TrimSpace(stderr)
	if decoded, ok := stderrObject(trimmed); ok {
		e.Kind = decoded.Kind
		e.Message = decoded.Message
		e.Hint = decoded.Hint
	}
	if e.Message == "" {
		e.Message = trimmed
	}
	return e
}

// stderrObject decodes the error object hc prints to stderr on failure. The
// bool is false when the text is not that JSON shape (not JSON, not an
// object, or without the error object).
func stderrObject(text string) (*Error, bool) {
	v, err := jsonjs.Parse([]byte(text))
	if err != nil {
		return nil, false
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return nil, false
	}
	raw, ok := obj.Get("error")
	if !ok {
		return nil, false
	}
	errObj, ok := raw.(*jsonjs.Object)
	if !ok {
		return nil, false
	}
	e := &Error{}
	if kind, ok := errObj.Get("kind"); ok {
		e.Kind = jsString(kind)
	}
	if message, ok := errObj.Get("message"); ok {
		e.Message = jsString(message)
	}
	if hint, ok := errObj.Get("hint"); ok {
		e.Hint = jsString(hint)
	}
	return e, true
}

// jsString renders one decoded JSON value the way the error fields carry
// it: strings as-is, a missing value as "".
func jsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
