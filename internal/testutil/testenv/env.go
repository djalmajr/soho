// Package testenv provides environment fixtures without platform dependencies.
package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionEnvPrefixes and sessionEnvNames name the families of variables an
// agent session, a soho orchestrator or a Herdr/Herdr Core client exports into
// its children. A test fixture never inherits them: only what a test sets
// explicitly reaches the code under test.
var (
	sessionEnvPrefixes = []string{
		"SOHO_", "HC_", "HERDR_", "CLAUDE_", "CODEX_", "PI_", "OPENCODE_", "GROK_", "GEMINI_", "CURSOR_",
	}
	sessionEnvNames = []string{"CLAUDECODE"}
)

// IsSessionVar reports whether key belongs to a session family that CleanEnv
// removes (compared case-insensitively, as Windows treats environment names).
func IsSessionVar(key string) bool {
	upper := strings.ToUpper(key)
	for _, name := range sessionEnvNames {
		if upper == name {
			return true
		}
	}
	for _, prefix := range sessionEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// SessionVars returns the names, in env's order, of the variables of the
// session families in env (KEY=value entries) that are not in explicit. It is
// the guard fixtures use to prove nothing but what a test sets reached them;
// it never returns values.
func SessionVars(env []string, explicit ...string) []string {
	allowed := make(map[string]bool, len(explicit))
	for _, key := range explicit {
		allowed[strings.ToUpper(key)] = true
	}
	var leaked []string
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && IsSessionVar(key) && !allowed[strings.ToUpper(key)] {
			leaked = append(leaked, key)
		}
	}
	return leaked
}

// CleanEnv returns the current environment without any variable of the
// session families (SOHO_*, HC_*, HERDR_*, CLAUDE_*, CLAUDECODE, CODEX_*, PI_*,
// OPENCODE_*, GROK_*, GEMINI_*, CURSOR_*): Herdr session values, native caller
// context (HC_TERMINAL_ID, HC_SCOPE, SOHO_TRANSPORT), soho state roots and
// limits (SOHO_REGISTRY_DIR, SOHO_DIR, SOHO_STATE_DIR, SOHO_MAX_WORKERS,
// SOHO_LANES) and agent caller identity (CLAUDE_CODE_SESSION_ID), so a test
// runner invoked from a live soho session or agent never leaks them into a
// fixture and never writes into the session's real registry or state. It
// also drops TMPDIR, points Herdr at a socket path that cannot resolve a live
// session, sets the machine-state resource pressure limits to zero (so a test
// never warns because of the host it runs on) and gives the test its own
// TMPDIR: caches live there (the model lists), and a shared one let a test in
// one package read what another package wrote. A test that needs one of the
// families sets it explicitly after the call.
func CleanEnv(t testing.TB) []string {
	t.Helper()

	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+4)
	for _, item := range inherited {
		key, _, ok := strings.Cut(item, "=")
		if !ok || IsSessionVar(key) || strings.EqualFold(key, "TMPDIR") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "HERDR_SOCKET_PATH="+filepath.Join(t.TempDir(), "herdr.sock"), "TMPDIR="+t.TempDir(),
		"SOHO_PRESSURE_DISK_FREE_PERCENT=0", "SOHO_PRESSURE_SWAP_PERCENT=0")
}
