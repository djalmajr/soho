package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDoctorTrustCwd(t *testing.T) {
	for _, value := range []string{"on", "off", "invalid"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			env := platform.Env{"SOHO_SKILL_DIR": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_LANES": "off"}
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				env[key] = filepath.Join(root, key)
				if err := os.Mkdir(env[key], 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv(key, env[key])
			}
			ctx := core.Config{Entries: map[string]core.ConfigEntry{"trust_cwd": {Value: value, Source: "session"}}}
			var out strings.Builder
			DoctorCheck(&ctx, env, root, &out)
			mode, description := "on", "codex workers trust their repository root for the session; set trust_cwd=off to keep codex's folder dialog"
			if value != "on" {
				mode = "off"
				description = "codex workers keep codex's folder dialog"
			}
			want := "trust_cwd: " + mode + " (" + description + ") [session]"
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q: %s", want, out.String())
			}
			warning := "config: trust_cwd='invalid' is not on|off (it applies as off)"
			if strings.Contains(out.String(), warning) != (value == "invalid") {
				t.Fatalf("invalid warning: %s", out.String())
			}
		})
	}
}
