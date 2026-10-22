package wait

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestProviderCursorPlanWait(t *testing.T) {
	for _, plan := range []string{"Plus", "Pro", "Free"} {
		t.Run(plan, func(t *testing.T) {
			line := "Error: Model unavailable on " + plan + ". " + plan + " includes Composer 2.5, Grok 4.7, Grok 4.6, and Grok 4.5 with fixed settings"
			f := newQueuedProbeFixture(t, "idle", "5", line+"\n› /tmp/worker-brief.md\n", map[string]string{"queued": "1 5 /tmp/worker-brief.md\n"})
			isolateProviderPlanFixture(t, f)
			f.env["SOHO_PROVIDER_RETRIES"] = "3"
			f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
			var out bytes.Buffer
			old := platform.Stdout
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			rc := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, 3000, false, t.TempDir())
			if rc != 14 || !strings.Contains(out.String(), `"status":"provider-error"`) || !strings.Contains(out.String(), line) {
				t.Fatalf("wait code=%d output=%s, want provider-error/14 and %q", rc, out.String(), line)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range calls {
				if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
					t.Fatalf("plan error retried: %#v", c)
				}
			}
		})
	}
}

func TestProviderPlanRevisionWait(t *testing.T) {
	line := "Error: Model unavailable on Plus"
	full := "```text\n" + strings.Repeat("quoted code\n", 40) + "```\n" + line
	fullLines := strings.Split(full, "\n")
	clipped := strings.Join(fullLines[len(fullLines)-40:], "\n")
	if got := len(strings.Split(clipped, "\n")); got != 40 {
		t.Fatalf("clipped fixture has %d lines, want 40", got)
	}
	for _, tc := range []struct {
		name, screen, cause string
		code                int
	}{
		{"clipped F3 forty lines", clipped, line, 14},
		{"unclosed fence", "```text\n" + line, line, 14},
		{"reverse pairing three fences", "```\nquoted code\n```\n" + line + "\n```", "", 6},
		{"fenced report", "The report quotes the observed error:\n```text\nError: Model unavailable on Plus\n```", "", 6},
		{"pipe", "Error: Model unavailable on Plus. Available models: Composer | Grok", "Error: Model unavailable on Plus. Available models: Composer | Grok", 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueuedProbeFixture(t, "idle", "5", tc.screen, nil)
			isolateProviderPlanFixture(t, f)
			f.env["SOHO_SETTLED_GRACE"] = "0"
			f.env["SOHO_PROVIDER_RETRIES"] = "3"
			f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
			var out bytes.Buffer
			old := platform.Stdout
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			rc := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, 3000, false, t.TempDir())
			if rc != tc.code {
				t.Fatalf("WaitFor exit=%d JSON=%s, want %d", rc, out.String(), tc.code)
			}
			if tc.cause != "" && (!strings.Contains(out.String(), `"status":"provider-error"`) || !strings.Contains(out.String(), tc.cause)) {
				t.Fatalf("missing provider cause: %s", out.String())
			}
			if tc.cause == "" && (!strings.Contains(out.String(), `"status":"settled-no-report"`) || strings.Contains(out.String(), `"cause":`)) {
				t.Fatalf("quoted error classified: %s", out.String())
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range calls {
				if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
					t.Fatalf("unexpected retry: %#v", c)
				}
			}
		})
	}
}

func TestProviderCursorPlanHistory(t *testing.T) {
	for name, screen := range map[string]string{
		"outside tail":  "Error: Model unavailable on Plus\n" + strings.Repeat("ordinary output\n", 10),
		"quoted brief":  "The brief cites Model unavailable on Plus.\n",
		"source":        "// Error: Model unavailable on Pro\n",
		"quoted report": "`Error: Model unavailable on Free`\n",
		"no plan":       "Error: Model unavailable on \n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newQueuedProbeFixture(t, "idle", "5", screen, nil)
			isolateProviderPlanFixture(t, f)
			for i := 0; i < 2; i++ {
				if got := f.probe(""); got == "provider-error" || got == "capacity" || got == "quota" {
					t.Fatalf("history classified as %q: %s", got, screen)
				}
			}
			if _, err := os.Stat(f.marker("provider")); !os.IsNotExist(err) {
				t.Fatalf("provider marker: %v", err)
			}
		})
	}
}

const providerTextsScreen = "  ┃  nenhum worker de inferência pronto\n"

func newProviderTextsFixture(t *testing.T, capacityText string) *queuedProbeFixture {
	t.Helper()
	f := newQueuedProbeFixture(t, "idle", "5", providerTextsScreen, nil)
	bin := filepath.Join(t.TempDir(), "bin")
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: providerTextsScreen},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: providerTextsScreen},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: providerTextsScreen},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
	}); err != nil {
		t.Fatal(err)
	}
	f.bin = bin
	f.env["PATH"] = bin
	f.env["SOHO_FAKECLI_CONFIG"] = bin
	f.env["SOHO_PROVIDER_RETRIES"] = "1"
	f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
	if capacityText != "" {
		f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{"provider_capacity_texts": {Value: capacityText}}}
	}
	return f
}

func TestProviderTextsWaitCases(t *testing.T) {
	t.Run("wait: a box line with a configured capacity text retries, then reports capacity", func(t *testing.T) {
		// The gateway line has no Error prefix; only the configured text detects it.
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f := newProviderTextsFixture(t, "nenhum worker de inferência pronto")
		for i := 0; i < 4; i++ {
			if got := f.probe(""); got != "working" {
				t.Fatalf("capacity probe %d = %q, want working", i+1, got)
			}
		}
		if got := f.probe(""); got != "capacity" {
			t.Fatalf("exhausted probe = %q, want capacity (not settled-no-report)", got)
		}
		if got, err := os.ReadFile(f.marker("provider-cause")); err != nil || string(got) != "nenhum worker de inferncia pronto\n" {
			t.Fatalf("provider-cause = %q err=%v", got, err)
		}
		prompts := 0
		for _, call := range f.calls() {
			if len(call.Argv) >= 3 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" && call.Argv[2] == "worker" {
				prompts++
			}
		}
		if prompts != 1 {
			t.Fatalf("continue count = %d, want 1 (provider_retries=1)", prompts)
		}
	})
	t.Run("wait: the same box line without the keys settles without a report", func(t *testing.T) {
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f := newProviderTextsFixture(t, "")
		if got := f.probe(""); got != "working" {
			t.Fatalf("first probe = %q, want working", got)
		}
		fixed = fixed.Add(90 * time.Second)
		if got := f.probe(""); got != "settled" {
			t.Fatalf("still screen after the grace = %q, want settled (the reported bug)", got)
		}
		if _, err := os.Stat(f.marker("provider")); !os.IsNotExist(err) {
			t.Fatalf("provider marker written without the key: err=%v", err)
		}
	})
}

// Keep configuration and temporary paths separate from the operator's session.
func isolateProviderPlanFixture(t *testing.T, f *queuedProbeFixture) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		f.env[key] = root
	}
	p := filepath.Join(f.sd, "agents.tsv")
	row, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(string(row), "\tclaude\t", "\tcursor\t")), 0600); err != nil {
		t.Fatal(err)
	}
}
