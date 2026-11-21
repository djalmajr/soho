package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/text"
)

// The hint names no path: only the escaped brief locator, the field and a
// literal placeholder. That makes it impossible to recommend a path other
// than the absolute one whose existence was actually checked, and keeps a
// hostile brief filename or field value from reaching stderr raw.
func TestBriefLintSpacedPathHint(t *testing.T) {
	env := repoTestEnv(t)
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	spaced := filepath.Join(t.TempDir(), "tree with spaces")
	if err := os.MkdirAll(spaced, 0o700); err != nil {
		t.Fatal(err)
	}
	review := spaced + ".md"
	if err := os.WriteFile(review, []byte("findings: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lint := func(t *testing.T, name, body string) (BriefLintResult, string) {
		t.Helper()
		brief := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return BriefLintFindings(brief, ctx, env, BriefLintOptions{PathsOnly: true}), brief
	}
	wantHint := func(brief, field string, line int) string {
		return fmt.Sprintf("brief %s line %d: the %s path contains a space and is read only up to it; write the whole path in backticks: %s: `/absolute/path`", text.QuoteControls(brief), line, field, field)
	}
	hintsOf := func(t *testing.T, got BriefLintResult) []string {
		out := []string{}
		for _, w := range got.Warnings {
			if strings.Contains(w, "contains a space and is read only up to it") {
				out = append(out, w)
			}
			if strings.ContainsAny(w, "\r\n\t") {
				t.Fatalf("a warning is not one line: %q", w)
			}
			for _, r := range w {
				if unicode.IsControl(r) {
					t.Fatalf("a raw control character survived: %q", w)
				}
			}
		}
		for _, w := range got.UnseenPaths {
			if strings.Contains(w, "contains a space and is read only up to it") {
				t.Fatalf("hint joined the strict refusals: %q", got.UnseenPaths)
			}
		}
		return out
	}
	assertHint := func(t *testing.T, got BriefLintResult, brief, field string, line int, absent ...string) {
		t.Helper()
		hints := hintsOf(t, got)
		if len(hints) != 1 || hints[0] != wantHint(brief, field, line) {
			t.Fatalf("hints = %q, want exactly %q", hints, wantHint(brief, field, line))
		}
		for _, path := range absent {
			if strings.Contains(hints[0], path) {
				t.Fatalf("the hint named a user path %q: %q", path, hints[0])
			}
		}
	}
	assertNone := func(t *testing.T, got BriefLintResult) {
		t.Helper()
		if hints := hintsOf(t, got); len(hints) != 0 {
			t.Fatalf("hints = %q, want none", hints)
		}
	}

	t.Run("absolute Worktree", func(t *testing.T) {
		got, brief := lint(t, "brief.md", "Worktree: "+spaced+"\n")
		assertHint(t, got, brief, "Worktree", 1, spaced)
	})
	t.Run("absolute Review", func(t *testing.T) {
		got, brief := lint(t, "brief.md", "Review: "+review+"\n")
		assertHint(t, got, brief, "Review", 1, review)
	})
	t.Run("absolute with a prose note", func(t *testing.T) {
		got, brief := lint(t, "brief.md", "Worktree: "+spaced+" (the slice)\n")
		assertHint(t, got, brief, "Worktree", 1, spaced)
	})
	t.Run("one hint per field", func(t *testing.T) {
		got, brief := lint(t, "brief.md", "Worktree: "+spaced+"\nReview: "+review+"\n")
		hints := hintsOf(t, got)
		if len(hints) != 2 || hints[0] != wantHint(brief, "Worktree", 1) || hints[1] != wantHint(brief, "Review", 2) {
			t.Fatalf("hints = %q", hints)
		}
	})
	t.Run("a quoted value needs no hint", func(t *testing.T) {
		got, _ := lint(t, "brief.md", "Worktree: `"+spaced+"`\nReview: `"+review+"`\n")
		assertNone(t, got)
	})
	t.Run("an existing first field needs no hint", func(t *testing.T) {
		base := t.TempDir()
		for strings.ContainsAny(base, " \t\r\n") {
			parent := filepath.Dir(base)
			if parent == base {
				t.Fatalf("no space-free ancestor of %q", base)
			}
			base = parent
		}
		dir, err := os.MkdirTemp(base, "soh493-lint-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		first := filepath.Join(dir, "path")
		full := first + " with spaces"
		for _, p := range []string{first, full} {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		body := "Worktree: " + full + "\n"
		got, _ := lint(t, "brief.md", body)
		assertNone(t, got)
		if DeclaredWorktree(body) != first {
			t.Fatal("the parser must still read only the first field")
		}
	})
	// SOH-494: a first field that exists as an entry (a symlink, broken or
	// looping) gets no hint.
	spaceFree := func(t *testing.T) string {
		t.Helper()
		base := t.TempDir()
		for strings.ContainsAny(base, " \t\r\n") {
			parent := filepath.Dir(base)
			if parent == base {
				t.Fatalf("no space-free ancestor of %q", base)
			}
			base = parent
		}
		dir, err := os.MkdirTemp(base, "soh494-lint-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	for _, tc := range []struct {
		name string
		link func(first string) error
	}{
		{"a looping symlink first field needs no hint", func(first string) error { return os.Symlink(first, first) }},
		{"a broken symlink first field needs no hint", func(first string) error { return os.Symlink(first+"-target-absent", first) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink creation needs privileges on Windows")
			}
			dir := spaceFree(t)
			first := filepath.Join(dir, "path")
			full := first + " with spaces"
			if err := os.Mkdir(full, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := tc.link(first); err != nil {
				t.Fatal(err)
			}
			got, _ := lint(t, "brief.md", "Worktree: "+full+"\n")
			assertNone(t, got)
		})
	}
	t.Run("a missing whole path needs no hint", func(t *testing.T) {
		got, _ := lint(t, "brief.md", "Worktree: "+filepath.Join(t.TempDir(), "absent with space")+"\n")
		assertNone(t, got)
	})
	t.Run("a relative value needs no hint", func(t *testing.T) {
		base := t.TempDir()
		t.Chdir(base)
		if err := os.Mkdir(filepath.Join(base, "relative space"), 0o700); err != nil {
			t.Fatal(err)
		}
		got, _ := lint(t, "brief.md", "Worktree: relative space\n")
		assertNone(t, got)
		if DeclaredWorktree("Worktree: relative space\n") != "" {
			t.Fatal("a relative value must remain unparsed")
		}
	})
	t.Run("a fenced declaration keeps its grammar and needs no hint", func(t *testing.T) {
		got, _ := lint(t, "brief.md", "```text\nWorktree: "+spaced+"\n```\n")
		assertNone(t, got)
	})

	// A control character anywhere in the declared value or the brief name
	// must stay out of the message; the hint only names its placeholder.
	t.Run("control characters in the value are not printed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows filenames cannot contain a tab")
		}
		value := filepath.Join(t.TempDir(), "with\ttab space")
		if err := os.Mkdir(value, 0o700); err != nil {
			t.Fatal(err)
		}
		got, brief := lint(t, "brief.md", "Worktree: "+value+"\n")
		assertHint(t, got, brief, "Worktree", 1, value, "with\ttab")
	})
	t.Run("a control character in the brief locator is escaped", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows filenames cannot contain a tab")
		}
		name := "brief\twith tab.md"
		got, brief := lint(t, name, "Worktree: "+spaced+"\n")
		hints := hintsOf(t, got)
		if len(hints) != 1 {
			t.Fatalf("hints = %q, want one", hints)
		}
		if !strings.Contains(hints[0], "brief "+text.QuoteControls(brief)+" line 1:") || strings.Contains(hints[0], "\t") {
			t.Fatalf("the locator was not escaped: %q", hints[0])
		}
		if strings.Contains(hints[0], spaced) {
			t.Fatalf("the hint named the checked path: %q", hints[0])
		}
	})
}
