package hc

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The fixtures mirror the retained primary Core (de97ea5, v0.1.20): the help
// texts are the generated help snapshots of that pin (docs/commands.md) and
// the OpenRPC document is the generated schema/hc.openrpc.json shape, with
// the terminal.send if param as a nullable union of the local IdleOrWorking
// reference. The ready fixtures add the ready word to the --if possible
// values and to the IdleOrWorking enum; the remote fixtures add the planned
// --on option to `hc schema --help` and the on param to rpc.discover.
const (
	sendHelpOld         = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	sendHelpNew         = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working, ready]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	sendHelpWorkingOnly = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nOptions:\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: working]\n  -h, --help               Print help\n"
	schemaHelpOld       = "Every API method with its JSON Schema, for app builders\n\nUsage: hc schema [OPTIONS]\n\nOptions:\n      --json  JSON output even in a terminal\n  -h, --help  Print help\n"
	schemaHelpNew       = "Every API method with its JSON Schema, for app builders\n\nUsage: hc schema [OPTIONS]\n\nOptions:\n      --on <MACHINE>  Discover on the named machine instead of the local core\n      --json  JSON output even in a terminal\n  -h, --help  Print help\n"
)

// schemaDocTemplate is the generated OpenRPC shape of the retained primary
// Core schema: argument 1 is the IdleOrWorking enum words, argument 2 the
// rpc.discover params (the on param when the local core proves the planned
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

// schemaDoc renders the template: enumWords like `"idle","working"`, withIf
// the presence of the if param, discoverOn the presence of the on param.
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

const (
	enumOld   = `"idle","working"`
	enumReady = `"idle","working","ready"`
)

func sendHelpRule(stdout string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"send", "--help"}, Stdout: stdout}
}
func schemaHelpRule(stdout string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"schema", "--help"}, Stdout: stdout}
}
func schemaRule(stdout string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"schema", "--json"}, Stdout: stdout}
}
func schemaOnRule(machine, stdout string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"schema", "--on", machine, "--json"}, Stdout: stdout}
}

func wantGuard(t *testing.T, env platform.Env, dir, machine, want string, wantCalls [][]string) {
	t.Helper()
	guard, err := New(env).SendIfGuard(context.Background(), machine)
	if err != nil {
		t.Fatalf("SendIfGuard: %v, want guard %q", err, want)
	}
	if guard != want {
		t.Fatalf("guard = %q, want %q", guard, want)
	}
	got := callArgvs(t, dir)
	if !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %v, want %v", got, wantCalls)
	}
}

func refusedGuard(t *testing.T, env platform.Env, dir, machine, wantErr string, wantCalls [][]string) {
	t.Helper()
	_, err := New(env).SendIfGuard(context.Background(), machine)
	if err == nil {
		t.Fatalf("SendIfGuard succeeded, want the %s refusal", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("err = %q, want to contain %q", err, wantErr)
	}
	got := callArgvs(t, dir)
	if !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %v, want %v", got, wantCalls)
	}
}

// TestSendIfGuardLocalMatrix is the parser x serving selection rule: ready
// only when both prove it, idle only when both prove idle.
func TestSendIfGuardLocalMatrix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parser  string
		serving string
		want    string
	}{
		{"new parser new serving picks ready", sendHelpNew, schemaDoc(enumReady, true, false), GuardReady},
		{"new parser old serving keeps idle", sendHelpNew, schemaDoc(enumOld, true, false), GuardIdle},
		{"old parser new serving keeps idle", sendHelpOld, schemaDoc(enumReady, true, false), GuardIdle},
		{"old parser old serving keeps idle", sendHelpOld, schemaDoc(enumOld, true, false), GuardIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, dir := newHCFake(t, []fakecli.Rule{sendHelpRule(tc.parser), schemaRule(tc.serving)})
			wantGuard(t, env, dir, "", tc.want, [][]string{{"send", "--help"}, {"schema", "--json"}})
		})
	}
}

// TestSendIfGuardNoCommonGuardRefuses: a parser and a serving enum that
// share no idle or ready word prove no guard — the refusal, with the two
// read-only probes and no send.
func TestSendIfGuardNoCommonGuardRefuses(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpWorkingOnly),
		schemaRule(schemaDoc(enumOld, true, false)),
	})
	refusedGuard(t, env, dir, "", "no --if guard is proven on both sides", [][]string{{"send", "--help"}, {"schema", "--json"}})
}

// TestSendIfGuardMissingIfParamRefuses: a serving core predating the if
// field names no if param — the refusal, never a guess about the missing
// guard.
func TestSendIfGuardMissingIfParamRefuses(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpOld),
		schemaRule(schemaDoc(enumOld, false, false)),
	})
	refusedGuard(t, env, dir, "", "no param named if", [][]string{{"send", "--help"}, {"schema", "--json"}})
}

// TestSendIfGuardMalformedSchemaRefuses: every malformed document shape is a
// refusal — truncated, multi-document, trailing data, a non-object top
// level, a missing methods list, and a duplicated terminal.send method.
func TestSendIfGuardMalformedSchemaRefuses(t *testing.T) {
	good := schemaDoc(enumOld, true, false)
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{"truncated document", good[:len(good)/2], "not one complete JSON document"},
		{"multi-document answer", good + good, "not one complete JSON document"},
		{"trailing data after the document", good + " not-json", "not one complete JSON document"},
		{"top level is an array", `[1,2,3]`, "not a JSON object"},
		{"no methods list", `{"components":{}}`, "no methods list"},
		{"duplicated terminal.send method", `{"methods":[{"name":"terminal.send"},{"name":"terminal.send"}]}`, "more than one method named terminal.send"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, dir := newHCFake(t, []fakecli.Rule{sendHelpRule(sendHelpOld), schemaRule(tc.doc)})
			refusedGuard(t, env, dir, "", tc.want, [][]string{{"send", "--help"}, {"schema", "--json"}})
		})
	}
}

// malformedComponentsDoc is one terminal.send method whose if param proves a
// nullable local reference, and the given literal components container — the
// parent's executed red shape (de97ea5 inert proof) at the document level.
func malformedComponentsDoc(container string) string {
	return `{"methods":[{"name":"terminal.send","params":[{"name":"if","schema":{"anyOf":[{"$ref":"#/components/schemas/SendCondition"},{"type":"null"}]}}]}],"components":` + container + `}`
}

// TestSendIfGuardMalformedContainersRefuses: null, scalar, or array at the
// components or the components.schemas level is a controlled refusal through
// the public seam — the two read-only probes, zero send (the backend
// consumer seam is the parent's executed red proof, rerun post-fix).
func TestSendIfGuardMalformedContainersRefuses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container string
		want      string
	}{
		{"components is null", "null", "the schema components are not an object"},
		{"components is a scalar", `"IdleOrWorking"`, "the schema components are not an object"},
		{"components is an array", "[1,2]", "the schema components are not an object"},
		{"components.schemas is null", `{"schemas":null}`, "the schema components.schemas is not an object"},
		{"components.schemas is a scalar", `{"schemas":"x"}`, "the schema components.schemas is not an object"},
		{"components.schemas is an array", `{"schemas":[1,2]}`, "the schema components.schemas is not an object"},
		{"components holds no schemas key", `{}`, "the schema components hold no schemas"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, dir := newHCFake(t, []fakecli.Rule{sendHelpRule(sendHelpNew), schemaRule(malformedComponentsDoc(tc.container))})
			refusedGuard(t, env, dir, "", tc.want, [][]string{{"send", "--help"}, {"schema", "--json"}})
		})
	}
}

// refDoc renders the template with the if param's anyOf branch replaced by
// the given $ref component name (the nullable union keeps its null branch).
func refDoc(ref string) string {
	return fmt.Sprintf(`{
  "components": { "schemas": { "A": {"$ref": "#/components/schemas/B"}, "B": {"$ref": "#/components/schemas/A"}, "Chain": {"$ref": "#/components/schemas/IdleOrWorking"}, "IdleOrWorking": {"enum": ["idle","working"], "type": "string"} } },
  "methods": [
    { "name": "terminal.send", "params": [ {"name": "if", "schema": {"anyOf": [{"$ref": "#/components/schemas/%s"}, {"type": "null"}]}}, {"name": "terminal", "schema": {"type": "string"}}, {"name": "text", "schema": {"type": "string"}}] }
  ]
}`, ref)
}

// TestSendIfGuardRefChainRules: an external reference, an unresolved
// component, and a cyclic reference chain are refusals; a finite local chain
// resolves to its enum.
func TestSendIfGuardRefChainRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{"external reference refused", `{"components":{"schemas":{"IdleOrWorking":{"enum":["idle"],"type":"string"}}},"methods":[{"name":"terminal.send","params":[{"name":"if","schema":{"anyOf":[{"$ref":"https://example.com/IdleOrWorking"},{"type":"null"}]}},{"name":"terminal","schema":{"type":"string"}},{"name":"text","schema":{"type":"string"}}]}]}`, "not a local components reference"},
		{"unresolved component refused", refDoc("Missing"), "has no components.schemas.Missing"},
		{"cyclic reference refused", refDoc("A"), "revisits components.schemas.A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, dir := newHCFake(t, []fakecli.Rule{sendHelpRule(sendHelpOld), schemaRule(tc.doc)})
			refusedGuard(t, env, dir, "", tc.want, [][]string{{"send", "--help"}, {"schema", "--json"}})
		})
	}
	t.Run("finite local chain resolves", func(t *testing.T) {
		env, dir := newHCFake(t, []fakecli.Rule{sendHelpRule(sendHelpNew), schemaRule(refDoc("Chain"))})
		wantGuard(t, env, dir, "", GuardIdle, [][]string{{"send", "--help"}, {"schema", "--json"}})
	})
}

// TestSendIfGuardDirectEnumAccepted: an if param schema holding its enum
// directly (no union) is the same proof in one object.
func TestSendIfGuardDirectEnumAccepted(t *testing.T) {
	doc := `{"components":{"schemas":{}},"methods":[{"name":"terminal.send","params":[{"name":"if","schema":{"enum":["idle","working","ready"],"type":"string"}},{"name":"terminal","schema":{"type":"string"}},{"name":"text","schema":{"type":"string"}}]}]}`
	env, dir := newHCFake(t, []fakecli.Rule{sendHelpRule(sendHelpNew), schemaRule(doc)})
	wantGuard(t, env, dir, "", GuardReady, [][]string{{"send", "--help"}, {"schema", "--json"}})
}

// TestSendIfGuardRemoteReady is the matching remote: the local preflight
// (the --on option in `hc schema --help`, the on param in the local serving
// rpc.discover) passes, the remote schema proves ready, and the full probe
// sequence is the four read-only calls in order.
func TestSendIfGuardRemoteReady(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpNew),
		schemaHelpRule(schemaHelpNew),
		schemaRule(schemaDoc(enumReady, true, true)),
		schemaOnRule("m1", schemaDoc(enumReady, true, false)),
	})
	wantCalls := [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}, {"schema", "--on", "m1", "--json"}}
	wantGuard(t, env, dir, "m1", GuardReady, wantCalls)
}

// TestSendIfGuardRemoteOldIdle is the proven older remote: its serving enum
// lacks ready, so the guard stays idle.
func TestSendIfGuardRemoteOldIdle(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpNew),
		schemaHelpRule(schemaHelpNew),
		schemaRule(schemaDoc(enumReady, true, true)),
		schemaOnRule("m1", schemaDoc(enumOld, true, false)),
	})
	wantCalls := [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}, {"schema", "--on", "m1", "--json"}}
	wantGuard(t, env, dir, "m1", GuardIdle, wantCalls)
}

// TestSendIfGuardStaleLocalCLIRefusesBeforeRemote: a local `hc schema
// --help` without the --on option is a refusal, and the remote query never
// runs — a stale local CLI must not parse or drop the field silently.
func TestSendIfGuardStaleLocalCLIRefusesBeforeRemote(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpNew),
		schemaHelpRule(schemaHelpOld),
		schemaRule(schemaDoc(enumReady, true, false)),
		schemaOnRule("m1", schemaDoc(enumReady, true, false)),
	})
	refusedGuard(t, env, dir, "m1", "has no --on option line", [][]string{{"send", "--help"}, {"schema", "--help"}})
}

// TestSendIfGuardStaleLocalServingRefusesBeforeRemote: a local serving
// rpc.discover without its on param is a refusal, and the remote query never
// runs — a stale local core must not answer the remote query with its own
// enum.
func TestSendIfGuardStaleLocalServingRefusesBeforeRemote(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpNew),
		schemaHelpRule(schemaHelpNew),
		schemaRule(schemaDoc(enumReady, true, false)),
		schemaOnRule("m1", schemaDoc(enumReady, true, false)),
	})
	refusedGuard(t, env, dir, "m1", "no param named on", [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}})
}

// TestSendIfGuardRemoteMissingIfRefuses: a remote core predating the if
// field proves no guard — the refusal after all four probes.
func TestSendIfGuardRemoteMissingIfRefuses(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpNew),
		schemaHelpRule(schemaHelpNew),
		schemaRule(schemaDoc(enumReady, true, true)),
		schemaOnRule("m1", schemaDoc(enumOld, false, false)),
	})
	refusedGuard(t, env, dir, "m1", "no param named if", [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}, {"schema", "--on", "m1", "--json"}})
}

// TestSendIfGuardProbeLaunchError: a failed `hc send --help` is the decoded
// launch error, and no later probe runs.
func TestSendIfGuardProbeLaunchError(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		{Argv: []string{"send", "--help"}, Code: 2, Stderr: `{"error":{"kind":"usage","message":"unknown flag"}}`},
		schemaRule(schemaDoc(enumOld, true, false)),
	})
	_, err := New(env).SendIfGuard(context.Background(), "")
	var e *Error
	if !errors.As(err, &e) || e.Exit != 2 || e.Kind != "usage" {
		t.Fatalf("err = %v, want the decoded exit-2 *Error", err)
	}
	got := callArgvs(t, dir)
	if !reflect.DeepEqual(got, [][]string{{"send", "--help"}}) {
		t.Fatalf("calls = %v, want only the failed help", got)
	}
}

// TestSendIfGuardRemoteQueryError: a failed `hc schema --on <machine>` is the
// decoded launch error, after the preflight probes.
func TestSendIfGuardRemoteQueryError(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		sendHelpRule(sendHelpNew),
		schemaHelpRule(schemaHelpNew),
		schemaRule(schemaDoc(enumReady, true, true)),
		{Argv: []string{"schema", "--on", "m1", "--json"}, Code: 4, Stderr: `{"error":{"kind":"machine_unknown","message":"no such machine m1"}}`},
	})
	_, err := New(env).SendIfGuard(context.Background(), "m1")
	var e *Error
	if !errors.As(err, &e) || e.Exit != 4 || e.Kind != "machine_unknown" {
		t.Fatalf("err = %v, want the decoded exit-4 *Error", err)
	}
	got := callArgvs(t, dir)
	want := [][]string{{"send", "--help"}, {"schema", "--help"}, {"schema", "--json"}, {"schema", "--on", "m1", "--json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestChooseGuard is the selection rule in isolation.
func TestChooseGuard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parser  []string
		serving []string
		want    string
		wantErr bool
	}{
		{"both ready", []string{"idle", "working", "ready"}, []string{"idle", "working", "ready"}, GuardReady, false},
		{"parser ready, serving idle", []string{"idle", "working", "ready"}, []string{"idle", "working"}, GuardIdle, false},
		{"parser idle, serving ready", []string{"idle", "working"}, []string{"idle", "working", "ready"}, GuardIdle, false},
		{"both idle only", []string{"idle"}, []string{"idle"}, GuardIdle, false},
		{"no common word", []string{"working"}, []string{"idle", "working", "ready"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseGuard(tc.parser, tc.serving)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("chooseGuard = %q, want the refusal", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("chooseGuard = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestSendIfHelpValues parses the --if possible-values section in isolation:
// the old and the new shapes, and the refusal shapes.
func TestSendIfHelpValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		help    string
		want    []string
		wantErr bool
	}{
		{"old shape", sendHelpOld, []string{"idle", "working"}, false},
		{"new shape", sendHelpNew, []string{"idle", "working", "ready"}, false},
		{"no --if option", "Usage: hc send [OPTIONS]\n\nOptions:\n      --json  JSON output\n", nil, true},
		{"--if without possible values", "Options:\n      --if <STATUS>  Only type if the status is exactly this\n", nil, true},
		{"unclosed possible values section", "Options:\n      --if <STATUS>  [possible values: idle, working\n", nil, true},
		{"empty word in possible values", "Options:\n      --if <STATUS>  [possible values: idle,, working]\n", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sendIfHelpValues(tc.help)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("sendIfHelpValues = %v, want the refusal", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sendIfHelpValues = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// TestSchemaOnHelp parses the --on option in isolation: the planned shape,
// and the refusal shapes.
func TestSchemaOnHelp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		help    string
		wantErr bool
	}{
		{"planned shape", schemaHelpNew, false},
		{"no --on option", schemaHelpOld, true},
		{"--on without a value", "Options:\n      --on  Discover on the named machine\n      --json  JSON output\n", true},
		{"duplicated --on", "Options:\n      --on <MACHINE>  Discover on the named machine\n      --on <MACHINE>  Again\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := schemaOnHelp(tc.help)
			if tc.wantErr && err == nil {
				t.Fatal("schemaOnHelp = nil, want the refusal")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("schemaOnHelp: %v", err)
			}
		})
	}
}
