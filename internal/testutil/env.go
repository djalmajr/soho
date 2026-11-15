package testutil

import (
	"github.com/djalmajr/soho/internal/testutil/testenv"
	"testing"
)

// IsSessionVar reports whether key belongs to a session family removed from fixtures.
func IsSessionVar(key string) bool { return testenv.IsSessionVar(key) }

// SessionVars returns unapproved session variable names, never their values.
func SessionVars(env []string, explicit ...string) []string {
	return testenv.SessionVars(env, explicit...)
}

// CleanEnv scrubs session families and supplies private temporary and socket paths.
// The implementation lives in a leaf package so platform's own tests can use it.
func CleanEnv(t testing.TB) []string { t.Helper(); return testenv.CleanEnv(t) }
