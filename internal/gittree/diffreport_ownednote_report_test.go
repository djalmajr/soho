package gittree_test

// SOH-495: the settled audit reads the real shape of both briefs that caused
// the false outside-Owned warning. An unrelated change still warns.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiffReportOwnedNotePathFallback(t *testing.T) {
	files := map[string]string{
		"src/a.go":                           "original\n",
		"internal/cli/dispatch.go":           "original\n",
		"internal/dispatch/brief_signals.go": "original\n",
		"docs/guide.md":                      "original\n",
		"outside.txt":                        "original\n",
	}
	for _, tc := range []struct {
		name, item, changed string
	}{
		{"dispatch.go note", "- internal/cli/dispatch.go (`ownedPathsIn` only)", "internal/cli/dispatch.go"},
		{"brief_signals.go note", "- internal/dispatch/brief_signals.go (or the file holding `BriefHasLiteralOwnedItems`)", "internal/dispatch/brief_signals.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixtureFiles(t, "# Owned files\n"+tc.item+"\n", "Completed.\n", "implementer", files)
			if err := os.WriteFile(filepath.Join(f.tree, filepath.FromSlash(tc.changed)), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			value, _, stderr := f.wait(t)
			if got := ownedOutside(value); len(got) != 0 {
				t.Fatalf("outside_owned=%q, want none; stderr=%q", got, stderr)
			}
			if strings.Contains(stderr, "outside its Owned files") {
				t.Fatalf("false outside warning: %q", stderr)
			}
		})
	}
	for _, item := range []string{
		"- src/a.go (see `x` in docs/guide.md)",
		"- src/a.go: see docs/guide.md for `x`",
	} {
		t.Run("a reference in the note stays outside: "+item, func(t *testing.T) {
			f := newDiffFixtureFiles(t, "# Owned files\n"+item+"\n", "Completed.\n", "implementer", files)
			for _, name := range []string{"src/a.go", "docs/guide.md"} {
				if err := os.WriteFile(filepath.Join(f.tree, filepath.FromSlash(name)), []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			value, _, stderr := f.wait(t)
			if got, want := ownedOutside(value), []string{"docs/guide.md"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("outside_owned=%q, want %q; stderr=%q", got, want, stderr)
			}
			if !strings.Contains(stderr, "outside its Owned files: docs/guide.md") {
				t.Fatalf("missing guide warning: %q", stderr)
			}
		})
	}
	t.Run("an unrelated change still warns", func(t *testing.T) {
		f := newDiffFixtureFiles(t, "# Owned files\n- internal/cli/dispatch.go (`ownedPathsIn` only)\n", "Completed.\n", "implementer", files)
		if err := os.WriteFile(filepath.Join(f.tree, "outside.txt"), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		value, _, stderr := f.wait(t)
		if got, want := ownedOutside(value), []string{"outside.txt"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("outside_owned=%q, want %q; stderr=%q", got, want, stderr)
		}
		if !strings.Contains(stderr, "outside its Owned files: outside.txt") {
			t.Fatalf("missing outside warning: %q", stderr)
		}
	})
}
