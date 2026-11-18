package doctor

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	skill "github.com/djalmajr/soho/skills"
)

func TestDoctorSohoSkill(t *testing.T) {
	for _, tc := range []struct {
		name    string
		warning string
	}{
		{"identical", ""},
		{"crlf", ""},
		{"crlf-changed", "differs from the skill of this build"},
		{"changed-role", "differs from the skill of this build"},
		{"extra", ""},
		{"override", ""},
		{"unreadable", "cannot read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir, err := skill.Materialize(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(home, ".agents", "skills")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir, filepath.Join(parent, "soho")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			env := platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": home, "TMPDIR": home, "PATH": ""}
			if strings.HasPrefix(tc.name, "crlf") {
				err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
					if walkErr != nil || entry.IsDir() {
						return walkErr
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					data = bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
					if tc.name == "crlf-changed" && path == filepath.Join(dir, "SKILL.md") {
						data = append(data, []byte("changed line\r\n")...)
					}
					return os.WriteFile(path, data, 0o600)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			switch tc.name {
			case "changed-role", "override":
				err = os.WriteFile(filepath.Join(dir, "roles", "implementer.md"), []byte("stale"), 0o600)
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "user-addition"), []byte("keep"), 0o600)
			case "unreadable":
				err = os.Remove(filepath.Join(dir, "config.defaults"))
				if err == nil {
					err = os.Mkdir(filepath.Join(dir, "config.defaults"), 0o700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "override" {
				env["SOHO_SKILL_DIR"] = dir
			}
			var out bytes.Buffer
			s := &Say{Out: &out}
			doctorSohoSkill(env, s)
			wantSource := "home"
			if tc.name == "override" {
				wantSource = "SOHO_SKILL_DIR"
			}
			if !strings.Contains(out.String(), "("+wantSource+")") {
				t.Fatalf("missing source: %s", out.String())
			}
			if tc.warning == "" && s.Warn != 0 || tc.warning != "" && (s.Warn != 1 || !strings.Contains(out.String(), tc.warning) || !strings.Contains(out.String(), filepath.Join(parent, "soho"))) {
				t.Fatalf("warnings=%d, output=%s", s.Warn, out.String())
			}
		})
	}
}
