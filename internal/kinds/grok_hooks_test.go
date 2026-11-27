package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func grokHookFixture(t *testing.T, settings string) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	for _, dir := range []string{home, project, filepath.Join(home, ".claude")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{
		"HOME":        home,
		"USERPROFILE": home,
		"GROK_HOME":   filepath.Join(home, ".grok"),
	}
	return env, project
}

func TestGrokClaudeHooksRisk(t *testing.T) {
	const withHooks = `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"reminder"}]}]}}`
	const noHooks = `{"hooks":{}}`
	cases := []struct {
		name     string
		settings string
		config   string
		env      map[string]string
		want     GrokHooksRisk
	}{
		{"hooks with default compatibility warns", withHooks, "", nil, GrokHooksWarn},
		{"hooks with config opt-out is quiet", withHooks, "[compat.claude]\nhooks = false\n", nil, GrokHooksQuiet},
		{"config opt-out in a table with a comment is quiet", withHooks, "[compat.claude] # local settings\nhooks = false\n", nil, GrokHooksQuiet},
		{"other table with a comment does not opt out", withHooks, "[compat.claude]\n[other] # other settings\nhooks = false\n", nil, GrokHooksWarn},
		{"hash in a quoted table name does not opt out", withHooks, "[compat.claude]\n[\"other#table\"] # other settings\nhooks = false\n", nil, GrokHooksWarn},
		{"hooks with env opt-out is quiet", withHooks, "", map[string]string{"GROK_CLAUDE_HOOKS_ENABLED": "0"}, GrokHooksQuiet},
		{"env true wins over config false", withHooks, "[compat.claude]\nhooks=false\n", map[string]string{"GROK_CLAUDE_HOOKS_ENABLED": "true"}, GrokHooksWarn},
		{"no hooks stays quiet", noHooks, "", nil, GrokHooksQuiet},
		{"malformed Boolean is inconclusive", withHooks, "[compat.claude]\nhooks = maybe\n", nil, GrokHooksInconclusive},
		{"injected config layer is inconclusive", withHooks, "", map[string]string{"GROK_CONFIG_PATH": "config.json"}, GrokHooksInconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, project := grokHookFixture(t, tc.settings)
			if tc.config != "" {
				if err := os.MkdirAll(env.Get("GROK_HOME"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(env.Get("GROK_HOME"), "config.toml"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range tc.env {
				env[key] = value
			}
			if got := GrokClaudeHooksRisk(env, project); got != tc.want {
				t.Fatalf("risk=%v, want %v", got, tc.want)
			}
		})
	}
}
