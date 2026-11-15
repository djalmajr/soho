package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

type fakeUncertainTerminalRuntime struct {
	startCalls []transport.Start
	reply      transport.Reply[string]
}

func (f *fakeUncertainTerminalRuntime) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	f.startCalls = append(f.startCalls, start)
	return f.reply
}

func (f *fakeUncertainTerminalRuntime) CallerAgentName(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{}
}

func (f *fakeUncertainTerminalRuntime) SendText(ctx context.Context, target transport.Target, text string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{}
}

func (f *fakeUncertainTerminalRuntime) GetGone(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{}
}

type fakeUncertainTransport struct {
	closeCalls []transport.Target
	listCalls  int
}

func (f *fakeUncertainTransport) Caller() transport.Caller { return transport.Caller{} }
func (f *fakeUncertainTransport) Current(ctx context.Context, call transport.Call) transport.Reply[transport.Caller] {
	return transport.Reply[transport.Caller]{}
}
func (f *fakeUncertainTransport) CheckCaller(ctx context.Context, call transport.Call) transport.Reply[transport.CallerCheck] {
	return transport.Reply[transport.CallerCheck]{}
}
func (f *fakeUncertainTransport) List(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.Terminal] {
	f.listCalls++
	return transport.Reply[[]transport.Terminal]{}
}
func (f *fakeUncertainTransport) Get(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[transport.Terminal] {
	return transport.Reply[transport.Terminal]{}
}
func (f *fakeUncertainTransport) Send(ctx context.Context, target transport.Target, send transport.Send, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{}
}
func (f *fakeUncertainTransport) Keys(ctx context.Context, target transport.Target, keys string, pane bool, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{}
}
func (f *fakeUncertainTransport) Read(ctx context.Context, target transport.Target, read transport.Read, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{}
}
func (f *fakeUncertainTransport) Close(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	f.closeCalls = append(f.closeCalls, target)
	return transport.Reply[bool]{Value: true, Valid: true}
}

func TestHcStartUnknownOutcome(t *testing.T) {
	const sensitiveSentinel = "SECRET-TOKEN-DO-NOT-LEAK-XYZ123"

	for _, tc := range []struct {
		name         string
		address      string
		expectedTerm string
	}{
		{
			name:         "KnownAddress",
			address:      "t99",
			expectedTerm: "t99",
		},
		{
			name:         "EmptyAddress",
			address:      "",
			expectedTerm: "worker-custom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			if err := os.MkdirAll(filepath.Join(state, "sX"), 0o700); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{
				"SOHO_TRANSPORT": "hc",
				"SOHO_DIR":       state,
				"HC_TERMINAL_ID": "t1",
			}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}

			termFake := &fakeUncertainTerminalRuntime{
				reply: transport.Reply[string]{
					Value: tc.address,
					Err: &transport.Failure{
						Kind:    transport.FailureRunOutcomeUnknown,
						Code:    "uncertain_dispatch",
						Message: "uncertain dispatch response: " + sensitiveSentinel,
						Cause:   errors.New("connection reset by peer: " + sensitiveSentinel),
					},
				},
			}
			transFake := &fakeUncertainTransport{}
			svc := transport.Services{
				Terminal: termFake,
				Runtime:  transFake,
			}

			opt := spawnOptions{
				name:    "worker-custom",
				cwd:     root,
				timeout: "30s",
			}

			var exit *platform.ExitError
			func() {
				defer func() {
					if r := recover(); r != nil {
						if e, ok := r.(*platform.ExitError); ok {
							exit = e
						} else {
							panic(r)
						}
					}
				}()
				startHcAgentUsingServices(ctx, &opt, "pi", env, []string{"arg1"}, svc)
			}()

			// Expected exit 4
			if exit == nil {
				t.Fatalf("expected ExitError, got nil")
			}
			if exit.Code != 4 {
				t.Errorf("exit code = %d, want 4", exit.Code)
			}

			// Diagnostic: manual inspection diagnostic identifying address or requested name
			if !strings.Contains(exit.Msg, "manual inspection") {
				t.Errorf("exit message %q does not contain 'manual inspection'", exit.Msg)
			}
			if !strings.Contains(exit.Msg, tc.expectedTerm) {
				t.Errorf("exit message %q does not contain expected term identifier %q", exit.Msg, tc.expectedTerm)
			}

			// Diagnostic: must not contain raw error cause or sensitive sentinel
			if strings.Contains(exit.Msg, sensitiveSentinel) {
				t.Errorf("exit message %q leaked sensitive sentinel", exit.Msg)
			}
			if strings.Contains(exit.Msg, "connection reset") {
				t.Errorf("exit message %q leaked error cause", exit.Msg)
			}

			// Invariants: exactly one Start call
			if len(termFake.startCalls) != 1 {
				t.Errorf("Start calls = %d, want 1", len(termFake.startCalls))
			}

			// Invariants: zero Close calls
			if len(transFake.closeCalls) != 0 {
				t.Errorf("Close calls = %d, want 0", len(transFake.closeCalls))
			}

			// Invariants: zero suffix probes
			if transFake.listCalls != 0 {
				t.Errorf("List calls (suffix probes) = %d, want 0", transFake.listCalls)
			}

			// Invariants: no success / roster registration
			if opt.pane != "" {
				t.Errorf("opt.pane = %q, want empty (no success)", opt.pane)
			}
			rosterPath := filepath.Join(state, "sX", "agents.tsv")
			if _, err := os.Stat(rosterPath); !os.IsNotExist(err) {
				t.Errorf("roster file exists at %s; want no roster registration", rosterPath)
			}
		})
	}
}

func TestSpawnHcUnknownOutcomeThroughCommand(t *testing.T) {
	const sensitiveSentinel = "SECRET-TOKEN-DO-NOT-LEAK-CMD-XYZ456"

	for _, tc := range []struct {
		name         string
		address      string
		expectedTerm string
	}{
		{
			name:         "KnownAddress",
			address:      "t99",
			expectedTerm: "t99",
		},
		{
			name:         "EmptyAddress",
			address:      "",
			expectedTerm: "implementer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, "implementer", "claude")
			rules := hcFixtureRules()
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}

			termFake := &fakeUncertainTerminalRuntime{
				reply: transport.Reply[string]{
					Value: tc.address,
					Err: &transport.Failure{
						Kind:    transport.FailureRunOutcomeUnknown,
						Code:    "uncertain_dispatch",
						Message: "uncertain dispatch response: " + sensitiveSentinel,
						Cause:   errors.New("connection reset by peer: " + sensitiveSentinel),
					},
				},
			}
			transFake := &fakeUncertainTransport{}
			svc := transport.Services{
				Terminal: termFake,
				Runtime:  transFake,
			}

			var exit *platform.ExitError
			func() {
				defer func() {
					if r := recover(); r != nil {
						if e, ok := r.(*platform.ExitError); ok {
							exit = e
						} else {
							panic(r)
						}
					}
				}()
				cmdSpawnWithHcStarter([]string{"implementer"}, ctx, env, cwd, func(c *core.Config, o *spawnOptions, k string, e platform.Env, a []string) {
					startHcAgentUsingServices(c, o, k, e, a, svc)
				})
			}()

			// Expected exit 4
			if exit == nil {
				t.Fatalf("expected ExitError, got nil")
			}
			if exit.Code != 4 {
				t.Errorf("exit code = %d, want 4", exit.Code)
			}

			// Diagnostic: manual inspection diagnostic identifying address or requested name
			if !strings.Contains(exit.Msg, "manual inspection") {
				t.Errorf("exit message %q does not contain 'manual inspection'", exit.Msg)
			}
			if !strings.Contains(exit.Msg, tc.expectedTerm) {
				t.Errorf("exit message %q does not contain expected term identifier %q", exit.Msg, tc.expectedTerm)
			}

			// Diagnostic: must not contain raw error cause or sensitive sentinel
			if strings.Contains(exit.Msg, sensitiveSentinel) {
				t.Errorf("exit message %q leaked sensitive sentinel", exit.Msg)
			}
			if strings.Contains(exit.Msg, "connection reset") {
				t.Errorf("exit message %q leaked error cause", exit.Msg)
			}

			// Invariants: exactly one Start call
			if len(termFake.startCalls) != 1 {
				t.Errorf("Start calls = %d, want 1", len(termFake.startCalls))
			}

			// Invariants: zero Close calls
			if len(transFake.closeCalls) != 0 {
				t.Errorf("Close calls = %d, want 0", len(transFake.closeCalls))
			}

			// Invariants: zero suffix probes
			if transFake.listCalls != 0 {
				t.Errorf("List calls (suffix probes) = %d, want 0", transFake.listCalls)
			}

			// Invariants: no worker roster registration
			rosterPath := filepath.Join(state, "sX", "agents.tsv")
			if data, err := os.ReadFile(rosterPath); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if line != "" && !strings.HasPrefix(line, "#") {
						t.Fatalf("roster row was written on uncertain start: %q", line)
					}
				}
			}

			// Verify fake hc received no kill or rm calls
			for _, c := range hcCallLog(t, bin) {
				switch c.Argv[0] {
				case "kill", "rm":
					t.Fatalf("unexpected hc cleanup call: %v", c.Argv)
				}
			}
		})
	}
}
