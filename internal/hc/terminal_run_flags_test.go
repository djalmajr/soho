package hc

// R329: no_notify and outlive_parent are per-request. The zero TerminalRun is
// the worker spawn's request byte for byte (no_notify:true,
// outlive_parent:true); each opt-in field moves exactly its own wire field.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// runFlagsRequest sends one terminal.run for input and returns the decoded
// params of the request the client wrote.
func runFlagsRequest(t *testing.T, input TerminalRun) map[string]any {
	t.Helper()
	fc := newFakeScriptConn()
	c := &Client{Connect: fc.Connect}
	var captured []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(fc.clientInR)
		_, _ = r.ReadString('\n')
		_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"generation":1,"core":{"name":"core1"}}}` + "\n"))
		if run, err := r.ReadBytes('\n'); err == nil {
			captured = run
			_, _ = fc.clientOutW.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"address":"t42"}}` + "\n"))
		}
	}()
	if _, err := c.RunTerminal(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-done
	var req struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("decode request %q: %v", captured, err)
	}
	return req.Params
}

func TestRunTerminal_NotifyAndOutliveFlags(t *testing.T) {
	for _, tc := range []struct {
		name          string
		notifyCaller  bool
		endWithCaller bool
		noNotify      bool
		outliveParent bool
	}{
		{"zero value is the spawn request", false, false, true, true},
		{"notify the caller only", true, false, false, true},
		{"end with the caller only", false, true, true, false},
		{"notify the caller and end with it", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validTerminalRunInput()
			input.NotifyCaller, input.EndWithCaller = tc.notifyCaller, tc.endWithCaller
			params := runFlagsRequest(t, input)
			if params["no_notify"] != tc.noNotify {
				t.Fatalf("no_notify=%v want %v", params["no_notify"], tc.noNotify)
			}
			if params["outlive_parent"] != tc.outliveParent {
				t.Fatalf("outlive_parent=%v want %v", params["outlive_parent"], tc.outliveParent)
			}
			if params["wait"] != false {
				t.Fatalf("wait=%v want false", params["wait"])
			}
		})
	}
}

// R330: the caller's state root rides in the dedicated StateDir field. The
// env is exactly SOHO_TRANSPORT=hc, plus SOHO_STATE_DIR only when the field is
// set; the zero value is the spawn request's env.
func TestRunTerminal_StateDirEnv(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "state root")
	for _, tc := range []struct {
		name     string
		stateDir string
		want     map[string]any
	}{
		{"zero value is the spawn request's env", "", map[string]any{"SOHO_TRANSPORT": "hc"}},
		{"an absolute root rides next to the transport marker", abs, map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_STATE_DIR": abs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validTerminalRunInput()
			input.StateDir = tc.stateDir
			params := runFlagsRequest(t, input)
			if !reflect.DeepEqual(params["env"], tc.want) {
				t.Fatalf("env=%v want %v", params["env"], tc.want)
			}
		})
	}
}

// R330: the field is validated before Connect, and the override map keeps
// refusing the reserved name, so the field is no general exception.
func TestRunTerminal_StateDirRefusals(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	for _, tc := range []struct {
		name  string
		input func(*TerminalRun)
		want  error
	}{
		{"relative", func(in *TerminalRun) { in.StateDir = "state" }, errStateDirInvalid},
		{"NUL", func(in *TerminalRun) { in.StateDir = root + "\x00x" }, errStateDirInvalid},
		{"CR", func(in *TerminalRun) { in.StateDir = root + "\rx" }, errStateDirInvalid},
		{"LF", func(in *TerminalRun) { in.StateDir = root + "\nx" }, errStateDirInvalid},
		{"invalid UTF-8", func(in *TerminalRun) { in.StateDir = root + "\xff" }, errStateDirInvalid},
		{"the override map still refuses SOHO_STATE_DIR", func(in *TerminalRun) {
			in.EnvOverrides = map[string]string{"SOHO_STATE_DIR": root}
		}, errEnvNameReserved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connects := 0
			c := &Client{Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
				connects++
				return nil, nil, nil, errors.New("should not be called")
			}}
			input := validTerminalRunInput()
			tc.input(&input)
			_, err := c.RunTerminal(context.Background(), input)
			if !errors.Is(err, tc.want) || err.Error() != tc.want.Error() {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if connects != 0 {
				t.Fatalf("Connect called %d times for a refused run", connects)
			}
		})
	}
}

// SOH-292: the registry hint and the agent name ride in the dedicated
// RegistryDir and Agent fields. Each adds exactly its own env entry, only
// when non-empty, next to SOHO_TRANSPORT=hc; the override map keeps
// refusing both names.
func TestRunTerminal_RegistryDirAndAgentEnv(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "state root")
	for _, tc := range []struct {
		name        string
		registryDir string
		agent       string
		want        map[string]any
	}{
		{"empty values add nothing", "", "", map[string]any{"SOHO_TRANSPORT": "hc"}},
		{"both ride next to the transport marker", abs, "worker-2", map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_REGISTRY_DIR": abs, "SOHO_AGENT": "worker-2"}},
		{"only the registry dir", abs, "", map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_REGISTRY_DIR": abs}},
		{"only the agent", "", "w_1", map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_AGENT": "w_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validTerminalRunInput()
			input.RegistryDir, input.Agent = tc.registryDir, tc.agent
			params := runFlagsRequest(t, input)
			if !reflect.DeepEqual(params["env"], tc.want) {
				t.Fatalf("env=%v want %v", params["env"], tc.want)
			}
		})
	}
}

func TestRunTerminal_RegistryDirAndAgentRefusals(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	for _, tc := range []struct {
		name  string
		input func(*TerminalRun)
		want  error
	}{
		{"relative registry dir", func(in *TerminalRun) { in.RegistryDir = "state" }, errRegistryDirInvalid},
		{"registry dir NUL", func(in *TerminalRun) { in.RegistryDir = root + "\x00x" }, errRegistryDirInvalid},
		{"registry dir CR", func(in *TerminalRun) { in.RegistryDir = root + "\rx" }, errRegistryDirInvalid},
		{"registry dir LF", func(in *TerminalRun) { in.RegistryDir = root + "\nx" }, errRegistryDirInvalid},
		{"registry dir invalid UTF-8", func(in *TerminalRun) { in.RegistryDir = root + "\xff" }, errRegistryDirInvalid},
		{"agent with a space", func(in *TerminalRun) { in.Agent = "a b" }, errAgentInvalid},
		{"agent uppercase", func(in *TerminalRun) { in.Agent = "Worker" }, errAgentInvalid},
		{"agent path-like", func(in *TerminalRun) { in.Agent = "../x" }, errAgentInvalid},
		{"agent NUL", func(in *TerminalRun) { in.Agent = "a\x00b" }, errAgentInvalid},
		{"agent CR", func(in *TerminalRun) { in.Agent = "a\rb" }, errAgentInvalid},
		{"agent LF", func(in *TerminalRun) { in.Agent = "a\nb" }, errAgentInvalid},
		{"agent trailing LF", func(in *TerminalRun) { in.Agent = "worker\n" }, errAgentInvalid},
		{"agent too long", func(in *TerminalRun) { in.Agent = "abcdefghijklmnopqrstuvwxyz0123456" }, errAgentInvalid},
		{"agent invalid UTF-8", func(in *TerminalRun) { in.Agent = "w\xff" }, errAgentInvalid},
		{"the override map still refuses SOHO_REGISTRY_DIR", func(in *TerminalRun) {
			in.EnvOverrides = map[string]string{"SOHO_REGISTRY_DIR": root}
		}, errEnvNameReserved},
		{"the override map still refuses SOHO_AGENT", func(in *TerminalRun) {
			in.EnvOverrides = map[string]string{"SOHO_AGENT": "worker"}
		}, errEnvNameReserved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connects := 0
			c := &Client{Connect: func(ctx context.Context, env platform.Env) (io.WriteCloser, io.Reader, func() error, error) {
				connects++
				return nil, nil, nil, errors.New("should not be called")
			}}
			input := validTerminalRunInput()
			tc.input(&input)
			_, err := c.RunTerminal(context.Background(), input)
			if !errors.Is(err, tc.want) || err.Error() != tc.want.Error() {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if connects != 0 {
				t.Fatalf("Connect called %d times for a refused run", connects)
			}
		})
	}
}
