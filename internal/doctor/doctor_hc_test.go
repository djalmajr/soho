package doctor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// hcExperimentalWarning is the warning every transport=hc doctor line is
// followed by (DJA-603 stage 2a).
const hcExperimentalWarning = "transport=hc is experimental: only doctor reads it so far; spawn, dispatch and wait still use herdr"

// doctorHcJSON is the fake hc 0.1.14 output of the capability steps, the
// strings of hc 0.1.14 as the brief records them.
const (
	doctorHcStatusJSON = `{"core":{"id":"cf7e6d","name":"run2biz"},"finished":2,"installed_by":null,"live":1,"pid":96382,"uptime_ms":57813348,"version":"0.1.14"}`
	doctorHcHelloOK    = `{"id":1,"jsonrpc":"2.0","result":{"core":{"id":"cf7e6d","name":"run2biz"},"features":["framed","attach","events","status","shell"],"framed":false,"generation":1,"methods":["hello","server.status"],"version":"0.1.14"}}`
	doctorHcHelloGen2  = `{"error":{"code":-32000,"data":{"hint":"this core speaks generation 1","kind":"unsupported_generation"},"message":"protocol generation 2 not supported"},"id":1,"jsonrpc":"2.0"}`
)

// doctorHcEnv builds a DoctorCheck environment with bin on PATH (where the
// fake hc may live), SOHO_FAKECLI_CONFIG pointing at that fake, no
// HERDR_ENV (no Herdr context checks run) and no herdr in PATH (the herdr
// version check warns without running).
func doctorHcEnv(t *testing.T, bin string) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	env := platform.Env{
		"HOME": root, "USERPROFILE": root, "PATH": bin,
		"SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root,
		"SOHO_FAKECLI_CONFIG": bin,
	}
	return env, root
}

// doctorHcCtx is the doctor config with the transport key at value, or
// absent when value is "".
func doctorHcCtx(transport string) *core.Config {
	entries := map[string]core.ConfigEntry{}
	if transport != "" {
		entries["transport"] = core.ConfigEntry{Value: transport}
	}
	return &core.Config{Entries: entries}
}

// doctorHcLines returns the doctor lines the hc transport check owns: the
// ones that mention transport=hc or start, after the ok/warn prefix, with
// hc.
func doctorHcLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		rest := strings.TrimPrefix(line, "ok     ")
		rest = strings.TrimPrefix(rest, "warn   ")
		if strings.Contains(rest, "transport=hc") || strings.HasPrefix(rest, "hc ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// hcCallCount is how many times the fake hc in bin ran; a missing calls
// log is zero.
func hcCallCount(t *testing.T, bin string) int {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read hc calls: %v", err)
	}
	return len(calls)
}

// TestDoctorDefaultTransportRunsNoHc: with a recording fake hc on PATH the
// default transport (the key absent, and transport=herdr set explicitly)
// prints no hc line and the fake records zero calls.
func TestDoctorDefaultTransportRunsNoHc(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"absent transport key", ""},
		{"explicit transport=herdr", "herdr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{{AnyArgs: true, Stdout: "hc 0.1.14\n"}}); err != nil {
				t.Fatal(err)
			}
			env, root := doctorHcEnv(t, bin)
			var out strings.Builder
			DoctorCheck(doctorHcCtx(tc.key), env, root, &out)
			if lines := doctorHcLines(out.String()); len(lines) != 0 {
				t.Fatalf("hc lines printed: %v", lines)
			}
			if n := hcCallCount(t, bin); n != 0 {
				t.Fatalf("hc calls = %d, want 0", n)
			}
		})
	}
}

// TestDoctorHcTransportLines is one test per transport=hc doctor line,
// asserting the exact text and the experimental warning in every case,
// plus how many probe steps the fake recorded before the probe stopped.
func TestDoctorHcTransportLines(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rules     []fakecli.Rule // nil = no hc on PATH at all
		key       string
		want      []string
		wantCalls int
	}{
		{
			name: "ok",
			rules: []fakecli.Rule{
				{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
				{Argv: []string{"server", "status", "--json"}, Stdout: doctorHcStatusJSON + "\n"},
				{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK + "\n"},
			},
			key: "hc",
			want: []string{
				"ok     hc 0.1.14, core run2biz running, protocol generation 1",
				"warn   " + hcExperimentalWarning,
			},
			wantCalls: 3,
		},
		{
			name:      "hc not in PATH",
			rules:     nil,
			key:       "hc",
			want:      []string{"warn   transport=hc but the hc CLI is not in PATH", "warn   " + hcExperimentalWarning},
			wantCalls: 0,
		},
		{
			name: "older than MinVersion",
			rules: []fakecli.Rule{
				{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.13\n"},
				{Argv: []string{"server", "status", "--json"}, Stdout: doctorHcStatusJSON + "\n"},
				{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK + "\n"},
			},
			key: "hc",
			want: []string{
				"warn   hc 0.1.13 is older than 0.1.14, the oldest version this soho supports with transport=hc: update hc",
				"warn   " + hcExperimentalWarning,
			},
			wantCalls: 1,
		},
		{
			name: "core unavailable with hint",
			rules: []fakecli.Rule{
				{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
				{Argv: []string{"server", "status", "--json"}, Code: 1, Stderr: `{"error":{"kind":"blocked","message":"the core is blocked","hint":"unblock it"}}` + "\n"},
				{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK + "\n"},
			},
			key: "hc",
			want: []string{
				"warn   hc core unavailable: blocked: the core is blocked (unblock it)",
				"warn   " + hcExperimentalWarning,
			},
			wantCalls: 2,
		},
		{
			name: "core unavailable without hint",
			rules: []fakecli.Rule{
				{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
				{Argv: []string{"server", "status", "--json"}, Code: 1, Stderr: `{"error":{"kind":"blocked","message":"the core is blocked"}}` + "\n"},
				{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK + "\n"},
			},
			key: "hc",
			want: []string{
				"warn   hc core unavailable: blocked: the core is blocked",
				"warn   " + hcExperimentalWarning,
			},
			wantCalls: 2,
		},
		{
			name: "another protocol generation",
			rules: []fakecli.Rule{
				{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
				{Argv: []string{"server", "status", "--json"}, Stdout: doctorHcStatusJSON + "\n"},
				{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloGen2 + "\n"},
			},
			key: "hc",
			want: []string{
				"warn   hc core speaks another protocol generation (this core speaks generation 1); this soho speaks generation 1",
				"warn   " + hcExperimentalWarning,
			},
			wantCalls: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			if tc.rules != nil {
				if _, err := fakecli.Install(t, bin, "hc", tc.rules); err != nil {
					t.Fatal(err)
				}
			}
			env, root := doctorHcEnv(t, bin)
			var out strings.Builder
			DoctorCheck(doctorHcCtx(tc.key), env, root, &out)
			got := doctorHcLines(out.String())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hc lines = %v, want %v\nfull output:\n%s", got, tc.want, out.String())
			}
			if n := hcCallCount(t, bin); n != tc.wantCalls {
				t.Fatalf("hc calls = %d, want %d", n, tc.wantCalls)
			}
		})
	}
}
