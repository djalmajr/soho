package cli

// SOH-495: the dispatch overlap warning reads a plain path before a note
// whose backticks hold only an identifier, as the lint and the audit do.

import (
	"reflect"
	"strings"
	"testing"
)

func TestOwnedNotePathsOverlap(t *testing.T) {
	for _, tc := range []struct {
		name, item, path string
	}{
		{"dispatch.go note", "- internal/cli/dispatch.go (`ownedPathsIn` only)", "internal/cli/dispatch.go"},
		{"brief_signals.go note", "- internal/dispatch/brief_signals.go (or the file holding `BriefHasLiteralOwnedItems`)", "internal/dispatch/brief_signals.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownedPaths("## Owned files\n"+tc.item+"\n", nil); !reflect.DeepEqual(got, []string{tc.path}) {
				t.Fatalf("ownedPaths = %q, want [%s]", got, tc.path)
			}
			warning := dja661OverlapFixture(t,
				func(string) string { return tc.item },
				func(string) string { return "- " + tc.path })
			if !strings.Contains(warning, "owns files that 'peer' is still editing: "+tc.path) {
				t.Fatalf("missing overlap warning: %q", warning)
			}
		})
	}
	for _, mine := range []string{
		"- src/a.go (see `x` in docs/guide.md)",
		"- src/a.go: see docs/guide.md for `x`",
	} {
		t.Run("a reference in the note does not overlap: "+mine, func(t *testing.T) {
			if got := ownedPaths("## Owned files\n"+mine+"\n", nil); !reflect.DeepEqual(got, []string{"src/a.go"}) {
				t.Fatalf("ownedPaths = %q, want [src/a.go]", got)
			}
			guide := dja661OverlapFixture(t,
				func(string) string { return mine },
				func(string) string { return "- docs/guide.md" })
			if strings.Contains(guide, "owns files") {
				t.Fatalf("a cited guide must not overlap: %q", guide)
			}
			owned := dja661OverlapFixture(t,
				func(string) string { return mine },
				func(string) string { return "- src/a.go" })
			if !strings.Contains(owned, "owns files that 'peer' is still editing: src/a.go") {
				t.Fatalf("the declared path must still overlap: %q", owned)
			}
		})
	}
	t.Run("a plain item keeps every path around a qualifier", func(t *testing.T) {
		mine := "- src/a.go (parser only) and src/b.go"
		if got := ownedPaths("## Owned files\n"+mine+"\n", nil); !reflect.DeepEqual(got, []string{"src/a.go", "src/b.go"}) {
			t.Fatalf("ownedPaths = %q, want [src/a.go src/b.go]", got)
		}
		warning := dja661OverlapFixture(t,
			func(string) string { return mine },
			func(string) string { return "- src/b.go" })
		if !strings.Contains(warning, "owns files that 'peer' is still editing: src/b.go") {
			t.Fatalf("zero-span overlap lost the second path: %q", warning)
		}
	})
	t.Run("a prose line with no backticks still owns nothing", func(t *testing.T) {
		body := "## Owned files\nnote B = /work/base\n`internal/k.go`\n"
		if got := ownedPaths(body, nil); !reflect.DeepEqual(got, []string{"internal/k.go"}) {
			t.Fatalf("ownedPaths = %q, want [internal/k.go]", got)
		}
	})
	t.Run("a backticked path still owns only itself", func(t *testing.T) {
		if got := ownedPaths("## Owned files\n- `src/a.go` and src/b.go\n", nil); !reflect.DeepEqual(got, []string{"src/a.go"}) {
			t.Fatalf("ownedPaths = %q, want [src/a.go]", got)
		}
		warning := dja661OverlapFixture(t,
			func(string) string { return "- `src/a.go` and src/b.go" },
			func(string) string { return "- src/b.go" })
		if strings.Contains(warning, "owns files") {
			t.Fatalf("a backticked path must keep the earlier rule: %q", warning)
		}
	})
}
