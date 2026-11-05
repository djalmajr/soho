package cli

// DJA-661: warnOwnedOverlap treats the same Owned entry the same way in the
// absolute and the relative spelling. dja661OverlapFixture builds both
// briefs' Owned entries from the fixture's own tree root (via the mine and
// theirs functions) and runs the real warn path; the assertions match the
// warning tail, so an unconverted absolute hit cannot satisfy them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func dja661OverlapFixture(t *testing.T, mine, theirs func(root string) string) string {
	t.Helper()
	root := t.TempDir()
	h := newTM3bHarnessAt(t, root, strings.Replace(tm3bFullBrief(), "internal/a.go", mine(root), 1))
	for _, dir := range []string{filepath.Join(h.ws, "briefs"), filepath.Join(h.ws, "reports")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.ws, "briefs", "peer.md"), []byte("# Brief\n# Owned files\n"+theirs(root)+"\n# Report contract\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "last-report-peer"), []byte(filepath.Join(h.ws, "reports", "peer.md")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte("peer\tw0test:p0b\tcodex\timplementer\topenai\t0\t"+root+"\tnow\tgpt-5\ttask\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldErr := platform.Stderr
	var stderr strings.Builder
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = oldErr })
	warnOwnedOverlap(h.brief, "implementer", "build", h.ws, h.ctx, h.env, root, root)
	return stderr.String()
}

func TestDJA661AbsoluteOwnedOverlap(t *testing.T) {
	// Same file, absolute in one brief and relative in the other: the
	// overlap is recognized, and the warning shows the converted entry.
	t.Run("same file, absolute vs relative entry, warns", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(root string) string { return "`" + filepath.Join(root, "internal", "a.go") + "`" },
			func(string) string { return "- internal/a.go" })
		if !strings.Contains(got, "owns files that 'peer' is still editing: internal/a.go") {
			t.Fatalf("absolute entry did not cross its relative spelling: %q", got)
		}
	})
	// Same folder, absolute with the trailing slash vs relative.
	t.Run("same folder, absolute vs relative entry, warns", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(root string) string { return "`" + filepath.Join(root, "internal") + "/`" },
			func(string) string { return "- internal/" })
		if !strings.Contains(got, "owns files that 'peer' is still editing: internal/") {
			t.Fatalf("absolute folder did not cross its relative spelling: %q", got)
		}
	})
	// Both briefs absolute (the reported worktree shape): still recognized,
	// shown in the converted relative form.
	t.Run("both entries absolute, warns", func(t *testing.T) {
		abs := func(root string) string { return "`" + filepath.Join(root, "internal", "a.go") + "`" }
		got := dja661OverlapFixture(t, abs, abs)
		if !strings.Contains(got, "owns files that 'peer' is still editing: internal/a.go") {
			t.Fatalf("two absolute spellings did not cross: %q", got)
		}
	})
	// An absolute entry outside the tree covers nothing inside it: silent.
	t.Run("absolute entry outside the tree stays silent", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(string) string { return "`/outside/internal/a.go`" },
			func(string) string { return "- internal/a.go" })
		if strings.Contains(got, "owns files") {
			t.Fatalf("outside-tree entry crossed the tree: %q", got)
		}
	})
	// Control: the relative overlap the DJA-636 fixture guards still warns.
	t.Run("relative overlap control still warns", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(string) string { return "- internal/a.go" },
			func(string) string { return "- internal/a.go" })
		if !strings.Contains(got, "owns files that 'peer' is still editing: internal/a.go") {
			t.Fatalf("relative overlap lost: %q", got)
		}
	})
	// Control: the DJA-636 absolute siblings outside any tree stay silent.
	t.Run("absolute siblings outside the tree stay silent", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(string) string { return "`/projects/x/.soho/qa/namebreak/r2/`" },
			func(string) string { return "- /projects/x/.soho/qa/shared/r2/" })
		if strings.Contains(got, "owns files") {
			t.Fatalf("absolute siblings outside the tree crossed: %q", got)
		}
	})
	// F4: an absolute entry that converts to the tree root (`.`) covers the
	// whole tree, so it overlaps every relative entry inside it.
	t.Run("absolute root entry covers the tree, warns", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(root string) string { return "`" + root + "`" },
			func(string) string { return "- internal/a.go" })
		if !strings.Contains(got, "owns files that 'peer' is still editing: .") {
			t.Fatalf("root entry did not cross the relative entry: %q", got)
		}
	})
	// Round 3: the root covers the tree and nothing outside it. An entry the
	// conversion left absolute is outside the tree, so it does not cross the
	// root — in either order.
	t.Run("root entry against outside-tree absolute entry stays silent", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(root string) string { return "`" + root + "`" },
			func(string) string { return "`/outside/internal/a.go`" })
		if strings.Contains(got, "owns files") {
			t.Fatalf("root entry crossed the outside-tree entry: %q", got)
		}
	})
	t.Run("outside-tree absolute entry against root entry stays silent", func(t *testing.T) {
		got := dja661OverlapFixture(t,
			func(string) string { return "`/outside/internal/a.go`" },
			func(root string) string { return "`" + root + "`" })
		if strings.Contains(got, "owns files") {
			t.Fatalf("outside-tree entry crossed the root entry: %q", got)
		}
	})
}

// F3: a backslash-terminated absolute entry is a folder on win32, like the
// trailing-slash form — the converted entry keeps the folder marker — and
// outside win32 the backslash is a name character, so the entry is not a
// folder and is not converted.
func TestOwnedPathForOverlapBackslashFolder(t *testing.T) {
	if got := ownedPathForOverlap(`C:\work\repo\src\`, `c:/work/repo`, "win32"); got != `src/` {
		t.Fatalf("win32 backslash folder entry = %q; want src/", got)
	}
	if got := ownedPathForOverlap(`C:\work\repo\src\`, `c:/work/repo`, "linux"); got != `C:\work\repo\src\` {
		t.Fatalf("linux backslash entry = %q; want untouched", got)
	}
	if got := ownedPathForOverlap(`C:/work/repo/src/`, `c:/work/repo`, "win32"); got != `src/` {
		t.Fatalf("slash folder entry = %q; want src/", got)
	}
	if got := ownedPathForOverlap(`C:\work\repo\src\a.go`, `c:/work/repo`, "win32"); got != `src/a.go` {
		t.Fatalf("backslash file entry = %q; want src/a.go", got)
	}
}
