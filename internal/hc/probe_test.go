package hc

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// probeFixtures are the fake hc outputs of the capability steps, the
// strings of hc 0.1.14 as the brief records them.
const (
	probeStatusJSON  = `{"core":{"id":"cf7e6d","name":"run2biz"},"finished":2,"installed_by":null,"live":1,"pid":96382,"uptime_ms":57813348,"version":"0.1.14"}`
	probeHelloOKJSON = `{"id":1,"jsonrpc":"2.0","result":{"core":{"id":"cf7e6d","name":"run2biz"},"features":["framed","attach","events","status","shell"],"framed":false,"generation":1,"methods":["hello","server.status"],"version":"0.1.14"}}`
	probeHelloGen2   = `{"error":{"code":-32000,"data":{"hint":"this core speaks generation 1","kind":"unsupported_generation"},"message":"protocol generation 2 not supported"},"id":1,"jsonrpc":"2.0"}`
)

func versionRule(stdout string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"--version", "--json"}, Stdout: stdout}
}
func statusRule(stdout string, code int, stderr string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"server", "status", "--json"}, Stdout: stdout, Code: code, Stderr: stderr}
}
func connectRule(stdout string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"connect", "--json"}, Stdout: stdout}
}

func callArgvs(t *testing.T, dir string) [][]string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(dir, "hc.calls.jsonl"))
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	out := make([][]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Argv)
	}
	return out
}

// TestProbeSuccess is the full capability: the three steps run in order and
// fill the Capability.
func TestProbeSuccess(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		versionRule("hc 0.1.14\n"),
		statusRule(probeStatusJSON+"\n", 0, ""),
		connectRule(probeHelloOKJSON + "\n"),
	})
	cap := Probe(context.Background(), env)
	if cap.Err != nil {
		t.Fatalf("Err = %v, want nil", cap.Err)
	}
	if cap.Version != "0.1.14" || !cap.VersionOK {
		t.Fatalf("Version = %q VersionOK = %v, want 0.1.14 true", cap.Version, cap.VersionOK)
	}
	if cap.CoreName != "run2biz" {
		t.Fatalf("CoreName = %q, want run2biz", cap.CoreName)
	}
	if cap.Generation != 1 {
		t.Fatalf("Generation = %d, want 1", cap.Generation)
	}
	if !reflect.DeepEqual(cap.Methods, []string{"hello", "server.status"}) {
		t.Fatalf("Methods = %v", cap.Methods)
	}
	if !reflect.DeepEqual(cap.Features, []string{"framed", "attach", "events", "status", "shell"}) {
		t.Fatalf("Features = %v", cap.Features)
	}
	got := callArgvs(t, dir)
	want := [][]string{{"--version", "--json"}, {"server", "status", "--json"}, {"connect", "--json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestProbeOlderVersionStopsAtStepOne: a version under MinVersion stops the
// probe after step one — the version is reported, VersionOK is false, and
// no later step ran.
func TestProbeOlderVersionStopsAtStepOne(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		versionRule("hc 0.1.13\n"),
		statusRule(probeStatusJSON+"\n", 0, ""),
		connectRule(probeHelloOKJSON + "\n"),
	})
	cap := Probe(context.Background(), env)
	if cap.Err != nil {
		t.Fatalf("Err = %v, want nil", cap.Err)
	}
	if cap.Version != "0.1.13" || cap.VersionOK {
		t.Fatalf("Version = %q VersionOK = %v, want 0.1.13 false", cap.Version, cap.VersionOK)
	}
	if cap.CoreName != "" || cap.Generation != 0 {
		t.Fatalf("later steps ran: CoreName = %q Generation = %d", cap.CoreName, cap.Generation)
	}
	got := callArgvs(t, dir)
	want := [][]string{{"--version", "--json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestProbeUnparsableVersionStopsAtStepOne: `hc --version` output that is
// not `hc X.Y.Z` is a step-one failure — an *Error with Kind "" and the
// output in the message — and no later step ran.
func TestProbeUnparsableVersionStopsAtStepOne(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		versionRule("hello there\n"),
		statusRule(probeStatusJSON+"\n", 0, ""),
		connectRule(probeHelloOKJSON + "\n"),
	})
	cap := Probe(context.Background(), env)
	if cap.Err == nil {
		t.Fatal("Err = nil, want the version parse failure")
	}
	var e *Error
	if !errors.As(cap.Err, &e) {
		t.Fatalf("Err = %v, want the wrapped *Error", cap.Err)
	}
	if e.Kind != "" || !strings.Contains(e.Message, "not 'hc X.Y.Z'") || !strings.Contains(e.Message, "hello there") {
		t.Fatalf("got kind=%q message=%q", e.Kind, e.Message)
	}
	if cap.Version != "" || cap.VersionOK {
		t.Fatalf("Version = %q VersionOK = %v, want empty false", cap.Version, cap.VersionOK)
	}
	if cap.CoreName != "" {
		t.Fatalf("CoreName = %q, want empty", cap.CoreName)
	}
	got := callArgvs(t, dir)
	want := [][]string{{"--version", "--json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestProbeServerStatusErrorStopsAtStepTwo: the status step fails with its
// JSON error — the probe stops with the decoded *Error and no hello ran.
func TestProbeServerStatusErrorStopsAtStepTwo(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		versionRule("hc 0.1.14\n"),
		statusRule("", 1, `{"error":{"kind":"blocked","message":"the core is blocked","hint":"unblock it"}}`+"\n"),
		connectRule(probeHelloOKJSON + "\n"),
	})
	cap := Probe(context.Background(), env)
	if cap.Version != "0.1.14" || !cap.VersionOK {
		t.Fatalf("Version = %q VersionOK = %v", cap.Version, cap.VersionOK)
	}
	if cap.CoreName != "" || cap.Generation != 0 {
		t.Fatalf("later steps ran: CoreName = %q Generation = %d", cap.CoreName, cap.Generation)
	}
	var e *Error
	if !errors.As(cap.Err, &e) {
		t.Fatalf("Err = %v, want *Error", cap.Err)
	}
	if e.Kind != "blocked" || e.Message != "the core is blocked" || e.Hint != "unblock it" || e.Exit != 1 {
		t.Fatalf("got kind=%q message=%q hint=%q exit=%d", e.Kind, e.Message, e.Hint, e.Exit)
	}
	got := callArgvs(t, dir)
	want := [][]string{{"--version", "--json"}, {"server", "status", "--json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestProbeHelloUnsupportedGenerationStopsAtStepThree: the hello answers
// the unsupported_generation JSON-RPC error — the probe stops with the
// decoded *Error and no generation.
func TestProbeHelloUnsupportedGenerationStopsAtStepThree(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{
		versionRule("hc 0.1.14\n"),
		statusRule(probeStatusJSON+"\n", 0, ""),
		connectRule(probeHelloGen2 + "\n"),
	})
	cap := Probe(context.Background(), env)
	if cap.Version != "0.1.14" || !cap.VersionOK {
		t.Fatalf("Version = %q VersionOK = %v", cap.Version, cap.VersionOK)
	}
	if cap.CoreName != "run2biz" {
		t.Fatalf("CoreName = %q, want run2biz (step two succeeded)", cap.CoreName)
	}
	if cap.Generation != 0 {
		t.Fatalf("Generation = %d, want 0", cap.Generation)
	}
	var e *Error
	if !errors.As(cap.Err, &e) {
		t.Fatalf("Err = %v, want *Error", cap.Err)
	}
	if e.Kind != "unsupported_generation" || e.Message != "protocol generation 2 not supported" || e.Hint != "this core speaks generation 1" {
		t.Fatalf("got kind=%q message=%q hint=%q", e.Kind, e.Message, e.Hint)
	}
	got := callArgvs(t, dir)
	want := [][]string{{"--version", "--json"}, {"server", "status", "--json"}, {"connect", "--json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestHelloSendsTheGenerationOneLine is the hello wire contract: the exact
// JSON line on stdin, and the client reads generation, methods, features
// and the core name from the answer.
func TestHelloSendsTheGenerationOneLine(t *testing.T) {
	dir := t.TempDir()
	if _, err := fakecli.InstallWithOptions(t, dir, "hc", []fakecli.Rule{
		connectRule(probeHelloOKJSON + "\n"),
	}, fakecli.InstallOptions{CaptureStdin: true}); err != nil {
		t.Fatal(err)
	}
	env := hcEnvFromDir(t, dir)
	hello, err := Hello(context.Background(), env)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if hello.Generation != 1 || hello.CoreName != "run2biz" || hello.Version != "0.1.14" {
		t.Fatalf("got %+v", hello)
	}
	if !reflect.DeepEqual(hello.Methods, []string{"hello", "server.status"}) || !reflect.DeepEqual(hello.Features, []string{"framed", "attach", "events", "status", "shell"}) {
		t.Fatalf("Methods = %v Features = %v", hello.Methods, hello.Features)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(dir, "hc.calls.jsonl"))
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls = %+v, %v; want exactly one", calls, err)
	}
	if !reflect.DeepEqual(calls[0].Argv, []string{"connect", "--json"}) {
		t.Fatalf("argv = %v", calls[0].Argv)
	}
	if calls[0].Stdin != HelloLine+"\n" {
		t.Fatalf("stdin = %q, want %q", calls[0].Stdin, HelloLine+"\n")
	}
}

// TestHelloUnsupportedGenerationIsTheDecodedError: the JSON-RPC error
// answer of a core of another generation is the *Error with the data
// object's kind and hint and the error's message.
func TestHelloUnsupportedGenerationIsTheDecodedError(t *testing.T) {
	env, _ := newHCFake(t, []fakecli.Rule{connectRule(probeHelloGen2 + "\n")})
	_, err := Hello(context.Background(), env)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if e.Kind != "unsupported_generation" || e.Message != "protocol generation 2 not supported" || e.Hint != "this core speaks generation 1" {
		t.Fatalf("got kind=%q message=%q hint=%q", e.Kind, e.Message, e.Hint)
	}
}

// TestVersionParsesTheHCLine keeps the `hc X.Y.Z` parse: a stray extra
// line does not hide the version line.
func TestVersionParsesTheHCLine(t *testing.T) {
	env, _ := newHCFake(t, []fakecli.Rule{versionRule("warning: something\nhc 0.1.14\n")})
	v, err := Version(context.Background(), env)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if v != "0.1.14" {
		t.Fatalf("version = %q, want 0.1.14", v)
	}
}
