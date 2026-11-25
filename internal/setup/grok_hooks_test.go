package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/kinds"
)

const grokHookSettings = `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"reminder"}]}]}}`

func seedGrokHookSettings(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(grokHookSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetupWarnsAboutGrokClaudeHooks(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	settings := seedGrokHookSettings(t, root)
	env["GROK_HOME"] = filepath.Join(env.Get("HOME"), ".grok")

	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds.GrokClaudeHooksRisk(env, root); got != kinds.GrokHooksWarn {
		t.Fatalf("detector=%v, want warn", got)
	}
	after, err := os.ReadFile(settings)
	if err != nil || string(before) != string(after) {
		t.Fatalf("warning check changed hook settings: %q -> %q err=%v", before, after, err)
	}

	_, _, stderr := runSetupWrite([]string{"--target", "AGENTS.md"}, ctx, env, root)
	if !strings.Contains(stderr, kinds.GrokClaudeHooksWarningText) {
		t.Fatalf("normal setup output lacks the Grok hook warning: %q", stderr)
	}
}

func TestSetupGrokHookWarningNegativePaths(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"roles skips", []string{"--roles"}, nil},
		{"effective opt-out", []string{"--target", "AGENTS.md"}, map[string]string{"GROK_CLAUDE_HOOKS_ENABLED": "0"}},
		{"inconclusive config", []string{"--target", "AGENTS.md"}, map[string]string{"GROK_CONFIG_PATH": "config.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			seedGrokHookSettings(t, root)
			env["GROK_HOME"] = filepath.Join(env.Get("HOME"), ".grok")
			for key, value := range tc.env {
				env[key] = value
			}
			_, _, stderr := runSetupWrite(tc.args, ctx, env, root)
			if strings.Contains(stderr, kinds.GrokClaudeHooksWarningText) {
				t.Fatalf("unexpected Grok hook warning in %q", stderr)
			}
		})
	}
}
