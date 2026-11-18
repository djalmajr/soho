package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestInheritPreviousLint(t *testing.T) {
	file := filepath.Join(t.TempDir(), "brief.md")
	body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n- inherit: previous brief\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, previous := range []bool{false, true} {
		result := BriefLintFindings(file, &core.Config{}, platform.Env{}, BriefLintOptions{PreviousTask: previous})
		warned := strings.Contains(strings.Join(result.Warnings, "\n"), "no previous task")
		if warned == previous {
			t.Fatalf("previous=%v warnings=%v", previous, result.Warnings)
		}
	}
}
