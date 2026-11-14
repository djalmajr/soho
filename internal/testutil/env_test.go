package testutil

import (
	"os"
	"strings"
	"testing"
)

func TestCleanEnvDropsInheritedHerdrValuesAndUsesMissingSocket(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOHO_DIR", "inherited-state")
	t.Setenv("HERDR_SOCKET_PATH", "inherited.sock")

	env := CleanEnv(t)
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[strings.ToUpper(key)] = value
		}
	}
	if _, ok := values["HERDR_ENV"]; ok {
		t.Fatal("inherited HERDR_ENV reached the fixture")
	}
	if _, ok := values["HERDR_SOHO_DIR"]; ok {
		t.Fatal("inherited HERDR_SOHO_DIR reached the fixture")
	}
	socket := values["HERDR_SOCKET_PATH"]
	if socket == "" || socket == "inherited.sock" {
		t.Fatalf("HERDR_SOCKET_PATH=%q", socket)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("HERDR_SOCKET_PATH exists or could not be checked: %v", err)
	}
}

func TestCleanEnvGivesEachTestItsOwnTMPDIR(t *testing.T) {
	// Mutation captured: keeping the inherited TMPDIR lets a model cache one
	// package writes there change another package's output.
	inherited := t.TempDir()
	t.Setenv("TMPDIR", inherited)
	var got []string
	for _, item := range CleanEnv(t) {
		if strings.HasPrefix(item, "TMPDIR=") {
			got = append(got, item)
		}
	}
	if len(got) != 1 || got[0] == "TMPDIR="+inherited {
		t.Fatalf("TMPDIR entries = %q, want one test-owned directory", got)
	}
}

// Mutation captured: leaking HC_TERMINAL_ID or SOHO_TRANSPORT into test
// fixtures causes commands to attempt native terminal lookups against a
// missing or invalid terminal session.
func TestCleanEnvDropsInheritedNativeContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{name: "uppercase", keys: []string{"HC_TERMINAL_ID", "SOHO_TRANSPORT"}},
		{name: "lowercase", keys: []string{"hc_terminal_id", "soho_transport"}},
		{name: "mixedcase", keys: []string{"Hc_Terminal_Id", "Soho_Transport"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range tc.keys {
				t.Setenv(k, "leak-value")
			}
			t.Setenv("TEST_CLEAN_ENV_KEEP", "keep-val")

			env := CleanEnv(t)
			for _, item := range env {
				key, value, ok := strings.Cut(item, "=")
				if !ok {
					continue
				}
				if strings.EqualFold(key, "HC_TERMINAL_ID") || strings.EqualFold(key, "SOHO_TRANSPORT") {
					t.Fatalf("inherited %s reached CleanEnv fixture: %q", key, value)
				}
			}
			foundKeep := false
			for _, item := range env {
				if strings.HasPrefix(item, "TEST_CLEAN_ENV_KEEP=") {
					foundKeep = true
					break
				}
			}
			if !foundKeep {
				t.Fatal("unrelated environment variable was unexpectedly dropped")
			}
		})
	}
}

// pollutedSessionEnv sets one obviously wrong value for each variable a live
// orchestrator or agent session exports, covering every family CleanEnv scrubs.
func pollutedSessionEnv(t *testing.T) []string {
	t.Helper()
	keys := []string{
		"SOHO_TRANSPORT", "SOHO_DIR", "SOHO_STATE_DIR", "SOHO_REGISTRY_DIR", "SOHO_MAX_WORKERS", "SOHO_LANES",
		"HC_SCOPE", "HC_TERMINAL_ID", "HC_TERMINAL_SOCKET",
		"HERDR_ENV", "HERDR_PANE_ID", "HERDR_WORKSPACE_ID",
		"CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CODEX_HOME", "PI_CODING_AGENT_DIR",
		"OPENCODE_CONFIG_DIR", "GROK_API_KEY", "GEMINI_API_KEY", "CURSOR_TRACE_ID",
		// Lowercase and mixed case: Windows environment names are case-insensitive.
		"soho_lanes", "Claude_Code_Session_Id",
	}
	for _, key := range keys {
		t.Setenv(key, "polluted-"+strings.ToLower(key))
	}
	return keys
}

// Mutation captured: removing the session-family scrub from CleanEnv lets the
// variables of a live orchestrator or agent reach every fixture built from it
// (SOHO_LANES, SOHO_MAX_WORKERS, CLAUDE_CODE_SESSION_ID changed test results).
func TestCleanEnvScrubsEverySessionFamily(t *testing.T) {
	polluted := pollutedSessionEnv(t)
	t.Setenv("TEST_CLEAN_ENV_KEEP", "keep-val")

	env := CleanEnv(t)
	// CleanEnv itself sets these three; they are the only family members a
	// fixture may carry without the test setting them.
	explicit := []string{"HERDR_SOCKET_PATH", "SOHO_PRESSURE_DISK_FREE_PERCENT", "SOHO_PRESSURE_SWAP_PERCENT"}
	if leaked := SessionVars(env, explicit...); len(leaked) != 0 {
		t.Fatalf("session variables reached the fixture environment: %v", leaked)
	}
	for _, key := range polluted {
		for _, item := range env {
			if name, value, _ := strings.Cut(item, "="); strings.EqualFold(name, key) && strings.HasPrefix(value, "polluted-") {
				t.Fatalf("polluted %s reached the fixture environment", name)
			}
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "TEST_CLEAN_ENV_KEEP=keep-val") {
		t.Fatal("unrelated environment variable was dropped")
	}
}

// Negative case for the guard: a fixture environment that carries a session
// variable the test did not set is reported by name, and one the test set
// explicitly is not; unrelated variables never count.
func TestSessionVarsReportsOnlyUnsetFamilyMembers(t *testing.T) {
	env := []string{"PATH=/bin", "SOHO_LANES=off", "claude_code_session_id=x", "CLAUDECODE=1", "HC_SCOPE=zz", "PIPE_FD=3", "HERDR_ENV=", "NOEQUALS"}
	got := SessionVars(env, "soho_lanes", "HERDR_ENV")
	want := []string{"claude_code_session_id", "CLAUDECODE", "HC_SCOPE"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SessionVars = %v, want %v", got, want)
	}
	if got := SessionVars([]string{"PATH=/bin", "HOME=/h", "GOCACHE=/c", "CLAUDEISH=1", "PIPE_FD=3"}); len(got) != 0 {
		t.Fatalf("unrelated variables reported as session variables: %v", got)
	}
}
