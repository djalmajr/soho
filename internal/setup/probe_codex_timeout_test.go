package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestProbeAgyAliasSharedDeadline(t *testing.T) {
	const wantCause = "timeout after 1s (a timeout does not prove the assistant is unavailable; retry with --timeout 90)"
	for _, tc := range []struct {
		name, envTimeout, flagTimeout, catalogTimeout string
		cachedMiss                                    bool
	}{
		{name: "flag overrides environment", envTimeout: "7", flagTimeout: "1", catalogTimeout: "0"},
		{name: "environment timeout", envTimeout: "1", catalogTimeout: "0.0005"},
		{name: "cache miss refresh shares the deadline", envTimeout: "1", catalogTimeout: "5", cachedMiss: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			env := fakeEnv(t, bin)
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
				env[key] = t.TempDir()
			}
			env["SOHO_PROBE_TIMEOUT"], env["SOHO_MODELS_TIMEOUT"] = tc.envTimeout, tc.catalogTimeout
			if tc.cachedMiss {
				if err := os.WriteFile(kinds.ModelsCacheFile("agy", env), []byte("unrelated-model\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fakecli.Install(t, bin, "agy", []fakecli.Rule{
				{Argv: []string{"models"}, Delay: 1800, Stdout: "gemini-4-hydrogen-high  Hydrogen\n"},
				{Argv: []string{"-p", ProbePrompt, "--model", "gemini-4-hydrogen-high"}, Stdout: "ok\n"},
			}); err != nil {
				t.Fatal(err)
			}
			old := platform.Stdout
			var out bytes.Buffer
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			saved := probeRunCLI
			var catalogBudget int
			probeRunCLI = func(exe string, argv []string, opts platform.RunOptions) platform.RunResult {
				if slices.Equal(argv, []string{"models"}) {
					catalogBudget = opts.TimeoutMs
					if opts.Context == nil {
						t.Fatal("catalog has no shared context")
					}
				}
				return saved(exe, argv, opts)
			}
			t.Cleanup(func() { probeRunCLI = saved })
			args := []string{"--probe", "--kind", "agy", "--model", "hydrogen"}
			if tc.flagTimeout != "" {
				args = append(args, "--timeout", tc.flagTimeout)
			}
			CmdSetup(args, &core.Config{}, env, t.TempDir())
			var got struct{ Probes []Probe }
			want := Probe{Kind: "agy", Model: "hydrogen", Status: "error", Cause: wantCause, Source: "configured"}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !slices.Equal(got.Probes, []Probe{want}) {
				t.Fatalf("argv=%q, stdout=%q, parse error=%v, want %+v", args, out.String(), err, want)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "agy.calls.jsonl"))
			if err != nil || len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"models"}) {
				t.Fatalf("expected only the catalog call, got %+v, err=%v", calls, err)
			}
			if catalogBudget < 1 || catalogBudget > 1000 {
				t.Fatalf("catalog budget=%d ms, want a positive portion of 1000 ms", catalogBudget)
			}
		})
	}
}

func TestProbeAgyAliasInferenceGetsRemainingBudget(t *testing.T) {
	bin := t.TempDir()
	env := fakeEnv(t, bin)
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		env[key] = t.TempDir()
	}
	env["SOHO_PROBE_TIMEOUT"], env["SOHO_MODELS_TIMEOUT"] = "60", "2"
	if _, err := fakecli.Install(t, bin, "agy", []fakecli.Rule{
		{Argv: []string{"models"}, Stdout: "gemini-4-hydrogen-high  Hydrogen\n"},
		{Argv: []string{"-p", ProbePrompt, "--model", "gemini-4-hydrogen-high"}, Code: 1, Stderr: "Please log in. fixture-detail"},
	}); err != nil {
		t.Fatal(err)
	}
	old := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = old })
	saved := probeRunCLI
	t.Cleanup(func() { probeRunCLI = saved })
	var catalogDeadline time.Time
	var seen []string
	probeRunCLI = func(exe string, args []string, opts platform.RunOptions) platform.RunResult {
		seen = append(seen, args[0])
		if opts.Context == nil {
			t.Fatalf("%v has no shared context", args)
		}
		deadline, ok := opts.Context.Deadline()
		if !ok || opts.TimeoutMs <= 0 {
			t.Fatalf("%v has no positive deadline: timeout_ms=%d", args, opts.TimeoutMs)
		}
		if args[0] == "models" {
			catalogDeadline = deadline
			if opts.TimeoutMs != 2000 {
				t.Fatalf("catalog lost its shorter override: %d ms", opts.TimeoutMs)
			}
			time.Sleep(100 * time.Millisecond)
		} else if !deadline.Equal(catalogDeadline) || opts.TimeoutMs > 4900 {
			t.Fatalf("inference got a new window: %d ms, catalog deadline %v, inference deadline %v", opts.TimeoutMs, catalogDeadline, deadline)
		}
		return saved(exe, args, opts)
	}
	CmdSetup([]string{"--probe", "--kind", "agy", "--model", "hydrogen", "--timeout", "5"}, &core.Config{}, env, t.TempDir())
	var got struct{ Probes []Probe }
	want := Probe{Kind: "agy", Model: "gemini-4-hydrogen-high", Status: "no-auth", Cause: "not authenticated", Source: "configured"}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !slices.Equal(got.Probes, []Probe{want}) || !slices.Equal(seen, []string{"models", "-p"}) {
		t.Fatalf("calls=%v stdout=%q parse error=%v, want %+v", seen, out.String(), err, want)
	}
}

func TestProbeAgyEffectiveTimeoutAndOverrides(t *testing.T) {
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "agy", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(t, bin)
	env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
	delete(env, "SOHO_PROBE_TIMEOUT")
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
	oldOut := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = oldOut })

	var timeouts []int
	saved := probeRunCLI
	probeRunCLI = func(_ string, _ []string, opts platform.RunOptions) platform.RunResult {
		timeouts = append(timeouts, opts.TimeoutMs)
		status := 0
		return platform.RunResult{Status: &status, Stdout: "ok\n"}
	}
	t.Cleanup(func() { probeRunCLI = saved })

	assertWindow := func(from int, want int, label string) {
		t.Helper()
		if len(timeouts) <= from {
			t.Fatalf("%s: no probe recorded: %v", label, timeouts)
		}
		for _, got := range timeouts[from:] {
			if got != want {
				t.Fatalf("%s: timeouts=%v, want %d ms from index %d", label, timeouts, want, from)
			}
		}
	}
	CmdProbe([]string{"--kind", "agy"}, ctx, env, t.TempDir())
	assertWindow(0, 60000, "agy default")
	env["SOHO_PROBE_TIMEOUT"] = "7"
	before := len(timeouts)
	CmdProbe([]string{"--kind", "agy"}, ctx, env, t.TempDir())
	assertWindow(before, 7000, "env override")
	before = len(timeouts)
	CmdProbe([]string{"--kind", "agy", "--timeout", "9"}, ctx, env, t.TempDir())
	assertWindow(before, 9000, "flag override")
}

// A 20-second timeout on the Codex probe was followed by a ready result at
// 60 seconds. Pin the extended defaults and the timeout retry hint.
func TestProbeCodexTimeout(t *testing.T) {
	const wantCause = "timeout after 1s (a timeout does not prove the assistant is unavailable; retry with --timeout 90)"
	t.Run("probe_codex_timeout_defaults_are_60s_codex_and_agy_20s_rest", func(t *testing.T) {
		// Mutation captured: a single 20 s default (or a codex/agy 20 s) fails
		// here; the per-kind default lives in probeKindDefaultTimeout.
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
	})
	t.Run("probe_codex_timeout_invalid_env_variable_dies_2", func(t *testing.T) {
		env := fakeEnv(t, t.TempDir())
		env["SOHO_PROBE_TIMEOUT"] = "bad"
		func() {
			defer func() {
				e, ok := recover().(*platform.ExitError)
				if !ok || e.Code != 2 {
					t.Fatalf("panic=%#v", recover())
				}
			}()
			CmdProbe([]string{"--kind", "codex"}, nil, env, t.TempDir())
		}()
	})
	t.Run("probe_codex_timeout_flag_timeout_wins_end_to_end", func(t *testing.T) {
		// The flag wins end to end: with the 60 s default the probe would
		// outlive this bound, so a timeout within it proves the flag applied.
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "codex", []fakecli.Rule{{Argv: []string{"exec", ProbePrompt}, Delay: 1800}}); err != nil {
			t.Fatal(err)
		}
		env := fakeEnv(t, bin)
		env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		t.Cleanup(func() { platform.Stdout = old })
		start := time.Now()
		CmdProbe([]string{"--kind", "codex", "--timeout", "1"}, ctx, env, t.TempDir())
		elapsed := time.Since(start)
		var got struct {
			Probes []Probe `json:"probes"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Probes) != 1 || got.Probes[0].Kind != "codex" || got.Probes[0].Status != "error" || got.Probes[0].Cause != wantCause || elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
			t.Fatalf("probe=%+v elapsed=%v output=%s", got.Probes, elapsed, out.String())
		}
	})
	t.Run("probe_codex_timeout_env_variable_wins_end_to_end", func(t *testing.T) {
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "codex", []fakecli.Rule{{Argv: []string{"exec", ProbePrompt}, Delay: 1800}}); err != nil {
			t.Fatal(err)
		}
		env := fakeEnv(t, bin)
		env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
		env["SOHO_PROBE_TIMEOUT"] = "1"
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		t.Cleanup(func() { platform.Stdout = old })
		start := time.Now()
		CmdProbe([]string{"--kind", "codex"}, ctx, env, t.TempDir())
		elapsed := time.Since(start)
		var got struct {
			Probes []Probe `json:"probes"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Probes) != 1 || got.Probes[0].Cause != wantCause || elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
			t.Fatalf("probe=%+v elapsed=%v output=%s", got.Probes, elapsed, out.String())
		}
	})
}
