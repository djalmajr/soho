package wait

// A pi worker that cannot resolve its provider key reports
// `Failed to resolve API key … from shell command: printenv NAME`. Under
// transport=hc, wait's provider-error cause then also tells the
// orchestrator which variable the worker's environment lacks and how to hand
// it over. The fixtures reuse the hc wake fixture.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func hcWakeErrorMsg(msg string) string {
	return fmt.Sprintf(`{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"error","msg":%q}}`, msg)
}

func TestWaitHcProviderErrorMissingKeyHint(t *testing.T) {
	const piMsg = "Failed to resolve API key for opencode from shell command: printenv OPENCODE_API_KEY"
	const hint = "no OPENCODE_API_KEY in the worker: native workers skip your shell profile; set login_shell=on"
	long := "Failed to resolve API key: " + strings.Repeat("x", 150) + " printenv OPENCODE_API_KEY"
	cases := []struct {
		name, msg, want string
	}{
		{"matching cause", piMsg, piMsg + ". " + hint},
		{"matching cause with trailing quote", `Failed to resolve API key from shell command: "printenv OPENCODE_API_KEY"`, `Failed to resolve API key from shell command: "printenv OPENCODE_API_KEY". ` + hint},
		{"matching cause in the middle", "Failed to resolve API key from shell command: printenv OPENCODE_API_KEY (exit 1)", "Failed to resolve API key from shell command: printenv OPENCODE_API_KEY (exit 1). " + hint},
		{"cause ending in a period", piMsg + ".", piMsg + ". " + hint},
		{"cause without printenv", "Failed to resolve API key for opencode", "Failed to resolve API key for opencode"},
		{"name starting with a digit", "Failed to resolve API key: printenv 9KEY", "Failed to resolve API key: printenv 9KEY"},
		{"name with a hyphen", "Failed to resolve API key: printenv OPEN-CODE_KEY", "Failed to resolve API key: printenv OPEN-CODE_KEY"},
		{"name with a path", "Failed to resolve API key: printenv ../KEY", "Failed to resolve API key: printenv ../KEY"},
		{"name with a substitution", "Failed to resolve API key: printenv $(KEY)", "Failed to resolve API key: printenv $(KEY)"},
		{"printenv inside a longer word", "Failed to resolve API key: myprintenv OPENCODE_API_KEY", "Failed to resolve API key: myprintenv OPENCODE_API_KEY"},
		{"printenv without a name", "Failed to resolve API key: printenv", "Failed to resolve API key: printenv"},
		{"long cause shortened to fit the hint", long, long[:200-len(hint)-2] + ". " + hint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHcWakeFixture(t, "1", "none")
			f.installHc(t, fakecliRuleGetWorker(hcWakeErrorMsg(tc.msg)))
			code, out, stderr := f.runWait(t, true, 0)
			if code != 14 || !strings.Contains(out, `"status":"provider-error"`) {
				t.Fatalf("code=%d out=%q stderr=%q, want the provider-error exit 14", code, out, stderr)
			}
			cause, ok := readWaitFile(f.sd, "worker", "provider-cause")
			if !ok || cause != tc.want {
				t.Fatalf("provider-cause=%q ok=%v, want %q", cause, ok, tc.want)
			}
			hinted := strings.Contains(tc.want, "login_shell")
			if !strings.Contains(out, `"cause":`+fmt.Sprintf("%q", tc.want)) {
				t.Fatalf("out=%q, want the JSON cause %q", out, tc.want)
			}
			if len(cause) > 200 || strings.Contains(cause, "--env-from") {
				t.Fatalf("provider-cause=%q (%d units), want at most 200 units and no --env-from advice", cause, len(cause))
			}
			if got := strings.Contains(stderr, "login_shell"); got != hinted {
				t.Fatalf("stderr=%q, hint in warning=%v, want %v", stderr, got, hinted)
			}
			f.assertNoHerdr(t)
		})
	}
}

// The hint reads the name from the whole message and never makes the cause
// longer than its 200-unit limit: a name too long to fit adds no hint, rather
// than a hint for a shorter, wrong name.
func TestWaitHcProviderErrorMissingKeyHintUsesTheWholeName(t *testing.T) {
	name := "PROVIDER_" + strings.Repeat("K", 230)
	msg := "Failed to resolve API key from shell command: printenv " + name
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t, fakecliRuleGetWorker(hcWakeErrorMsg(msg)))
	code, _, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d stderr=%q, want exit 14", code, stderr)
	}
	cause, _ := readWaitFile(f.sd, "worker", "provider-cause")
	cause = strings.TrimSuffix(cause, "\n")
	if strings.Contains(cause, "login_shell") || len(cause) > 200 {
		t.Fatalf("provider-cause=%q (%d units), want no hint and at most 200 units", cause, len(cause))
	}
}

// Legacy transport: the assistant's own error word is read only under
// transport=hc, so no provider cause is written and no hint appears, whatever
// the assistant's text says.
func TestWaitLegacyTransportAddsNoMissingKeyHint(t *testing.T) {
	base := t.TempDir()
	state, bin := filepath.Join(base, "state", "sW"), filepath.Join(base, "bin")
	if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const msg = "Failed to resolve API key from shell command: printenv OPENCODE_API_KEY"
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: fmt.Sprintf(`{"result":{"agent":{"agent_status":"error","message":%q,"msg":%q,"state_change_seq":1}}}`, msg, msg)},
		{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Stdout: msg},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HERDR_SOCKET_PATH": filepath.Join(base, "none.sock"), "SOHO_WAIT_POLL_MS": "1"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := ProbeAgent(state, "worker", "", ctx, env)
	if got == "provider-error" {
		t.Fatalf("ProbeAgent=%q, want no assistant-error mapping on the legacy transport", got)
	}
	if cause, ok := readWaitFile(state, "worker", "provider-cause"); ok && strings.Contains(cause, "login_shell") {
		t.Fatalf("provider-cause=%q, want no hint on the legacy transport", cause)
	}
}
