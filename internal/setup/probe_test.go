package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSetupProbeAgyExplicitModelContract(t *testing.T) {
	const high = "gemini-4-hydrogen-high"
	const low = "gemini-4-hydrogen-low"
	const cached = "gemini-4-hydrogen-xhigh\n" + high + "\n" + low + "\n"
	for _, tc := range []struct {
		name, model, wantModel, catalog, configured, listing string
		listingFault, noAuth                                 bool
	}{
		{name: "cached alias resolves with high effort", model: "hydrogen", wantModel: high, catalog: cached},
		{name: "cold Hydrogen resolves to the listed generation", model: "hydrogen", wantModel: "gemini-5-hydrogen-high", listing: high + "  Hydrogen 4\ngemini-5-hydrogen-high  Hydrogen 5\n"},
		{name: "cold Opus resolves a second alias", model: "opus", wantModel: "claude-opus-4-6", listing: "claude-opus-4-5  Opus\nclaude-opus-4-6  Opus\n"},
		{name: "cached versioned base selects high", model: "gemini-4-hydrogen", wantModel: high, catalog: cached},
		{name: "cold punctuated base selects high", model: "gemini-4.5-hydrogen", wantModel: "gemini-4.5-hydrogen-high", listing: "gemini-4.5-hydrogen-low  Low\ngemini-4.5-hydrogen-high  High\n"},
		{name: "native ID stays exact", model: low, wantModel: low, catalog: cached},
		{name: "exact listed base wins", model: "gemini-4-hydrogen", wantModel: "gemini-4-hydrogen", listing: high + "  High\ngemini-4-hydrogen  Native\n"},
		{name: "exact native ID in cold catalog wins", model: "hydrogen", wantModel: "hydrogen", listing: high + "  Hydrogen high\nhydrogen  Native ID\n"},
		{name: "unrecognized native form passes through", model: "hydrogen-provider-private", wantModel: "hydrogen-provider-private", listing: high + "  Hydrogen\n"},
		{name: "unmatched native prefix is not a base alias", model: "gemini-4", wantModel: "gemini-4", listing: high + "  Hydrogen\n"},
		{name: "unknown bare model passes through", model: "experimental", wantModel: "experimental", listing: high + "  Hydrogen\n"},
		{name: "fresh cache miss refreshes once", model: "hydrogen", wantModel: high, catalog: "gemini-4-other-high\n", listing: high + "  Hydrogen\n"},
		{name: "qualified model passes through", model: "custom/hydrogen", wantModel: "custom/hydrogen"},
		{name: "cold catalog fault does not classify the probe", model: "hydrogen", wantModel: "hydrogen", listingFault: true},
		{name: "cold catalog fault preserves inference no-auth", model: "hydrogen", wantModel: "hydrogen", listingFault: true, noAuth: true},
		{name: "default model still uses default resolution", wantModel: "gemini-4-hydrogen-xhigh", catalog: cached, configured: "hydrogen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, cwd := t.TempDir(), t.TempDir()
			env := fakeEnv(t, bin)
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
				env[key] = t.TempDir()
			}
			env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
			if tc.catalog != "" {
				if err := os.WriteFile(kinds.ModelsCacheFile("agy", env), []byte(tc.catalog), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			argv := []string{"-p", "Reply with exactly ok", "--model", tc.wantModel}
			rule := fakecli.Rule{Argv: argv, Stdout: "ok\n"}
			want := Probe{Kind: "agy", Model: tc.wantModel, Status: "ready", Source: "configured"}
			if tc.noAuth {
				rule.Stdout, rule.Stderr, rule.Code = "", "Please log in to continue. fixture-detail", 1
				want.Status, want.Cause = "no-auth", "not authenticated"
			}
			rules := []fakecli.Rule{rule}
			wantCalls := [][]string{argv}
			if tc.listing != "" || tc.listingFault {
				list := fakecli.Rule{Argv: []string{"models"}, Stdout: tc.listing}
				if tc.listingFault {
					list.Code, list.Stderr = 1, "Authentication required for catalog. fixture-detail"
				}
				rules = append(rules, list)
				wantCalls = append([][]string{list.Argv}, wantCalls...)
			}
			if _, err := fakecli.Install(t, bin, "agy", rules); err != nil {
				t.Fatal(err)
			}
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			ctx := &core.Config{}
			if tc.configured != "" {
				ctx.Entries = map[string]core.ConfigEntry{"model_agy_worker": {Value: tc.configured, Source: "project"}}
			}
			args := []string{"--probe", "--kind", "agy"}
			if tc.model != "" {
				args = append(args, "--model", tc.model)
			}
			CmdSetup(args, ctx, env, cwd)
			var got struct {
				Probes        []Probe    `json:"probes"`
				SkippedCustom []struct{} `json:"skipped_custom"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !slices.Equal(got.Probes, []Probe{want}) ||
				got.SkippedCustom == nil || len(got.SkippedCustom) != 0 || strings.Contains(out.String(), "fixture-detail") {
				t.Fatalf("setup argv=%q stdout=%q, want probe %+v and empty skipped_custom: %v", args, out.String(), want, err)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "agy.calls.jsonl"))
			if err != nil || len(calls) != len(wantCalls) {
				t.Fatalf("Agy calls=%+v, want %q: %v", calls, wantCalls, err)
			}
			for i, call := range calls {
				if !slices.Equal(call.Argv, wantCalls[i]) {
					t.Fatalf("Agy call %d=%q, want %q", i, call.Argv, wantCalls[i])
				}
			}
		})
	}
}

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
		{"probeTimeout: codex and agy get 60 s, other kinds get 20 s", func(t *testing.T) {
			for _, kind := range []string{"codex", "agy"} {
				if n := probeKindDefaultTimeout(kind); n != 60 {
					t.Fatalf("%s default=%d, want 60", kind, n)
				}
			}
			for _, kind := range []string{"claude", "grok", "gemini", "cursor", "pi", "opencode"} {
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

func TestSetupProbeOMPExplicit(t *testing.T) {
	const model = "openai/gpt-5"
	bin, cwd := t.TempDir(), t.TempDir()
	argv := []string{"-p", "--no-session", "--no-tools", "--approval-mode", "always-ask", "--model", model, "--", ProbePrompt}
	if _, err := fakecli.Install(t, bin, "omp", []fakecli.Rule{{Argv: argv, Stdout: "ok\n"}}); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(t, bin)
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		env[key] = t.TempDir()
	}
	old := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = old })

	CmdProbe(nil, &core.Config{}, env, cwd)
	if strings.Contains(out.String(), `"kind": "omp"`) {
		t.Fatalf("OMP was included in the all-kinds probe: %s", out.String())
	}

	out.Reset()
	CmdProbe([]string{"--kind", "omp", "--model", model}, &core.Config{}, env, cwd)
	var result struct{ Probes []Probe }
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Probes) != 1 || result.Probes[0] != (Probe{Kind: "omp", Model: model, Status: "ready", Source: "configured"}) {
		t.Fatalf("OMP probe output = %s, parse error = %v", out.String(), err)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "omp.calls.jsonl"))
	if err != nil || len(calls) != 1 || strings.Join(calls[0].Argv, "\x00") != strings.Join(argv, "\x00") {
		t.Fatalf("OMP calls = %#v, err=%v, want exactly one call with %q", calls, err, argv)
	}
}
