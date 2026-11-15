package cli

import (
	"testing"

	"github.com/djalmajr/soho/internal/testutil"
)

// Mutation captured: tm6CLIEnv started from an environment that kept
// SOHO_LANES, SOHO_MAX_WORKERS and the native caller context of the process
// running the tests, so explain and dispatch output depended on who ran them.
func TestCLIFixtureEnvironmentIsHermetic(t *testing.T) {
	for _, key := range []string{
		"SOHO_TRANSPORT", "SOHO_STATE_DIR", "SOHO_MAX_WORKERS", "SOHO_LANES", "SOHO_REGISTRY_DIR", "HC_SCOPE",
		"HC_TERMINAL_ID", "HC_TERMINAL_SOCKET", "HERDR_PANE_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CODEX_HOME",
		"PI_CODING_AGENT_DIR", "OPENCODE_CONFIG_DIR", "GROK_API_KEY", "GEMINI_API_KEY", "CURSOR_TRACE_ID",
		"SOHO_DIR",
	} {
		t.Setenv(key, "polluted")
	}
	// What tm6CLIEnv and commandFixture assign on purpose.
	explicit := []string{
		"HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_WORKSPACE_ID", "SOHO_DIR", "SOHO_SKILL_DIR",
		"SOHO_PRESSURE_DISK_FREE_PERCENT", "SOHO_PRESSURE_SWAP_PERCENT",
	}
	check := func(name string, env map[string]string) {
		t.Helper()
		var entries []string
		for key, value := range env {
			entries = append(entries, key+"="+value)
			if value == "polluted" {
				t.Errorf("%s: inherited value of %s reached the environment", name, key)
			}
		}
		if leaked := testutil.SessionVars(entries, explicit...); len(leaked) != 0 {
			t.Errorf("%s: session variables the fixture did not set: %v", name, leaked)
		}
	}
	tm6, _ := tm6CLIEnv(t)
	check("tm6CLIEnv", tm6)
	base, _ := commandFixture(t)
	check("commandFixture", base)
}
