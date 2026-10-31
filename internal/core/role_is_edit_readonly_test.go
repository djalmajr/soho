package core_test

// Permanent consumers for the bounded read-only custom-role read
// (DJA-611): RoleIsEditReadOnly keeps the built-in word shortcut, the
// shared fmGetText parser semantics (quotes, commas, whitespace), and
// refuses — without unbounded reads or traversal — malformed, missing,
// oversized, linked or unsafe selected role metadata, with no writes.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// reRoleFixture builds a project cwd with a .agents/soho-roles dir under
// the hermetic fixture: the env points HOME/USERPROFILE/PATH at the
// fixture (no git on PATH, so ProjectRoot falls back to cwd), and the
// skill lookup finds no source, so RoleDirsReadOnly lists only the
// project roles dir (plus SOHO_ROLES when set).
func reRoleFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	env, root := roFixture(t)
	roles := filepath.Join(root, ".agents", "soho-roles")
	if err := os.MkdirAll(roles, 0o755); err != nil {
		t.Fatal(err)
	}
	return env, root
}

func reRoleWrite(t *testing.T, dir, role, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, role+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRoleIsEditReadOnlyCustomRoles: valid edit and read-only roles,
// malformed frontmatter, missing metadata, filename traversal refused
// before any outside-body consumption, the .append/empty grammar, the
// built-in shortcut and the no-writes invariant.
func TestRoleIsEditReadOnlyCustomRoles(t *testing.T) {
	env, root := reRoleFixture(t)
	roles := filepath.Join(root, ".agents", "soho-roles")

	// Valid custom edit roles through the shared parser value rules.
	reRoleWrite(t, roles, "custom-edit", "---\nmode: edit\n---\nbody\n")
	reRoleWrite(t, roles, "custom-quoted", "---\nmode: \"edit\"\n---\nbody\n")
	reRoleWrite(t, roles, "custom-bracket", "---\nmode: [edit]\n---\nbody\n")
	reRoleWrite(t, roles, "custom-ro", "---\nmode: read-only\n---\nbody\n")
	reRoleWrite(t, roles, "custom-nomode", "---\nkind: implementer\n---\nbody\n")
	reRoleWrite(t, roles, "custom-nocolon", "---\nmode edit\n---\nbody\n")
	reRoleWrite(t, roles, "custom-bare", "no frontmatter at all\n")
	if got := core.RoleIsEditReadOnly("custom-edit", env, root); !got {
		t.Fatal("a valid custom edit role must resolve true")
	}
	for _, role := range []string{"custom-quoted", "custom-bracket"} {
		if got := core.RoleIsEditReadOnly(role, env, root); !got {
			t.Fatalf("the shared parser value rules changed for %s: got false", role)
		}
	}
	for _, role := range []string{"custom-ro", "custom-nomode", "custom-nocolon", "custom-bare", "custom-missing"} {
		if got := core.RoleIsEditReadOnly(role, env, root); got {
			t.Fatalf("%s must not resolve edit through read-only inspection", role)
		}
	}

	// Filename traversal is refused before the outside body is consumed:
	// a valid edit role outside the roles dir must not be reached.
	outside := filepath.Join(root, ".agents", "evil.md")
	reRoleWrite(t, filepath.Join(root, ".agents"), "evil", "---\nmode: edit\n---\noutside\n")
	for _, role := range []string{"../evil", "evil/..", ""} {
		if got := core.RoleIsEditReadOnly(role, env, root); got {
			t.Fatalf("role %q must be refused as an unsafe filename before any lookup", role)
		}
	}
	// The .append grammar stays: append roles are not role files.
	if got := core.RoleIsEditReadOnly("custom-edit.append", env, root); got {
		t.Fatal("an .append role must not resolve edit")
	}

	// Built-in compatibility: the shortcut holds with no files at all.
	for _, role := range strings.Fields(core.EditRoles) {
		if got := core.RoleIsEditReadOnly(role, env, filepath.Join(root, "elsewhere")); !got {
			t.Fatalf("built-in role %s must keep the edit shortcut", role)
		}
	}

	// No writes: the fixture tree is untouched by the inspection.
	before := roSnapshot(t, root)
	_ = core.RoleIsEditReadOnly("custom-edit", env, root)
	_ = core.RoleIsEditReadOnly("../evil", env, root)
	if got := roSnapshot(t, root); !reflect.DeepEqual(got, before) {
		t.Fatalf("the read-only role inspection created or changed files: %s", diffMaps(got, before))
	}
	if want := "---\nmode: edit\n---\noutside\n"; readString(t, outside) != want {
		t.Fatalf("the outside role file changed: %q", readString(t, outside))
	}
}

// TestReadOnlyRoleOversizeBounded: an 8MiB custom edit role refuses
// through the bounded configuration read without reading or allocating
// the body (the original Root RED was accepted=true allocated=50141808).
func TestReadOnlyRoleOversizeBounded(t *testing.T) {
	env, root := reRoleFixture(t)
	roles := filepath.Join(root, ".agents", "soho-roles")
	file := filepath.Join(roles, "big.md")
	if err := os.WriteFile(file, append([]byte("---\nmode: edit\n---\n"), make([]byte, 8<<20)[:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	accepted := core.RoleIsEditReadOnly("big", env, root)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("oversized role accepted=%v allocated=%d", accepted, allocated)
	if accepted || allocated >= 4<<20 {
		t.Fatalf("the oversize role must refuse within the readonly bound (allocated %d bytes)", allocated)
	}
}

// TestReadOnlyRoleSymlinkRefused: a selected role file that is a link to
// a valid edit role outside the roles dir is refused, never followed,
// and the target stays untouched.
func TestReadOnlyRoleSymlinkRefused(t *testing.T) {
	env, root := reRoleFixture(t)
	roles := filepath.Join(root, ".agents", "soho-roles")
	target := filepath.Join(root, "outside-target.md")
	reRoleWrite(t, root, "outside-target", "---\nmode: edit\n---\noutside\n")
	link := filepath.Join(roles, "linked.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := readString(t, target)
	if got := core.RoleIsEditReadOnly("linked", env, root); got {
		t.Fatal("a linked role file must be refused, never followed")
	}
	if got := readString(t, target); got != before {
		t.Fatalf("the link target changed: %q", got)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the link itself must remain in place: %v", err)
	}
}

// TestFmGetParserSemanticsPreserved: the ordinary FmGet keeps the shared
// value rules after the parser extraction (quotes, commas, whitespace,
// the .append suffix and the frontmatter shape).
func TestFmGetParserSemanticsPreserved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "role.md")
	if err := os.WriteFile(file, []byte("---\nmode: edit\nname: \"a,  b\"\nlist: [x, y ,  z]\nindented:   spaced value  \n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"mode":     "edit",
		"name":     "a b",
		"list":     "x y z",
		"indented": "spaced value",
		"absent":   "",
		"MODE":     "",
	}
	for key, want := range cases {
		if got := core.FmGet(file, key); got != want {
			t.Fatalf("FmGet %q = %q, want %q", key, got, want)
		}
	}
	appendFile := filepath.Join(dir, "role.append.md")
	if err := os.WriteFile(appendFile, []byte("---\nmode: edit\n---\nappend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := core.FmGet(appendFile, "mode"); got != "" {
		t.Fatalf("the .append.md suffix must keep refusing FmGet, got %q", got)
	}
	bare := filepath.Join(dir, "bare.md")
	if err := os.WriteFile(bare, []byte("no frontmatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := core.FmGet(bare, "mode"); got != "" {
		t.Fatalf("a file without the frontmatter fence must keep returning empty, got %q", got)
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
