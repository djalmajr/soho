package cli

// SOH-415/SOH-455: the lint, the dispatch overlap reader and the settled
// audit read the same filtered, normalized Owned files stream. Emphasis is
// stripped from a labeled path, and an inherit under a negative label does
// not open the previous scope; only an inherit after a positive label does.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/platform"
)

func TestOwnedFilesReaderParity(t *testing.T) {
	cases := []struct {
		name, section string
		normalized    []string
		overlap       []string
		audit         []string
		inherit       bool
		lintWarns     bool
	}{
		{
			name:       "labeled emphasis is stripped",
			section:    "- Owned: **NOTES.MD**",
			normalized: []string{"- NOTES.MD"},
			overlap:    []string{"NOTES.MD"},
			audit:      []string{"NOTES.MD"},
		},
		{
			name:       "inherit after Forbidden is excluded",
			section:    "- Forbidden:\n- inherit: previous brief",
			normalized: []string{},
			lintWarns:  true,
		},
		{
			name:       "inherit after Owned reopens the scope",
			section:    "- Forbidden:\n- inherit: previous brief\n- Owned:\n- inherit: previous brief",
			normalized: []string{"- inherit: previous brief"},
			audit:      []string{"previous.md"},
			inherit:    true,
		},
		{
			name:       "prose-only section",
			section:    "Os da rodada 12.",
			normalized: []string{"Os da rodada 12."},
			lintWarns:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n" + tc.section + "\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
			brief := filepath.Join(dir, "worker.brief.md")
			if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			sidecar := filepath.Join(dir, "worker.dispatch.json")
			if err := os.WriteFile(sidecar, []byte(`{"git_tree":"","git_previous_owned":["previous.md"]}`), 0o600); err != nil {
				t.Fatal(err)
			}

			// The three readers' shared stream: one normalizer, one result.
			filtered, _ := dispatch.OwnedSectionLines(strings.Split(tc.section, "\n"))
			normalized := []string{}
			for _, line := range filtered {
				if strings.TrimSpace(line) != "" {
					normalized = append(normalized, line)
				}
			}
			if !reflect.DeepEqual(normalized, tc.normalized) {
				t.Fatalf("normalized = %q, want %q", normalized, tc.normalized)
			}

			// Lint: the prose-only and excluded-inherit cases warn.
			env := platform.Env{"SOHO_BRIEF_LINT": "warn", "HOME": dir, "USERPROFILE": dir, "XDG_CONFIG_HOME": dir, "TMPDIR": dir}
			findings := dispatch.BriefLintFindings(brief, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, dispatch.BriefLintOptions{PreviousTask: true, WorkerCwd: dir})
			warns := false
			for _, warning := range findings.Warnings {
				if strings.Contains(warning, ": Owned files names no path;") {
					warns = true
				}
			}
			if warns != tc.lintWarns {
				t.Errorf("lint no-path warning = %t, want %t; warnings=%q", warns, tc.lintWarns, findings.Warnings)
			}

			// Overlap reader: declared paths plus the inherit directive.
			gotOverlap := ownedPaths(body, nil)
			if len(gotOverlap) == 0 {
				gotOverlap = nil
			}
			if !reflect.DeepEqual(gotOverlap, tc.overlap) {
				t.Errorf("overlap paths = %q, want %q", gotOverlap, tc.overlap)
			}
			if got := gittree.InheritsPrevious(body); got != tc.inherit {
				t.Errorf("overlap inherit = %t, want %t", got, tc.inherit)
			}

			// Settled audit: the same paths, with an inherit importing the
			// previous scope recorded in the sidecar.
			got, found := gittree.EffectiveOwned(sidecar, "worker")
			if !found {
				t.Fatal("audit did not find the Owned files section")
			}
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tc.audit) {
				t.Errorf("audit paths = %q, want %q", got, tc.audit)
			}
			if got := gittree.InheritsPrevious(body); got != tc.inherit {
				t.Errorf("audit inherit = %t, want %t", got, tc.inherit)
			}
		})
	}
}
