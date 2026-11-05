package gittree_test

// DJA-661 / DJA-666: absolute Owned entries inside the recorded tree are
// the same entries as their relative spellings, and the cited check reads
// only the report's `## Files changed` section.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/platform"
)

// diffReplaceOwned rewrites the fixture's composed brief with a new Owned
// section and restores the fixed dispatch cutoff, so an entry built from
// the fixture's real tree path is audited against the same cutoff the
// fixture installs.
func diffReplaceOwned(t *testing.T, f *diffFixture, owned string) {
	t.Helper()
	brief := filepath.Join(f.state, "briefs", "worker-fixture.md")
	diffWrite(t, brief, "# Role\n# Brief\n# Owned files\n"+owned+"\n# Report contract\nComplete.\n")
	cutoff := time.Unix(1700000100, 0)
	if err := os.Chtimes(brief, cutoff, cutoff); err != nil {
		t.Fatal(err)
	}
}

// diffPointFixtureAt moves the recorded tree (sidecar and roster) to a
// different spelling of the same directory, such as a symlinked prefix.
func diffPointFixtureAt(t *testing.T, f *diffFixture, tree string) {
	t.Helper()
	diffWrite(t, f.sidecar, fmt.Sprintf(`{"git_tree":%q,"git_head":%q,"git_branch":"main","role":"implementer"}`, tree, f.head))
	diffWrite(t, filepath.Join(f.state, "agents.tsv"), "# header\nworker\tp1\tcodex\timplementer\topenai\t0\t"+tree+"\tnow\n")
}

// Scenario 1: an absolute Owned entry — file, folder and glob — that sits
// inside the recorded tree is the same entry as its relative spelling, so
// the changed file does not go to outside_owned. The "outside" cases are
// the decisions that must keep accusing: a really-outside file and an
// absolute entry that points outside the tree.
func TestDiffReportAbsoluteOwnedEntries(t *testing.T) {
	for _, kind := range []string{"file", "directory", "glob", "root", "really-outside", "outside-tree"} {
		t.Run(kind, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer")
			abs := f.tree
			owned, wantOutside := "", []any(nil)
			switch kind {
			case "file":
				owned = "- `" + filepath.Join(abs, "src", "b.go") + "`\n"
			case "directory":
				owned = "- `" + filepath.Join(abs, "src") + "/`\n"
			case "glob":
				owned = "- `" + filepath.Join(abs, "src") + "/*.go`\n"
			case "root":
				owned = "- `" + abs + "`\n"
			case "really-outside":
				// The entry is inside the tree, but it names another file:
				// the real outsider stays accused.
				owned = "- `" + filepath.Join(abs, "src", "a.go") + "`\n"
				wantOutside = []any{"src/b.go"}
			case "outside-tree":
				owned = "- `/outside-of-tree/src/b.go`\n"
				wantOutside = []any{"src/b.go"}
			}
			diffReplaceOwned(t, f, owned)
			diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "package b\n// changed\n")
			if kind == "root" {
				// The root entry covers the whole tree: a file outside the
				// src/ folder is covered too.
				diffWrite(t, filepath.Join(f.tree, "forbidden.go"), "package forbidden\n// changed\n")
			}
			value, _, stderr := f.wait(t)
			if wantOutside == nil {
				if value["outside_owned"] != nil || value["cited_unchanged"] != nil || stderr != "" {
					t.Fatalf("outside=%v cited=%v stderr=%q", value["outside_owned"], value["cited_unchanged"], stderr)
				}
			} else {
				if !reflect.DeepEqual(value["outside_owned"], wantOutside) || !strings.Contains(stderr, "outside its Owned files: "+wantOutside[0].(string)) {
					t.Fatalf("outside=%v want=%v stderr=%q", value["outside_owned"], wantOutside, stderr)
				}
			}
		})
	}
}

// Scenario 3: the tree and the entry can spell each other through a
// symlink (/var and /private/var). The audit resolves the largest existing
// prefix on both sides; the entry file may not exist yet.
func TestDiffReportSymlinkedTreeAbsoluteOwned(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer")
	link := filepath.Join(filepath.Dir(f.tree), "link")
	if err := os.Symlink(f.tree, link); err != nil {
		t.Skipf("symlink unavailable on this system: %v", err)
	}
	goos := platform.Current()
	// The tree is recorded through the symlink; the entry uses the real
	// path. The converted entry must keep covering the changed file.
	diffPointFixtureAt(t, f, link)
	realEntry := filepath.Join(f.tree, "src", "b.go")
	diffReplaceOwned(t, f, "- `"+realEntry+"`\n")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	value, _, stderr := f.wait(t)
	if value["outside_owned"] != nil || value["cited_unchanged"] != nil || stderr != "" {
		t.Fatalf("real entry against symlinked tree: %v stderr=%q", value, stderr)
	}
	// The reverse: the tree is recorded by its real path and the entry goes
	// through the symlink, naming a file that does not exist yet (the
	// largest existing prefix resolves). A file the entry does not name
	// stays accused, which proves the conversion kept its scope.
	f2 := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer")
	link2 := filepath.Join(filepath.Dir(f2.tree), "link")
	if err := os.Symlink(f2.tree, link2); err != nil {
		t.Skipf("symlink unavailable on this system: %v", err)
	}
	absMissing := filepath.Join(link2, "src", "brand-new.go")
	diffReplaceOwned(t, f2, "- `"+absMissing+"`\n")
	diffWrite(t, filepath.Join(f2.tree, "src", "b.go"), "changed\n")
	value2, _, stderr2 := f2.wait(t)
	if !reflect.DeepEqual(value2["outside_owned"], []any{"src/b.go"}) || !strings.Contains(stderr2, "outside its Owned files: src/b.go") {
		t.Fatalf("symlink entry did not convert: %v stderr=%q", value2, stderr2)
	}
	if got := gittree.TreeRelative(absMissing, f2.tree, goos); got != "src/brand-new.go" {
		t.Fatalf("TreeRelative through the symlink = %q", got)
	}
	if got := gittree.TreeRelative(realEntry, link, goos); got != "src/b.go" {
		t.Fatalf("TreeRelative real entry against symlinked tree = %q", got)
	}
	if got := gittree.TreeRelative(link, f.tree, goos); got != "." {
		t.Fatalf("symlinked tree root = %q want .", got)
	}
	if got := gittree.TreeRelative(link2, f2.tree, goos); got != "." {
		t.Fatalf("real tree root via symlink = %q want .", got)
	}
	if got := gittree.TreeRelative(filepath.Join(link2, "src")+"/*.go", f2.tree, goos); got != "src/*.go" {
		t.Fatalf("TreeRelative glob through the symlink = %q", got)
	}
}

// Scenario 4: backslash and case spelling, with the per-system decision the
// shared SamePath/PathWithin rules already take (win32 folds case and
// separators, POSIX stays case-sensitive).
func TestTreeRelativeAbsoluteEntriesBySystem(t *testing.T) {
	cases := []struct {
		entry, tree, goos string
		want              string
	}{
		// Inside the tree: the absolute entry is its relative spelling.
		{`C:\work\repo\src\a.go`, `c:/work/repo`, "win32", "src/a.go"},
		{`c:/WORK/REPO/src/a.go`, `C:\work\repo`, "win32", "src/a.go"},
		{`C:\work\repo\src\deep\*.go`, `c:/work/repo`, "win32", "src/deep/*.go"},
		{"/work/repo/src/a.go", "/work/repo", "linux", "src/a.go"},
		{"/work/repo/src/a.go", "/work/repo/", "darwin", "src/a.go"},
		// The entry equal to the tree root covers the whole tree.
		{`C:\work\repo`, `c:/work/repo`, "win32", "."},
		{`C:\WORK\REPO`, `c:/work/repo`, "win32", "."},
		{"/work/repo", "/work/repo", "darwin", "."},
		{"/work/repo/", "/work/repo", "linux", "."},
		// Outside the tree: the entry covers nothing inside it.
		{"/work/other/a.go", "/work/repo", "linux", ""},
		{`C:\other\repo\a.go`, `c:/work/repo`, "win32", ""},
		{"/work/repoX/a.go", "/work/repo", "linux", ""},
		// POSIX stays case-sensitive; win32 folds case.
		{"/WORK/repo/src/a.go", "/work/repo", "linux", ""},
		{"/Work/repo/src/a.go", "/work/repo", "darwin", ""},
		// F2: the canonical form decides the comparison only; the returned
		// relative keeps the entry's own spelling, so an upper-case file on
		// win32 keeps its case.
		{`C:/work/repo/src/Foo.go`, `c:/work/repo`, "win32", "src/Foo.go"},
		{`C:\WORK\REPO\src\Foo.go`, `c:/work/repo`, "win32", "src/Foo.go"},
		{`C:\work\repo\src\Deep\a.GO`, `c:/work/repo`, "win32", "src/Deep/a.GO"},
		{"/work/repo/src/Foo.go", "/work/repo", "darwin", "src/Foo.go"},
		// Round 3: the raw spelling is cleaned first, so `..` and `.` in the
		// entry do not lose the tail's spelling; the rule applies on every
		// system.
		{`C:/work/repo/src/../src/Foo.go`, `c:/work/repo`, "win32", "src/Foo.go"},
		{`C:/work/repo/./src/Foo.go`, `c:/work/repo`, "win32", "src/Foo.go"},
		{`c:/work/repo/src/../src/a.go`, `c:/work/repo`, "win32", "src/a.go"},
		{"/work/repo/src/../src/A.go", "/work/repo", "darwin", "src/A.go"},
		// F3: a trailing-backslash entry is a folder on win32; the
		// conversion keeps its scope (the marker rides on the caller).
		{`C:\work\repo\src\`, `c:/work/repo`, "win32", "src"},
		{`C:\work\repo\`, `c:/work/repo`, "win32", "."},
		{`c:/work/repo/../../outside`, `c:/work/repo`, "win32", ""},
		// Relative entries keep today's diffRelative decision.
		{"src/a.go", "/work/repo", "linux", "src/a.go"},
		{"../outside.go", "/work/repo", "linux", ""},
		{"a.go", "/work/repo", "linux", "a.go"},
		{"", "/work/repo", "linux", ""},
	}
	for _, tc := range cases {
		if got := gittree.TreeRelative(tc.entry, tc.tree, tc.goos); got != tc.want {
			t.Fatalf("TreeRelative(%q, %q, %q) = %q; want %q", tc.entry, tc.tree, tc.goos, got, tc.want)
		}
	}
}

// F3: a recorded Owned entry is a folder when it ends with the separator
// marker — a trailing slash on every system, and a trailing backslash on
// win32, where the backslash is the separator. Outside win32 the
// backslash is a name character and the entry keeps its file spelling.
func TestOwnedEntryIsFolder(t *testing.T) {
	cases := []struct {
		entry, goos string
		want        bool
	}{
		{"src/", "win32", true},
		{"src/", "linux", true},
		{"src/", "darwin", true},
		{`src\`, "win32", true},
		{`C:\work\repo\src\`, "win32", true},
		{`src\`, "linux", false},
		{`src\`, "darwin", false},
		{"Makefile\\", "darwin", false},
		{"src", "win32", false},
		{"", "win32", false},
	}
	for _, tc := range cases {
		if got := gittree.OwnedEntryIsFolder(tc.entry, tc.goos); got != tc.want {
			t.Errorf("OwnedEntryIsFolder(%q, %q) = %v; want %v", tc.entry, tc.goos, got, tc.want)
		}
	}
}

// F2, end to end: the changed file is versioned with an upper-case name and
// the Owned entry spells it absolutely with the same case; it must not go
// to outside_owned (the control that keeps the spelling rule honest on this
// platform's case-sensitive filesystem). Round 3, decision 4: the file is
// in the commit (newDiffFixtureFiles) and is modified after the dispatch, so
// the audit sees a modified tracked file, not an untracked one.
func TestDiffReportAbsoluteOwnedUpperCaseVersioned(t *testing.T) {
	f := newDiffFixtureFiles(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer", map[string]string{
		"src/a.go":     "package a\n",
		"src/b.go":     "package b\n",
		"forbidden.go": "package forbidden\n",
		"README.md":    "fixture\n",
		"src/Foo.go":   "package foo\n",
	})
	diffReplaceOwned(t, f, "- `"+filepath.Join(f.tree, "src", "Foo.go")+"`\n")
	diffWrite(t, filepath.Join(f.tree, "src", "Foo.go"), "package foo\n// changed\n")
	value, _, stderr := f.wait(t)
	if value["outside_owned"] != nil || value["cited_unchanged"] != nil || stderr != "" {
		t.Fatalf("upper-case versioned file: %v stderr=%q", value, stderr)
	}
}

// Round 3, decision 1, with real symlinks: a different-depth symlink in the
// prefix keeps the entry's spelling in the relative tail (the raw part
// count differs from the canonical one, and the last n parts still match);
// a symlink inside the suffix that renames a part falls back to the
// canonical relative, without an error.
func TestTreeRelativeSymlinkSpelling(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "001", "tree")
	alias := filepath.Join(root, "002", "alias", "deep")
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "src", "Foo.go"), []byte("package foo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	// The recorded tree is the deeper alias; the entry is spelled through
	// it. The canonical forms use the real path; the tail keeps the entry's
	// spelling.
	if got := gittree.TreeRelative(filepath.Join(alias, "src", "Foo.go"), alias, platform.Current()); got != "src/Foo.go" {
		t.Fatalf("different-depth prefix symlink: TreeRelative = %q; want src/Foo.go", got)
	}
	// A symlink inside the suffix renames the part: the canonical relative
	// stands, without an error.
	if err := os.MkdirAll(filepath.Join(real, "realdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "realdir", "Foo.go"), []byte("package foo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "realdir"), filepath.Join(real, "lnk")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	// The canonical form folds case on Windows.
	want := "realdir/Foo.go"
	if platform.Current() == "win32" {
		want = "realdir/foo.go"
	}
	if got := gittree.TreeRelative(filepath.Join(real, "lnk", "Foo.go"), real, platform.Current()); got != want {
		t.Fatalf("symlink inside the suffix: TreeRelative = %q; want %s", got, want)
	}
}

// Scenarios 6-9 (DJA-666): the cited check reads only the report's
// `## Files changed` section (any heading level, case-insensitive, the
// Portuguese titles included). The section ends at the next heading of the
// same or a lower level; without a section there is no cited warning.
func TestDiffReportCitedOnlyFilesChangedSection(t *testing.T) {
	cases := []struct {
		name   string
		report string
		want   []any
	}{
		// 6: the changed file is in the section; the source cited in another
		// section is not accused.
		{"cited outside the section", "Completed.\n\n## Files changed\n- `src/b.go`\n\n## Sources\n- `src/a.go`\n", nil},
		{"cited before the section", "Completed.\n\n`src/a.go` was reviewed during the audit.\n\n## Files changed\n- `src/b.go`\n", nil},
		// 7: an unchanged file inside the section is accused, named in the
		// field and in the warning.
		{"unchanged inside the section", "Completed.\n\n## Files changed\n- `src/a.go`\n- `src/b.go`\n", []any{"src/a.go"}},
		// 8: the section ends at the next heading of the same or lower level;
		// a citation after the end is not accused.
		{"cited after the section", "Completed.\n\n## Files changed\n- `src/b.go`\n\n## Validation\npassed `src/a.go`\n", nil},
		// 9: without the section there is no cited warning at all.
		{"no section", "Completed `src/a.go` and `src/b.go`.\n", nil},
		// Edge: fenced code inside the section is ignored, as today.
		{"fenced inside the section", "Completed.\n\n## Files changed\n- `src/b.go`\n```go\n`src/a.go`\n```\n", nil},
		// Edge: two sections both count.
		{"two sections", "Completed.\n\n## Files changed\n- `src/b.go`\n\n## Notes\nx\n\n## Files changed\n- `src/a.go`\n", []any{"src/a.go"}},
		// Edge: the Portuguese titles count, at any level, case-insensitive.
		{"portuguese title", "Concluído.\n\n### Arquivos Alterados\n- `src/a.go`\n", []any{"src/a.go"}},
		{"portuguese modified", "Concluído.\n\n## ARQUIVOS MODIFICADOS\n- `src/a.go`\n", []any{"src/a.go"}},
		{"portuguese mudados", "Concluído.\n\n## arquivos mudados\n- `src/a.go`\n", []any{"src/a.go"}},
		// Edge: a deeper subheading does not end the section; the forbidden
		// rule keeps ignoring its block inside the section.
		{"forbidden nested in the section", "Completed.\n\n## Files changed\n- `src/b.go`\n\n### Forbidden\n- `src/a.go`\n", nil},
		// F5: opening Files changed does not clear the forbidden state. The
		// re-opened heading is deeper, so the Forbidden subsection is still
		// open and the citation under the last heading stays out.
		{"forbidden survives a re-opened section", "Completed.\n\n## Files changed\n- `src/b.go`\n\n### Forbidden\n- `src/c.go`\n\n#### Files changed\n- `src/a.go`\n", nil},
		// Round 3: the decision-3 report. The second same-level Files changed
		// heading closes the section it re-opens, and the close must not
		// clear forbidden: the citation under the last heading stays out.
		{"forbidden scope: decision-3 report", "Completed.\n\n## Files changed\n- `src/b.go`\n\n### Forbidden\n- `src/c.go`\n\n#### Files changed\n\n#### Files changed\n- `src/a.go`\n", nil},
		// Round 3: forbidden survives a deeper re-opened heading as well, so
		// a citation under a fifth heading still inside the Forbidden scope
		// stays out.
		{"forbidden scope: deeper re-open stays forbidden", "Completed.\n\n## Files changed\n- `src/b.go`\n\n### Forbidden\n- `src/c.go`\n\n#### Files changed\n\n#### Files changed\n\n##### Files changed\n- `src/a.go`\n", nil},
		// Round 3: forbidden ends only at its own level or above: a top-level
		// Files changed heading after the Forbidden subsection re-opens the
		// section at the top level, and citations count again.
		// A Forbidden section that opens outside any Files changed section has
		// its own scope too: a nested Files changed title stays inside it.
		{"forbidden scope: opens after the section closed", "Completed.\n\n## Files changed\n- `src/b.go`\n\n## Forbidden\n\n### Files changed\n- `src/a.go`\n", nil},
		{"forbidden scope: top-level section counts again", "Completed.\n\n## Files changed\n- `src/b.go`\n\n### Forbidden\n- `src/c.go`\n\n## Files changed\n- `src/a.go`\n", []any{"src/a.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/b.go\n", tc.report, "implementer")
			diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "package b\n// changed\n")
			value, _, stderr := f.wait(t)
			if tc.want == nil {
				if value["cited_unchanged"] != nil || strings.Contains(stderr, "cites files the tree shows unchanged") {
					t.Fatalf("cited=%v stderr=%q", value["cited_unchanged"], stderr)
				}
				return
			}
			if !reflect.DeepEqual(value["cited_unchanged"], tc.want) || !strings.Contains(stderr, "cites files the tree shows unchanged: src/a.go") {
				t.Fatalf("cited=%v want=%v stderr=%q", value["cited_unchanged"], tc.want, stderr)
			}
		})
	}
}

// An absolute citation inside the section is the same citation as its
// relative spelling (DJA-661 applies to the report's entries too).
func TestDiffReportCitedAbsolutePathInsideSection(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer")
	diffWrite(t, f.report, "Completed.\n\n## Files changed\n- `"+filepath.Join(f.tree, "src", "a.go")+"`\n- `src/b.go`\n")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "package b\n// changed\n")
	value, _, stderr := f.wait(t)
	if !reflect.DeepEqual(value["cited_unchanged"], []any{"src/a.go"}) || !strings.Contains(stderr, "cites files the tree shows unchanged: src/a.go") {
		t.Fatalf("absolute citation not converted: %v stderr=%q", value, stderr)
	}
	// The same absolute citation outside the tree covers nothing.
	f2 := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer")
	diffWrite(t, f2.report, "Completed.\n\n## Files changed\n- `/outside-tree/src/a.go`\n- `src/b.go`\n")
	diffWrite(t, filepath.Join(f2.tree, "src", "b.go"), "package b\n// changed\n")
	value2, _, _ := f2.wait(t)
	if value2["cited_unchanged"] != nil {
		t.Fatalf("outside-tree citation converted: %v", value2)
	}
}
