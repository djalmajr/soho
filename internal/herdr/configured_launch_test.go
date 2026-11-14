package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/launchprofile"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const configuredCapability = `{"running":true,"capabilities":{"native_agent_launch_v1":true}}`
const configuredReady = `{"id":"configured-start","result":{"type":"agent_configured_started","agent":{"name":"worker","agent":"opencode","pane_id":"p1","terminal_id":"t1","agent_status":"idle","interactive_ready":true,"launch_profile":"opencode-primary-v2"}}}`
const configuredSecret = "FIXED_SYNTHETIC_CONFIGURED_SECRET"

const configuredReuseReady = `{"result":{"type":"agent_info","agent":{"name":"worker","agent":"opencode","pane_id":"p1","terminal_id":"t1","agent_status":"idle","interactive_ready":true,"launch_profile":"opencode-primary-v2"}}}`

func TestConfiguredReuseMetadata(t *testing.T) {
	cases := []struct {
		name, body string
		allow      bool
	}{
		{"idle", configuredReuseReady, true},
		{"done", strings.Replace(configuredReuseReady, `"idle"`, `"done"`, 1), true},
		{"pending-false", strings.Replace(configuredReuseReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_pending":false`, 1), true},
		{"missing-profile", strings.Replace(configuredReuseReady, `,"launch_profile":"opencode-primary-v2"`, "", 1), false},
		{"wrong-profile", strings.Replace(configuredReuseReady, "opencode-primary-v2", "legacy", 1), false},
		{"null-profile", strings.Replace(configuredReuseReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":null`, 1), false},
		{"wrong-profile-type", strings.Replace(configuredReuseReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":true`, 1), false},
		{"nested-profile", strings.Replace(configuredReuseReady, `"launch_profile":"opencode-primary-v2"`, `"metadata":{"launch_profile":"opencode-primary-v2"}`, 1), false},
		{"wrong-type", strings.Replace(configuredReuseReady, "agent_info", "agent_configured_started", 1), false},
		{"wrong-name", strings.Replace(configuredReuseReady, `"worker"`, `"other"`, 1), false},
		{"wrong-kind", strings.Replace(configuredReuseReady, `"opencode"`, `"codex"`, 1), false},
		{"wrong-pane", strings.Replace(configuredReuseReady, `"p1"`, `"p2"`, 1), false},
		{"empty-terminal", strings.Replace(configuredReuseReady, `"t1"`, `""`, 1), false},
		{"missing-terminal", strings.Replace(configuredReuseReady, `"terminal_id":"t1",`, "", 1), false},
		{"false-terminal", strings.Replace(configuredReuseReady, `"terminal_id":"t1"`, `"terminal_id":false`, 1), false},
		{"invalid-terminal", strings.Replace(configuredReuseReady, `"t1"`, `"t\n1"`, 1), false},
		{"missing-pane", strings.Replace(configuredReuseReady, `"pane_id":"p1",`, "", 1), false},
		{"missing-name", strings.Replace(configuredReuseReady, `"name":"worker",`, "", 1), false},
		{"not-ready", strings.Replace(configuredReuseReady, `"interactive_ready":true`, `"interactive_ready":false`, 1), false},
		{"missing-ready", strings.Replace(configuredReuseReady, `"interactive_ready":true,`, "", 1), false},
		{"working", strings.Replace(configuredReuseReady, `"idle"`, `"working"`, 1), false},
		{"pending", strings.Replace(configuredReuseReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_pending":true`, 1), false},
		{"pending-null", strings.Replace(configuredReuseReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_pending":null`, 1), false},
		{"error-envelope", strings.Replace(configuredReuseReady, `"result":`, `"error":null,"result":`, 1), false},
		{"duplicate-profile", strings.Replace(configuredReuseReady, `"launch_profile":`, `"launch_profile":"legacy","launch_profile":`, 1), false},
		{"duplicate-escaped", strings.Replace(configuredReuseReady, `"interactive_ready":true`, `"interactive_ready":false,"interactive_\u0072eady":true`, 1), false},
		{"trailing", configuredReuseReady + ` {}`, false},
		{"invalid-utf8", strings.Replace(configuredReuseReady, "t1", string([]byte{0xff}), 1), false},
		{"native-zero-empty", `{}`, false},
		{"null", `null`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := configuredRequest(t)
			b := New(nil).Terminal.(*backend)
			n := 0
			b.runner = func(exe string, argv []string, o platform.RunOptions) platform.RunResult {
				n++
				if exe != "herdr" || !reflect.DeepEqual(argv, []string{"agent", "get", "p1"}) || o.Input != "" || !o.StrictOutputUTF8 || o.MergeOutput || o.OutputFiles || o.Context == nil || o.TimeoutMs <= 0 {
					t.Fatalf("unexpected observation %s %q %+v", exe, argv, o)
				}
				z := 0
				return platform.RunResult{Status: &z, Stdout: tc.body, Stderr: configuredSecret, Resolved: configuredSecret}
			}
			got := b.PreflightConfiguredReuse(context.Background(), s, transport.Call{Timeout: time.Second})
			if n != 1 || got.Valid != tc.allow || got.Value != tc.allow || (got.Err == nil) != tc.allow || got.Process.Status == nil || *got.Process.Status != 0 {
				t.Fatalf("calls=%d reply=%+v", n, got)
			}
			assertConfiguredPrivate(t, transport.Reply[string]{Process: got.Process, Err: got.Err})
		})
	}
}

func TestConfiguredReuseInputAndProcess(t *testing.T) {
	for _, change := range []func(*transport.Start){func(s *transport.Start) { s.Name = "" }, func(s *transport.Start) { s.PaneID = "" }, func(s *transport.Start) { s.Kind = "codex" }, func(s *transport.Start) { s.LaunchProfile = "legacy" }, func(s *transport.Start) { s.Args = nil }, func(s *transport.Start) { s.Timeout = "0" }, func(s *transport.Start) { s.EnvOverrides["OPENCODE_CONFIG_CONTENT"] = "invalid" }} {
		s := configuredRequest(t)
		change(&s)
		b := New(nil).Terminal.(*backend)
		n := 0
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult { n++; return platform.RunResult{} }
		got := b.PreflightConfiguredReuse(context.Background(), s, transport.Call{})
		if n != 0 || got.Valid || got.Value || got.Err == nil || got.Err.Kind != transport.FailureProtocol {
			t.Fatalf("calls=%d reply=%+v", n, got)
		}
	}
	z, seven := 0, 7
	for _, r := range []platform.RunResult{{Status: &seven}, {Status: &z, TimedOut: true}, {NotFound: true}, {Error: configuredSecret}, {Status: &z, Signal: configuredSecret}} {
		b := New(nil).Terminal.(*backend)
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
			r.Stdout = configuredReuseReady
			r.Stderr = configuredSecret
			return r
		}
		got := b.PreflightConfiguredReuse(context.Background(), configuredRequest(t), transport.Call{})
		if got.Valid || got.Value || got.Err == nil || got.Process.NotFound != r.NotFound || got.Process.TimedOut != r.TimedOut || !reflect.DeepEqual(got.Process.Status, r.Status) {
			t.Fatalf("reply=%+v", got)
		}
		assertConfiguredPrivate(t, transport.Reply[string]{Process: got.Process, Err: got.Err})
	}
	b := New(nil).Terminal.(*backend)
	n := 0
	b.runner = func(string, []string, platform.RunOptions) platform.RunResult { n++; return platform.RunResult{} }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := b.PreflightConfiguredReuse(ctx, configuredRequest(t), transport.Call{})
	if n != 0 || got.Valid || got.Err == nil {
		t.Fatalf("cancelled observation=%+v calls=%d", got, n)
	}
}

func TestConfiguredReuseRealCLI(t *testing.T) {
	for _, outcome := range []string{"ready", "native-failure", "stdout-utf8", "stderr-utf8"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			rule := fakecli.Rule{Argv: []string{"agent", "get", "p1"}, Stdout: configuredReuseReady, Stderr: configuredSecret}
			switch outcome {
			case "native-failure":
				rule.Code = 7
			case "stdout-utf8":
				rule.StdoutBytes = []byte{0xff}
			case "stderr-utf8":
				rule.StderrBytes = []byte{0xff}
			}
			if _, err := fakecli.InstallWithOptions(t, root, "herdr", []fakecli.Rule{rule}, fakecli.InstallOptions{CaptureStdin: true, CaptureEnv: []string{"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR"}}); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for _, e := range fakecli.Env([]string{"HOME=" + root, "USERPROFILE=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + root}, root) {
				k, v, _ := strings.Cut(e, "=")
				env[k] = v
			}
			got := New(env).Terminal.(transport.ConfiguredLaunchReuse).PreflightConfiguredReuse(context.Background(), configuredRequest(t), transport.Call{Timeout: 5 * time.Second})
			allow := outcome == "ready"
			if got.Valid != allow || got.Value != allow || (got.Err == nil) != allow {
				t.Fatalf("reply=%+v", got)
			}
			assertConfiguredPrivate(t, transport.Reply[string]{Process: got.Process, Err: got.Err})
			calls, err := fakecli.ReadCalls(filepath.Join(root, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, []string{"agent", "get", "p1"}) || calls[0].Stdin != "" || len(calls[0].Env) != 0 {
				t.Fatalf("calls=%+v", calls)
			}
		})
	}
}

func configuredRequest(t *testing.T) transport.Start {
	t.Helper()
	p, err := launchprofile.ResolveOpenCode("provider/model#variant", `{"synthetic":"`+configuredSecret+`"}`, "/synthetic directory/'quoted'")
	if err != nil {
		t.Fatal(err)
	}
	return transport.Start{Name: "worker", Kind: "opencode", PaneID: "p1", Timeout: "90000", LaunchProfile: p.Name, Args: p.ExtraArgs, EnvOverrides: p.EnvOverrides}
}

func TestConfiguredCapability(t *testing.T) {
	for _, body := range []string{`{}`, `{"running":false,"capabilities":{"native_agent_launch_v1":true}}`, `{"running":true,"capabilities":{"native_agent_launch_v1":false}}`, `{"running":true,"capabilities":{"native_agent_launch_v1":"true"}}`, `{"result":` + configuredCapability + `}`, `{"running":true,"running":true,"capabilities":{"native_agent_launch_v1":true}}`, `{"running":true,"capabilities":{"native_agent_launch_v1":true,"native_agent_launch_v1":true}}`, configuredCapability + ` {}`, `null`, `{"running":true,"capabilities":null}`} {
		t.Run(body, func(t *testing.T) {
			n := 0
			b := New(nil).Terminal.(*backend)
			b.runner = func(_ string, a []string, o platform.RunOptions) platform.RunResult {
				n++
				if strings.Join(a, " ") != "status server --json" {
					t.Fatalf("unexpected start: %v", a)
				}
				z := 0
				return platform.RunResult{Status: &z, Stdout: body}
			}
			got := b.Start(context.Background(), configuredRequest(t), transport.Call{})
			if got.Valid || got.Err == nil || n != 1 || got.Process.Status == nil || *got.Process.Status != 0 {
				t.Fatalf("calls=%d got=%+v", n, got)
			}
		})
	}
}

func TestConfiguredInvalidProfile(t *testing.T) {
	for _, change := range []func(*transport.Start){func(s *transport.Start) { s.LaunchProfile = "" }, func(s *transport.Start) { s.Kind = "OpenCode" }, func(s *transport.Start) { s.Args = nil }, func(s *transport.Start) { delete(s.EnvOverrides, "OPENCODE_CONFIG_CONTENT") }, func(s *transport.Start) { s.Timeout = "0" }, func(s *transport.Start) { s.Timeout = "18446744073709551616" }, func(s *transport.Start) { s.Name = "bad\nname" }} {
		s := configuredRequest(t)
		change(&s)
		n := 0
		b := New(nil).Terminal.(*backend)
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult { n++; return platform.RunResult{} }
		got := b.Start(context.Background(), s, transport.Call{})
		if got.Valid || got.Err == nil || n != 0 {
			t.Fatalf("calls=%d got=%+v", n, got)
		}
	}
}

func assertConfiguredPrivate(t *testing.T, got transport.Reply[string]) {
	t.Helper()
	if strings.Contains(fmt.Sprintf("%+v %+v", got, got.Err), configuredSecret) {
		t.Fatal("synthetic sentinel leaked")
	}
	if got.Process.Stdout != "" || got.Process.Stderr != "" || got.Process.Error != "" || got.Process.Resolved != "" || got.Process.Signal != "" || (got.Err != nil && got.Err.Cause != nil) {
		t.Fatalf("public process fields exposed: %+v", got)
	}
}

func TestConfiguredProbeProcessFailures(t *testing.T) {
	one := 1
	zero := 0
	for _, r := range []platform.RunResult{{Status: &one, Stdout: configuredCapability}, {Status: &zero, Stdout: configuredCapability, TimedOut: true}, {NotFound: true}, {TimedOut: true}, {Error: configuredSecret}, {Status: &zero, Signal: configuredSecret}, {Status: &zero, Stdout: configuredCapability, Error: configuredSecret}} {
		n := 0
		b := New(nil).Terminal.(*backend)
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
			n++
			r.Stderr = configuredSecret
			r.Resolved = configuredSecret
			return r
		}
		got := b.Start(context.Background(), configuredRequest(t), transport.Call{})
		assertConfiguredPrivate(t, got)
		if got.Valid || got.Err == nil || n != 1 || got.Process.NotFound != r.NotFound || got.Process.TimedOut != r.TimedOut {
			t.Fatalf("calls=%d got=%+v", n, got)
		}
	}
}

func TestConfiguredReadyEnvelope(t *testing.T) {
	cases := []struct {
		name, body string
		pass       bool
	}{
		{"idle", configuredReady, true},
		{"done", strings.Replace(configuredReady, `"idle"`, `"done"`, 1), true},
		{"untrusted metadata", strings.Replace(configuredReady, `"terminal_id":"t1"`, `"terminal_id":"t1","cwd":"`+configuredSecret+`"`, 1), true},
		{"literal replacement metadata", strings.Replace(configuredReady, `"terminal_id":"t1"`, `"terminal_id":"t1","cwd":"�"`, 1), true},
		{"wrong id", strings.Replace(configuredReady, "configured-start", "wrong", 1), false},
		{"wrong type", strings.Replace(configuredReady, "agent_configured_started", "agent_started", 1), false},
		{"wrong name", strings.Replace(configuredReady, "worker", "other", 1), false},
		{"wrong kind", strings.Replace(configuredReady, `"opencode"`, `"codex"`, 1), false},
		{"wrong pane", strings.Replace(configuredReady, `"p1"`, `"p2"`, 1), false},
		{"empty terminal", strings.Replace(configuredReady, `"t1"`, `""`, 1), false},
		{"long terminal", strings.Replace(configuredReady, `"t1"`, `"`+strings.Repeat("t", 257)+`"`, 1), false},
		{"missing ready", strings.Replace(configuredReady, `,"interactive_ready":true`, "", 1), false},
		{"false ready", strings.Replace(configuredReady, `"interactive_ready":true`, `"interactive_ready":false`, 1), false},
		{"working", strings.Replace(configuredReady, `"idle"`, `"working"`, 1), false},
		{"pending", strings.Replace(configuredReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_pending":true`, 1), false},
		{"bad pending", strings.Replace(configuredReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_pending":"false"`, 1), false},
		{"missing profile", strings.Replace(configuredReady, `,"launch_profile":"opencode-primary-v2"`, "", 1), false},
		{"null profile", strings.Replace(configuredReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":null`, 1), false},
		{"empty profile", strings.Replace(configuredReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":""`, 1), false},
		{"unknown profile", strings.Replace(configuredReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":"unknown"`, 1), false},
		{"wrong profile", strings.Replace(configuredReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":"opencode-primary-v3"`, 1), false},
		{"wrong profile type", strings.Replace(configuredReady, `"launch_profile":"opencode-primary-v2"`, `"launch_profile":true`, 1), false},
		{"nested profile", strings.Replace(configuredReady, `"launch_profile":"opencode-primary-v2"`, `"metadata":{"launch_profile":"opencode-primary-v2"}`, 1), false},
		{"duplicate profile", strings.Replace(configuredReady, `"launch_profile":`, `"launch_profile":"opencode-primary-v2","launch_profile":`, 1), false},
		{"duplicate escaped profile", strings.Replace(configuredReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_\u0070rofile":"opencode-primary-v2"`, 1), false},
		{"duplicate id", strings.Replace(configuredReady, `"id":`, `"id":"configured-start","id":`, 1), false},
		{"duplicate state", strings.Replace(configuredReady, `"agent_status":`, `"agent_status":"working","agent_status":`, 1), false},
		{"duplicate escaped", strings.Replace(configuredReady, `"interactive_ready":true`, `"interactive_ready":false,"interactive_\u0072eady":true`, 1), false},
		{"extra frame", configuredReady + "\n{}", false},
		{"malformed", configuredSecret, false},
		{"invalid utf8", strings.Replace(configuredReady, "t1", string([]byte{0xff}), 1), false},
		{"error and result", strings.Replace(configuredReady, `"result":`, `"error":{"code":"agent_not_ready","message":"`+configuredSecret+`"},"result":`, 1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := 0
			b := New(nil).Terminal.(*backend)
			b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
				n++
				body := configuredCapability
				if n == 2 {
					body = c.body
				}
				z := 0
				return platform.RunResult{Status: &z, Stdout: body, Stderr: configuredSecret, Resolved: configuredSecret}
			}
			got := b.Start(context.Background(), configuredRequest(t), transport.Call{})
			assertConfiguredPrivate(t, got)
			if got.Valid != c.pass || (got.Err == nil) != c.pass || n != 2 || got.Process.Status == nil || *got.Process.Status != 0 {
				t.Fatalf("calls=%d got=%+v", n, got)
			}
			if c.pass && got.Value != `{"result":{"started":true}}` {
				t.Fatalf("unexpected public success: %s", got.Value)
			}
			if !c.pass && (got.Value != "" || got.Err.Kind != transport.FailureRunOutcomeUnknown) {
				t.Fatalf("invalid outcome: %+v", got)
			}
		})
	}
}

func TestConfiguredStartProcessAndCodes(t *testing.T) {
	zero := 0
	one := 1
	for _, r := range []platform.RunResult{{Status: &zero, Stdout: configuredReady, TimedOut: true}, {NotFound: true}, {TimedOut: true}, {Signal: configuredSecret}, {Error: configuredSecret}, {Status: &one, Stdout: configuredSecret, Stderr: configuredSecret}, {Status: &zero, Stdout: configuredReady, Error: configuredSecret}} {
		n := 0
		b := New(nil).Terminal.(*backend)
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
			n++
			if n == 1 {
				return platform.RunResult{Status: &zero, Stdout: configuredCapability}
			}
			return r
		}
		got := b.Start(context.Background(), configuredRequest(t), transport.Call{})
		assertConfiguredPrivate(t, got)
		if got.Valid || got.Err == nil || n != 2 || got.Process.NotFound != r.NotFound || got.Process.TimedOut != r.TimedOut {
			t.Fatalf("calls=%d got=%+v", n, got)
		}
		if !r.NotFound && got.Err.Kind != transport.FailureRunOutcomeUnknown {
			t.Fatalf("unsafe failure classification: %+v", got)
		}
	}
	for _, code := range []string{"agent_not_ready", "agent_pane_busy", "untrusted_" + configuredSecret} {
		for _, id := range []string{"configured-start", "wrong"} {
			for _, stderr := range []bool{false, true} {
				n := 0
				b := New(nil).Terminal.(*backend)
				b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
					n++
					if n == 1 {
						return platform.RunResult{Status: &zero, Stdout: configuredCapability}
					}
					r := platform.RunResult{Status: &one}
					body := `{"id":"` + id + `","error":{"code":"` + code + `","message":"` + configuredSecret + `"}}`
					if stderr {
						r.Stderr = body
					} else {
						r.Stdout = body
					}
					return r
				}
				got := b.Start(context.Background(), configuredRequest(t), transport.Call{})
				assertConfiguredPrivate(t, got)
				want := ""
				if id == "configured-start" && code != "untrusted_"+configuredSecret {
					want = code
				}
				if got.Valid || got.Err == nil || got.Err.Code != want {
					t.Fatalf("got=%+v want code=%s", got, want)
				}
			}
		}
	}
}

func TestConfiguredRealCLI(t *testing.T) {
	argv := []string{"agent", "start-configured", "worker", "--kind", "opencode", "--pane", "p1", "--timeout", "18446744073709551615"}
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			root := t.TempDir()
			rule := fakecli.Rule{Argv: argv, Stdout: strings.Replace(configuredReady, `"terminal_id":"t1"`, `"terminal_id":"t1","cwd":"`+configuredSecret+`"`, 1), Stderr: configuredSecret}
			if failure {
				rule.Code = 7
				rule.Stdout = configuredSecret
			}
			_, err := fakecli.InstallWithOptions(t, root, "herdr", []fakecli.Rule{{Argv: []string{"status", "server", "--json"}, Stdout: configuredCapability}, rule}, fakecli.InstallOptions{CaptureStdin: true, CaptureEnv: []string{"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR", "OPENCODE_DISABLE_AUTOUPDATE", "SYNTHETIC_INHERITED"}})
			if err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for _, entry := range fakecli.Env([]string{"HOME=" + root, "USERPROFILE=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + root, "SYNTHETIC_INHERITED=parent-only", "OPENCODE_CONFIG_CONTENT=parent-config"}, root) {
				k, v, _ := strings.Cut(entry, "=")
				env[k] = v
			}
			beforeEnv := env.Clone()
			s := configuredRequest(t)
			s.Timeout = "18446744073709551615"
			s.Args = []string{"--auto", "--standalone"}
			beforeArgs := append([]string(nil), s.Args...)
			beforeOverrides := map[string]string{}
			for k, v := range s.EnvOverrides {
				beforeOverrides[k] = v
			}
			terminal := New(env).Terminal
			early := s
			early.Name, early.PaneID = "", ""
			pre := terminal.(transport.ConfiguredLaunchPreflight).PreflightConfiguredStart(context.Background(), early, transport.Call{Timeout: 10 * time.Second})
			if !pre.Valid || !pre.Value || pre.Err != nil || pre.Process.Status == nil || *pre.Process.Status != 0 {
				t.Fatalf("pre-allocation preflight=%+v", pre)
			}
			missing := terminal.Start(context.Background(), early, transport.Call{})
			if missing.Valid || missing.Err == nil || missing.Err.Kind != transport.FailureProtocol || missing.Process.Status != nil {
				t.Fatalf("unbound Start=%+v", missing)
			}
			got := terminal.Start(context.Background(), s, transport.Call{Timeout: 10 * time.Second})
			assertConfiguredPrivate(t, got)
			if got.Valid == failure || (got.Err != nil) != failure || got.Process.Status == nil {
				t.Fatalf("got=%+v failure=%v", got, failure)
			}
			wantStatus := 0
			if failure {
				wantStatus = 7
			}
			if *got.Process.Status != wantStatus {
				t.Fatalf("native status=%d", *got.Process.Status)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(root, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 3 || !reflect.DeepEqual(calls[0].Argv, []string{"status", "server", "--json"}) || !reflect.DeepEqual(calls[1].Argv, []string{"status", "server", "--json"}) || !reflect.DeepEqual(calls[2].Argv, argv) || calls[0].Stdin != "" || calls[1].Stdin != "" {
				t.Fatalf("unexpected CLI calls: %+v", calls)
			}
			for _, c := range calls {
				if !reflect.DeepEqual(c.Env, map[string]string{"SYNTHETIC_INHERITED": "parent-only", "OPENCODE_CONFIG_CONTENT": "parent-config"}) {
					t.Fatalf("override leaked into CLI environment: %+v", c.Env)
				}
			}
			input := calls[2].Stdin
			if !strings.HasPrefix(input, "HERDR_CONFIGURED_START_V1\n") || !strings.HasSuffix(input, "\n") || strings.Count(input, "\n") != 2 || len(input) > 524288 {
				t.Fatal("invalid private frame")
			}
			var frame struct {
				Method string
				Params struct {
					Name, Kind string
					Pane       string      `json:"pane_id"`
					Timeout    json.Number `json:"timeout_ms"`
					Args       []string
					Env        [][2]string
				}
			}
			if err := json.Unmarshal([]byte(strings.Split(input, "\n")[1]), &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Method != "agent.start_configured" || frame.Params.Name != "worker" || frame.Params.Kind != "opencode" || frame.Params.Pane != "p1" || frame.Params.Timeout != "18446744073709551615" || !reflect.DeepEqual(frame.Params.Args, []string{"--auto", "--standalone"}) {
				t.Fatalf("frame lost request: %+v", frame)
			}
			wantPairs := [][2]string{{"OPENCODE_CONFIG_CONTENT", beforeOverrides["OPENCODE_CONFIG_CONTENT"]}, {"OPENCODE_CONFIG_DIR", beforeOverrides["OPENCODE_CONFIG_DIR"]}, {"OPENCODE_DISABLE_AUTOUPDATE", "1"}}
			if !reflect.DeepEqual(frame.Params.Env, wantPairs) || strings.Contains(input, "parent-only") || strings.Contains(input, "parent-config") {
				t.Fatal("explicit environment was not preserved and sorted")
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal([]byte(strings.Split(input, "\n")[1]), &wire); err != nil {
				t.Fatal(err)
			}
			var params map[string]json.RawMessage
			if err := json.Unmarshal(wire["params"], &params); err != nil {
				t.Fatal(err)
			}
			if len(wire) != 2 || len(params) != 7 {
				t.Fatal("unexpected private metadata")
			}
			wantKeys := []string{"args", "env", "kind", "launch_profile", "name", "pane_id", "timeout_ms"}
			gotKeys := make([]string, 0, len(params))
			for key := range params {
				gotKeys = append(gotKeys, key)
			}
			sort.Strings(gotKeys)
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				t.Fatalf("unexpected private params: %v", gotKeys)
			}
			if string(params["launch_profile"]) != `"opencode-primary-v2"` {
				t.Fatalf("forwarded declaration=%s", params["launch_profile"])
			}
			if !reflect.DeepEqual(env, beforeEnv) || !reflect.DeepEqual(s.Args, beforeArgs) || !reflect.DeepEqual(s.EnvOverrides, beforeOverrides) {
				t.Fatal("caller input mutated")
			}
		})
	}
}

// TestConfiguredFrameForwardsLaunchProfile decodes the bytes the configured
// start actually hands to the child, so the forwarded declaration is checked
// against the emitted frame rather than against the builder's own helper.
func TestConfiguredFrameForwardsLaunchProfile(t *testing.T) {
	cases := []struct {
		name   string
		change func(*transport.Start)
	}{
		{"canonical", func(*transport.Start) {}},
		{"reordered args", func(s *transport.Start) { s.Args = []string{"--standalone", "--auto"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			s := configuredRequest(t)
			s.Timeout = "60000"
			c.change(&s)
			argv := []string{"agent", "start-configured", "worker", "--kind", "opencode", "--pane", "p1", "--timeout", "60000"}
			_, err := fakecli.InstallWithOptions(t, root, "herdr", []fakecli.Rule{
				{Argv: []string{"status", "server", "--json"}, Stdout: configuredCapability},
				{Argv: argv, Stdout: configuredReady},
			}, fakecli.InstallOptions{CaptureStdin: true, CaptureEnv: []string{"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR", "OPENCODE_DISABLE_AUTOUPDATE", "SYNTHETIC_INHERITED"}})
			if err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for _, entry := range fakecli.Env([]string{"HOME=" + root, "USERPROFILE=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + root, "SYNTHETIC_INHERITED=parent-only"}, root) {
				k, v, _ := strings.Cut(entry, "=")
				env[k] = v
			}
			got := New(env).Terminal.Start(context.Background(), s, transport.Call{Timeout: 10 * time.Second})
			if !got.Valid || got.Err != nil {
				t.Fatalf("start=%+v", got)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(root, "herdr.calls.jsonl"))
			if err != nil || len(calls) != 2 {
				t.Fatalf("calls=%d err=%v", len(calls), err)
			}
			input := calls[1].Stdin
			const prefix = "HERDR_CONFIGURED_START_V1\n"
			if !strings.HasPrefix(input, prefix) || !strings.HasSuffix(input, "\n") || strings.Count(input, "\n") != 2 {
				t.Fatalf("framing lost: %q", input)
			}
			body := strings.TrimSuffix(strings.TrimPrefix(input, prefix), "\n")
			if len(input) != len(prefix)+len(body)+1 || len(input) > configuredFrameLimit {
				t.Fatalf("frame size=%d body=%d", len(input), len(body))
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &wire); err != nil {
				t.Fatal(err)
			}
			var params map[string]json.RawMessage
			if err := json.Unmarshal(wire["params"], &params); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(params))
			for key := range params {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, []string{"args", "env", "kind", "launch_profile", "name", "pane_id", "timeout_ms"}) {
				t.Fatalf("private params=%v", keys)
			}
			var profile string
			if err := json.Unmarshal(params["launch_profile"], &profile); err != nil {
				t.Fatalf("declaration is not a JSON string: %s", params["launch_profile"])
			}
			if want := launchprofile.OpenCodePrimaryProfile; want != "opencode-primary-v2" {
				t.Fatalf("resolver profile renamed to %q", want)
			}
			if profile != launchprofile.OpenCodePrimaryProfile {
				t.Fatalf("forwarded declaration=%q", profile)
			}
			if strings.Count(input, "opencode-primary-v2") != 1 {
				t.Fatal("declaration not forwarded exactly once")
			}
			if !strings.Contains(body, `"launch_profile":"opencode-primary-v2"`) {
				t.Fatalf("declaration not emitted as written: %s", params["launch_profile"])
			}
			var args []string
			if err := json.Unmarshal(params["args"], &args); err != nil || !reflect.DeepEqual(args, s.Args) {
				t.Fatalf("args=%v err=%v", args, err)
			}
			var pairs [][2]string
			if err := json.Unmarshal(params["env"], &pairs); err != nil {
				t.Fatal(err)
			}
			overrides := make([]string, 0, len(s.EnvOverrides))
			for key := range s.EnvOverrides {
				overrides = append(overrides, key)
			}
			sort.Strings(overrides)
			want := make([][2]string, 0, len(overrides))
			for _, key := range overrides {
				want = append(want, [2]string{key, s.EnvOverrides[key]})
			}
			if !reflect.DeepEqual(pairs, want) || strings.Contains(input, "parent-only") || len(pairs) != 3 {
				t.Fatalf("env=%v", pairs)
			}
		})
	}
	// An untagged configured start is refused by the shared validator, so it
	// never reaches a frame carrying an empty or inferred declaration.
	untagged := configuredRequest(t)
	untagged.LaunchProfile = ""
	if frame, ok := configuredFrame(untagged); ok || frame != "" {
		t.Fatalf("untagged configured start framed: %q", frame)
	}
	// Ordinary starts bypass this builder entirely: no frame, no effect.
	for _, ordinary := range []transport.Start{{}, {Kind: "opencode", Args: []string{"--standalone"}, EnvOverrides: map[string]string{}}} {
		n := 0
		b := New(nil).Terminal.(*backend)
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
			n++
			return platform.RunResult{}
		}
		if pre := b.PreflightConfiguredStart(context.Background(), ordinary, transport.Call{}); !pre.Valid || !pre.Value || pre.Err != nil || n != 0 {
			t.Fatalf("ordinary preflight=%+v calls=%d", pre, n)
		}
		if frame, ok := configuredFrame(ordinary); ok || frame != "" {
			t.Fatal("ordinary start framed")
		}
	}
}

func TestConfiguredOptionsAndPreflight(t *testing.T) {
	s := configuredRequest(t)
	n := 0
	env := platform.Env{"SYNTHETIC_INHERITED": "parent-only"}
	b := New(env).Terminal.(*backend)
	b.runner = func(exe string, argv []string, o platform.RunOptions) platform.RunResult {
		n++
		if exe != "herdr" || !o.StrictOutputUTF8 || o.MergeOutput || o.OutputFiles || o.Cwd != "" || o.OnStart != nil || !reflect.DeepEqual(o.Env, env) || o.Context == nil || o.TimeoutMs <= 0 {
			t.Fatalf("unsafe options: %+v", o)
		}
		if n <= 2 {
			if !reflect.DeepEqual(argv, []string{"status", "server", "--json"}) || o.Input != "" {
				t.Fatal("preflight/start did not independently probe")
			}
		} else if !strings.HasPrefix(o.Input, "HERDR_CONFIGURED_START_V1\n") {
			t.Fatal("no private input")
		}
		z := 0
		body := configuredCapability
		if n == 3 {
			body = configuredReady
		}
		return platform.RunResult{Status: &z, Stdout: body}
	}
	for _, legacy := range []transport.Start{{}, {Kind: "opencode", Args: []string{"legacy"}, EnvOverrides: map[string]string{}}} {
		got := b.PreflightConfiguredStart(context.Background(), legacy, transport.Call{})
		if !got.Valid || !got.Value || got.Err != nil || n != 0 {
			t.Fatalf("legacy preflight: %+v", got)
		}
	}
	bad := s
	bad.LaunchProfile = "unknown"
	if got := b.PreflightConfiguredStart(context.Background(), bad, transport.Call{}); got.Valid || got.Err == nil || n != 0 {
		t.Fatalf("invalid preflight: %+v", got)
	}
	pre := b.PreflightConfiguredStart(context.Background(), s, transport.Call{})
	if !pre.Valid || !pre.Value || pre.Err != nil || n != 1 {
		t.Fatalf("preflight=%+v", pre)
	}
	got := b.Start(context.Background(), s, transport.Call{})
	if !got.Valid || got.Err != nil || n != 3 {
		t.Fatalf("start=%+v calls=%d", got, n)
	}
	// A prior successful preflight confers no authority on a later Start.
	b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
		n++
		z := 0
		return platform.RunResult{Status: &z, Stdout: `{}`}
	}
	got = b.Start(context.Background(), s, transport.Call{})
	if got.Valid || n != 4 {
		t.Fatalf("preflight authority reused: %+v", got)
	}
}

func TestConfiguredDeadline(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint(early), func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			budget := 120 * time.Millisecond
			if early {
				ctx, cancel = context.WithTimeout(ctx, 90*time.Millisecond)
			}
			defer cancel()
			n := 0
			var deadline time.Time
			firstMS := 0
			b := New(nil).Terminal.(*backend)
			b.runner = func(_ string, _ []string, o platform.RunOptions) platform.RunResult {
				n++
				d, ok := o.Context.Deadline()
				if !ok {
					t.Fatal("missing bound")
				}
				if n == 1 {
					deadline = d
					firstMS = o.TimeoutMs
					time.Sleep(30 * time.Millisecond)
					z := 0
					return platform.RunResult{Status: &z, Stdout: configuredCapability}
				}
				if !d.Equal(deadline) || o.TimeoutMs >= firstMS-15 {
					t.Fatalf("budget reset: first=%d next=%d deadlines %v %v", firstMS, o.TimeoutMs, deadline, d)
				}
				<-o.Context.Done()
				return platform.RunResult{TimedOut: true, Error: configuredSecret}
			}
			before := time.Now()
			got := b.Start(ctx, configuredRequest(t), transport.Call{Timeout: budget})
			assertConfiguredPrivate(t, got)
			if got.Valid || got.Err == nil || got.Err.Kind != transport.FailureRunOutcomeUnknown || !got.Process.TimedOut || n != 2 || time.Since(before) > budget+200*time.Millisecond {
				t.Fatalf("calls=%d elapsed=%v got=%+v", n, time.Since(before), got)
			}
			if early {
				parent, _ := ctx.Deadline()
				if !deadline.Equal(parent) {
					t.Fatal("earlier parent deadline extended")
				}
			}
		})
	}
	for _, duringProbe := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		n := 0
		b := New(nil).Terminal.(*backend)
		if !duringProbe {
			cancel()
		}
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
			n++
			cancel()
			z := 0
			return platform.RunResult{Status: &z, Stdout: configuredCapability}
		}
		got := b.Start(ctx, configuredRequest(t), transport.Call{})
		cancel()
		want := 0
		if duringProbe {
			want = 1
		}
		if got.Valid || got.Err == nil || n != want {
			t.Fatalf("cancel calls=%d got=%+v", n, got)
		}
	}
}

func TestConfiguredInputBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		change func(*transport.Start)
		pass   bool
	}{
		{"max name", func(s *transport.Start) { s.Name = strings.Repeat("a", 256) }, true},
		{"long name", func(s *transport.Start) { s.Name = strings.Repeat("a", 257) }, false},
		{"long pane", func(s *transport.Start) { s.PaneID = strings.Repeat("p", 257) }, false},
		{"invalid utf8 name", func(s *transport.Start) { s.Name = string([]byte{0xff}) }, false},
		{"control name", func(s *transport.Start) { s.Name = "bad\nname" }, false},
		{"wrong kind", func(s *transport.Start) { s.Kind = "OpenCode" }, false},
		{"empty kind", func(s *transport.Start) { s.Kind = "" }, false},
		{"invalid utf8 pane", func(s *transport.Start) { s.PaneID = string([]byte{0xff}) }, false},
		{"control pane", func(s *transport.Start) { s.PaneID = "p\u0085" }, false},
		{"empty timeout", func(s *transport.Start) { s.Timeout = "" }, false},
		{"signed timeout", func(s *transport.Start) { s.Timeout = "+1" }, false},
		{"negative timeout", func(s *transport.Start) { s.Timeout = "-1" }, false},
		{"decimal timeout", func(s *transport.Start) { s.Timeout = "1.0" }, false},
		{"whitespace timeout", func(s *transport.Start) { s.Timeout = " 1" }, false},
		{"unknown profile", func(s *transport.Start) { s.LaunchProfile = "unknown" }, false},
		{"missing profile", func(s *transport.Start) { s.LaunchProfile = "" }, false},
		{"missing args", func(s *transport.Start) { s.Args = nil }, false},
		{"zero timeout", func(s *transport.Start) { s.Timeout = "0" }, false},
		{"overflow timeout", func(s *transport.Start) { s.Timeout = "18446744073709551616" }, false},
		{"invalid model config", func(s *transport.Start) { s.EnvOverrides["OPENCODE_CONFIG_CONTENT"] = `{"model":"invalid"}` }, false},
		{"unknown override", func(s *transport.Start) { s.EnvOverrides["SYNTHETIC"] = configuredSecret }, false},
		{"fallback model argv", func(s *transport.Start) { s.Args = []string{"--standalone", "-m", "provider/model"} }, false},
		{"oversize frame", func(s *transport.Start) {
			p, err := launchprofile.ResolveOpenCode("provider/model", `{"synthetic":"`+strings.Repeat("x", 524288)+`"}`, "")
			if err != nil {
				t.Fatal(err)
			}
			s.EnvOverrides = p.EnvOverrides
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := configuredRequest(t)
			c.change(&s)
			n := 0
			b := New(nil).Terminal.(*backend)
			b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
				n++
				z := 0
				return platform.RunResult{Status: &z, Stdout: configuredCapability}
			}
			got := b.PreflightConfiguredStart(context.Background(), s, transport.Call{})
			wantCalls := 0
			if c.pass {
				wantCalls = 1
			}
			if got.Valid != c.pass || (got.Err == nil) != c.pass || n != wantCalls {
				t.Fatalf("calls=%d got=%+v", n, got)
			}
			if !c.pass {
				final := b.Start(context.Background(), s, transport.Call{})
				if final.Valid || final.Err == nil || n != 0 {
					t.Fatalf("known-invalid bound Start: calls=%d final=%+v", n, final)
				}
				// Missing allocation fields must not bypass any known-input refusal.
				s.Name, s.PaneID = "", ""
				c.change(&s)
				pre := b.PreflightConfiguredStart(context.Background(), s, transport.Call{})
				final = b.Start(context.Background(), s, transport.Call{})
				if pre.Valid || pre.Err == nil || final.Valid || final.Err == nil || n != 0 {
					t.Fatalf("known-invalid unbound input: calls=%d pre=%+v final=%+v", n, pre, final)
				}
			}
		})
	}
}

func TestConfiguredPreallocation(t *testing.T) {
	for _, missing := range []string{"name", "pane", "both"} {
		for _, capability := range []struct {
			name, body string
			pass       bool
		}{
			{"true", configuredCapability, true},
			{"absent", `{"running":true,"capabilities":{}}`, false},
			{"false", `{"running":true,"capabilities":{"native_agent_launch_v1":false}}`, false},
		} {
			t.Run(missing+"/"+capability.name, func(t *testing.T) {
				s := configuredRequest(t)
				if missing != "pane" {
					s.Name = ""
				}
				if missing != "name" {
					s.PaneID = ""
				}
				n := 0
				b := New(nil).Terminal.(*backend)
				b.runner = func(exe string, argv []string, o platform.RunOptions) platform.RunResult {
					n++
					if exe != "herdr" || !reflect.DeepEqual(argv, []string{"status", "server", "--json"}) || o.Input != "" || !o.StrictOutputUTF8 {
						t.Fatal("preflight invoked an effect or sent private input")
					}
					z := 0
					return platform.RunResult{Status: &z, Stdout: capability.body}
				}
				// Final admission must refuse before even probing capability.
				final := b.Start(context.Background(), s, transport.Call{})
				if final.Valid || final.Err == nil || final.Err.Kind != transport.FailureProtocol || n != 0 {
					t.Fatalf("missing-ID Start: calls=%d reply=%+v", n, final)
				}
				pre := b.PreflightConfiguredStart(context.Background(), s, transport.Call{})
				if pre.Valid != capability.pass || pre.Value != capability.pass || (pre.Err == nil) != capability.pass || n != 1 || pre.Process.Status == nil || *pre.Process.Status != 0 {
					t.Fatalf("calls=%d preflight=%+v", n, pre)
				}
				if pre.Err != nil && (pre.Err.Kind != transport.FailureUnsupported || pre.Err.Message != configuredRefusal) {
					t.Fatalf("unexpected refusal=%+v", pre.Err)
				}
			})
		}
	}
}

func TestConfiguredPreallocationFrameBoundary(t *testing.T) {
	s := configuredRequest(t)
	// Calibrate only the fixture's size with short, unescaped bound IDs; the
	// actual admission assertions below go through the consumer, not this helper.
	base, valid := configuredFrame(s)
	if !valid {
		t.Fatal("invalid baseline fixture")
	}
	padding := configuredFrameLimit - len(base) + len(s.Name) + len(s.PaneID)
	s.EnvOverrides["OPENCODE_CONFIG_CONTENT"] = strings.Replace(s.EnvOverrides["OPENCODE_CONFIG_CONTENT"], configuredSecret, configuredSecret+strings.Repeat("x", padding), 1)
	s.Name, s.PaneID = "", ""
	n := 0
	b := New(nil).Terminal.(*backend)
	b.runner = func(_ string, argv []string, o platform.RunOptions) platform.RunResult {
		n++
		if !reflect.DeepEqual(argv, []string{"status", "server", "--json"}) || o.Input != "" {
			t.Fatal("boundary preflight dispatched input")
		}
		z := 0
		return platform.RunResult{Status: &z, Stdout: configuredCapability}
	}
	pre := b.PreflightConfiguredStart(context.Background(), s, transport.Call{})
	if !pre.Valid || !pre.Value || pre.Err != nil || n != 1 {
		t.Fatalf("exact known-input limit rejected (reserved or dummy metadata): calls=%d pre=%+v", n, pre)
	}
	// Binding even short actual IDs exceeds the complete-frame limit; a prior
	// successful preflight must not authorize either a probe or a start here.
	bound := s
	bound.Name, bound.PaneID = "worker", "p1"
	final := b.Start(context.Background(), bound, transport.Call{})
	if final.Valid || final.Err == nil || final.Err.Kind != transport.FailureProtocol || n != 1 {
		t.Fatalf("oversize bound Start: calls=%d reply=%+v", n, final)
	}
	s.EnvOverrides["OPENCODE_CONFIG_CONTENT"] = strings.Replace(s.EnvOverrides["OPENCODE_CONFIG_CONTENT"], configuredSecret, configuredSecret+"x", 1)
	pre = b.PreflightConfiguredStart(context.Background(), s, transport.Call{})
	if pre.Valid || pre.Err == nil || pre.Err.Kind != transport.FailureProtocol || n != 1 {
		t.Fatalf("oversize known input probed: calls=%d pre=%+v", n, pre)
	}
}

func TestConfiguredRealInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	raw := []byte(strings.Replace(configuredReady, "t1", string([]byte{0xff}), 1))
	_, err := fakecli.Install(t, root, "herdr", []fakecli.Rule{{Argv: []string{"status", "server", "--json"}, Stdout: configuredCapability}, {Argv: []string{"agent", "start-configured", "worker", "--kind", "opencode", "--pane", "p1", "--timeout", "90000"}, StdoutBytes: raw}})
	if err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range fakecli.Env([]string{"HOME=" + root, "USERPROFILE=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + root}, root) {
		k, v, _ := strings.Cut(entry, "=")
		env[k] = v
	}
	got := New(env).Terminal.Start(context.Background(), configuredRequest(t), transport.Call{Timeout: 10 * time.Second})
	assertConfiguredPrivate(t, got)
	if got.Valid || got.Err == nil || got.Process.Status == nil || *got.Process.Status != 0 {
		t.Fatalf("rewritten invalid bytes accepted: %+v", got)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(root, "herdr.calls.jsonl"))
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
}

func TestConfiguredStrictCapabilityTypes(t *testing.T) {
	for _, body := range []string{
		`{"running":true,"capabilities":{"other":true}}`,
		`{"running":true,"capabilities":{"native_agent_launch_v1":null}}`,
		`{"running":"true","capabilities":{"native_agent_launch_v1":true}}`,
		`{"running":1,"capabilities":{"native_agent_launch_v1":true}}`,
		`{"running":true,"capabilities":{"native_agent_launch_v1":1}}`,
		`{"running":true,"capabilities":{"native_agent_launch_v1":true},"capabilities":{"native_agent_launch_v1":true}}`,
		`{"running":true,"capabilities":{"native_agent_launch_v1":true}`,
	} {
		n := 0
		b := New(nil).Terminal.(*backend)
		b.runner = func(string, []string, platform.RunOptions) platform.RunResult {
			n++
			z := 0
			return platform.RunResult{Status: &z, Stdout: body, Stderr: configuredSecret}
		}
		pre := b.PreflightConfiguredStart(nil, configuredRequest(t), transport.Call{})
		if pre.Valid || pre.Value || pre.Err == nil || n != 1 || pre.Process.Stdout != "" || pre.Process.Stderr != "" {
			t.Fatalf("calls=%d preflight=%+v", n, pre)
		}
	}
}

func TestConfiguredRealStrictUTF8(t *testing.T) {
	metadata := `,"unknown":"` + configuredSecret + `�"`
	capability := strings.TrimSuffix(configuredCapability, "}") + metadata + "}"
	ready := strings.Replace(configuredReady, `"terminal_id":"t1"`, `"terminal_id":"t1"`+metadata, 1)
	argv := []string{"agent", "start-configured", "worker", "--kind", "opencode", "--pane", "p1", "--timeout", "90000"}
	for _, tc := range []struct {
		name, probe, start, probeErr, startErr string
		pass                                   bool
		calls                                  int
	}{
		{"literal metadata", capability, ready, "�", "�", true, 2},
		{"invalid probe stdout", strings.Replace(capability, "�", "\xff", 1), ready, "", "", false, 1},
		{"invalid start stdout", capability, strings.Replace(ready, "�", "\xff", 1), "", "", false, 2},
		{"invalid probe stderr", capability, ready, "private-\xff", "", false, 1},
		{"invalid start stderr", capability, ready, "", "private-\xff", false, 2},
		{"probe bom", "\xef\xbb\xbf" + capability, ready, "", "", false, 1},
		{"start bom", capability, "\xef\xbb\xbf" + ready, "", "", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := fakecli.InstallWithOptions(t, root, "herdr", []fakecli.Rule{
				{Argv: []string{"status", "server", "--json"}, StdoutBytes: []byte(tc.probe), StderrBytes: []byte(tc.probeErr)},
				{Argv: argv, StdoutBytes: []byte(tc.start), StderrBytes: []byte(tc.startErr)},
			}, fakecli.InstallOptions{CaptureStdin: true})
			if err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for _, entry := range fakecli.Env([]string{"HOME=" + root, "USERPROFILE=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + root}, root) {
				k, v, _ := strings.Cut(entry, "=")
				env[k] = v
			}
			s := configuredRequest(t)
			got := New(env).Terminal.Start(context.Background(), s, transport.Call{Timeout: 10 * time.Second})
			assertConfiguredPrivate(t, got)
			if got.Valid != tc.pass || (got.Err == nil) != tc.pass || got.Process.Status == nil || *got.Process.Status != 0 || got.Process.NotFound || got.Process.TimedOut {
				t.Fatalf("unexpected validity/native classification: %+v", got)
			}
			if tc.pass && got.Value != `{"result":{"started":true}}` || !tc.pass && got.Value != "" {
				t.Fatal("unknown metadata exposed or incorrect public value")
			}
			if !tc.pass && got.Err.Message != configuredRefusal {
				t.Fatal("non-fixed public refusal")
			}
			calls, err := fakecli.ReadCalls(filepath.Join(root, "herdr.calls.jsonl"))
			if err != nil || len(calls) != tc.calls {
				t.Fatalf("calls=%d err=%v", len(calls), err)
			}
			if !reflect.DeepEqual(calls[0].Argv, []string{"status", "server", "--json"}) || calls[0].Stdin != "" {
				t.Fatal("probe argv/stdin changed")
			}
			if len(calls) == 2 {
				wantInput, valid := configuredFrame(s)
				if !valid || !reflect.DeepEqual(calls[1].Argv, argv) || calls[1].Stdin != wantInput {
					t.Fatal("configured argv/private stdin changed")
				}
			}
		})
	}
}
