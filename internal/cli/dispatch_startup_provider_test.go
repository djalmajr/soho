package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const agyStartupScreen = "⚠ failed to construct executor: plan model not specified; you must specify a valid model\n" +
	"⚠ Warning\n" +
	"  ⎿  model claude-sonnet-5-5-medium is not recognized as a known model or custom model in settings. Ignoring the flag.\n"

const codexConfigScreen = "■ Failed to start turn: turn/start failed in TUI: turn/start failed: failed to load configuration:\n" +
	"invalid transport\n" +
	"in `mcp_servers.old-server`\n" +
	" (code -32600)\n"

// TestDispatchHcCodexConfigFailureDiagnosesReopen is the #314 consumer: after
// one accepted send with no turn rise, the Codex config refusal is the cause
// and the next command is soho reopen. No second send is issued.
func TestDispatchHcCodexConfigFailureDiagnosesReopen(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: startupHcScreen(t, codexConfigScreen, true)},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
	}...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 14 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "provider-error" {
		t.Fatalf("status=%q, want provider-error (out=%s)", status, out)
	}
	if !strings.Contains(out, provider.CodexConfigCause) || !strings.Contains(errText, provider.CodexConfigCause) {
		t.Fatalf("missing reopen cause: out=%s stderr=%s", out, errText)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 1 {
		t.Fatalf("sends=%#v, want exactly one (no retry)", sends)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchAgyModelFailureDiagnosesProviderError is the #253 consumer on
// the legacy transport: the first-turn model rejection is a provider stop
// with a fixed cause, not a settled-no-report or a resend.
func TestDispatchAgyModelFailureDiagnosesProviderError(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0",
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Call: 1, Stdout: "ready"},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Call: 2, Stdout: "ready"},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Call: 3, Stdout: "ready"},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: agyStartupScreen},
	)
	roster := filepath.Join(f.state, "ws", "agents.tsv")
	body, err := os.ReadFile(roster)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "\tcodex\timplementer\t", "\tagy\timplementer\t", 1))
	if err := os.WriteFile(roster, body, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if code != 14 {
		t.Fatalf("code=%d out=%s stderr=%s calls=%#v", code, out, errText, calls)
	}
	if !strings.Contains(out, `"wait_status":"provider-error"`) || !strings.Contains(out, provider.AgyModelCause) {
		t.Fatalf("out=%s, want provider-error with the fixed Agy cause", out)
	}
	if !strings.Contains(errText, provider.AgyModelCause) {
		t.Fatalf("stderr=%s, want the fixed Agy cause", errText)
	}
	prompts := 0
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("prompt calls=%d, want exactly one (no resend)", prompts)
	}
}

// TestDispatchNarrowStartupStopKindGate pins the kind boundary without a
// provider call: the same screen under another kind or inside a quote is not
// admitted as either narrow cause.
func TestDispatchNarrowStartupStopKindGate(t *testing.T) {
	for _, tc := range []struct {
		name, kind, screen string
		wantCause          string
	}{
		{name: "codex config", kind: "codex", screen: codexConfigScreen, wantCause: provider.CodexConfigCause},
		{name: "agy model", kind: "agy", screen: agyStartupScreen, wantCause: provider.AgyModelCause},
		{name: "other kind", kind: "claude", screen: codexConfigScreen},
		{name: "quoted codex", kind: "codex", screen: "```text\n" + codexConfigScreen + "```\n"},
		{name: "quoted agy", kind: "agy", screen: "```text\n" + agyStartupScreen + "```\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := provider.StartupProviderStop(tc.kind, "idle", tc.screen)
			admitted := d != nil
			if tc.wantCause == "" {
				if admitted {
					t.Fatalf("admitted detection=%#v, want none", d)
				}
				return
			}
			if !admitted || d.Cause != tc.wantCause {
				t.Fatalf("admitted=%t detection=%#v, want cause %q", admitted, d, tc.wantCause)
			}
		})
	}
}

func startupHcScreen(t *testing.T, screen string, complete bool) string {
	t.Helper()
	record, err := json.Marshal(map[string]any{"text": screen, "complete": complete, "how": "screen"})
	if err != nil {
		t.Fatal(err)
	}
	return string(record)
}

func TestDispatchHcStartupStopObservation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, visible, history string
		complete, arrived            bool
		wantCode, wantReads          int
	}{
		{"healthy", "codex", "ready", "", true, false, 15, 1},
		{"scrollback only", "codex", "ready", codexConfigScreen, true, false, 15, 1},
		{"stale on screen", "codex", codexConfigScreen + "\nReady for a new turn", "", true, false, 15, 1},
		{"incomplete", "codex", codexConfigScreen, codexConfigScreen, false, false, 15, 1},
		{"live draft", "codex", codexConfigScreen + "\n› Continue my draft", codexConfigScreen, true, false, 15, 1},
		{"other kind", "pi", codexConfigScreen, codexConfigScreen, true, false, 15, 0},
		{"send confirms arrival", "codex", codexConfigScreen, codexConfigScreen, true, true, 0, 0},
		{"turn rises", "codex", codexConfigScreen, codexConfigScreen, true, false, 0, 0},
		{"ceiling exhausted", "codex", codexConfigScreen, codexConfigScreen, true, false, 15, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send := `{"turn":7,"started":false}`
			if tc.arrived {
				send = `{"turn":7,"started":true}`
			}
			turn := 7
			if tc.name == "turn rises" {
				turn = 8
			}
			f := newDispatchHcFixture(t, append(guardProbeRules(),
				fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(turn, "idle")},
				fakecli.Rule{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: startupHcScreen(t, tc.visible, tc.complete)},
				fakecli.Rule{Argv: []string{"read", "worker", "--lines", "40", "--json"}, Stdout: startupHcScreen(t, tc.history, true)},
				fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: send},
			))
			roster := filepath.Join(f.sd, "agents.tsv")
			body, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(roster, []byte(strings.Replace(string(body), "\tcodex\t", "\t"+tc.kind+"\t", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.name == "ceiling exhausted" {
				oldClock := dispatchCeilingNow
				t.Cleanup(func() { dispatchCeilingNow = oldClock })
				start := time.Now()
				dispatchCeilingNow = func() time.Time {
					sent := false
					for _, call := range f.hcCalls(t) {
						if isRealSend(call.Argv) {
							sent = true
						}
						if sent && len(call.Argv) >= 2 && call.Argv[0] == "get" && call.Argv[1] == "worker" {
							return start.Add(time.Minute)
						}
					}
					return start
				}
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != tc.wantCode || strings.Contains(out+stderr, provider.CodexConfigCause) {
				t.Errorf("exit=%d want=%d out=%q stderr=%q", code, tc.wantCode, out, stderr)
			}
			reads, sends := 0, 0
			for _, call := range f.hcCalls(t) {
				switch call.Argv[0] {
				case "send":
					if isRealSend(call.Argv) {
						sends++
					}
				case "read":
					if sends == 0 || strings.Join(call.Argv, " ") != "read worker --lines 0 --json" {
						t.Errorf("unexpected screen read: %q", call.Argv)
					}
					reads++
				case "keys":
					t.Errorf("unexpected key: %q", call.Argv)
				}
			}
			if reads != tc.wantReads || sends != 1 {
				t.Errorf("reads=%d want=%d sends=%d want=1", reads, tc.wantReads, sends)
			}
			f.assertHcNeverTouchedHerdr(t)
		})
	}
}

func TestDispatchLegacyStartupStopIsNew(t *testing.T) {
	for _, source := range []string{"visible", "recent-unwrapped"} {
		t.Run(source, func(t *testing.T) {
			visibleBefore, recent := "ready", ""
			if source == "visible" {
				visibleBefore = agyStartupScreen
			} else {
				recent = agyStartupScreen
			}
			f := newDispatchArrivalFixture(t, "idle", 1, 1, recent, "0",
				fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Call: 1, Stdout: visibleBefore},
				fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: agyStartupScreen},
			)
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			body, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(roster, []byte(strings.Replace(string(body), "\tcodex\t", "\tagy\t", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != 15 || strings.Contains(out+stderr, provider.AgyModelCause) {
				t.Fatalf("previously observed %s frame: exit=%d out=%q stderr=%q", source, code, out, stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if countArgvPrefix(calls, []string{"agent", "prompt", "worker"}) != 1 || countArgvPrefix(calls, []string{"agent", "send-keys", "worker"}) != 0 {
				t.Fatal("previously observed frame caused extra worker input")
			}
		})
	}
}
