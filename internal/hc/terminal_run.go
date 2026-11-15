package hc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// maxLineBytes bounds incoming JSON-RPC response lines to at most 1 MiB.
const maxLineBytes = 1024 * 1024

// reapTimeout bounds how long the default connector waits for a child bridge
// to exit after its stdin is closed.
const reapTimeout = 2 * time.Second

// ErrRunOutcomeUnknown indicates that terminal.run may have been submitted to
// the core, but the outcome cannot be determined. Callers must not retry or
// force cleanup blindly because a live terminal may exist.
var ErrRunOutcomeUnknown = errors.New("terminal run outcome unknown")

// The value-free preflight refusals for explicit environment overrides. None
// of them carries a supplied name or value, so a bad override cannot be
// echoed back through the error channel.
var (
	errEnvNameReserved      = errors.New("terminal run: reserved environment override name")
	errEnvNameInvalid       = errors.New("terminal run: invalid environment override name")
	errEnvValueNUL          = errors.New("terminal run: environment override value contains NUL")
	errEnvValueInvalidUTF8  = errors.New("terminal run: environment override value contains invalid UTF-8")
	errLaunchProfileInvalid = errors.New("terminal run: invalid launch profile name")
)

// TerminalRun specifies the parameters required to start a terminal via
// the native terminal.run RPC client.
type TerminalRun struct {
	Name    string
	Kind    string
	Program string
	Cwd     string
	Caller  string
	Session string
	Args    []string

	// EnvOverrides are the explicit per-child environment entries merged
	// into the terminal.run request's env alongside the mandatory
	// SOHO_TRANSPORT=hc. Reserved identity/routing names (SOHO_, HC_ and
	// HERDR_ prefixes, case-insensitive), names that are not portable
	// identifiers (letters, digits and underscore, non-digit first), and
	// values containing NUL or invalid UTF-8 are refused before Connect
	// with value-free errors; valid Unicode and literal U+FFFD values
	// are preserved unchanged. A nil or empty map changes nothing: the
	// existing exact request is preserved.
	EnvOverrides map[string]string

	// LaunchProfile is the optional resolved launch profile emitted as
	// the soho.launch_profile label alongside the existing soho.session
	// label ("" for the old session-only request). The client validates
	// only the portable identifier shape — 1..64 ASCII bytes, first a-z,
	// the rest a-z0-9_- — never kind or model support, and refuses every
	// other value before Connect with the fixed value-free
	// errLaunchProfileInvalid. Native per-kind policy stays the shared
	// kinds layer's later responsibility.
	LaunchProfile string
}

// runParams is the by-name parameter struct sent to the Core's terminal.run RPC.
type runParams struct {
	Argv          []string          `json:"argv"`
	Caller        string            `json:"caller"`
	Cwd           string            `json:"cwd"`
	Env           map[string]string `json:"env"`
	Labels        map[string]string `json:"labels"`
	Name          string            `json:"name"`
	NoNotify      bool              `json:"no_notify"`
	OutliveParent bool              `json:"outlive_parent"`
	Wait          bool              `json:"wait"`
}

// runRequest is the JSON-RPC 2.0 request envelope for terminal.run.
type runRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Method  string    `json:"method"`
	Params  runParams `json:"params"`
}

// isScriptPath reports whether the resolved path ends with a Windows script extension (.cmd or .bat).
func isScriptPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")
}

// defaultConnect launches a native `hc connect --json` process directly with Go,
// refusing .cmd/.bat script paths and returning pipes for stdin, stdout, and a bounded reap function.
func defaultConnect(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
	resolved, ok := platform.FindExecutable("hc", env, platform.Current())
	if !ok {
		return nil, nil, nil, ErrNotInstalled
	}
	if isScriptPath(resolved) {
		return nil, nil, nil, fmt.Errorf("hc binary %q is a script; native executable required", resolved)
	}

	cmd := exec.CommandContext(ctx, resolved, "connect", "--json")
	if platform.Current() != "win32" {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return cmd.Process.Signal(syscall.SIGTERM)
		}
	} else {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return cmd.Process.Kill()
		}
	}
	cmd.WaitDelay = 1 * time.Second
	cmd.Env = env.List()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, err
	}

	var reapOnce sync.Once
	var reapErr error
	reap := func() error {
		reapOnce.Do(func() {
			_ = stdin.Close()
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()
			select {
			case err := <-done:
				reapErr = err
			case <-time.After(reapTimeout):
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				reapErr = <-done
			}
		})
		return reapErr
	}
	return stdin, stdout, reap, nil
}

// RunTerminal runs a terminal program through hc's native terminal.run RPC
// using a client initialized for env.
func RunTerminal(ctx context.Context, env platform.Env, input TerminalRun) (string, error) {
	return New(env).RunTerminal(ctx, input)
}

// RunTerminal runs a terminal program through hc's native terminal.run RPC.
func (c *Client) RunTerminal(ctx context.Context, input TerminalRun) (string, error) {
	if c.Connect == nil {
		return "", errors.New("terminal run: client connect seam is not configured")
	}
	if err := validateTerminalRun(input); err != nil {
		return "", err
	}
	// One exact per-call snapshot of the caller's explicit overrides, taken
	// and validated before Connect. The serialized request is built only
	// from this clone, so a synchronous Connect seam that mutates the
	// caller's map cannot inject reserved routing/identity names or change
	// the captured environment.
	snapshot := cloneEnvOverrides(input.EnvOverrides)
	if err := validateEnvOverrides(snapshot); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	in, out, reap, err := c.Connect(ctx, c.Env)
	if err != nil {
		return "", err
	}
	defer func() {
		if in != nil {
			_ = in.Close()
		}
		if reap != nil {
			_ = reap()
		}
	}()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 1. Send generation 1 hello (id 1)
	if _, err := io.WriteString(in, HelloLine+"\n"); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("failed to send hello handshake: %w", err)
	}

	reader := bufio.NewReader(out)
	helloLine, err := readResponseLine(reader, maxLineBytes)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return "", errors.New("EOF waiting for hello response")
		}
		return "", fmt.Errorf("failed to read hello response: %w", err)
	}

	// Strictly validate hello response BEFORE writing terminal.run
	if err := validateHelloResponse(helloLine); err != nil {
		return "", err
	}

	// 2. Prepare terminal.run request (id 2)
	argv := make([]string, 0, 1+len(input.Args))
	argv = append(argv, input.Program)
	argv = append(argv, input.Args...)

	// The child env is the mandatory transport marker plus only the
	// validated per-call snapshot, never the caller's environment.
	env := map[string]string{"SOHO_TRANSPORT": "hc"}
	for name, value := range snapshot {
		env[name] = value
	}

	// The labels are exactly the existing session label plus, only when a
	// validated nonempty profile was supplied, the launch profile label;
	// an empty profile keeps the old session-only request.
	labels := map[string]string{"soho.session": input.Session}
	if input.LaunchProfile != "" {
		labels[transport.LaunchProfileLabel] = input.LaunchProfile
	}

	req := runRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "terminal.run",
		Params: runParams{
			Argv:          argv,
			Caller:        input.Caller,
			Cwd:           input.Cwd,
			Env:           env,
			Labels:        labels,
			Name:          input.Name,
			NoNotify:      true,
			OutliveParent: true,
			Wait:          false,
		},
	}

	var reqBuf bytes.Buffer
	enc := json.NewEncoder(&reqBuf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return "", fmt.Errorf("failed to encode terminal.run request: %w", err)
	}
	payload := reqBuf.Bytes()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 3. Write terminal.run request. Any failure from here on may have submitted the run.
	if _, err := in.Write(payload); err != nil {
		if ctx.Err() != nil {
			return "", wrapRunOutcomeUnknown(ctx.Err())
		}
		return "", wrapRunOutcomeUnknown(err)
	}

	// 4. Read terminal.run response line.
	runLine, err := readResponseLine(reader, maxLineBytes)
	if err != nil {
		if ctx.Err() != nil {
			return "", wrapRunOutcomeUnknown(ctx.Err())
		}
		if errors.Is(err, io.EOF) {
			return "", wrapRunOutcomeUnknownMsg("EOF waiting for terminal.run response")
		}
		return "", wrapRunOutcomeUnknown(err)
	}

	if err := ctx.Err(); err != nil {
		return "", wrapRunOutcomeUnknown(err)
	}

	// 5. Parse and validate terminal.run response.
	addr, err := parseRunResponse(runLine)
	if err != nil {
		return "", err
	}

	// If a valid address was observed, retain it even when connection shutdown reports an error.
	return addr, nil
}

// validateTerminalRun checks that required fields are nonempty and all strings contain valid UTF-8.
func validateTerminalRun(input TerminalRun) error {
	if strings.TrimSpace(input.Name) == "" {
		return errors.New("terminal run: name is required")
	}
	if !utf8.ValidString(input.Name) {
		return errors.New("terminal run: name contains invalid UTF-8")
	}
	if strings.TrimSpace(input.Kind) == "" {
		return errors.New("terminal run: kind is required")
	}
	if !utf8.ValidString(input.Kind) {
		return errors.New("terminal run: kind contains invalid UTF-8")
	}
	if strings.TrimSpace(input.Program) == "" {
		return errors.New("terminal run: program is required")
	}
	if !utf8.ValidString(input.Program) {
		return errors.New("terminal run: program contains invalid UTF-8")
	}
	if strings.TrimSpace(input.Cwd) == "" {
		return errors.New("terminal run: cwd is required")
	}
	if !utf8.ValidString(input.Cwd) {
		return errors.New("terminal run: cwd contains invalid UTF-8")
	}
	if strings.TrimSpace(input.Caller) == "" {
		return errors.New("terminal run: caller is required")
	}
	if !utf8.ValidString(input.Caller) {
		return errors.New("terminal run: caller contains invalid UTF-8")
	}
	if strings.TrimSpace(input.Session) == "" {
		return errors.New("terminal run: session is required")
	}
	if !utf8.ValidString(input.Session) {
		return errors.New("terminal run: session contains invalid UTF-8")
	}
	for i, arg := range input.Args {
		if !utf8.ValidString(arg) {
			return fmt.Errorf("terminal run: args[%d] contains invalid UTF-8", i)
		}
	}
	if !isValidLaunchProfile(input.LaunchProfile) {
		return errLaunchProfileInvalid
	}
	return nil
}

// cloneEnvOverrides copies one per-call override map into a snapshot the
// caller can no longer reach, so the serialized request is stable for the
// whole call even if the Connect seam mutates the original.
func cloneEnvOverrides(overrides map[string]string) map[string]string {
	clone := make(map[string]string, len(overrides))
	for name, value := range overrides {
		clone[name] = value
	}
	return clone
}

// validateEnvOverrides checks the per-call override snapshot before Connect:
// reserved identity/routing names are refused so the caller cannot rebind
// routing or identity, names must be portable identifiers, and values must
// not contain NUL or invalid UTF-8 bytes. Actual UTF-8 validity, not a
// replacement-rune ban, is the invariant: valid Unicode and a literal U+FFFD
// value pass unchanged. Errors are value-free: they name the failure class
// only, never the supplied name or value.
func validateEnvOverrides(overrides map[string]string) error {
	for name, value := range overrides {
		if isReservedEnvName(name) {
			return errEnvNameReserved
		}
		if !isValidEnvName(name) {
			return errEnvNameInvalid
		}
		if strings.ContainsRune(value, 0) {
			return errEnvValueNUL
		}
		if !utf8.ValidString(value) {
			return errEnvValueInvalidUTF8
		}
	}
	return nil
}

// isReservedEnvName reports whether the name carries a SOHO_, HC_ or HERDR_
// prefix, case-insensitively: the identity and routing environment the core
// owns, which an explicit override must never rebind.
func isReservedEnvName(name string) bool {
	upper := strings.ToUpper(name)
	return strings.HasPrefix(upper, "SOHO_") || strings.HasPrefix(upper, "HC_") || strings.HasPrefix(upper, "HERDR_")
}

// isValidEnvName reports whether the name is a portable environment
// identifier: letters, digits and underscore with a non-digit first
// character, so it is safe on every platform the core runs on.
func isValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isValidLaunchProfile reports whether the value is a portable launch
// profile identifier: 1..64 ASCII bytes, the first a lowercase letter and
// the rest lowercase letters, digits, underscore or dash. Empty is valid
// (the unknown profile, which keeps the session-only request). Kind and
// model support is never judged here: that stays the shared kinds layer's
// later responsibility.
func isValidLaunchProfile(profile string) bool {
	if profile == "" {
		return true
	}
	if len(profile) > 64 {
		return false
	}
	for i := 0; i < len(profile); i++ {
		c := profile[i]
		if i == 0 {
			if c < 'a' || c > 'z' {
				return false
			}
		} else if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// containsWhitespaceOrControl reports whether s contains any whitespace or control character.
func containsWhitespaceOrControl(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// validateRpcError strictly validates a JSON-RPC 2.0 error object:
// - code must be a finite integral number
// - message must be a string
// - data (if present and non-null) must be an object; every present kind and hint member must be a string (null members are invalid).
func validateRpcError(errObj *jsonjs.Object) (*Error, error) {
	rawCode, ok := errObj.Get("code")
	if !ok {
		return nil, errors.New("missing error code")
	}
	codeFloat, ok := rawCode.(float64)
	if !ok || math.IsNaN(codeFloat) || math.IsInf(codeFloat, 0) || codeFloat != math.Trunc(codeFloat) {
		return nil, errors.New("non-integral error code")
	}

	rawMsg, ok := errObj.Get("message")
	if !ok {
		return nil, errors.New("missing error message")
	}
	msgStr, ok := rawMsg.(string)
	if !ok {
		return nil, errors.New("non-string error message")
	}

	e := &Error{
		Message: msgStr,
		Exit:    0,
	}

	rawErrData, hasData := errObj.Get("data")
	if hasData && rawErrData != nil {
		dataObj, ok := rawErrData.(*jsonjs.Object)
		if !ok {
			return nil, errors.New("error data is not an object")
		}
		if rawKind, hasKind := dataObj.Get("kind"); hasKind {
			kindStr, ok := rawKind.(string)
			if !ok {
				return nil, errors.New("error data kind is not a string")
			}
			e.Kind = kindStr
		}
		if rawHint, hasHint := dataObj.Get("hint"); hasHint {
			hintStr, ok := rawHint.(string)
			if !ok {
				return nil, errors.New("error data hint is not a string")
			}
			e.Hint = hintStr
		}
	}
	if e.Kind == "" && e.Message != "" {
		e.Kind = e.Message
	}
	return e, nil
}

// validateHelloResponse strictly validates the hello response line invariants:
// valid single JSON object, jsonrpc == "2.0", exact numeric id == 1.0, exactly one of
// result or error, and exact generation == 1.0 when result is present.
func validateHelloResponse(line []byte) error {
	v, err := jsonjs.Parse(line)
	if err != nil {
		return errors.New("malformed JSON in hello response")
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return errors.New("hello response is not a JSON object")
	}
	rawRpc, ok := obj.Get("jsonrpc")
	if !ok {
		return errors.New("invalid or missing jsonrpc in hello response")
	}
	rpcStr, ok := rawRpc.(string)
	if !ok || rpcStr != "2.0" {
		return errors.New("invalid or missing jsonrpc in hello response")
	}
	rawId, ok := obj.Get("id")
	if !ok {
		return errors.New("missing id in hello response")
	}
	idFloat, ok := rawId.(float64)
	if !ok || idFloat != 1.0 {
		return errors.New("mismatched or non-numeric id in hello response")
	}

	rawErr, hasErr := obj.Get("error")
	rawRes, hasRes := obj.Get("result")
	if hasErr && hasRes {
		return errors.New("hello response has both result and error")
	}
	if !hasErr && !hasRes {
		return errors.New("hello response has neither result nor error")
	}
	if hasErr {
		errObj, ok := rawErr.(*jsonjs.Object)
		if !ok {
			return errors.New("malformed error object in hello response")
		}
		e, err := validateRpcError(errObj)
		if err != nil {
			return errors.New("malformed error object in hello response")
		}
		return e
	}

	resObj, ok := rawRes.(*jsonjs.Object)
	if !ok {
		return errors.New("hello result is not a JSON object")
	}
	rawGen, ok := resObj.Get("generation")
	if !ok {
		return errors.New("missing generation in hello result")
	}
	genFloat, ok := rawGen.(float64)
	if !ok {
		return errors.New("non-numeric generation in hello result")
	}
	if genFloat != 1.0 {
		return fmt.Errorf("unsupported protocol generation: %v", genFloat)
	}
	return nil
}

// parseRunResponse parses and validates the terminal.run response line:
// strictly validates jsonrpc == "2.0", exact numeric id == 2.0, genuine error or
// genuine non-coerced string address with no blanks, whitespace or control characters.
func parseRunResponse(line []byte) (string, error) {
	v, err := jsonjs.Parse(line)
	if err != nil {
		return "", wrapRunOutcomeUnknownMsg("malformed JSON in terminal.run response")
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return "", wrapRunOutcomeUnknownMsg("terminal.run response is not a JSON object")
	}
	rawRpc, ok := obj.Get("jsonrpc")
	if !ok {
		return "", wrapRunOutcomeUnknownMsg("invalid or missing jsonrpc in terminal.run response")
	}
	rpcStr, ok := rawRpc.(string)
	if !ok || rpcStr != "2.0" {
		return "", wrapRunOutcomeUnknownMsg("invalid or missing jsonrpc in terminal.run response")
	}
	rawId, ok := obj.Get("id")
	if !ok {
		return "", wrapRunOutcomeUnknownMsg("missing id in terminal.run response")
	}
	idFloat, ok := rawId.(float64)
	if !ok || idFloat != 2.0 {
		return "", wrapRunOutcomeUnknownMsg("mismatched or non-numeric id in terminal.run response")
	}

	rawErr, hasErr := obj.Get("error")
	rawRes, hasRes := obj.Get("result")
	if hasErr && hasRes {
		return "", wrapRunOutcomeUnknownMsg("terminal.run response has both result and error")
	}
	if !hasErr && !hasRes {
		return "", wrapRunOutcomeUnknownMsg("terminal.run response has neither result nor error")
	}
	if hasErr {
		errObj, ok := rawErr.(*jsonjs.Object)
		if !ok {
			return "", wrapRunOutcomeUnknownMsg("malformed error object in terminal.run response")
		}
		e, err := validateRpcError(errObj)
		if err != nil {
			return "", wrapRunOutcomeUnknownMsg("malformed error object in terminal.run response")
		}
		// A structured, genuine JSON-RPC error response from the core is an explicit rejection, not an unknown outcome.
		return "", e
	}

	resObj, ok := rawRes.(*jsonjs.Object)
	if !ok {
		return "", wrapRunOutcomeUnknownMsg("terminal.run result is not a JSON object")
	}
	rawAddr, ok := resObj.Get("address")
	if !ok {
		return "", wrapRunOutcomeUnknownMsg("missing address in terminal.run result")
	}
	addr, ok := rawAddr.(string)
	if !ok {
		return "", wrapRunOutcomeUnknownMsg("terminal.run result address is not a string")
	}
	if strings.TrimSpace(addr) == "" {
		return "", wrapRunOutcomeUnknownMsg("empty address in terminal.run result")
	}
	if containsWhitespaceOrControl(addr) {
		return "", wrapRunOutcomeUnknownMsg("terminal.run result address contains whitespace or control characters")
	}
	return addr, nil
}

// wrapRunOutcomeUnknown wraps an underlying cause with ErrRunOutcomeUnknown,
// preserving errors.Is for the cause.
func wrapRunOutcomeUnknown(cause error) error {
	if cause == nil {
		return ErrRunOutcomeUnknown
	}
	return fmt.Errorf("%w: %w", ErrRunOutcomeUnknown, cause)
}

// wrapRunOutcomeUnknownMsg wraps a message description with ErrRunOutcomeUnknown.
func wrapRunOutcomeUnknownMsg(msg string) error {
	return fmt.Errorf("%w: %s", ErrRunOutcomeUnknown, msg)
}

// readResponseLine reads a single newline-terminated line from reader, bounded
// to maxBytes. Empty lines are skipped.
func readResponseLine(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	for {
		var buf []byte
		for {
			line, isPrefix, err := reader.ReadLine()
			if len(line) > 0 {
				if len(buf)+len(line) > maxBytes {
					return nil, errors.New("response line exceeds maximum allowed size")
				}
				buf = append(buf, line...)
			}
			if err != nil {
				if len(buf) > 0 && err == io.EOF {
					break
				}
				return nil, err
			}
			if !isPrefix {
				break
			}
		}
		trimmed := bytes.TrimSpace(buf)
		if len(trimmed) == 0 {
			continue
		}
		return trimmed, nil
	}
}
