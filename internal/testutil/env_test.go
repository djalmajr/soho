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
