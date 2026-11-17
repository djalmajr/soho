package dispatch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestOrchestratorFilesLintOptional(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	for _, typ := range []string{"", "mechanical", "backend", "ui", "docs", "review", "security"} {
		t.Run(typ, func(t *testing.T) {
			base := "Type: " + typ + "\n# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
			file := filepath.Join(t.TempDir(), "brief.md")
			if err := os.WriteFile(file, []byte(base), 0600); err != nil {
				t.Fatal(err)
			}
			without := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
			if err := os.WriteFile(file, []byte(base+"## Orchestrator files\n- `docs/**`\n"), 0600); err != nil {
				t.Fatal(err)
			}
			with := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
			if !reflect.DeepEqual(with, without) {
				t.Fatalf("section changed lint: without=%+v with=%+v", without, with)
			}
		})
	}
}
