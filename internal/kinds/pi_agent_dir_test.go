package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// PI_CODING_AGENT_DIR moves pi's models, settings and sessions: soho reads
// the same directory (absolute or "~/" forms); a relative value is ignored.
func TestPiAgentDirHonorsTheOverride(t *testing.T) {
	home := t.TempDir()
	write := func(dir, id string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		content := `{"providers":{"vendor":{"apiKey":"$KEY","models":[{"id":"` + id + `"}]}}}`
		if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".pi", "agent"), "default")
	override := filepath.Join(home, "private-pi")
	write(override, "override")
	for _, tc := range []struct{ name, value, want string }{
		{"unset", "", "vendor/default"},
		{"absolute", override, "vendor/override"},
		{"tilde", "~/private-pi", "vendor/override"},
		{"relative ignored", "private-pi", "vendor/default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := platform.Env{"HOME": home, "USERPROFILE": home}
			if tc.value != "" {
				env["PI_CODING_AGENT_DIR"] = tc.value
			}
			got := PiOwnModels(env)
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("PI_CODING_AGENT_DIR=%q: got %#v, want %s", tc.value, got, tc.want)
			}
		})
	}
}
