package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestInheritPreviousLintHintOption(t *testing.T) {
	file := filepath.Join(t.TempDir(), "brief.md")
	body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n- inherit: previous brief\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	defaultResult := BriefLintFindings(file, &core.Config{}, platform.Env{}, BriefLintOptions{})
	wantDefault := "brief " + file + " inherits Owned files but the worker has no previous task in this worktree"
	if len(defaultResult.Warnings) != 1 || defaultResult.Warnings[0] != wantDefault {
		t.Fatalf("dispatch warning=%q; want %q", defaultResult.Warnings, wantDefault)
	}
	hintResult := BriefLintFindings(file, &core.Config{}, platform.Env{}, BriefLintOptions{LintInheritHintPath: "'brief.md'"})
	wantHint := "brief 'brief.md' inherits Owned files from the worker's previous task; lint cannot check that without the worker: soho lint 'brief.md' --agent <name> (dispatch checks it)"
	if len(hintResult.Warnings) != 1 || hintResult.Warnings[0] != wantHint {
		t.Fatalf("lint hint warning=%q; want %q", hintResult.Warnings, wantHint)
	}
}

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
