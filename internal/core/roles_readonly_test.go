package core

// Focused read-only companion tests for RoleIsEditReadOnly (review-2 F2):
// the inspection's edit-role decision shares RoleIsEdit's built-in rule and
// the FmGet parser, but never materializes the skill bundle and never Dies.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func rolesReadOnlyFixture(t *testing.T, skillDir string) (string, platform.Env) {
	t.Helper()
	cwd := t.TempDir()
	home := t.TempDir()
	if skillDir != "" {
		if err := os.MkdirAll(filepath.Join(cwd, ".agents", "soho-roles"), 0o700); err != nil {
			t.Fatal(err)
		}
		// A project role in edit mode: the read-only lookup must keep the
		// current rules for readable project roles.
		if err := os.WriteFile(filepath.Join(cwd, ".agents", "soho-roles", "project-reviewer.md"), []byte("---\nmode: edit\n---\nbody\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, ".gitkeep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":            filepath.Join(cwd, "no-such-bin"),
		"HOME":            home,
		"USERPROFILE":     home,
		"TMPDIR":          cwd,
		"XDG_CONFIG_HOME": home,
		"XDG_CACHE_HOME":  filepath.Join(home, "cache"),
		"SOHO_DIR":        t.TempDir(),
	}
	if skillDir != "" {
		env["SOHO_SKILL_DIR"] = skillDir
	}
	return cwd, env
}

// TestRoleIsEditReadOnlyBuiltinKeepsTheWordRule: the built-in edit roles are
// true without any file or skill lookup at all.
func TestRoleIsEditReadOnlyBuiltinKeepsTheWordRule(t *testing.T) {
	cwd, env := rolesReadOnlyFixture(t, "")
	if !RoleIsEditReadOnly("implementer", env, cwd) {
		t.Fatal("the built-in implementer role must stay edit")
	}
	if !RoleIsEditReadOnly("designer", env, cwd) {
		t.Fatal("the built-in designer role must stay edit")
	}
	if RoleIsEditReadOnly("reviewer", env, cwd) {
		t.Fatal("the built-in reviewer role is not edit")
	}
}

// TestRoleIsEditReadOnlyUnavailableCustomRoleIsFalseAndNonfatal: a custom
// role with no project, env or existing-skill metadata returns false —
// without materializing the bundle and without the Die of RoleFile — even
// with SOHO_NOWRITE set, where the ordinary lookup dies.
func TestRoleIsEditReadOnlyUnavailableCustomRoleIsFalseAndNonfatal(t *testing.T) {
	cwd, env := rolesReadOnlyFixture(t, "")
	env["SOHO_NOWRITE"] = "1"
	before, err := os.ReadDir(filepath.Join(env.Get("HOME"), "cache"))
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	var pan any
	func() {
		defer func() { pan = recover() }()
		// The ordinary lookup Dies (exit 2) on this fixture; the read-only
		// one must not.
		if RoleIsEdit("custom-review", env, cwd) {
			t.Fatal("the ordinary lookup must not find a custom role here")
		}
	}()
	exit, ok := pan.(*platform.ExitError)
	if !ok || exit.Code != 2 || exit.Msg != "cannot find skill directory; set SOHO_SKILL_DIR" {
		t.Fatalf("the ordinary lookup panic = %#v, want the skill-dir Die", pan)
	}
	if RoleIsEditReadOnly("custom-review", env, cwd) {
		t.Fatal("the read-only lookup must report false on unavailable custom role metadata")
	}
	if RoleIsEditReadOnly("no-such-role.append", env, cwd) {
		t.Fatal("the .append suffix must keep the ordinary refusal")
	}
	after, err := os.ReadDir(filepath.Join(env.Get("HOME"), "cache"))
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if before != nil {
			t.Fatal("the cache directory vanished")
		}
		return
	}
	if before == nil {
		t.Fatalf("the read-only lookup materialized the cache: %v", after)
	}
	if len(before) != len(after) {
		t.Fatalf("the read-only lookup changed the cache: before=%v after=%v", before, after)
	}
}

// TestRoleIsEditReadOnlyExistingSkillRoleKeepsTheCurrentRules: an existing
// skill role dir (SOHO_SKILL_DIR) is read like the ordinary lookup: the
// mode: edit file keeps its rule.
func TestRoleIsEditReadOnlyExistingSkillRoleKeepsTheCurrentRules(t *testing.T) {
	skill := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "roles", "custom-review.md"), []byte("---\nmode: edit\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, env := rolesReadOnlyFixture(t, skill)
	if !RoleIsEditReadOnly("custom-review", env, cwd) {
		t.Fatal("the readable existing-skill role must keep its edit rule")
	}
	cwd2, env2 := rolesReadOnlyFixture(t, skill)
	// A project role dir in the cwd with a non-edit file: the project layer
	// still wins and keeps its rule.
	if err := os.MkdirAll(filepath.Join(cwd2, ".agents", "soho-roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd2, ".agents", "soho-roles", "custom-review.md"), []byte("---\nmode: review\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if RoleIsEditReadOnly("custom-review", env2, cwd2) {
		t.Fatal("the project layer must win with its non-edit rule")
	}
}
