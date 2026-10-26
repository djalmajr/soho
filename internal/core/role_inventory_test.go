package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRoleConfigInventoryExcludesAppend(t *testing.T) {
	for _, role := range []string{"implementer", "critical-implementer"} {
		t.Run(role, func(t *testing.T) {
			env, repo := fixture(t)
			env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
			env["SOHO_SKILL_DIR"] = t.TempDir()
			project := filepath.Join(repo, ".agents", "soho-roles")
			write(t, filepath.Join(project, role+".append.md"), "---\nkind: ignored\n---\nproject body\n")
			ctx := &Config{Entries: map[string]ConfigEntry{}}
			// DottedKeyName accepts a key string and preserves unknown spellings.
			// A literal dot in the API input lets the hyphenated case distinguish
			// an undeclared append from a known role in this private inventory.
			middle := strings.ReplaceAll(role, "-", "_") + ".append"
			key := "role_" + middle + "_kind"
			want := "role." + middle + ".kind"
			got := DottedKeyName(key, ctx, env, repo)
			t.Logf("append-only role=%s input=%q output=%q want unknown spelling=%q", role, key, got, want)
			if got != want {
				t.Fatalf("append restored a declared role spelling: got=%q want=%q", got, want)
			}
			if file := RoleFile(role+".append", env, repo); file != "" {
				t.Fatalf("append is executable: %s", file)
			}
			// Conventional normalized CLI keys must not become an append role.
			if got := DottedKeyName("role_"+strings.ReplaceAll(role, "-", "_")+"_append_kind", ctx, env, repo); strings.Contains(got, ".append.") {
				t.Fatalf("pseudo-role key=%q", got)
			}
			// A real role remains in the inventory and retains its spelling.
			write(t, filepath.Join(project, role+".md"), "---\nkind: grok\n---\nbase\n")
			if got := DottedKeyName("role_"+strings.ReplaceAll(role, "-", "_")+"_kind", ctx, env, repo); got != "role."+role+".kind" {
				t.Fatalf("real role lost its spelling: %q", got)
			}
		})
	}
}
