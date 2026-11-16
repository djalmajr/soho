package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorPrintenvWarningOnHc(t *testing.T) {
	for _, tc := range []struct {
		name, transport, login, key string
		warn                        bool
	}{
		{"bare", "hc", "off", "!printenv OPENCODE_API_KEY", true},
		{"login", "hc", "on", "!printenv OPENCODE_API_KEY", false},
		{"shell", "hc", "off", "!/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv OPENCODE_API_KEY'", false},
		{"legacy", "herdr", "off", "!printenv OPENCODE_API_KEY", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, root := tm6DoctorEnv(t)
			env["SOHO_TRANSPORT"] = tc.transport
			env["SOHO_LOGIN_SHELL"] = tc.login
			file := filepath.Join(env.Get("HOME"), ".pi", "agent", "models.json")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(map[string]any{"providers": map[string]any{"vendor": map[string]any{"apiKey": tc.key, "models": []any{map[string]any{"id": "model"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, b, 0600); err != nil {
				t.Fatal(err)
			}
			out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
			marker := "pi provider 'vendor': worker inherits only the Core environment"
			if strings.Contains(out, marker) != tc.warn {
				t.Fatalf("want warning=%t; output=%s", tc.warn, out)
			}
			if tc.warn && (!strings.Contains(out, "OPENCODE_API_KEY") || !strings.Contains(out, "login_shell=on")) {
				t.Fatalf("missing remediation: %s", out)
			}
		})
	}
}
