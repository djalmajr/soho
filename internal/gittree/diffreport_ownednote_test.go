package gittree

// SOH-495: an Owned item whose backticked note names no path keeps the plain
// path before it. The same reader serves the lint, the scope check and the
// audit; an item with a backticked path keeps the old result exactly.

import (
	"reflect"
	"strings"
	"testing"
)

func ownedItemQuoted(item string) []string {
	matches := diffTokenRE.FindAllStringSubmatch(item, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func TestOwnedItemPathsNoteRealItems(t *testing.T) {
	cases := []struct {
		name, item, want string
	}{
		{"dispatch.go note", "- internal/cli/dispatch.go (`ownedPathsIn` only)", "internal/cli/dispatch.go"},
		{"brief_signals.go note", "- internal/dispatch/brief_signals.go (or the file holding `BriefHasLiteralOwnedItems`)", "internal/dispatch/brief_signals.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := strings.TrimSpace(diffBulletRE.ReplaceAllString(tc.item, ""))
			if got, want := OwnedItemPaths(item, ownedItemQuoted(item), "", nil), []string{tc.want}; !reflect.DeepEqual(got, want) {
				t.Fatalf("OwnedItemPaths(%q) = %q, want %q", item, got, want)
			}
			if got := OwnedItemLintMessages([]string{tc.item}, ""); len(got) != 0 {
				t.Fatalf("lint messages = %q, want none", got)
			}
			if !OwnedSectionDeclaresScope([]string{tc.item}, "") {
				t.Fatal("the item must declare scope")
			}
		})
	}
}

func TestOwnedItemPathsBacktickedPathUnchanged(t *testing.T) {
	cases := []struct {
		name, item string
		want       []string
	}{
		{"a backticked path beside a plain path", "- `src/a.go` and src/b.go", []string{"src/a.go"}},
		{"a backticked path with an identifier note", "- `src/a.go`, only in `ai.openChat`", []string{"src/a.go"}},
		{"a plain path before a backticked path", "- src/a.go and `src/b.go`", []string{"src/b.go"}},
		{"a backticked declaration with a reference", "- `src/a.go` (see `x` in docs/guide.md)", []string{"src/a.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := strings.TrimSpace(diffBulletRE.ReplaceAllString(tc.item, ""))
			if got := OwnedItemPaths(item, ownedItemQuoted(item), "", nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("OwnedItemPaths(%q) = %q, want %q", item, got, tc.want)
			}
		})
	}
}

func TestOwnedItemPathsFirstTokenOnly(t *testing.T) {
	cases := []struct {
		name, item string
		want       []string
	}{
		{"a reference after an identifier", "- src/a.go (see `x` in docs/guide.md)", []string{"src/a.go"}},
		{"a reference before an identifier", "- src/a.go (`x` in docs/guide.md)", []string{"src/a.go"}},
		{"a colon before a reference", "- src/a.go: see docs/guide.md for `x`", []string{"src/a.go"}},
		{"a colon with no declared path", "- See: docs/guide.md for `x`", nil},
		{"a reference after a bare backtick", "- src/a.go `x` docs/guide.md", []string{"src/a.go"}},
		{"two declared paths before a reference", "- src/a.go and src/b.go (see `x` in docs/guide.md)", []string{"src/a.go"}},
		{"a reference-only item", "- (see `x` in docs/guide.md)", nil},
		{"a reference before the identifier in a note", "- (see docs/guide.md for `x`)", nil},
		{"a path opening a parenthetical aside", "- (docs/guide.md describes `x`)", nil},
		{"a path opening a bracketed aside", "- [docs/guide.md] for `x`", nil},
		{"a path opening a quoted aside", "- \"docs/guide.md\" holds `x`", nil},
		{"a path opening a curly-quoted aside", "- “docs/guide.md” holds `x`", nil},
		{"a labeled reference-only item", "- Owned: (see `x` in docs/guide.md)", nil},
		{"a wrapped reference line", "  See `x` in docs/guide.md.", nil},
		{"no backtick span is left to the caller", "- src/a.go (parser only) and src/b.go", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := strings.TrimSpace(diffBulletRE.ReplaceAllString(tc.item, ""))
			if got := OwnedItemPaths(item, ownedItemQuoted(item), "", nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("OwnedItemPaths(%q) = %q, want %q", item, got, tc.want)
			}
		})
	}
	mixed := "- src/a.go (see `x` in docs/guide.md)"
	if got := OwnedItemLintMessages([]string{mixed}, ""); len(got) != 0 {
		t.Fatalf("mixed lint messages = %q, want none", got)
	}
	if !OwnedSectionDeclaresScope([]string{mixed}, "") {
		t.Fatal("the mixed item must declare src/a.go")
	}
	only := "- (see `x` in docs/guide.md)"
	if got, want := OwnedItemLintMessages([]string{only}, ""), []string{"'(see `x` in docs/guide.md)' names no path"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reference-only lint messages = %q, want %q", got, want)
	}
	if OwnedSectionDeclaresScope([]string{only}, "") {
		t.Fatal("a reference-only item must not declare scope")
	}
}

func TestOwnedItemPathsProseNoteStillWarns(t *testing.T) {
	item := "- See `ai.openChat` for details"
	if got := OwnedItemPaths(strings.TrimSpace(diffBulletRE.ReplaceAllString(item, "")), ownedItemQuoted(item), "", nil); len(got) != 0 {
		t.Fatalf("prose item = %q, want no paths", got)
	}
	if got, want := OwnedItemLintMessages([]string{item}, ""), []string{"'See `ai.openChat` for details' names no path"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lint messages = %q, want %q", got, want)
	}
	if OwnedSectionDeclaresScope([]string{item}, "") {
		t.Fatal("a prose-only item must not declare scope")
	}
	upper := "- `NOTES.MD` (handled separately)"
	if got := OwnedItemPaths(strings.TrimSpace(diffBulletRE.ReplaceAllString(upper, "")), ownedItemQuoted(upper), "", nil); len(got) != 0 {
		t.Fatalf("uppercase backticked note = %q, want no paths", got)
	}
	if !strings.Contains(strings.Join(OwnedItemLintMessages([]string{upper}, ""), ""), "names no path") {
		t.Fatal("an uppercase backticked note with no plain path must warn")
	}
}
