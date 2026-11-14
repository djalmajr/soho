package spawn

import (
	"testing"

	"github.com/djalmajr/soho/internal/testutil"
)

// Mutation captured: a fixture that inherits the session variables of the
// process running the tests (SOHO_MAX_WORKERS, CLAUDE_CODE_SESSION_ID, ...)
// changes spawn results by who runs them. The environment of a spawn fixture
// holds only the session variables the fixture sets itself.
func TestSpawnFixtureEnvironmentIsHermetic(t *testing.T) {
	for _, key := range []string{
		"SOHO_TRANSPORT", "SOHO_STATE_DIR", "SOHO_MAX_WORKERS", "SOHO_REGISTRY_DIR", "HC_SCOPE", "HC_TERMINAL_ID",
		"HC_TERMINAL_SOCKET", "HERDR_PANE_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CODEX_HOME", "PI_CODING_AGENT_DIR",
		"OPENCODE_SERVER_PASSWORD", "GROK_API_KEY", "GEMINI_API_KEY", "CURSOR_TRACE_ID",
		// Set by the fixtures themselves; the inherited value must not survive.
		"SOHO_DIR", "SOHO_LANES", "SOHO_LAYOUT", "OPENCODE_CONFIG_DIR",
	} {
		t.Setenv(key, "polluted")
	}
	// What newSpawnFixture, configuredFixture and fakecli.Env assign on purpose.
	explicit := []string{
		"HERDR_SOCKET_PATH", "HERDR_WORKSPACE_ID", "SOHO_DIR", "SOHO_SKILL_DIR", "SOHO_ROLES", "SOHO_LANES", "SOHO_FAKECLI_CONFIG",
		"SOHO_PRESSURE_DISK_FREE_PERCENT", "SOHO_PRESSURE_SWAP_PERCENT",
		"SOHO_LAYOUT", "SOHO_REGRID", "SOHO_WAIT_POLL_MS", "SOHO_OPENCODE_TUI_PROFILE",
		"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR",
	}
	for name, f := range map[string]spawnFixture{"base": newSpawnFixture(t, nil), "configured": configuredFixture(t, nil)} {
		var entries []string
		for key, value := range f.env {
			entries = append(entries, key+"="+value)
			if value == "polluted" {
				t.Errorf("%s fixture: inherited value of %s reached the environment", name, key)
			}
		}
		if leaked := testutil.SessionVars(entries, explicit...); len(leaked) != 0 {
			t.Errorf("%s fixture: session variables the fixture did not set: %v", name, leaked)
		}
	}
}
