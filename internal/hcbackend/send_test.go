package hcbackend

// DJA-604 2b-3a: `hc send` and the raw session listing. The guarded
// (non-queue) send selects its --if guard from the client's capability
// probes (DJA-611 core-ready integration): the fixtures mirror the retained
// primary Core (de97ea5, v0.1.20) — the `hc send --help` and `hc schema
// --help` texts are the generated help snapshots of that pin, and the
// OpenRPC document is the generated schema/hc.openrpc.json shape.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const sendPrompt = "Read the file /tmp/brief.md in full."

const (
	sendHelpOld   = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	sendHelpNew   = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working, ready]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	schemaHelpOld = "Every API method with its JSON Schema, for app builders\n\nUsage: hc schema [OPTIONS]\n\nOptions:\n      --json  JSON output even in a terminal\n  -h, --help  Print help\n"
	schemaHelpNew = "Every API method with its JSON Schema, for app builders\n\nUsage: hc schema [OPTIONS]\n\nOptions:\n      --on <MACHINE>  Discover on the named machine instead of the local core\n      --json  JSON output even in a terminal\n  -h, --help  Print help\n"
)

// schemaDocTemplate is the generated OpenRPC shape of the retained primary
// Core schema: argument 1 is the IdleOrWorking enum words, argument 2 the
// rpc.discover on param (empty when the local core predates the planned
// --on interface), argument 3 the terminal.send if param (empty on a core
// predating the if field).
const schemaDocTemplate = `{
  "openrpc": "1.3.2",
  "info": { "version": "0.1.20" },
  "components": { "schemas": { "IdleOrWorking": { "enum": [%[1]s], "type": "string" } } },
  "methods": [
    { "name": "terminal.send", "paramStructure": "by-name", "params": [%[3]s{"name": "terminal", "required": true, "schema": {"type": "string"}}, {"name": "text", "required": true, "schema": {"type": "string"}}], "result": {"name": "result", "schema": {"$ref": "#/components/schemas/SendResult"}} },
    { "name": "rpc.discover", "paramStructure": "by-name", "params": [%[2]s], "result": {"name": "result", "schema": {"$ref": "https://meta.open-rpc.org/"}} }
  ]
}
`

func schemaDoc(enumWords string, withIf, discoverOn bool) string {
	ifParam := ""
	if withIf {
		ifParam = `{"name": "if", "required": false, "schema": {"anyOf": [{"$ref": "#/components/schemas/IdleOrWorking"}, {"type": "null"}], "description": "Only type if the status is exactly this."}},`
	}
	discover := ""
	if discoverOn {
		discover = `{"name": "on", "required": false, "schema": {"type": ["string", "null"]}}`
	}
	return fmt.Sprintf(schemaDocTemplate, enumWords, discover, ifParam)
}

// probeRules renders the capability probe rules for the local target:
// parserHelp the `hc send --help` text, servingDoc the local `hc schema`
// answer, machine "" for a local target or the machine name for a
// machine/t7 target (which adds the --on preflight probes and the remote
// query, remoteDoc its answer).
func probeRules(machine, parserHelp, localDoc string, remoteDoc ...string) []fakecli.Rule {
	rules := []fakecli.Rule{
		{Argv: []string{"send", "--help"}, Stdout: parserHelp},
	}
	if machine == "" {
		rules = append(rules, fakecli.Rule{Argv: []string{"schema", "--json"}, Stdout: localDoc})
		return rules
	}
	rules = append(rules,
		fakecli.Rule{Argv: []string{"schema", "--help"}, Stdout: schemaHelpNew},
		fakecli.Rule{Argv: []string{"schema", "--json"}, Stdout: localDoc},
		fakecli.Rule{Argv: []string{"schema", "--on", machine, "--json"}, Stdout: remoteDoc[0]})
	return rules
}

// TestBackendSend is the send contract: the guarded (non-queue) send proves
// its --if guard through the capability probes — here the old runtime
// (parser and serving both idle), so the send keeps the exact legacy argv —
// and the queue send lifts the guard with no probes.
func TestBackendSend(t *testing.T) {
	argv := []string{"send", "--if", "idle", "--json", "--", "worker", sendPrompt}
	idleProbes := probeRules("", sendHelpOld, schemaDoc(`"idle","working"`, true, false))
	t.Run("exit 0 returns the raw SendResult record", func(t *testing.T) {
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: argv, Stdout: `{"turn":7,"started":true}`})
		env, calls := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != `{"turn":7,"started":true}` {
			t.Fatalf("rep=%+v", rep)
		}
		wantArgv(t, calls(), append(idleProbesArgv(), []string{"get", "worker", "--json"}, argv))
	})
	t.Run("a text that starts with a dash is a value, not a flag", func(t *testing.T) {
		dash := []string{"send", "--if", "idle", "--json", "--", "worker", "-force"}
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: dash, Stdout: `{"turn":1,"started":true}`})
		env, calls := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: "-force"}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep=%+v", rep)
		}
		wantArgv(t, calls(), append(idleProbesArgv(), []string{"get", "worker", "--json"}, dash))
	})
	t.Run("--queue drops the --if guard and runs no probes", func(t *testing.T) {
		queueArgv := []string{"send", "--json", "--", "worker", sendPrompt}
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: queueArgv, Stdout: `{"turn":8,"started":false}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt, Queue: true}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != `{"turn":8,"started":false}` {
			t.Fatalf("rep=%+v", rep)
		}
		wantArgv(t, calls(), [][]string{queueArgv})
	})
	t.Run("exit 9 is FailureBlocked with the person-must-answer message", func(t *testing.T) {
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: argv, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"worker is blocked"}}`})
		env, _ := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureBlocked || rep.Value != "" {
			t.Fatalf("rep=%+v", rep)
		}
		if want := "worker worker is blocked: a person must answer its prompt"; rep.Err.Message != want {
			t.Fatalf("message=%q, want %q", rep.Err.Message, want)
		}
	})
	t.Run("exit 10 is FailureBusy with the not-idle message", func(t *testing.T) {
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: argv, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`})
		env, _ := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureBusy || rep.Value != "" {
			t.Fatalf("rep=%+v", rep)
		}
		if want := "worker worker is not idle"; rep.Err.Message != want {
			t.Fatalf("message=%q, want %q", rep.Err.Message, want)
		}
	})
	t.Run("exit 11 (finished) is FailureNotFound", func(t *testing.T) {
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: argv, Code: 11, Stderr: `{"error":{"kind":"finished","message":"no such running terminal"}}`})
		env, _ := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("rep=%+v", rep)
		}
	})
	t.Run("exit 3 (not found) is FailureNotFound", func(t *testing.T) {
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: argv, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`})
		env, _ := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureNotFound {
			t.Fatalf("rep=%+v", rep)
		}
	})
	t.Run("any other exit takes the backend's usual mapping", func(t *testing.T) {
		rules := append(append([]fakecli.Rule{}, idleProbes...), fakecli.Rule{Argv: argv, Code: 1, Stderr: "boom"})
		env, _ := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep=%+v", rep)
		}
		if !strings.Contains(rep.Err.Message, "boom") {
			t.Fatalf("message=%q, want the hc error text", rep.Err.Message)
		}
	})
}

// idleProbesArgv is the recorded argv of the old-runtime local probes.
func idleProbesArgv() [][]string {
	return [][]string{{"send", "--help"}, {"schema", "--json"}}
}

// readySendArgv is the guarded send argv under the proven ready guard.
func readySendArgv(targetID, text string) []string {
	return []string{"send", "--if", "ready", "--json", "--", targetID, text}
}

// idleSendArgv is the guarded send argv under the idle guard.
func idleSendArgv(targetID, text string) []string {
	return []string{"send", "--if", "idle", "--json", "--", targetID, text}
}

// TestBackendSendGuardReady is the matching local runtime: the parser and
// the serving enum both prove ready, and the send goes out with the ready
// guard after exactly the two read-only probes.
func TestBackendSendGuardReady(t *testing.T) {
	rules := append(probeRules("", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, false)),
		fakecli.Rule{Argv: readySendArgv("worker", sendPrompt), Stdout: `{"turn":3,"started":true}`})
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err != nil || !rep.Valid || rep.Value != `{"turn":3,"started":true}` {
		t.Fatalf("rep=%+v", rep)
	}
	want := append(idleProbesArgv(), readySendArgv("worker", sendPrompt))
	wantArgv(t, calls(), want)
}

// TestBackendSendGuardIdleCompat is the compatibility path at the consumer:
// a ready parser over an old serving core proves no ready, so the send keeps
// the exact legacy --if idle argv.
func TestBackendSendGuardIdleCompat(t *testing.T) {
	rules := append(probeRules("", sendHelpNew, schemaDoc(`"idle","working"`, true, false)),
		fakecli.Rule{Argv: idleSendArgv("worker", sendPrompt), Stdout: `{"turn":4,"started":true}`})
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep=%+v", rep)
	}
	want := append(idleProbesArgv(), []string{"get", "worker", "--json"}, idleSendArgv("worker", sendPrompt))
	wantArgv(t, calls(), want)
}

// TestBackendSendGuardRefusalZeroSend is the fail-closed contract: a schema
// or help error, a missing or unsupported if param, or no proven common
// guard is a refusal — the failure names the refusal (never busy or
// blocked), and the recorded call log holds the probes but no hc send.
func TestBackendSendGuardRefusalZeroSend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []fakecli.Rule
		kind  transport.FailureKind
		want  string
		calls [][]string
	}{
		{
			name: "schema launch error is unavailable",
			rules: []fakecli.Rule{
				{Argv: []string{"send", "--help"}, Stdout: sendHelpOld},
				{Argv: []string{"schema", "--json"}, Code: 5, Stderr: `{"error":{"kind":"unavailable","message":"the core is not running"}}`},
			},
			kind:  transport.FailureUnavailable,
			want:  "the core is not running",
			calls: [][]string{{"send", "--help"}, {"schema", "--json"}},
		},
		{
			name: "missing if param is a protocol refusal",
			rules: []fakecli.Rule{
				{Argv: []string{"send", "--help"}, Stdout: sendHelpOld},
				{Argv: []string{"schema", "--json"}, Stdout: schemaDoc(`"idle","working"`, false, false)},
			},
			kind:  transport.FailureProtocol,
			want:  "no param named if",
			calls: [][]string{{"send", "--help"}, {"schema", "--json"}},
		},
		{
			name: "malformed schema document is a protocol refusal",
			rules: []fakecli.Rule{
				{Argv: []string{"send", "--help"}, Stdout: sendHelpOld},
				{Argv: []string{"schema", "--json"}, Stdout: `{"methods":`},
			},
			kind:  transport.FailureProtocol,
			want:  "not one complete JSON document",
			calls: [][]string{{"send", "--help"}, {"schema", "--json"}},
		},
		{
			name: "no proven common guard is a protocol refusal",
			rules: []fakecli.Rule{
				{Argv: []string{"send", "--help"}, Stdout: sendHelpOld},
				{Argv: []string{"schema", "--json"}, Stdout: schemaDoc(`"working"`, true, false)},
			},
			kind:  transport.FailureProtocol,
			want:  "no --if guard is proven on both sides",
			calls: [][]string{{"send", "--help"}, {"schema", "--json"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, calls := fakeHc(t, tc.rules)
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != tc.kind {
				t.Fatalf("rep=%+v, want the %s refusal", rep, tc.kind)
			}
			if !strings.Contains(rep.Err.Message, tc.want) {
				t.Fatalf("message=%q, want to contain %q", rep.Err.Message, tc.want)
			}
			if rep.Value != "" {
				t.Fatalf("Value=%q, want no send result", rep.Value)
			}
			wantArgv(t, calls(), tc.calls)
		})
	}
}

// TestBackendSendMachineTargetReadyRemote is the matching remote: the local
// preflight passes, the remote schema proves ready, and the send goes out
// to the machine address with the ready guard — the full probe sequence is
// the four read-only calls in order.
func TestBackendSendMachineTargetReadyRemote(t *testing.T) {
	rules := append(probeRules("m1", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, true), schemaDoc(`"idle","working","ready"`, true, false)),
		fakecli.Rule{Argv: readySendArgv("m1/t7", sendPrompt), Stdout: `{"turn":9,"started":true}`})
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep=%+v", rep)
	}
	want := [][]string{
		{"send", "--help"},
		{"schema", "--help"},
		{"schema", "--json"},
		{"schema", "--on", "m1", "--json"},
		readySendArgv("m1/t7", sendPrompt),
	}
	wantArgv(t, calls(), want)
}

// TestBackendSendQualifiedIDMachineTarget: a machine/t7 embedded in the
// target's id is a remote address too — the same machine is derived, the
// same probes run, and the send keeps the qualified address.
func TestBackendSendQualifiedIDMachineTarget(t *testing.T) {
	rules := append(probeRules("m1", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, true), schemaDoc(`"idle","working","ready"`, true, false)),
		fakecli.Rule{Argv: readySendArgv("m1/t7", sendPrompt), Stdout: `{"turn":9,"started":true}`})
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(context.Background(), transport.Target{ID: "m1/t7"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep=%+v", rep)
	}
	want := [][]string{
		{"send", "--help"},
		{"schema", "--help"},
		{"schema", "--json"},
		{"schema", "--on", "m1", "--json"},
		readySendArgv("m1/t7", sendPrompt),
	}
	wantArgv(t, calls(), want)
}

// TestBackendSendRemoteOldIdle is the proven older remote: its serving enum
// lacks ready, so the qualified send keeps the idle guard.
func TestBackendSendRemoteOldIdle(t *testing.T) {
	rules := append(probeRules("m1", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, true), schemaDoc(`"idle","working"`, true, false)),
		fakecli.Rule{Argv: idleSendArgv("m1/t7", sendPrompt), Stdout: `{"turn":9,"started":true}`})
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep=%+v", rep)
	}
	want := [][]string{
		{"send", "--help"},
		{"schema", "--help"},
		{"schema", "--json"},
		{"schema", "--on", "m1", "--json"},
		{"get", "m1/t7", "--json"},
		idleSendArgv("m1/t7", sendPrompt),
	}
	wantArgv(t, calls(), want)
}

// TestBackendSendStaleLocalRemoteZeroSend: a stale local side of the --on
// interface refuses before the remote query — the recorded calls hold the
// local probes only, never the remote discovery, and never a send.
func TestBackendSendStaleLocalRemoteZeroSend(t *testing.T) {
	t.Run("stale local CLI without the --on option", func(t *testing.T) {
		rules := []fakecli.Rule{
			{Argv: []string{"send", "--help"}, Stdout: sendHelpNew},
			{Argv: []string{"schema", "--help"}, Stdout: schemaHelpOld},
			{Argv: []string{"schema", "--json"}, Stdout: schemaDoc(`"idle","working","ready"`, true, true)},
			{Argv: []string{"schema", "--on", "m1", "--json"}, Stdout: schemaDoc(`"idle","working","ready"`, true, false)},
		}
		env, calls := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
			t.Fatalf("rep=%+v, want the protocol refusal", rep)
		}
		if !strings.Contains(rep.Err.Message, "no --on option line") {
			t.Fatalf("message=%q, want the missing --on proof named", rep.Err.Message)
		}
		wantArgv(t, calls(), [][]string{{"send", "--help"}, {"schema", "--help"}})
	})
	t.Run("stale local serving without the on param", func(t *testing.T) {
		rules := []fakecli.Rule{
			{Argv: []string{"send", "--help"}, Stdout: sendHelpNew},
			{Argv: []string{"schema", "--help"}, Stdout: schemaHelpNew},
			{Argv: []string{"schema", "--json"}, Stdout: schemaDoc(`"idle","working","ready"`, true, false)},
			{Argv: []string{"schema", "--on", "m1", "--json"}, Stdout: schemaDoc(`"idle","working","ready"`, true, false)},
		}
		env, calls := fakeHc(t, rules)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Send(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, transport.Send{Text: sendPrompt}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
			t.Fatalf("rep=%+v, want the protocol refusal", rep)
		}
		if !strings.Contains(rep.Err.Message, "no param named on") {
			t.Fatalf("message=%q, want the missing on param named", rep.Err.Message)
		}
		wantArgv(t, calls(), [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}})
	})
}

// TestBackendSendRemoteMissingIfZeroSend: a remote core predating the if
// field proves no guard — the refusal after all four probes, no send.
func TestBackendSendRemoteMissingIfZeroSend(t *testing.T) {
	rules := probeRules("m1", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, true), schemaDoc(`"idle","working"`, false, false))
	env, calls := fakeHc(t, rules)
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(context.Background(), transport.Target{Machine: "m1", ID: "t7"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureProtocol {
		t.Fatalf("rep=%+v, want the protocol refusal", rep)
	}
	if !strings.Contains(rep.Err.Message, "no param named if") {
		t.Fatalf("message=%q, want the missing remote if param named", rep.Err.Message)
	}
	want := [][]string{
		{"send", "--help"},
		{"schema", "--help"},
		{"schema", "--json"},
		{"schema", "--on", "m1", "--json"},
	}
	wantArgv(t, calls(), want)
}

// TestBackendSendExitsUnderReadyGuard keeps the exit translation under the
// ready guard: exit 9 the blocked worker, exit 10 the mismatch against the
// guard that was actually used ("not ready"), exit 11 the finished
// terminal.
func TestBackendSendExitsUnderReadyGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		stderr string
		kind   transport.FailureKind
		want   string
	}{
		{"exit 9 is FailureBlocked", 9, `{"error":{"kind":"blocked","message":"worker is blocked"}}`, transport.FailureBlocked, "worker worker is blocked: a person must answer its prompt"},
		{"exit 10 is FailureBusy naming the ready guard", 10, `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`, transport.FailureBusy, "worker worker is not ready"},
		{"exit 11 (finished) is FailureNotFound", 11, `{"error":{"kind":"finished","message":"no such running terminal"}}`, transport.FailureNotFound, "no such running terminal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := append(probeRules("", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, false)),
				fakecli.Rule{Argv: readySendArgv("worker", sendPrompt), Code: tc.code, Stderr: tc.stderr})
			env, _ := fakeHc(t, rules)
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Send(context.Background(), transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != tc.kind {
				t.Fatalf("rep=%+v, want %s", rep, tc.kind)
			}
			if !strings.Contains(rep.Err.Message, tc.want) {
				t.Fatalf("message=%q, want to contain %q", rep.Err.Message, tc.want)
			}
		})
	}
}

// TestBackendSendCanceledContextZeroSend: a cancelled context refuses before
// any launch — no probe, no send, the cancellation surfaced as the usual
// unavailable mapping.
func TestBackendSendCanceledContextZeroSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env, calls := fakeHc(t, probeRules("", sendHelpNew, schemaDoc(`"idle","working","ready"`, true, false)))
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Send(ctx, transport.Target{ID: "worker"}, transport.Send{Text: sendPrompt}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
		t.Fatalf("rep=%+v, want the unavailable refusal", rep)
	}
	if len(calls()) != 0 {
		t.Fatalf("calls=%#v, want zero launches", calls())
	}
}

func TestBackendAgentListRaw(t *testing.T) {
	const list = `{"terminals":[{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}},{"address":"t9","name":"reviewer","state":"live","turn":2,"reported":true,"status":{"state":"working"}}]}`
	t.Run("the raw terminal records of the session", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: list},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentListRaw(context.Background(), transport.Call{})
		if rep.Err != nil || !rep.Valid || len(rep.Value) != 2 {
			t.Fatalf("rep=%+v", rep)
		}
		// The raw records keep the fields beyond the terminal projection
		// (name, address, turn, labels a reader may need).
		for _, needle := range []string{`"address":"t7"`, `"name":"worker"`, `"turn":5`} {
			if !strings.Contains(string(rep.Value[0]), needle) {
				t.Fatalf("record 0 = %s, want to hold %s", rep.Value[0], needle)
			}
		}
		if !strings.Contains(string(rep.Value[1]), `"name":"reviewer"`) {
			t.Fatalf("record 1 = %s, want the reviewer", rep.Value[1])
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"ls", "--label", "soho.session=sX", "--json"},
		})
	})
	t.Run("an empty session has an empty, valid listing", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentListRaw(context.Background(), transport.Call{})
		if rep.Err != nil || !rep.Valid || len(rep.Value) != 0 {
			t.Fatalf("rep=%+v", rep)
		}
	})
	t.Run("a failed listing is unavailable, never not-found", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such label"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.AgentListRaw(context.Background(), transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("rep=%+v", rep)
		}
	})
}
