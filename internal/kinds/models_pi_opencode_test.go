package kinds

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// modelTmpInventory records every file under TMPDIR with its content so a
// test can prove nothing was created, removed or rewritten.
func modelTmpInventory(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func modelFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParsePiModelsFromCapture(t *testing.T) {
	want := []string{
		"anthropic/claude-fable-5",
		"anthropic/claude-opus-4-6",
		"provider-abc1/qwen3.8-27b",
		"console-openai/gpt-6.1-terra",
		"opencode-go/hy4-flash-free",
	}
	if got := ParsePiModels(modelFixture(t, "pi-models.txt")); !reflect.DeepEqual(got, want) {
		t.Fatalf("pi ids = %q, want %q", got, want)
	}
}

func TestParseOpenCodeModelsFromCapture(t *testing.T) {
	want := []string{
		"anthropic/claude-fable-5",
		"anthropic/claude-opus-4-6",
		"provider-abc1/qwen3.8-27b",
		"console-openai/gpt-6.1-terra",
		"opencode-go/hy4-flash-free",
	}
	if got := ParseOpenCodeModels(modelFixture(t, "opencode-models.txt")); !reflect.DeepEqual(got, want) {
		t.Fatalf("opencode ids = %q, want %q", got, want)
	}
}

func TestParsePiOpenCodeModelsIgnoresNoise(t *testing.T) {
	pi := "\x1b[36mprovider           model                        context  max-out  thinking  images\x1b[0m\n" +
		"\x1b[36mopencode-go        hy4-flash-free               1.0M     65.5K    yes       yes   \x1b[0m\n" +
		"error: no models\n\n"
	if got := ParsePiModels(pi); !reflect.DeepEqual(got, []string{"opencode-go/hy4-flash-free"}) {
		t.Fatalf("pi ids with ANSI and noise = %q", got)
	}
	open := "\x1b[32mopencode-go/hy4-flash-free\x1b[0m\nerror: no models\n\n"
	if got := ParseOpenCodeModels(open); !reflect.DeepEqual(got, []string{"opencode-go/hy4-flash-free"}) {
		t.Fatalf("opencode ids with ANSI and noise = %q", got)
	}
}

func TestListModelsPiOpenCodeUsesItsOwnCommand(t *testing.T) {
	cases := []struct {
		kind, exe, fixture string
		args               []string
	}{
		{"pi", "pi", "pi-models.txt", []string{"--list-models"}},
		{"opencode", "opencode", "opencode-models.txt", []string{"models"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			env, bin := fakeEnv(t, map[string][]fakecli.Rule{tc.exe: {{Argv: tc.args, Stdout: modelFixture(t, tc.fixture)}}})
			ids, err := ListModels(tc.kind, env)
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 5 || ids[0] != "anthropic/claude-fable-5" || ids[4] != "opencode-go/hy4-flash-free" {
				t.Fatalf("ids = %q", ids)
			}
			calls, readErr := fakecli.ReadCallsForConfig(filepath.Join(bin, tc.exe+".json"))
			if readErr != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, tc.args) {
				t.Fatalf("calls = %+v err = %v, want one call with %q", calls, readErr, tc.args)
			}
			if _, statErr := os.Stat(ModelsCacheFile(tc.kind, env)); !os.IsNotExist(statErr) {
				t.Fatalf("explicit listing created a cache: %v", statErr)
			}
		})
	}
}

func TestListModelsPiOpenCodeFailures(t *testing.T) {
	cases := []struct {
		name, kind, wantReason, wantError string
		rules                             map[string][]fakecli.Rule
		missing                           bool
		timeout                           bool
	}{
		{
			name: "pi non-zero exit", kind: "pi", wantReason: "failed (exit 7)",
			wantError: "pi --list-models failed (exit 7)",
			rules:     map[string][]fakecli.Rule{"pi": {{Argv: []string{"--list-models"}, Stdout: "anthropic/claude-fable-5\n", Code: 7}}},
		},
		{
			name: "opencode non-zero exit", kind: "opencode", wantReason: "failed (exit 3)",
			wantError: "opencode models failed (exit 3)",
			rules:     map[string][]fakecli.Rule{"opencode": {{Argv: []string{"models"}, Stdout: "anthropic/claude-fable-5\n", Code: 3}}},
		},
		{
			name: "pi timeout", kind: "pi", wantReason: "timed out",
			wantError: "pi --list-models timed out", timeout: true,
			rules: map[string][]fakecli.Rule{"pi": {{Argv: []string{"--list-models"}, Stdout: "anthropic/claude-fable-5\n", Delay: 1200}}},
		},
		{
			name: "opencode timeout", kind: "opencode", wantReason: "timed out",
			wantError: "opencode models timed out", timeout: true,
			rules: map[string][]fakecli.Rule{"opencode": {{Argv: []string{"models"}, Stdout: "anthropic/claude-fable-5\n", Delay: 1200}}},
		},
		{
			name: "pi missing executable", kind: "pi", wantReason: "not found",
			wantError: "pi --list-models not found", missing: true,
		},
		{
			name: "opencode missing executable", kind: "opencode", wantReason: "not found",
			wantError: "opencode models not found", missing: true,
		},
		{
			name: "pi empty output", kind: "pi", wantReason: "listed no models",
			wantError: "pi --list-models listed no models",
			rules:     map[string][]fakecli.Rule{"pi": {{Argv: []string{"--list-models"}, Stdout: "no models available\n"}}},
		},
		{
			name: "opencode empty output", kind: "opencode", wantReason: "listed no models",
			wantError: "opencode models listed no models",
			rules:     map[string][]fakecli.Rule{"opencode": {{Argv: []string{"models"}, Stdout: "no models available\n"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env platform.Env
			if tc.missing {
				env, _ = fakeEnv(t, nil)
			} else {
				env, _ = fakeEnv(t, tc.rules)
			}
			if tc.timeout {
				env["SOHO_MODELS_TIMEOUT"] = "0.02"
			}
			ids, err := ListModels(tc.kind, env)
			if len(ids) != 0 || err == nil {
				t.Fatalf("ids=%q err=%v, want no ids and an error", ids, err)
			}
			failure, ok := err.(*ModelListError)
			if !ok || failure.Kind != tc.kind || failure.Reason != tc.wantReason || failure.Error() != tc.wantError {
				t.Fatalf("err = %#v, want %s kind %s reason %s", err, tc.wantError, tc.kind, tc.wantReason)
			}
			if _, statErr := os.Stat(ModelsCacheFile(tc.kind, env)); !os.IsNotExist(statErr) {
				t.Fatalf("failed listing wrote a cache: %v", statErr)
			}
		})
	}
}

func TestListModelsPiOpenCodeIgnoresStaleCache(t *testing.T) {
	for _, tc := range []struct {
		kind, exe, fixture string
		args               []string
	}{
		{"pi", "pi", "pi-models.txt", []string{"--list-models"}},
		{"opencode", "opencode", "opencode-models.txt", []string{"models"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			env, bin := fakeEnv(t, map[string][]fakecli.Rule{tc.exe: {{Argv: tc.args, Stdout: modelFixture(t, tc.fixture)}}})
			cache := ModelsCacheFile(tc.kind, env)
			if err := os.WriteFile(cache, []byte("anthropic/cached-model\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := modelTmpInventory(t, env.Get("TMPDIR"))
			ids, err := ListModels(tc.kind, env)
			if err != nil || !reflect.DeepEqual(ids, []string{
				"anthropic/claude-fable-5", "anthropic/claude-opus-4-6", "provider-abc1/qwen3.8-27b",
				"console-openai/gpt-6.1-terra", "opencode-go/hy4-flash-free",
			}) {
				t.Fatalf("ids=%q err=%v, want the fresh CLI list", ids, err)
			}
			calls, readErr := fakecli.ReadCallsForConfig(filepath.Join(bin, tc.exe+".json"))
			if readErr != nil || len(calls) != 1 {
				t.Fatalf("CLI calls = %+v err = %v, want one fresh listing", calls, readErr)
			}
			if after := modelTmpInventory(t, env.Get("TMPDIR")); !reflect.DeepEqual(before, after) {
				t.Fatalf("stale cache was read or rewritten: before=%v after=%v", before, after)
			}
		})
	}
}

func TestResolveModelPiOpenCodeRunsNoCLIAndWritesNoFile(t *testing.T) {
	for _, kind := range []string{"pi", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			argv := []string{"models"}
			if kind == "pi" {
				argv = []string{"--list-models"}
			}
			env, bin := fakeEnv(t, map[string][]fakecli.Rule{kind: {{Argv: argv, Stdout: "anthropic/listed-model\n"}}})
			cache := ModelsCacheFile(kind, env)
			if err := os.WriteFile(cache, []byte("anthropic/cached-model\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := modelTmpInventory(t, env.Get("TMPDIR"))
			var warns []string
			got, err := ResolveModel(kind, "provider/model", "", env, func(m string) { warns = append(warns, m) })
			if err != nil || got != "provider/model" || len(warns) != 0 {
				t.Fatalf("ResolveModel(%s) = %q err=%v warns=%v, want the spec unchanged", kind, got, err, warns)
			}
			if ids := ModelIDs(kind, env); len(ids) != 0 {
				t.Fatalf("ModelIDs(%s) = %q, want no ids in resolution", kind, ids)
			}
			calls, readErr := fakecli.ReadCallsForConfig(filepath.Join(bin, kind+".json"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatalf("cannot read %s call log: %v", kind, readErr)
			}
			if len(calls) != 0 {
				t.Fatalf("resolution called %s: calls=%+v", kind, calls)
			}
			if after := modelTmpInventory(t, env.Get("TMPDIR")); !reflect.DeepEqual(before, after) {
				t.Fatalf("resolution changed the file tree: before=%v after=%v", before, after)
			}
		})
	}
}

func TestListModelsOtherKindsKeepEmptyListBehavior(t *testing.T) {
	env, _ := fakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "unparsable\n"}}})
	ids, err := ListModels("grok", env)
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids=%q err=%v, want empty list without an error", ids, err)
	}
}

func TestListModelsPiOpenCodeUnusableTimeoutKeepsTheFallback(t *testing.T) {
	saved := modelListFallbackMs
	modelListFallbackMs = 50
	t.Cleanup(func() { modelListFallbackMs = saved })
	for _, kind := range []string{"pi", "opencode"} {
		for _, raw := range []string{"abc", "-1", "0", "0.0005"} {
			t.Run(kind+"/"+raw, func(t *testing.T) {
				argv := []string{"models"}
				if kind == "pi" {
					argv = []string{"--list-models"}
				}
				env, _ := fakeEnv(t, map[string][]fakecli.Rule{kind: {{Argv: argv, Stdout: "anthropic/claude-fable-5\n", Delay: 1200}}})
				env["SOHO_MODELS_TIMEOUT"] = raw
				_, err := ListModels(kind, env)
				failure, ok := err.(*ModelListError)
				if !ok || failure.Reason != "timed out" {
					t.Fatalf("SOHO_MODELS_TIMEOUT=%s: err = %#v, want the fallback timeout to fire", raw, err)
				}
			})
		}
	}
}
