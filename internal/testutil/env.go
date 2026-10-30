package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CleanEnv returns the current environment without inherited Herdr session
// values (the machine-state resource pressure limits included, so a test
// never warns because of the host it runs on) or soho state roots
// (SOHO_REGISTRY_DIR, SOHO_DIR and SOHO_STATE_DIR, so a test run inside a
// soho pane never writes into the session's real registry or state), points
// Herdr at a socket path that cannot resolve a live session, and gives the
// test its own TMPDIR: caches live there (the model lists), and a shared one
// let a test in one package read what another package wrote.
func CleanEnv(t testing.TB) []string {
	t.Helper()

	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+4)
	for _, item := range inherited {
		key, _, ok := strings.Cut(item, "=")
		if !ok ||
			strings.HasPrefix(strings.ToUpper(key), "HERDR_") ||
			strings.EqualFold(key, "TMPDIR") ||
			strings.EqualFold(key, "SOHO_REGISTRY_DIR") ||
			strings.EqualFold(key, "SOHO_DIR") ||
			strings.EqualFold(key, "SOHO_STATE_DIR") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "HERDR_SOCKET_PATH="+filepath.Join(t.TempDir(), "herdr.sock"), "TMPDIR="+t.TempDir(),
		"SOHO_PRESSURE_DISK_FREE_PERCENT=0", "SOHO_PRESSURE_SWAP_PERCENT=0")
}
