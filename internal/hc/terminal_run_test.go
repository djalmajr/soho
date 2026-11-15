package hc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func init() {
	if os.Getenv("SOHO_TEST_HC_HELPER") == "1" {
		runTestHcHelper()
	}
}

func runTestHcHelper() {
	mode := os.Getenv("SOHO_HELPER_MODE")
	reader := bufio.NewReader(os.Stdin)

	// Read hello line
	_, err := reader.ReadString('\n')
	if err != nil {
		os.Exit(1)
	}

	if mode == "refuse_hello" {
		fmt.Println(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unsupported_generation","data":{"kind":"unsupported_generation"}}}`)
		_, _ = io.ReadAll(reader)
		os.Exit(0)
	}

	// Output valid hello
	fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"generation":1,"core":{"id":"test","name":"testcore"},"features":[],"methods":[]}}`)

	// Read terminal.run line
	_, err = reader.ReadString('\n')
	if err != nil {
		os.Exit(1)
	}

	switch mode {
	case "eof_after_run":
		// Close stdout and exit immediately without reply
		os.Exit(0)
	case "hang":
		// Hang until killed/signaled
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		// Normal success
		fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_native_glue_42"}}`)
		// Wait for stdin EOF
		_, _ = io.ReadAll(reader)
		os.Exit(0)
	}
}

type fakeScriptConn struct {
	clientInR  *io.PipeReader
	clientInW  *io.PipeWriter
	clientOutR *io.PipeReader
	clientOutW *io.PipeWriter

	mu        sync.Mutex
	inClosed  bool
	reapCount int
	reapErr   error
	written   bytes.Buffer
}

type trackingWriteCloser struct {
	w  io.WriteCloser
	fc *fakeScriptConn
}

func (t *trackingWriteCloser) Write(p []byte) (int, error) {
	t.fc.mu.Lock()
	t.fc.written.Write(p)
	t.fc.mu.Unlock()
	return t.w.Write(p)
}

func (t *trackingWriteCloser) Close() error {
	t.fc.mu.Lock()
	t.fc.inClosed = true
	t.fc.mu.Unlock()
	return t.w.Close()
}

func newFakeScriptConn() *fakeScriptConn {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	return &fakeScriptConn{
		clientInR:  inR,
		clientInW:  inW,
		clientOutR: outR,
		clientOutW: outW,
	}
}

func (fc *fakeScriptConn) Connect(ctx context.Context, _ platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
	stopCtx := make(chan struct{})
	var closeOnce sync.Once
	go func() {
		select {
		case <-ctx.Done():
			_ = fc.clientOutW.CloseWithError(ctx.Err())
			_ = fc.clientInR.CloseWithError(ctx.Err())
		case <-stopCtx:
		}
	}()

	reap := func() error {
		closeOnce.Do(func() {
			close(stopCtx)
		})
		fc.mu.Lock()
		fc.reapCount++
		err := fc.reapErr
		fc.mu.Unlock()
		return err
	}
	return &trackingWriteCloser{w: fc.clientInW, fc: fc}, fc.clientOutR, reap, nil
}

func (fc *fakeScriptConn) getWritten() []byte {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	cp := make([]byte, fc.written.Len())
	copy(cp, fc.written.Bytes())
	return cp
}

func (fc *fakeScriptConn) isInClosed() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.inClosed
}

func (fc *fakeScriptConn) getReapCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.reapCount
}

func validTerminalRunInput() TerminalRun {
	return TerminalRun{
		Name:    "worker-1",
		Kind:    "echo",
		Program: "/bin/echo",
		Cwd:     "/tmp",
		Caller:  "t0",
		Session: "sess-abc",
		Args:    []string{"hello", "world"},
	}
}

func TestRunTerminal_Validation(t *testing.T) {
	connectCalled := false
	c := &Client{
		Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
			connectCalled = true
			return nil, nil, nil, errors.New("should not be called")
		},
	}

	tests := []struct {
		name    string
		modify  func(*TerminalRun)
		wantErr string
	}{
		{"empty name", func(r *TerminalRun) { r.Name = "" }, "name is required"},
		{"whitespace name", func(r *TerminalRun) { r.Name = "   " }, "name is required"},
		{"invalid utf8 name", func(r *TerminalRun) { r.Name = string([]byte{0xff, 0xfe}) }, "name contains invalid UTF-8"},
		{"empty kind", func(r *TerminalRun) { r.Kind = "" }, "kind is required"},
		{"whitespace kind", func(r *TerminalRun) { r.Kind = "\t" }, "kind is required"},
		{"invalid utf8 kind", func(r *TerminalRun) { r.Kind = string([]byte{0x80, 0x81}) }, "kind contains invalid UTF-8"},
		{"empty program", func(r *TerminalRun) { r.Program = "" }, "program is required"},
		{"whitespace program", func(r *TerminalRun) { r.Program = "   " }, "program is required"},
		{"invalid utf8 program", func(r *TerminalRun) { r.Program = string([]byte{0x80, 0x81}) }, "program contains invalid UTF-8"},
		{"empty cwd", func(r *TerminalRun) { r.Cwd = "" }, "cwd is required"},
		{"whitespace cwd", func(r *TerminalRun) { r.Cwd = "  " }, "cwd is required"},
		{"invalid utf8 cwd", func(r *TerminalRun) { r.Cwd = string([]byte{0xf0, 0x28, 0x8c, 0x28}) }, "cwd contains invalid UTF-8"},
		{"empty caller", func(r *TerminalRun) { r.Caller = "" }, "caller is required"},
		{"whitespace caller", func(r *TerminalRun) { r.Caller = " " }, "caller is required"},
		{"invalid utf8 caller", func(r *TerminalRun) { r.Caller = string([]byte{0xc0, 0xaf}) }, "caller contains invalid UTF-8"},
		{"empty session", func(r *TerminalRun) { r.Session = "" }, "session is required"},
		{"whitespace session", func(r *TerminalRun) { r.Session = " \n" }, "session is required"},
		{"invalid utf8 session", func(r *TerminalRun) { r.Session = string([]byte{0xed, 0xa0, 0x80}) }, "session contains invalid UTF-8"},
		{"invalid utf8 in args", func(r *TerminalRun) { r.Args = []string{"ok", string([]byte{0xff})} }, "contains invalid UTF-8"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := validTerminalRunInput()
			tc.modify(&input)
			connectCalled = false

			_, err := c.RunTerminal(context.Background(), input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
			if connectCalled {
				t.Fatalf("connect should not be called when input validation fails")
			}
		})
	}
}

func TestRunTerminal_MissingOrInvalidProgramNoConnect(t *testing.T) {
	connectCalled := false
	c := &Client{
		Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
			connectCalled = true
			return nil, nil, nil, errors.New("unexpected connect invocation")
		},
	}

	for _, invalidProgram := range []struct {
		desc    string
		program string
		wantErr string
	}{
		{"omitted/empty", "", "program is required"},
		{"whitespace", "   \t  ", "program is required"},
		{"invalid utf8", string([]byte{0xff, 0xfe}), "program contains invalid UTF-8"},
	} {
		t.Run(invalidProgram.desc, func(t *testing.T) {
			connectCalled = false
			input := validTerminalRunInput()
			input.Program = invalidProgram.program

			addr, err := c.RunTerminal(context.Background(), input)
			if addr != "" {
				t.Fatalf("expected empty address, got %q", addr)
			}
			if err == nil {
				t.Fatalf("expected error for program %q, got nil", invalidProgram.desc)
			}
			if !strings.Contains(err.Error(), invalidProgram.wantErr) {
				t.Fatalf("expected error containing %q, got %q", invalidProgram.wantErr, err.Error())
			}
			if connectCalled {
				t.Fatalf("Connect seam was called despite invalid program")
			}
			if errors.Is(err, ErrRunOutcomeUnknown) {
				t.Fatalf("preflight validation failure must not wrap ErrRunOutcomeUnknown")
			}
		})
	}
}

func TestRunTerminal_NilConnect(t *testing.T) {
	launchCalled := false
	c := &Client{
		Env: platform.Env{"PATH": t.TempDir()},
		Launch: func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			launchCalled = true
			return platform.RunResult{}
		},
		Connect: nil,
	}

	_, err := c.RunTerminal(context.Background(), validTerminalRunInput())
	if err == nil {
		t.Fatalf("expected error on nil Connect seam, got nil")
	}
	if !strings.Contains(err.Error(), "client connect seam is not configured") {
		t.Fatalf("expected configuration error, got: %v", err)
	}
	if errors.Is(err, ErrRunOutcomeUnknown) {
		t.Fatalf("preflight refusal must not wrap ErrRunOutcomeUnknown")
	}
	if launchCalled {
		t.Fatalf("launch must not be called on nil Connect preflight refusal")
	}
}

func TestRunTerminal_HelloBeforeRun(t *testing.T) {
	fc := newFakeScriptConn()
	c := &Client{Connect: fc.Connect}

	go func() {
		r := bufio.NewReader(fc.clientInR)
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.Contains(line, `"method":"hello"`) {
			return
		}
		// Core rejects hello
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unsupported_generation","data":{"kind":"unsupported_generation"}}}` + "\n"))
	}()

	addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
	if addr != "" {
		t.Fatalf("expected empty address, got %q", addr)
	}
	if err == nil {
		t.Fatalf("expected hello error, got nil")
	}
	var hcErr *Error
	if !errors.As(err, &hcErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if hcErr.Kind != "unsupported_generation" {
		t.Fatalf("expected kind unsupported_generation, got %q", hcErr.Kind)
	}
	if errors.Is(err, ErrRunOutcomeUnknown) {
		t.Fatalf("hello rejection must not wrap ErrRunOutcomeUnknown")
	}

	// Verify no terminal.run bytes were written
	written := string(fc.getWritten())
	if strings.Contains(written, "terminal.run") {
		t.Fatalf("terminal.run was written after hello rejection: %s", written)
	}
	if !fc.isInClosed() {
		t.Fatalf("stdin pipe must be closed")
	}
	if fc.getReapCount() != 1 {
		t.Fatalf("reap must be called exactly once, got %d", fc.getReapCount())
	}
}

func TestRunTerminal_HelloStrictValidation(t *testing.T) {
	cases := []struct {
		name          string
		helloResponse string
		wantErr       string
	}{
		{"not JSON", "not-json\n", "malformed JSON"},
		{"not object", `[1, 2, 3]` + "\n", "not a JSON object"},
		{"wrong jsonrpc version", `{"jsonrpc":"1.0","id":1,"result":{"generation":1}}` + "\n", "invalid or missing jsonrpc"},
		{"missing jsonrpc", `{"id":1,"result":{"generation":1}}` + "\n", "invalid or missing jsonrpc"},
		{"missing id", `{"jsonrpc":"2.0","result":{"generation":1}}` + "\n", "missing id"},
		{"wrong id", `{"jsonrpc":"2.0","id":2,"result":{"generation":1}}` + "\n", "mismatched or non-numeric id"},
		{"string id", `{"jsonrpc":"2.0","id":"1","result":{"generation":1}}` + "\n", "mismatched or non-numeric id"},
		{"both result and error", `{"jsonrpc":"2.0","id":1,"result":{"generation":1},"error":{"code":-32000,"message":"e"}}` + "\n", "both result and error"},
		{"neither result nor error", `{"jsonrpc":"2.0","id":1}` + "\n", "neither result nor error"},
		{"result not object", `{"jsonrpc":"2.0","id":1,"result":"ok"}` + "\n", "not a JSON object"},
		{"missing generation", `{"jsonrpc":"2.0","id":1,"result":{"methods":[]}}` + "\n", "missing generation"},
		{"generation 2", `{"jsonrpc":"2.0","id":1,"result":{"generation":2}}` + "\n", "unsupported protocol generation: 2"},
		{"generation 0", `{"jsonrpc":"2.0","id":1,"result":{"generation":0}}` + "\n", "unsupported protocol generation: 0"},
		{"string generation", `{"jsonrpc":"2.0","id":1,"result":{"generation":"1"}}` + "\n", "non-numeric generation"},
		{"fractional id 1.9", `{"jsonrpc":"2.0","id":1.9,"result":{"generation":1}}` + "\n", "mismatched or non-numeric id"},
		{"fractional generation 1.9", `{"jsonrpc":"2.0","id":1,"result":{"generation":1.9}}` + "\n", "unsupported protocol generation"},
		{"empty error", `{"jsonrpc":"2.0","id":1,"error":{}}` + "\n", "malformed error object in hello response"},
		{"error missing code", `{"jsonrpc":"2.0","id":1,"error":{"message":"fail"}}` + "\n", "malformed error object in hello response"},
		{"error non-integral code", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000.5,"message":"fail"}}` + "\n", "malformed error object in hello response"},
		{"error missing message", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000}}` + "\n", "malformed error object in hello response"},
		{"error composite message", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":{"leak":"SECRET_HELLO_MSG"}}}` + "\n", "malformed error object in hello response"},
		{"error data null kind", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"fail","data":{"kind":null}}}` + "\n", "malformed error object in hello response"},
		{"error data null hint", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"fail","data":{"hint":null}}}` + "\n", "malformed error object in hello response"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeScriptConn()
			c := &Client{Connect: fc.Connect}

			go func() {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(tc.helloResponse))
				_ = fc.clientOutW.Close()
				_, _ = io.Copy(io.Discard, fc.clientInR)
			}()

			_, err := c.RunTerminal(context.Background(), validTerminalRunInput())
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
			if errors.Is(err, ErrRunOutcomeUnknown) {
				t.Fatalf("hello validation failure must not wrap ErrRunOutcomeUnknown")
			}
			written := string(fc.getWritten())
			if strings.Contains(written, "terminal.run") {
				t.Fatalf("terminal.run must not be written on hello validation failure")
			}
			if !fc.isInClosed() {
				t.Fatalf("stdin must be closed")
			}
			if fc.getReapCount() != 1 {
				t.Fatalf("reap must be called once")
			}
		})
	}
}

func TestRunTerminal_ExactParamsAndBytePreserving(t *testing.T) {
	fc := newFakeScriptConn()
	c := &Client{Connect: fc.Connect}

	input := TerminalRun{
		Name:    "worker-special",
		Kind:    "custom-agent",
		Program: "/opt/bin/custom-agent",
		Cwd:     "/path/to/project",
		Caller:  "t10",
		Session: "sess-xyz-99",
		Args:    []string{"<input.txt", "a&b", "café", "日本語"},
	}

	var capturedRun []byte
	done := make(chan struct{})

	go func() {
		defer close(done)
		r := bufio.NewReader(fc.clientInR)
		// Read hello
		_, _ = r.ReadString('\n')
		// Send hello reply
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1,"core":{"name":"core1"}}}` + "\n"))

		// Read terminal.run
		runBytes, err := r.ReadBytes('\n')
		if err == nil {
			capturedRun = runBytes
			// Send terminal.run reply
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t42"}}` + "\n"))
		}
	}()

	addr, err := c.RunTerminal(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "t42" {
		t.Fatalf("expected address t42, got %q", addr)
	}

	<-done

	// Verify byte-preserving UTF-8: raw bytes must contain exact characters, not \u003c or \u0026
	runStr := string(capturedRun)
	if !strings.Contains(runStr, "<input.txt") {
		t.Fatalf("expected raw '<input.txt' in run payload, got: %s", runStr)
	}
	if strings.Contains(runStr, `\u003c`) {
		t.Fatalf("payload escaped '<' to '\\u003c': %s", runStr)
	}
	if !strings.Contains(runStr, "a&b") {
		t.Fatalf("expected raw 'a&b' in run payload, got: %s", runStr)
	}
	if strings.Contains(runStr, `\u0026`) {
		t.Fatalf("payload escaped '&' to '\\u0026': %s", runStr)
	}
	if !strings.Contains(runStr, "café") || !strings.Contains(runStr, "日本語") {
		t.Fatalf("payload did not preserve UTF-8 runes: %s", runStr)
	}

	// Verify exact JSON structure
	var parsed map[string]any
	if err := json.Unmarshal(capturedRun, &parsed); err != nil {
		t.Fatalf("failed to unmarshal run payload: %v", err)
	}

	if parsed["jsonrpc"] != "2.0" {
		t.Fatalf("expected jsonrpc 2.0, got %v", parsed["jsonrpc"])
	}
	if parsed["id"] != float64(2) {
		t.Fatalf("expected id 2, got %v", parsed["id"])
	}
	if parsed["method"] != "terminal.run" {
		t.Fatalf("expected method terminal.run, got %v", parsed["method"])
	}

	params, ok := parsed["params"].(map[string]any)
	if !ok {
		t.Fatalf("params is not an object: %v", parsed["params"])
	}

	if params["name"] != "worker-special" {
		t.Fatalf("expected name worker-special, got %v", params["name"])
	}
	if params["cwd"] != "/path/to/project" {
		t.Fatalf("expected cwd /path/to/project, got %v", params["cwd"])
	}
	if params["caller"] != "t10" {
		t.Fatalf("expected caller t10, got %v", params["caller"])
	}
	if params["wait"] != false {
		t.Fatalf("expected wait false, got %v", params["wait"])
	}
	if params["no_notify"] != true {
		t.Fatalf("expected no_notify true, got %v", params["no_notify"])
	}
	if params["outlive_parent"] != true {
		t.Fatalf("expected outlive_parent true, got %v", params["outlive_parent"])
	}
	if _, hasParent := params["parent"]; hasParent {
		t.Fatalf("parent must not be set, got %v", params["parent"])
	}

	// Verify argv: [Program] + Args
	argvRaw, ok := params["argv"].([]any)
	if !ok {
		t.Fatalf("argv is not a slice: %v", params["argv"])
	}
	expectedArgv := []string{"/opt/bin/custom-agent", "<input.txt", "a&b", "café", "日本語"}
	if len(argvRaw) != len(expectedArgv) {
		t.Fatalf("expected argv len %d, got %d", len(expectedArgv), len(argvRaw))
	}
	for i, exp := range expectedArgv {
		if argvRaw[i] != exp {
			t.Fatalf("argv[%d]: expected %q, got %v", i, exp, argvRaw[i])
		}
	}

	// Verify labels: exactly {soho.session:Session}
	labels, ok := params["labels"].(map[string]any)
	if !ok {
		t.Fatalf("labels is not an object: %v", params["labels"])
	}
	if labels["soho.session"] != "sess-xyz-99" || len(labels) != 1 {
		t.Fatalf("expected labels {soho.session:sess-xyz-99}, got %v", labels)
	}

	// Verify env: exactly {SOHO_TRANSPORT:hc}
	envObj, ok := params["env"].(map[string]any)
	if !ok {
		t.Fatalf("env is not an object: %v", params["env"])
	}
	if envObj["SOHO_TRANSPORT"] != "hc" || len(envObj) != 1 {
		t.Fatalf("expected env exactly {SOHO_TRANSPORT:hc}, got %v", envObj)
	}
}

func TestRunTerminal_EnvOverridesMergedIntoCapturedRPC(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
		wantEnv   map[string]any
	}{
		{
			name: "valid overrides merge into the captured env",
			overrides: map[string]string{
				"OPENCODE_DISABLE_AUTOUPDATE": "1",
				"OPENCODE_CONFIG_CONTENT":     `{"model":"synthetic/provider-a/model-1"}`,
			},
			wantEnv: map[string]any{
				"SOHO_TRANSPORT":              "hc",
				"OPENCODE_DISABLE_AUTOUPDATE": "1",
				"OPENCODE_CONFIG_CONTENT":     `{"model":"synthetic/provider-a/model-1"}`,
			},
		},
		{
			name:      "nil overrides preserve the exact zero-override request",
			overrides: nil,
			wantEnv:   map[string]any{"SOHO_TRANSPORT": "hc"},
		},
		{
			name:      "empty non-nil overrides preserve the exact zero-override request",
			overrides: map[string]string{},
			wantEnv:   map[string]any{"SOHO_TRANSPORT": "hc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeScriptConn()
			c := &Client{Connect: fc.Connect}

			input := validTerminalRunInput()
			input.EnvOverrides = tc.overrides

			var capturedRun []byte
			done := make(chan struct{})
			go func() {
				defer close(done)
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
				runBytes, err := r.ReadBytes('\n')
				if err == nil {
					capturedRun = runBytes
					_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_env"}}` + "\n"))
				}
			}()

			addr, err := c.RunTerminal(context.Background(), input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if addr != "t_env" {
				t.Fatalf("expected address t_env, got %q", addr)
			}
			<-done

			var parsed map[string]any
			if err := json.Unmarshal(capturedRun, &parsed); err != nil {
				t.Fatalf("failed to unmarshal captured terminal.run payload: %v", err)
			}
			params, ok := parsed["params"].(map[string]any)
			if !ok {
				t.Fatalf("params is not an object: %v", parsed["params"])
			}
			envObj, ok := params["env"].(map[string]any)
			if !ok {
				t.Fatalf("env is not an object: %v", params["env"])
			}
			if len(envObj) != len(tc.wantEnv) {
				t.Fatalf("captured env must be exactly %v, got %v", tc.wantEnv, envObj)
			}
			for name, value := range tc.wantEnv {
				if envObj[name] != value {
					t.Fatalf("captured env[%q]: expected %v, got %v", name, value, envObj[name])
				}
			}
		})
	}
}

func TestRunTerminal_EnvOverridesPreConnectRefusals(t *testing.T) {
	connectCalled := false
	c := &Client{
		Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
			connectCalled = true
			return nil, nil, nil, errors.New("should not be called")
		},
	}

	cases := []struct {
		name      string
		overrides map[string]string
		wantErr   error
	}{
		{"reserved SOHO_ prefix", map[string]string{"SOHO_SESSION_ID": "x"}, errEnvNameReserved},
		{"reserved lowercase soho_ prefix", map[string]string{"soho_transport": "x"}, errEnvNameReserved},
		{"reserved lowercase hc_ prefix", map[string]string{"hc_pane_id": "x"}, errEnvNameReserved},
		{"reserved mixed-case Herdr_ prefix", map[string]string{"Herdr_Env": "x"}, errEnvNameReserved},
		{"invalid empty name", map[string]string{"": "x"}, errEnvNameInvalid},
		{"invalid digit-first name", map[string]string{"9NAME": "x"}, errEnvNameInvalid},
		{"invalid dash name", map[string]string{"A-B": "x"}, errEnvNameInvalid},
		{"invalid dot name", map[string]string{"A.B": "x"}, errEnvNameInvalid},
		{"NUL in value", map[string]string{"GOOD_NAME": "a\x00b"}, errEnvValueNUL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connectCalled = false
			input := validTerminalRunInput()
			input.EnvOverrides = tc.overrides

			_, err := c.RunTerminal(context.Background(), input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			// The fixed value-free message: exact equality proves no
			// supplied name or value reached the error channel.
			if err.Error() != tc.wantErr.Error() {
				t.Fatalf("expected the fixed message %q, got %q", tc.wantErr.Error(), err.Error())
			}
			if connectCalled {
				t.Fatalf("Connect must not be called for a refused override")
			}
			if errors.Is(err, ErrRunOutcomeUnknown) {
				t.Fatalf("preflight refusal must not wrap ErrRunOutcomeUnknown")
			}
		})
	}
}

// Mutation captured: re-reading input.EnvOverrides after Connect would
// serialize whatever a synchronous Connect seam injected into the caller's
// map; the per-call snapshot keeps the captured request exact.
func TestRunTerminal_EnvOverridesSynchronousConnectMutationNotInRequest(t *testing.T) {
	fc := newFakeScriptConn()
	input := validTerminalRunInput()
	input.EnvOverrides = map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "1"}

	c := &Client{
		Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
			// Synchronous seam mutation of the original map while
			// Connect is in flight: reserved routing/identity names
			// injected and the valid entry rewritten.
			input.EnvOverrides["HC_IDENTITY"] = "injected"
			input.EnvOverrides["SOHO_TRANSPORT"] = "forged"
			input.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"] = "0"
			return fc.Connect(ctx, env)
		},
	}

	var capturedRun []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(fc.clientInR)
		_, _ = r.ReadString('\n')
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
		runBytes, err := r.ReadBytes('\n')
		if err == nil {
			capturedRun = runBytes
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_snap"}}` + "\n"))
		}
	}()

	addr, err := c.RunTerminal(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "t_snap" {
		t.Fatalf("expected address t_snap, got %q", addr)
	}
	<-done

	// The seam did mutate the caller's map...
	if input.EnvOverrides["HC_IDENTITY"] != "injected" || input.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"] != "0" {
		t.Fatalf("precondition: the Connect seam did not mutate the caller map: %v", input.EnvOverrides)
	}

	// ...and the serialized request carries only the pre-Connect snapshot.
	var parsed map[string]any
	if err := json.Unmarshal(capturedRun, &parsed); err != nil {
		t.Fatalf("failed to unmarshal captured terminal.run payload: %v", err)
	}
	params, ok := parsed["params"].(map[string]any)
	if !ok {
		t.Fatalf("params is not an object: %v", parsed["params"])
	}
	envObj, ok := params["env"].(map[string]any)
	if !ok {
		t.Fatalf("env is not an object: %v", params["env"])
	}
	if len(envObj) != 2 {
		t.Fatalf("captured env must be exactly the snapshot plus the transport marker, got %v", envObj)
	}
	if envObj["SOHO_TRANSPORT"] != "hc" {
		t.Fatalf("captured SOHO_TRANSPORT must stay hc, got %v", envObj["SOHO_TRANSPORT"])
	}
	if envObj["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Fatalf("captured override must keep the validated snapshot value, got %v", envObj["OPENCODE_DISABLE_AUTOUPDATE"])
	}
	if _, has := envObj["HC_IDENTITY"]; has {
		t.Fatalf("injected reserved name must not reach the captured request: %v", envObj)
	}
}

func TestRunTerminal_EnvOverridesCallerMapNotMutated(t *testing.T) {
	fc := newFakeScriptConn()
	c := &Client{Connect: fc.Connect}

	// A separate pre-call expected map that does not alias the caller
	// map: an in-place value edit cannot hide from the comparison.
	expected := map[string]string{
		"OPENCODE_DISABLE_AUTOUPDATE": "1",
		"OPENCODE_CONFIG_CONTENT":     `{"model":"prov/model"}`,
	}
	original := map[string]string{
		"OPENCODE_DISABLE_AUTOUPDATE": "1",
		"OPENCODE_CONFIG_CONTENT":     `{"model":"prov/model"}`,
	}
	input := validTerminalRunInput()
	input.EnvOverrides = original

	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(fc.clientInR)
		_, _ = r.ReadString('\n')
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
		_, _ = r.ReadString('\n')
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_mut"}}` + "\n"))
	}()

	if _, err := c.RunTerminal(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-done

	if len(input.EnvOverrides) != len(expected) {
		t.Fatalf("caller map must not gain or lose entries: %v", input.EnvOverrides)
	}
	for name, value := range expected {
		got, ok := input.EnvOverrides[name]
		if !ok {
			t.Fatalf("caller map lost entry %q: %v", name, input.EnvOverrides)
		}
		if got != value {
			t.Fatalf("caller map entry changed: %q %q -> %q", name, value, got)
		}
	}
}

// The launch profile label at the actual captured RPC: a validated
// nonempty portable profile adds exactly the soho.launch_profile label to
// the existing session label in the serialized terminal.run request, an
// empty profile keeps the exact old session-only request, and every other
// value is refused before Connect with the fixed value-free error (no run
// request, no unknown-create classification). Domain identifiers are
// intentionally ASCII; Unicode environment values keep their established
// behavior untouched.
func TestRunTerminal_LaunchProfile(t *testing.T) {
	valid := []struct {
		name    string
		profile string
	}{
		{"synthetic portable profile", "opencode-primary-1"},
		{"64-byte boundary profile", "p" + strings.Repeat("a", 63)},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeScriptConn()
			c := &Client{Connect: fc.Connect}

			input := validTerminalRunInput()
			input.LaunchProfile = tc.profile

			var capturedRun []byte
			done := make(chan struct{})
			go func() {
				defer close(done)
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
				runBytes, err := r.ReadBytes('\n')
				if err == nil {
					capturedRun = runBytes
					_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_profile"}}` + "\n"))
				}
			}()

			addr, err := c.RunTerminal(context.Background(), input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if addr != "t_profile" {
				t.Fatalf("expected address t_profile, got %q", addr)
			}
			<-done

			// The external serialized request carries the exact label pair.
			if !bytes.Contains(capturedRun, []byte(`"soho.session":"sess-abc"`)) {
				t.Fatalf("captured payload must carry the exact session label: %s", capturedRun)
			}
			if !bytes.Contains(capturedRun, []byte(`"soho.launch_profile":"`+tc.profile+`"`)) {
				t.Fatalf("captured payload must carry the exact profile label: %s", capturedRun)
			}

			var parsed map[string]any
			if err := json.Unmarshal(capturedRun, &parsed); err != nil {
				t.Fatalf("failed to unmarshal captured terminal.run payload: %v", err)
			}
			if parsed["jsonrpc"] != "2.0" || parsed["id"] != float64(2) || parsed["method"] != "terminal.run" {
				t.Fatalf("captured envelope changed: jsonrpc=%v id=%v method=%v", parsed["jsonrpc"], parsed["id"], parsed["method"])
			}
			params, ok := parsed["params"].(map[string]any)
			if !ok {
				t.Fatalf("params is not an object: %v", parsed["params"])
			}
			labels, ok := params["labels"].(map[string]any)
			if !ok {
				t.Fatalf("labels is not an object: %v", params["labels"])
			}
			if len(labels) != 2 || labels["soho.session"] != "sess-abc" || labels["soho.launch_profile"] != tc.profile {
				t.Fatalf("captured labels must be exactly {soho.session:sess-abc soho.launch_profile:%s}, got %v", tc.profile, labels)
			}
			// Every other serialized field stays unchanged by the profile.
			if params["name"] != "worker-1" || params["caller"] != "t0" || params["cwd"] != "/tmp" {
				t.Fatalf("captured identity fields changed: %v", params)
			}
			argv, ok := params["argv"].([]any)
			if !ok || len(argv) != 3 || argv[0] != "/bin/echo" || argv[1] != "hello" || argv[2] != "world" {
				t.Fatalf("captured argv changed: %v", params["argv"])
			}
			envObj, ok := params["env"].(map[string]any)
			if !ok || len(envObj) != 1 || envObj["SOHO_TRANSPORT"] != "hc" {
				t.Fatalf("captured env must stay exactly {SOHO_TRANSPORT:hc}: %v", envObj)
			}
			if params["wait"] != false || params["no_notify"] != true || params["outlive_parent"] != true {
				t.Fatalf("captured flags changed: %v", params)
			}
		})
	}

	t.Run("empty profile keeps the exact session-only label and the old zero request", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		input := validTerminalRunInput()

		var capturedRun []byte
		done := make(chan struct{})
		go func() {
			defer close(done)
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
			runBytes, err := r.ReadBytes('\n')
			if err == nil {
				capturedRun = runBytes
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_profile0"}}` + "\n"))
			}
		}()

		addr, err := c.RunTerminal(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if addr != "t_profile0" {
			t.Fatalf("expected address t_profile0, got %q", addr)
		}
		<-done

		// The label set is exactly the old session-only map: no profile
		// label key appears anywhere in the serialized request.
		if bytes.Contains(capturedRun, []byte("soho.launch_profile")) {
			t.Fatalf("empty profile must not emit the profile label: %s", capturedRun)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedRun, &parsed); err != nil {
			t.Fatalf("failed to unmarshal captured terminal.run payload: %v", err)
		}
		params, ok := parsed["params"].(map[string]any)
		if !ok {
			t.Fatalf("params is not an object: %v", parsed["params"])
		}
		labels, ok := params["labels"].(map[string]any)
		if !ok {
			t.Fatalf("labels is not an object: %v", params["labels"])
		}
		if len(labels) != 1 || labels["soho.session"] != "sess-abc" {
			t.Fatalf("captured labels must be exactly {soho.session:sess-abc}, got %v", labels)
		}
		if params["name"] != "worker-1" || params["caller"] != "t0" || params["cwd"] != "/tmp" ||
			params["wait"] != false || params["no_notify"] != true || params["outlive_parent"] != true {
			t.Fatalf("captured zero-request fields changed: %v", params)
		}
		argv, ok := params["argv"].([]any)
		if !ok || len(argv) != 3 || argv[0] != "/bin/echo" || argv[1] != "hello" || argv[2] != "world" {
			t.Fatalf("captured argv changed: %v", params["argv"])
		}
		envObj, ok := params["env"].(map[string]any)
		if !ok || len(envObj) != 1 || envObj["SOHO_TRANSPORT"] != "hc" {
			t.Fatalf("captured env must stay exactly {SOHO_TRANSPORT:hc}: %v", envObj)
		}
	})

	t.Run("invalid profile names are refused before Connect with the fixed value-free error", func(t *testing.T) {
		connectCalled := false
		c := &Client{
			Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
				connectCalled = true
				return nil, nil, nil, errors.New("should not be called")
			},
		}

		cases := []struct {
			name  string
			value string
		}{
			{"NUL", "a\x00b"},
			{"control tab", "a\tb"},
			{"unicode", "caf\u00e9"},
			{"raw invalid UTF-8", string([]byte{0xff, 0xfe})},
			{"uppercase", "Opencode"},
			{"leading digit", "9profile"},
			{"leading underscore", "_profile"},
			{"period", "a.b"},
			{"65 bytes over the 64 bound", strings.Repeat("a", 65)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				connectCalled = false
				input := validTerminalRunInput()
				input.LaunchProfile = tc.value

				_, err := c.RunTerminal(context.Background(), input)
				if !errors.Is(err, errLaunchProfileInvalid) {
					t.Fatalf("expected the fixed launch profile refusal, got %v", err)
				}
				// Exact fixed message: no supplied name can reach the error.
				if err.Error() != errLaunchProfileInvalid.Error() {
					t.Fatalf("expected the fixed message %q, got %q", errLaunchProfileInvalid.Error(), err.Error())
				}
				if connectCalled {
					t.Fatalf("Connect must not be called for a refused profile name")
				}
				if errors.Is(err, ErrRunOutcomeUnknown) {
					t.Fatalf("preflight refusal must not wrap ErrRunOutcomeUnknown")
				}
			})
		}
	})
}

// The encoding boundary at the actual captured RPC: malformed UTF-8 values
// are refused on the owned snapshot before Connect with a fixed value-free
// error that never echoes the input, while valid Unicode and a literal
// U+FFFD value reach the serialized terminal.run env byte-exactly.
func TestRunTerminal_EnvOverridesEncodingBoundary(t *testing.T) {
	t.Run("malformed value refused before Connect without echoing the input", func(t *testing.T) {
		connectCalled := false
		c := &Client{
			Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
				connectCalled = true
				return nil, nil, nil, errors.New("should not be called")
			},
		}

		for _, malformed := range []string{
			"enc-boundary-marker\xff",
			"\x80lone-continuation-byte",
		} {
			connectCalled = false
			input := validTerminalRunInput()
			input.EnvOverrides = map[string]string{"ENC_BOUNDARY_VALUE": malformed}

			_, err := c.RunTerminal(context.Background(), input)
			if err == nil {
				t.Fatalf("expected refusal for malformed value, got nil")
			}
			if !errors.Is(err, errEnvValueInvalidUTF8) {
				t.Fatalf("expected the fixed invalid-UTF-8 refusal, got %v", err)
			}
			// Exact fixed message: no supplied value can reach the error.
			if err.Error() != errEnvValueInvalidUTF8.Error() {
				t.Fatalf("expected the fixed message %q, got %q", errEnvValueInvalidUTF8.Error(), err.Error())
			}
			// Synthetic non-echo marker: the marker text must never appear.
			if strings.Contains(err.Error(), "enc-boundary-marker") {
				t.Fatalf("error must not echo the supplied value: %q", err.Error())
			}
			if connectCalled {
				t.Fatalf("Connect must not be called for a malformed value")
			}
			if errors.Is(err, ErrRunOutcomeUnknown) {
				t.Fatalf("preflight refusal must not wrap ErrRunOutcomeUnknown")
			}
		}
	})

	t.Run("valid unicode reaches the captured env byte-exactly", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		utf8Value := "café ☕ 日本語"
		input := validTerminalRunInput()
		input.EnvOverrides = map[string]string{"ENC_UNICODE_VALUE": utf8Value}

		var capturedRun []byte
		done := make(chan struct{})
		go func() {
			defer close(done)
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
			runBytes, err := r.ReadBytes('\n')
			if err == nil {
				capturedRun = runBytes
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_utf8"}}` + "\n"))
			}
		}()

		addr, err := c.RunTerminal(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if addr != "t_utf8" {
			t.Fatalf("expected address t_utf8, got %q", addr)
		}
		<-done

		// The raw serialized payload must carry the exact UTF-8 bytes of
		// the value, not an escaped or rewritten form.
		if !bytes.Contains(capturedRun, []byte(utf8Value)) {
			t.Fatalf("captured payload did not carry the exact UTF-8 bytes of the value: %s", capturedRun)
		}

		var parsed map[string]any
		if err := json.Unmarshal(capturedRun, &parsed); err != nil {
			t.Fatalf("failed to unmarshal captured terminal.run payload: %v", err)
		}
		params, ok := parsed["params"].(map[string]any)
		if !ok {
			t.Fatalf("params is not an object: %v", parsed["params"])
		}
		envObj, ok := params["env"].(map[string]any)
		if !ok {
			t.Fatalf("env is not an object: %v", params["env"])
		}
		if envObj["SOHO_TRANSPORT"] != "hc" {
			t.Fatalf("captured SOHO_TRANSPORT must be hc, got %v", envObj["SOHO_TRANSPORT"])
		}
		if envObj["ENC_UNICODE_VALUE"] != utf8Value {
			t.Fatalf("captured value must be byte-exact: %v", envObj["ENC_UNICODE_VALUE"])
		}
		if len(envObj) != 2 {
			t.Fatalf("captured env must be exactly the marker plus the override, got %v", envObj)
		}
	})

	t.Run("literal U+FFFD reaches the captured env byte-exactly", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		// A literal replacement rune is valid UTF-8 and must survive the
		// boundary unchanged; the raw payload must contain its exact
		// EF BF BD bytes.
		fffdValue := "pre\uFFFDpost"
		input := validTerminalRunInput()
		input.EnvOverrides = map[string]string{"ENC_FFFD_VALUE": fffdValue}

		var capturedRun []byte
		done := make(chan struct{})
		go func() {
			defer close(done)
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
			runBytes, err := r.ReadBytes('\n')
			if err == nil {
				capturedRun = runBytes
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_fffd"}}` + "\n"))
			}
		}()

		addr, err := c.RunTerminal(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if addr != "t_fffd" {
			t.Fatalf("expected address t_fffd, got %q", addr)
		}
		<-done

		if !bytes.Contains(capturedRun, []byte(fffdValue)) {
			t.Fatalf("captured payload did not carry the literal U+FFFD bytes: %s", capturedRun)
		}

		var parsed map[string]any
		if err := json.Unmarshal(capturedRun, &parsed); err != nil {
			t.Fatalf("failed to unmarshal captured terminal.run payload: %v", err)
		}
		params, ok := parsed["params"].(map[string]any)
		if !ok {
			t.Fatalf("params is not an object: %v", parsed["params"])
		}
		envObj, ok := params["env"].(map[string]any)
		if !ok {
			t.Fatalf("env is not an object: %v", params["env"])
		}
		if envObj["SOHO_TRANSPORT"] != "hc" {
			t.Fatalf("captured SOHO_TRANSPORT must be hc, got %v", envObj["SOHO_TRANSPORT"])
		}
		if envObj["ENC_FFFD_VALUE"] != fffdValue {
			t.Fatalf("literal U+FFFD must be preserved byte-exactly, got %v", envObj["ENC_FFFD_VALUE"])
		}
		if len(envObj) != 2 {
			t.Fatalf("captured env must be exactly the marker plus the override, got %v", envObj)
		}
	})
}

func TestRunTerminal_ExplicitRunError(t *testing.T) {
	fc := newFakeScriptConn()
	c := &Client{Connect: fc.Connect}

	runCount := 0
	go func() {
		r := bufio.NewReader(fc.clientInR)
		_, _ = r.ReadString('\n')
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, "terminal.run") {
				runCount++
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"name_taken","data":{"kind":"name_taken","hint":"choose another"}}}` + "\n"))
			}
		}
	}()

	addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
	if addr != "" {
		t.Fatalf("expected empty address, got %q", addr)
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var hcErr *Error
	if !errors.As(err, &hcErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if hcErr.Kind != "name_taken" {
		t.Fatalf("expected kind name_taken, got %q", hcErr.Kind)
	}
	if hcErr.Hint != "choose another" {
		t.Fatalf("expected hint choose another, got %q", hcErr.Hint)
	}
	if errors.Is(err, ErrRunOutcomeUnknown) {
		t.Fatalf("explicit run error must not wrap ErrRunOutcomeUnknown")
	}
	if runCount != 1 {
		t.Fatalf("expected exactly 1 terminal.run attempt (no retry), got %d", runCount)
	}
}

func TestRunTerminal_RunMissingOrMalformedResult(t *testing.T) {
	cases := []struct {
		name        string
		runResponse string
		wantErrMsg  string
	}{
		{"missing address", `{"jsonrpc":"2.0","id":2,"result":{}}` + "\n", "missing address"},
		{"empty address", `{"jsonrpc":"2.0","id":2,"result":{"address":""}}` + "\n", "empty address"},
		{"whitespace address", `{"jsonrpc":"2.0","id":2,"result":{"address":"   "}}` + "\n", "empty address"},
		{"result not object", `{"jsonrpc":"2.0","id":2,"result":"t7"}` + "\n", "result is not a JSON object"},
		{"not JSON", `not-json` + "\n", "malformed JSON"},
		{"trailing JSON", `{"jsonrpc":"2.0","id":2,"result":{"address":"t7"}}{"trailing":true}` + "\n", "malformed JSON"},
		{"mismatched id", `{"jsonrpc":"2.0","id":99,"result":{"address":"t7"}}` + "\n", "mismatched or non-numeric id"},
		{"fractional run id 2.9", `{"jsonrpc":"2.0","id":2.9,"result":{"address":"t7"}}` + "\n", "mismatched or non-numeric id"},
		{"string id", `{"jsonrpc":"2.0","id":"2","result":{"address":"t7"}}` + "\n", "mismatched or non-numeric id"},
		{"number address", `{"jsonrpc":"2.0","id":2,"result":{"address":123}}` + "\n", "result address is not a string"},
		{"boolean false address", `{"jsonrpc":"2.0","id":2,"result":{"address":false}}` + "\n", "result address is not a string"},
		{"boolean true address", `{"jsonrpc":"2.0","id":2,"result":{"address":true}}` + "\n", "result address is not a string"},
		{"object address", `{"jsonrpc":"2.0","id":2,"result":{"address":{"id":"t1"}}}` + "\n", "result address is not a string"},
		{"array address", `{"jsonrpc":"2.0","id":2,"result":{"address":["t1"]}}` + "\n", "result address is not a string"},
		{"null address", `{"jsonrpc":"2.0","id":2,"result":{"address":null}}` + "\n", "result address is not a string"},
		{"whitespace-bearing address", `{"jsonrpc":"2.0","id":2,"result":{"address":"t 1"}}` + "\n", "address contains whitespace or control characters"},
		{"control-char address", `{"jsonrpc":"2.0","id":2,"result":{"address":"t1\u0000"}}` + "\n", "address contains whitespace or control characters"},
		{"empty error", `{"jsonrpc":"2.0","id":2,"error":{}}` + "\n", "malformed error object in terminal.run response"},
		{"error absent code", `{"jsonrpc":"2.0","id":2,"error":{"message":"fail"}}` + "\n", "malformed error object in terminal.run response"},
		{"error non-integral code", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000.5,"message":"fail"}}` + "\n", "malformed error object in terminal.run response"},
		{"error wrong-type code string", `{"jsonrpc":"2.0","id":2,"error":{"code":"-32000","message":"fail"}}` + "\n", "malformed error object in terminal.run response"},
		{"error missing message", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000}}` + "\n", "malformed error object in terminal.run response"},
		{"error composite message", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":{"leak":"SECRET_ERR_MSG"}}}` + "\n", "malformed error object in terminal.run response"},
		{"error wrong data type", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fail","data":"bad"}}` + "\n", "malformed error object in terminal.run response"},
		{"error wrong kind type", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fail","data":{"kind":{"leak":"SECRET_KIND"}}}}` + "\n", "malformed error object in terminal.run response"},
		{"error wrong hint type", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fail","data":{"kind":"name_taken","hint":["SECRET_HINT"]}}}` + "\n", "malformed error object in terminal.run response"},
		{"error data null kind", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fail","data":{"kind":null}}}` + "\n", "malformed error object in terminal.run response"},
		{"error data null hint", `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fail","data":{"hint":null}}}` + "\n", "malformed error object in terminal.run response"},
		{"invalid jsonrpc", `{"jsonrpc":"1.0","id":2,"result":{"address":"t7"}}` + "\n", "invalid or missing jsonrpc"},
		{"both result and error", `{"jsonrpc":"2.0","id":2,"result":{"address":"t7"},"error":{"code":-32000,"message":"e"}}` + "\n", "both result and error"},
		{"neither result nor error", `{"jsonrpc":"2.0","id":2}` + "\n", "neither result nor error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeScriptConn()
			c := &Client{Connect: fc.Connect}

			go func() {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))

				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(tc.runResponse))
			}()

			addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
			if addr != "" {
				t.Fatalf("expected empty address, got %q", addr)
			}
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrRunOutcomeUnknown) {
				t.Fatalf("expected ErrRunOutcomeUnknown, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrMsg, err.Error())
			}
		})
	}
}

func TestRunTerminal_LostReplyAfterSend(t *testing.T) {
	fc := newFakeScriptConn()
	c := &Client{Connect: fc.Connect}

	runCount := 0
	go func() {
		r := bufio.NewReader(fc.clientInR)
		_, _ = r.ReadString('\n')
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, "terminal.run") {
				runCount++
				// Close stdout immediately after receiving run
				_ = fc.clientOutW.Close()
				return
			}
		}
	}()

	addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
	if addr != "" {
		t.Fatalf("expected empty address, got %q", addr)
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrRunOutcomeUnknown) {
		t.Fatalf("expected ErrRunOutcomeUnknown, got: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected exactly 1 terminal.run attempt, got %d", runCount)
	}
}

func TestRunTerminal_CancelledContext(t *testing.T) {
	t.Run("canceled before run write", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			// Cancel before replying to hello
			cancel()
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
		}()

		_, err := c.RunTerminal(ctx, validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
		if errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("cancel before run write must not wrap ErrRunOutcomeUnknown")
		}
	})

	t.Run("canceled after run write", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))

			// Read run request, then cancel
			_, _ = r.ReadString('\n')
			cancel()
			// Don't send response
		}()

		_, err := c.RunTerminal(ctx, validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("expected ErrRunOutcomeUnknown, got: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected errors.Is(err, context.Canceled) to be true, got: %v", err)
		}
	})

	t.Run("deadline exceeded after run write", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))

			// Read run request, then sleep beyond deadline
			_, _ = r.ReadString('\n')
			time.Sleep(100 * time.Millisecond)
		}()

		_, err := c.RunTerminal(ctx, validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("expected ErrRunOutcomeUnknown, got: %v", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected errors.Is(err, context.DeadlineExceeded) to be true, got: %v", err)
		}
	})
}

func TestRunTerminal_CloseAndReap(t *testing.T) {
	t.Run("success reaps and closes stdin", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t9"}}` + "\n"))
		}()

		addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err != nil || addr != "t9" {
			t.Fatalf("expected success with t9, got addr=%q err=%v", addr, err)
		}
		if !fc.isInClosed() {
			t.Fatalf("expected stdin to be closed on success")
		}
		if fc.getReapCount() != 1 {
			t.Fatalf("expected reap to be called once, got %d", fc.getReapCount())
		}
	})

	t.Run("valid address retained even if cleanup fails", func(t *testing.T) {
		fc := newFakeScriptConn()
		fc.reapErr = errors.New("cleanup error: process kill failed")
		c := &Client{Connect: fc.Connect}

		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t_retained_123"}}` + "\n"))
		}()

		addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err != nil {
			t.Fatalf("expected nil error when valid address is observed, got: %v", err)
		}
		if addr != "t_retained_123" {
			t.Fatalf("expected address t_retained_123, got: %q", addr)
		}
	})
}

func TestRunTerminal_OversizedResponse(t *testing.T) {
	t.Run("oversized hello line", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			// Write 1MiB + 10 bytes without newline
			huge := make([]byte, 1024*1024+10)
			for i := range huge {
				huge[i] = ' '
			}
			_, _ = fc.clientOutW.Write(huge)
			_, _ = fc.clientOutW.Write([]byte("\n"))
		}()

		_, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error on oversized hello line, got nil")
		}
		if !strings.Contains(err.Error(), "exceeds maximum allowed size") {
			t.Fatalf("expected oversized line error, got: %v", err)
		}
		if errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("oversized hello must not wrap ErrRunOutcomeUnknown")
		}
	})

	t.Run("oversized run line", func(t *testing.T) {
		fc := newFakeScriptConn()
		c := &Client{Connect: fc.Connect}

		go func() {
			r := bufio.NewReader(fc.clientInR)
			_, _ = r.ReadString('\n')
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))

			_, _ = r.ReadString('\n')
			huge := make([]byte, 1024*1024+10)
			for i := range huge {
				huge[i] = ' '
			}
			_, _ = fc.clientOutW.Write(huge)
			_, _ = fc.clientOutW.Write([]byte("\n"))
		}()

		_, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error on oversized run line, got nil")
		}
		if !errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("oversized run line must wrap ErrRunOutcomeUnknown")
		}
		if !strings.Contains(err.Error(), "exceeds maximum allowed size") {
			t.Fatalf("expected oversized line error, got: %v", err)
		}
	})
}

func TestRunTerminal_NoSensitiveTextInErrors(t *testing.T) {
	sentinelSecret := "SUPER_SECRET_TOKEN_ABCD_1234"

	compositeErrorKindEnvelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"error": map[string]any{
			"code":    -32000,
			"message": "fail",
			"data": map[string]any{
				"kind": map[string]any{
					"secret": sentinelSecret,
				},
			},
		},
	}
	compositeErrorKindPayload, err := json.Marshal(compositeErrorKindEnvelope)
	if err != nil {
		t.Fatalf("failed to marshal composite error kind payload: %v", err)
	}
	compositeErrorKindPayload = append(compositeErrorKindPayload, '\n')

	cases := []struct {
		name          string
		runPayload    []byte
		setupServer   func(fc *fakeScriptConn)
		wantSubstring string
		wantUnknown   bool
		validateJSON  bool
	}{
		{
			name: "secret in malformed hello line",
			setupServer: func(fc *fakeScriptConn) {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"bad": "` + sentinelSecret + `"}` + "\n"))
			},
		},
		{
			name: "secret in malformed run line",
			setupServer: func(fc *fakeScriptConn) {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"bad": "` + sentinelSecret + `"}` + "\n"))
			},
		},
		{
			name: "secret in oversized run line",
			setupServer: func(fc *fakeScriptConn) {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
				_, _ = r.ReadString('\n')
				huge := bytes.Repeat([]byte(sentinelSecret), 50000)
				_, _ = fc.clientOutW.Write(huge)
				_, _ = fc.clientOutW.Write([]byte("\n"))
			},
		},
		{
			name: "secret in composite error message",
			setupServer: func(fc *fakeScriptConn) {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":{"secret":"` + sentinelSecret + `"}}}` + "\n"))
			},
			wantSubstring: "malformed error object in terminal.run response",
			wantUnknown:   true,
		},
		{
			name:          "secret in composite error kind",
			runPayload:    compositeErrorKindPayload,
			wantSubstring: "malformed error object in terminal.run response",
			wantUnknown:   true,
			validateJSON:  true,
		},
		{
			name: "secret in composite error hint",
			setupServer: func(fc *fakeScriptConn) {
				r := bufio.NewReader(fc.clientInR)
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
				_, _ = r.ReadString('\n')
				_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fail","data":{"kind":"k","hint":["` + sentinelSecret + `"]}}}` + "\n"))
			},
			wantSubstring: "malformed error object in terminal.run response",
			wantUnknown:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeScriptConn()
			c := &Client{Connect: fc.Connect}

			input := validTerminalRunInput()
			input.Args = []string{"--key=" + sentinelSecret}

			if tc.validateJSON {
				trimmed := bytes.TrimSpace(tc.runPayload)
				if !json.Valid(trimmed) {
					t.Fatalf("runPayload must be valid JSON: %s", trimmed)
				}
			}

			if tc.setupServer != nil {
				go tc.setupServer(fc)
			} else if tc.runPayload != nil {
				go func() {
					r := bufio.NewReader(fc.clientInR)
					_, _ = r.ReadString('\n')
					_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1}}` + "\n"))
					_, _ = r.ReadString('\n')
					_, _ = fc.clientOutW.Write(tc.runPayload)
				}()
			}

			_, err := c.RunTerminal(context.Background(), input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			errStr := err.Error()
			if strings.Contains(errStr, sentinelSecret) {
				t.Fatalf("protocol error leaked sensitive sentinel text: %s", errStr)
			}
			if tc.wantUnknown && !errors.Is(err, ErrRunOutcomeUnknown) {
				t.Fatalf("expected error wrapping ErrRunOutcomeUnknown, got: %v", err)
			}
			if tc.wantSubstring != "" && !strings.Contains(errStr, tc.wantSubstring) {
				t.Fatalf("expected error containing %q, got %q", tc.wantSubstring, errStr)
			}
		})
	}
}

func setupTestHcBinary(t *testing.T, mode string) platform.Env {
	t.Helper()
	tmpDir := t.TempDir()
	execPath, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to find test executable: %v", err)
	}

	binName := "hc"
	if runtime.GOOS == "windows" {
		binName = "hc.exe"
	}
	target := filepath.Join(tmpDir, binName)

	if err := os.Symlink(execPath, target); err != nil {
		data, err := os.ReadFile(execPath)
		if err != nil {
			t.Fatalf("failed to read test executable: %v", err)
		}
		if err := os.WriteFile(target, data, 0o755); err != nil {
			t.Fatalf("failed to copy test executable: %v", err)
		}
	}

	envMap := map[string]string{
		"PATH":                tmpDir,
		"SOHO_TEST_HC_HELPER": "1",
		"SOHO_HELPER_MODE":    mode,
	}
	if sysRoot := testutil.HostSystemRoot(); sysRoot != "" {
		envMap["SYSTEMROOT"] = sysRoot
	}
	if tmp := testutil.HostTempDir(); tmp != "" {
		envMap["TMPDIR"] = tmp
	}
	return platform.Env(envMap)
}

func TestDefaultConnect_NativeProcessGlue(t *testing.T) {
	t.Run("success exercises launch stdin stdout EOF and reap", func(t *testing.T) {
		env := setupTestHcBinary(t, "success")
		c := New(env)

		addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err != nil {
			t.Fatalf("unexpected error from native process glue: %v", err)
		}
		if addr != "t_native_glue_42" {
			t.Fatalf("expected address t_native_glue_42, got %q", addr)
		}
	})

	t.Run("cancellation kills and reaps bridge process", func(t *testing.T) {
		env := setupTestHcBinary(t, "hang")
		c := New(env)

		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()

		_, err := c.RunTerminal(ctx, validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error from canceled hanging process, got nil")
		}
		if !errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("expected ErrRunOutcomeUnknown, got: %v", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context error preserved, got: %v", err)
		}
	})

	t.Run("EOF after run wraps ErrRunOutcomeUnknown", func(t *testing.T) {
		env := setupTestHcBinary(t, "eof_after_run")
		c := New(env)

		_, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err == nil {
			t.Fatalf("expected error on EOF after run, got nil")
		}
		if !errors.Is(err, ErrRunOutcomeUnknown) {
			t.Fatalf("expected ErrRunOutcomeUnknown, got: %v", err)
		}
	})

	t.Run("success with executable in path with spaces", func(t *testing.T) {
		spaceDir := filepath.Join(t.TempDir(), "dir with spaces")
		if err := os.MkdirAll(spaceDir, 0o755); err != nil {
			t.Fatalf("failed to create space dir: %v", err)
		}
		execPath, err := os.Executable()
		if err != nil {
			t.Fatalf("failed to find test executable: %v", err)
		}
		binName := "hc"
		if runtime.GOOS == "windows" {
			binName = "hc.exe"
		}
		target := filepath.Join(spaceDir, binName)
		if err := os.Symlink(execPath, target); err != nil {
			data, err := os.ReadFile(execPath)
			if err != nil {
				t.Fatalf("failed to read test executable: %v", err)
			}
			if err := os.WriteFile(target, data, 0o755); err != nil {
				t.Fatalf("failed to copy test executable: %v", err)
			}
		}
		env := platform.Env{
			"PATH":                spaceDir,
			"SOHO_TEST_HC_HELPER": "1",
			"SOHO_HELPER_MODE":    "success",
		}
		c := New(env)
		addr, err := c.RunTerminal(context.Background(), validTerminalRunInput())
		if err != nil {
			t.Fatalf("unexpected error from path with spaces: %v", err)
		}
		if addr != "t_native_glue_42" {
			t.Fatalf("expected address t_native_glue_42, got %q", addr)
		}
	})

	t.Run("isScriptPath recognizes cmd and bat extensions", func(t *testing.T) {
		for _, p := range []string{"hc.cmd", "hc.bat", "HC.CMD", "HC.BAT", `C:\tools\hc.cmd`, `C:\tools\hc.bat`} {
			if !isScriptPath(p) {
				t.Errorf("expected isScriptPath(%q) to be true", p)
			}
		}
		for _, p := range []string{"hc", "hc.exe", "HC.EXE", `/usr/local/bin/hc`} {
			if isScriptPath(p) {
				t.Errorf("expected isScriptPath(%q) to be false", p)
			}
		}
	})
}
