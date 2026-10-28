package spawn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestConfigNativeArgsCombinesKindAndRoleArgs(t *testing.T) {
	stubCodexHelp(t, false)
	root := t.TempDir()
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(roles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "designer.md"), []byte("---\nkind: codex\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"SOHO_ROLES": roles, "HOME": root, "SOHO_SKILL_DIR": root}
	if err := os.WriteFile(filepath.Join(root, "config.defaults"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{"args_codex": {Value: "--verbose", Source: "project"}, "role_designer_args": {Value: "-c x=y", Source: "project"}}, Order: []string{"args_codex", "role_designer_args"}}
	if got := ConfigNativeArgs("codex", "", "designer", &ctx, env, root, nil); got != "--verbose -c x=y" {
		t.Fatalf("got %q", got)
	}
}
