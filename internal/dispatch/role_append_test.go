package dispatch

import (
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeProjectRoleAppend(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	skill := t.TempDir()
	base := filepath.Join(skill, "roles", "implementer.md")
	for p, body := range map[string]string{base: "---\nname: Implementer\n---\nbase rule\n", filepath.Join(root, ".agents", "soho-roles", "implementer.append.md"): "---\nkind: ignore-this\n---\nproject rule\n"} {
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": "", "SOHO_SKILL_DIR": skill}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	text := ComposePrompt(base, "implementer", "worker", "brief body", filepath.Join(root, "report.md"), ctx, env, "codex", "", "", false)
	if !strings.Contains(text, "base rule\n\nproject rule") || strings.Contains(text, "ignore-this") || strings.Index(text, "project rule") > strings.Index(text, "# Brief") {
		t.Fatalf("composition=%s", text)
	}
	// Explicit callers of ComposePrompt retain the passed base, even if a
	// separate RoleFile lookup would select another file.
	explicit := filepath.Join(t.TempDir(), "explicit.md")
	if e := os.WriteFile(explicit, []byte("explicit base\n"), 0600); e != nil {
		t.Fatal(e)
	}
	text = ComposePrompt(explicit, "implementer", "worker", "brief body", "report.md", ctx, env, "codex", "", "", false)
	if !strings.Contains(text, "explicit base\n\nproject rule") || strings.Contains(text, "base rule") {
		t.Fatalf("explicit composition=%s", text)
	}
}
