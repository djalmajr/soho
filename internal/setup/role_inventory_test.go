package setup

import (
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRoleDetectionExcludesAppend(t *testing.T) {
	env, root := migrateEnv(t)
	env["TMPDIR"] = t.TempDir()
	env["SOHO_SKILL_DIR"] = t.TempDir()
	project := filepath.Join(root, ".agents", "soho-roles")
	mustWriteFile(t, filepath.Join(project, "implementer.append.md"), "---\nkind: ignored\n---\nproject extension\n")
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	assert := func(wantBase bool) {
		t.Helper()
		// Run the detection consumer, not just the directory-reading helper.
		doc := Detect(ctx, env, root)
		config, _ := doc.Get("config")
		roles, _ := config.(*jsonjs.Object).Get("role_kinds")
		rows := roles.([]any)
		found := false
		for _, item := range rows {
			row := item.(*jsonjs.Object)
			key, _ := row.Get("key")
			if strings.Contains(key.(string), ".append") {
				t.Fatalf("append detected as role: %s", jsonjs.Stringify(doc))
			}
			if key == "role.implementer.kind" {
				found = true
				kind, _ := row.Get("value")
				source, _ := row.Get("source")
				if kind != "grok" || source != "role" {
					t.Fatalf("base metadata changed: %s", jsonjs.Stringify(row))
				}
			}
		}
		if found != wantBase || (!wantBase && len(rows) != 0) {
			t.Fatalf("role inventory=%s want base=%v", jsonjs.Stringify(rows), wantBase)
		}
	}
	assert(false)
	mustWriteFile(t, filepath.Join(project, "implementer.md"), "---\nkind: grok\n---\nbase body\n")
	assert(true)
}
