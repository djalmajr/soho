package dispatch

// SOH-495: brief lint stops warning "names no path" for the real brief items,
// while a prose item whose backticks hold no path still warns.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestOwnedNotePathLint(t *testing.T) {
	root := t.TempDir()
	brief := filepath.Join(root, "worker.brief.md")
	lint := func() BriefLintResult {
		t.Helper()
		env := platform.Env{"SOHO_BRIEF_LINT": "warn", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root}
		return BriefLintFindings(brief, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, BriefLintOptions{WorkerCwd: root})
	}
	for _, tc := range []struct {
		name, section string
		wantWarning   bool
	}{
		{"dispatch.go note", "- internal/cli/dispatch.go (`ownedPathsIn` only)", false},
		{"brief_signals.go note", "- internal/dispatch/brief_signals.go (or the file holding `BriefHasLiteralOwnedItems`)", false},
		{"declaration with a reference in the note", "- src/a.go (see `x` in docs/guide.md)", false},
		{"declaration before a colon reference", "- src/a.go: see docs/guide.md for `x`", false},
		{"reference only", "- (see `x` in docs/guide.md)", true},
		{"colon reference only", "- See: docs/guide.md for `x`", true},
		{"prose note only", "- See `ai.openChat` for details", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n" + tc.section + "\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
			if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got := lint()
			warns := false
			for _, warning := range got.Warnings {
				if strings.Contains(warning, "names no path") {
					warns = true
				}
			}
			if warns != tc.wantWarning {
				t.Fatalf("names-no-path warning = %t, want %t; warnings=%q", warns, tc.wantWarning, got.Warnings)
			}
			if tc.wantWarning {
				strict := false
				for _, reason := range got.UnseenPaths {
					if strings.Contains(reason, "names no path") {
						strict = true
					}
				}
				if !strict {
					t.Fatalf("the no-scope reason is missing from UnseenPaths: %q", got.UnseenPaths)
				}
			}
		})
	}
}
