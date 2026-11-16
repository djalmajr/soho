package kinds

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// PI_CODING_AGENT_DIR moves pi's models, settings and sessions: soho reads
// the same directory (absolute or "~/" forms); a relative value is ignored.
func TestPiAgentDirHonorsTheOverride(t *testing.T) {
	home := t.TempDir()
	write := func(dir, id string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		content := `{"providers":{"vendor":{"apiKey":"$KEY","models":[{"id":"` + id + `"}]}}}`
		if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".pi", "agent"), "default")
	override := filepath.Join(home, "private-pi")
	write(override, "override")
	for _, tc := range []struct{ name, value, want string }{
		{"unset", "", "vendor/default"},
		{"absolute", override, "vendor/override"},
		{"tilde", "~/private-pi", "vendor/override"},
		{"relative ignored", "private-pi", "vendor/default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := platform.Env{"HOME": home, "USERPROFILE": home}
			if tc.value != "" {
				env["PI_CODING_AGENT_DIR"] = tc.value
			}
			got := PiOwnModels(env)
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("PI_CODING_AGENT_DIR=%q: got %#v, want %s", tc.value, got, tc.want)
			}
		})
	}
}

// The two diagnostic readers follow the override too: the bare !printenv
// warning reads the overridden models.json, and the maxTokens headroom check
// reads thinkingBudgets from the overridden settings.json (R396 F1).
func TestPiAgentDirDiagnosticReaders(t *testing.T) {
	stubPiCoreEnvLaunch(t, func(_ context.Context, _ platform.Env, _ ...string) platform.RunResult {
		return platform.RunResult{NotFound: true}
	})
	home := t.TempDir()
	override := filepath.Join(home, "private-pi")
	if err := os.MkdirAll(override, 0700); err != nil {
		t.Fatal(err)
	}
	// Default models.json: nothing to warn about.
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".pi", "agent", "models.json"), []byte(`{"providers":{"plain":{"apiKey":"$KEY","models":[{"id":"m","maxTokens":100000}]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Overridden: a bare !printenv key, and maxTokens 40000, which has
	// headroom under the built-in high budget (16384+8192) and lacks it only
	// under the overridden settings.json budget of 32000.
	if err := os.WriteFile(filepath.Join(override, "models.json"), []byte(`{"providers":{"vendor":{"apiKey":"!printenv OPENCODE_API_KEY","models":[{"id":"m","maxTokens":40000}]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "settings.json"), []byte(`{"thinkingBudgets":{"high":32000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, "xdg"), "SOHO_TRANSPORT": "hc", "PI_CODING_AGENT_DIR": override}
	_, warnings := OwnProviderDoctorLines(&core.Config{}, env, home)
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "pi provider 'vendor': worker inherits only the Core environment") {
		t.Fatalf("bare !printenv in the overridden models.json not reported: %q", warnings)
	}
	if !strings.Contains(joined, "pi model vendor/m: maxTokens 40000 leaves less than 8192 tokens over the high reasoning budget (32000)") {
		t.Fatalf("thinkingBudgets from the overridden settings.json not read: %q", warnings)
	}
	if strings.Contains(joined, "'plain'") || strings.Contains(joined, "plain/m") {
		t.Fatalf("the default directory was read: %q", warnings)
	}
}
