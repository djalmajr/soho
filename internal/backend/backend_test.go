package backend

import (
	"fmt"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// backendTypeName returns the concrete type name of the Services' Runtime
// implementation (e.g. "…internal/herdr.backend"), so a test can name which
// backend New selected without launching a binary: the two backends'
// concrete types are unexported, and only their package paths differ.
func backendTypeName(t *testing.T, env platform.Env) string {
	t.Helper()
	return fmt.Sprintf("%T", New(env).Runtime)
}

// TestNewPicksTheHcBackend pins the selector: SOHO_TRANSPORT=hc selects the
// hc backend.
func TestNewPicksTheHcBackend(t *testing.T) {
	got := backendTypeName(t, platform.Env{"SOHO_TRANSPORT": "hc"})
	if !strings.HasSuffix(got, "hcbackend.backend") {
		t.Fatalf("type %q, want the hc backend", got)
	}
}

// TestNewKeepsHerdrByDefault pins the selector's default: an empty or an
// invalid value keeps the herdr backend (herdr is the only mode that runs
// today, so nothing else may change the selection).
func TestNewKeepsHerdrByDefault(t *testing.T) {
	cases := map[string]string{
		"absent":    "",
		"empty":     "",
		"herdr":     "herdr",
		"invalid":   "tmux",
		"lowercase": "HC",
	}
	for name, value := range cases {
		name, value := name, value
		t.Run(name, func(t *testing.T) {
			env := platform.Env{}
			if value != "" || name == "empty" {
				env["SOHO_TRANSPORT"] = value
			}
			got := backendTypeName(t, env)
			if !strings.HasSuffix(got, "herdr.backend") {
				t.Fatalf("SOHO_TRANSPORT=%q: type %q, want the herdr backend", value, got)
			}
		})
	}
}
