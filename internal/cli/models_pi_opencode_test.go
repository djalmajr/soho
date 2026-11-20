package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func runModelsForTest(t *testing.T, env platform.Env, kind string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	return cmdModels([]string{kind}, env), out.String(), stderr.String()
}

func modelsFixture(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source directory")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "kinds", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestModelsPiAndOpenCodeListCapturedIds(t *testing.T) {
	cases := []struct {
		kind, exe, fixture string
		args               []string
	}{
		{"pi", "pi", "pi-models.txt", []string{"--list-models"}},
		{"opencode", "opencode", "opencode-models.txt", []string{"models"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			env := commandFakeEnv(t, map[string][]fakecli.Rule{tc.exe: {{Argv: tc.args, Stdout: modelsFixture(t, tc.fixture)}}})
			code, out, stderr := runModelsForTest(t, env, tc.kind)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			want := []string{
				"anthropic/claude-fable-5", "anthropic/claude-opus-4-6", "provider-abc1/qwen3.8-27b",
				"console-openai/gpt-6.1-terra", "opencode-go/hy4-flash-free",
			}
			sort.Strings(lines)
			sort.Strings(want)
			if !strings.EqualFold(strings.Join(lines, "\n"), strings.Join(want, "\n")) {
				t.Fatalf("stdout = %q, want ids %q", out, want)
			}
		})
	}
}

func TestModelsPiAndOpenCodeFailureExitAndStderr(t *testing.T) {
	cases := []struct {
		name, kind, exe, wantLine, reason string
		rule                              *fakecli.Rule
		missing, timeout                  bool
	}{
		{
			name: "pi non-zero exit", kind: "pi", exe: "pi", reason: "failed (exit 7)",
			wantLine: "soho: models: pi: pi --list-models failed (exit 7)",
			rule:     &fakecli.Rule{Argv: []string{"--list-models"}, Stdout: "anthropic/cached\n", Code: 7},
		},
		{
			name: "opencode non-zero exit", kind: "opencode", exe: "opencode", reason: "failed (exit 3)",
			wantLine: "soho: models: opencode: opencode models failed (exit 3)",
			rule:     &fakecli.Rule{Argv: []string{"models"}, Stdout: "anthropic/cached\n", Code: 3},
		},
		{
			name: "pi timeout", kind: "pi", exe: "pi", reason: "timed out", timeout: true,
			wantLine: "soho: models: pi: pi --list-models timed out",
			rule:     &fakecli.Rule{Argv: []string{"--list-models"}, Stdout: "anthropic/cached\n", Delay: 1200},
		},
		{
			name: "opencode timeout", kind: "opencode", exe: "opencode", reason: "timed out", timeout: true,
			wantLine: "soho: models: opencode: opencode models timed out",
			rule:     &fakecli.Rule{Argv: []string{"models"}, Stdout: "anthropic/cached\n", Delay: 1200},
		},
		{
			name: "pi missing executable", kind: "pi", exe: "pi", reason: "not found", missing: true,
			wantLine: "soho: models: pi: pi --list-models not found",
		},
		{
			name: "opencode missing executable", kind: "opencode", exe: "opencode", reason: "not found", missing: true,
			wantLine: "soho: models: opencode: opencode models not found",
		},
		{
			name: "pi empty output", kind: "pi", exe: "pi", reason: "listed no models",
			wantLine: "soho: models: pi: pi --list-models listed no models",
			rule:     &fakecli.Rule{Argv: []string{"--list-models"}, Stdout: "no models available\n"},
		},
		{
			name: "opencode empty output", kind: "opencode", exe: "opencode", reason: "listed no models",
			wantLine: "soho: models: opencode: opencode models listed no models",
			rule:     &fakecli.Rule{Argv: []string{"models"}, Stdout: "no models available\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env platform.Env
			if tc.missing {
				env = commandFakeEnv(t, nil)
			} else {
				env = commandFakeEnv(t, map[string][]fakecli.Rule{tc.exe: {*tc.rule}})
			}
			if tc.timeout {
				env["SOHO_MODELS_TIMEOUT"] = "0.02"
			}
			code, out, stderr := runModelsForTest(t, env, tc.kind)
			if code != 1 || out != "" || stderr != tc.wantLine+"\n" {
				t.Fatalf("code=%d stdout=%q stderr=%q, want 1, empty stdout and %q", code, out, stderr, tc.wantLine+"\n")
			}
		})
	}
}

func TestModelsOtherKindsKeepTheirEmptyListingBehavior(t *testing.T) {
	env := commandFakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "unparsable\n"}}})
	code, out, stderr := runModelsForTest(t, env, "grok")
	if code != 0 || out != "\n" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q, want 0, a blank line and no stderr", code, out, stderr)
	}
}
