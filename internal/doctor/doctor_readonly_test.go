package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func doctorGitInit(t *testing.T, root string) {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved // git reports the resolved spelling (macOS /tmp)
	}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
}

func doctorDiagnosticFixture(t *testing.T) (string, platform.Env) {
	t.Helper()
	root := t.TempDir()
	doctorGitInit(t, root)
	home := t.TempDir()
	env := platform.Env{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"TMPDIR":          home,
		"SOHO_SKILL_DIR":  filepath.Join(t.TempDir(), "no-skill"),
		"PATH":            a13GitOnlyPath(t),
	}
	return root, env
}

func TestDoctorDiagnosticDoesNotTouchGitignore(t *testing.T) {
	t.Run("absent gitignore stays absent", func(t *testing.T) {
		root, env := doctorDiagnosticFixture(t)
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		var out strings.Builder
		DoctorCheck(&ctx, env, root, &out)
		if !strings.Contains(out.String(), "state dir writable") {
			t.Fatalf("ordinary state-dir branch missing:\n%s", out.String())
		}
		if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
			t.Fatalf("doctor created .gitignore: %v", err)
		}
	})

	t.Run("existing gitignore keeps bytes and mode", func(t *testing.T) {
		root, env := doctorDiagnosticFixture(t)
		ignore := filepath.Join(root, ".gitignore")
		original := []byte("# pre-existing\n")
		if err := os.WriteFile(ignore, original, 0o600); err != nil {
			t.Fatalf("fixture write: %v", err)
		}
		before, err := os.Stat(ignore)
		if err != nil {
			t.Fatalf("fixture stat: %v", err)
		}
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		var out strings.Builder
		DoctorCheck(&ctx, env, root, &out)
		if !strings.Contains(out.String(), "state dir writable") {
			t.Fatalf("ordinary state-dir branch missing:\n%s", out.String())
		}
		got, err := os.ReadFile(ignore)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf(".gitignore bytes changed: %q err=%v", got, err)
		}
		after, err := os.Stat(ignore)
		if err != nil {
			t.Fatalf("post-doctor stat: %v", err)
		}
		if before.Mode().Perm() != after.Mode().Perm() {
			t.Errorf(".gitignore mode: want %04o, got %04o", before.Mode().Perm(), after.Mode().Perm())
		}
	})
}
