package kinds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDoctorPiBarePrintenv(t *testing.T) {
	for _, tc := range []struct {
		name, key, transport, login string
		want                        bool
	}{
		{"bare", "!printenv OPENCODE_API_KEY", "hc", "off", true},
		{"spaces", "!  printenv   OPENCODE_API_KEY  ", "hc", "off", true},
		{"login on", "!printenv OPENCODE_API_KEY", "hc", "on", false},
		{"legacy", "!printenv OPENCODE_API_KEY", "herdr", "off", false},
		{"zsh", "!/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv OPENCODE_API_KEY'", "hc", "off", false},
		{"suffix command", "!printenv OPENCODE_API_KEY; echo no", "hc", "off", false},
		{"argument", "!printenv OPENCODE_API_KEY OTHER", "hc", "off", false},
		{"substitution", "!printenv $(touch bad)", "hc", "off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, ".pi", "agent", "models.json")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(map[string]any{"providers": map[string]any{"vendor": map[string]any{"apiKey": tc.key, "models": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}}, "override": map[string]any{"apiKey": tc.key}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "xdg"), "SOHO_TRANSPORT": tc.transport, "SOHO_LOGIN_SHELL": tc.login}
			_, warnings := OwnProviderDoctorLines(&core.Config{}, env, root)
			var matched []string
			for _, w := range warnings {
				if strings.Contains(w, "Core environment") {
					matched = append(matched, w)
				}
			}
			if tc.want {
				if len(matched) != 2 {
					t.Fatalf("want warning per provider, got %q", warnings)
				}
				for _, w := range matched {
					for _, s := range []string{"OPENCODE_API_KEY", "login_shell=on", "/usr/bin/env -u ZDOTDIR /bin/zsh -c"} {
						if !strings.Contains(w, s) {
							t.Errorf("missing %q in %q", s, w)
						}
					}
					if strings.Contains(w, tc.key) {
						t.Errorf("apiKey leaked: %q", w)
					}
				}
			} else if len(matched) != 0 {
				t.Fatalf("unexpected warnings: %q", warnings)
			}
		})
	}
}
