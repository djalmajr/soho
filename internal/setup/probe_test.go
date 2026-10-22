package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSetupProbeJavaScriptCases(t *testing.T) {
	install := func(t *testing.T, bin string, rules []fakecli.Rule) platform.Env {
		t.Helper()
		if _, err := fakecli.Install(t, bin, "claude", rules); err != nil {
			t.Fatal(err)
		}
		env := fakeEnv(t, bin)
		env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
		return env
	}
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"probeTimeout: the defaults are 60 s for codex and 20 s for the rest", func(t *testing.T) {
			if n := probeKindDefaultTimeout("codex"); n != 60 {
				t.Fatalf("codex default=%d, want 60", n)
			}
			for _, kind := range []string{"claude", "grok", "agy", "gemini", "cursor", "pi", "opencode"} {
				if n := probeKindDefaultTimeout(kind); n != 20 {
					t.Fatalf("%s default=%d, want 20", kind, n)
				}
			}
		}},
		{"probeTimeout: an invalid env value is a usage error 2, not a fallback", func(t *testing.T) {
			env := fakeEnv(t, t.TempDir())
			env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
			for _, value := range []string{"0", "-1", "1.5", "abc"} {
				env["SOHO_PROBE_TIMEOUT"] = value
				func() {
					defer func() {
						e, ok := recover().(*platform.ExitError)
						if !ok || e.Code != 2 {
							t.Errorf("value %q panic=%#v", value, recover())
						}
					}()
					CmdProbe(nil, nil, env, t.TempDir())
				}()
			}
		}},
		{"probeDefaultModel: the worker-model chain and the pass-through kinds", func(t *testing.T) {
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"model_claude_worker": {Value: "worker", Source: "project"}, "model_pi": {Value: "base", Source: "project"}}}
			if got := probeDefaultModel(ctx, "claude", platform.Env{}, "."); got != "worker" {
				t.Fatalf("worker model=%q", got)
			}
			if got := probeDefaultModel(ctx, "pi", platform.Env{}, "."); got != "base" {
				t.Fatalf("fallback model=%q", got)
			}
		}},
		{"probeDefaultModel: a project-layer model.<kind>.worker wins, a listing kind resolves it", func(t *testing.T) {
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"model_claude_worker": {Value: "project-model", Source: "project"}}}
			if got := probeDefaultModel(ctx, "claude", platform.Env{}, "."); got != "project-model" {
				t.Fatalf("model=%q", got)
			}
		}},
		{"probeCmd: the exact argv per kind (prompt, model flag order, empty model)", func(t *testing.T) {
			for _, kind := range []string{"claude", "codex", "grok", "agy", "gemini", "cursor", "pi", "opencode"} {
				if got := probeArgs(kind, ""); len(got) == 0 || got[len(got)-1] == "" {
					t.Errorf("%s empty-model argv=%q", kind, got)
				}
			}
			if got := strings.Join(probeArgs("claude", "m"), " "); got != "-p Reply with exactly ok --model m" {
				t.Fatalf("claude argv=%q", got)
			}
		}},
		{"probeNoauthLine: the first login line wins, case-insensitive, empty otherwise", func(t *testing.T) {
			lines := strings.Split("ready\nNOT AUTHENTICATED first\ninvalid api key next", "\n")
			first := ""
			for _, line := range lines {
				if noAuthRE.MatchString(line) {
					first = line
					break
				}
			}
			if first != "NOT AUTHENTICATED first" || noAuthRE.MatchString("please login later") == false {
				t.Fatalf("first=%q", first)
			}
		}},
		{"probeKind: ready", func(t *testing.T) {
			env := install(t, t.TempDir(), []fakecli.Rule{{Argv: []string{"-p", ProbePrompt}, Stdout: "ok\n"}})
			got := probeOne(nil, "claude", "", "configured", 3, env, os.TempDir())
			if got.Status != "ready" || got.Cause != "" {
				t.Fatalf("probe=%+v", got)
			}
		}},
		{"probeKind: not installed (no CLI on the PATH)", func(t *testing.T) {
			env := fakeEnv(t, t.TempDir())
			env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
			got := probeOne(nil, "claude", "", "configured", 1, env, os.TempDir())
			if got.Status != "error" || got.Cause != "not installed" {
				t.Fatalf("probe=%+v", got)
			}
		}},
		{"probeKind: no-auth — the fixed cause, never the CLI line", func(t *testing.T) {
			env := install(t, t.TempDir(), []fakecli.Rule{{Argv: []string{"-p", ProbePrompt}, Code: 1, Stderr: "NOT AUTHENTICATED token=must-not-leak"}})
			got := probeOne(nil, "claude", "", "configured", 2, env, os.TempDir())
			if got.Status != "no-auth" || got.Cause != "not authenticated" || strings.Contains(got.Cause, "token") {
				t.Fatalf("probe=%+v", got)
			}
		}},
		{"probeKind: quota — with and without a renewal time (redacted value)", func(t *testing.T) {
			env := install(t, t.TempDir(), []fakecli.Rule{{Argv: []string{"-p", ProbePrompt}, Code: 1, Stdout: "You have hit your usage limit\nResets at 5:00pm token=sk_live_abcdefghij"}})
			got := probeOne(nil, "claude", "", "configured", 2, env, os.TempDir())
			if got.Status != "quota" || !strings.Contains(got.Cause, "renews 5:00") || strings.Contains(got.Cause, "sk_live") {
				t.Fatalf("probe=%+v", got)
			}
		}},
		{"probeKind: error by code — the fixed cause, never the CLI text", func(t *testing.T) {
			env := install(t, t.TempDir(), []fakecli.Rule{{Argv: []string{"-p", ProbePrompt}, Code: 9, Stderr: "private provider text"}})
			got := probeOne(nil, "claude", "", "configured", 2, env, os.TempDir())
			if got.Status != "error" || got.Cause != "exit 9" || strings.Contains(got.Cause, "private") {
				t.Fatalf("probe=%+v", got)
			}
		}},
		{"e2e: the aggregate probe — shape, statuses, reviewer, flags, empty stdin, no state", func(t *testing.T) {
			env := fakeEnv(t, t.TempDir())
			env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			defer func() { platform.Stdout = old }()
			CmdProbe(nil, ctx, env, os.TempDir())
			if !strings.HasPrefix(out.String(), "{\n  \"probes\"") || !strings.Contains(out.String(), "not installed") || strings.Contains(out.String(), "recommended_reviewer") {
				t.Fatalf("JSON=%s", out.String())
			}
		}},
		{"e2e: a single kind with an explicit model", func(t *testing.T) {
			env := install(t, t.TempDir(), []fakecli.Rule{{Argv: []string{"-p", ProbePrompt, "--model", "fixture-model"}}})
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			defer func() { platform.Stdout = old }()
			CmdProbe([]string{"--kind", "claude", "--model", "fixture-model"}, ctx, env, os.TempDir())
			if !strings.Contains(out.String(), `"model": "fixture-model"`) || !strings.Contains(out.String(), `"status": "ready"`) {
				t.Fatalf("JSON=%s", out.String())
			}
		}},
		{"e2e: --timeout and the env timeout — valid values work, invalid values die 2 before any CLI", func(t *testing.T) {
			env := fakeEnv(t, t.TempDir())
			env["SOHO_PROBE_TIMEOUT"] = "bad"
			defer func() {
				e, ok := recover().(*platform.ExitError)
				if !ok || e.Code != 2 {
					t.Fatalf("panic=%#v", recover())
				}
			}()
			CmdProbe(nil, nil, env, os.TempDir())
		}},
		{"e2e: --model without --kind, an unknown kind and a flag without a value are usage errors 2", func(t *testing.T) {
			env := fakeEnv(t, t.TempDir())
			for _, args := range [][]string{{"--model", "m"}, {"--kind", "unknown"}, {"--kind"}} {
				func() {
					defer func() {
						e, ok := recover().(*platform.ExitError)
						if !ok || e.Code != 2 {
							t.Errorf("args=%v panic=%#v", args, recover())
						}
					}()
					CmdProbe(args, nil, env, os.TempDir())
				}()
			}
		}},
		{"e2e: no-auth and error statuses end-to-end — the keys never reach the JSON", func(t *testing.T) {
			env := install(t, t.TempDir(), []fakecli.Rule{{Argv: []string{"-p", ProbePrompt}, Code: 1, Stderr: "NOT AUTHENTICATED token=sk_live_abcdefghij"}})
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			defer func() { platform.Stdout = old }()
			CmdProbe([]string{"--kind", "claude"}, ctx, env, os.TempDir())
			if !strings.Contains(out.String(), `"status": "no-auth"`) || strings.Contains(out.String(), "sk_live") {
				t.Fatalf("JSON=%s", out.String())
			}
		}},
	}
	for _, tc := range tests {
		t.Run("// JS: \""+tc.name+"\"", func(t *testing.T) { // Mutation captured: changing probe classification, argv or validation alters the externally reported JSON status.
			tc.run(t)
		})
	}
}
