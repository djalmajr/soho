package dispatch

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

// DJA-684: an amendment that declares no Worktree: line keeps the original
// brief's line, so the amended review stays tied to its author.
func TestComposeAmendmentKeepsOriginalWorktree(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}

	// The original declares the worktree and the amendment does not: the
	// composed amendment carries the original's line, outside any fenced
	// block (DeclaredWorktree only reads unfenced lines).
	composed := ComposeAmendment("# Amend\n\nFix the thing.\n", "/report.md", "Type: backend\nWorktree: /abs/slice\n", ctx, nil, "codex", "", "", false)
	if tree := DeclaredWorktree(composed); tree != "/abs/slice" {
		t.Fatalf("amendment lost the original's worktree (DeclaredWorktree=%q):\n%s", tree, headOf(composed))
	}
	if !strings.Contains(composed, "# Amend\n\nFix the thing.") {
		t.Fatal("the amendment body was not kept verbatim")
	}

	// A path with spaces stays whole: the line is kept as written, so it reads
	// the same in the composed amendment and in the next one.
	composed = ComposeAmendment("Fix the thing.\n", "/report.md", "Worktree: `/abs/my slice` (the slice)\n", ctx, nil, "codex", "", "", false)
	if tree := DeclaredWorktree(composed); tree != "/abs/my slice" {
		t.Fatalf("a quoted path did not survive (DeclaredWorktree=%q):\n%s", tree, headOf(composed))
	}
	composed = ComposeAmendment("Fix it again.\n", "/report.md", composed, ctx, nil, "codex", "", "", false)
	if tree := DeclaredWorktree(composed); tree != "/abs/my slice" {
		t.Fatalf("a quoted path did not survive a second amendment (DeclaredWorktree=%q):\n%s", tree, headOf(composed))
	}

	// The amendment's own line wins over the original's.
	composed = ComposeAmendment("Worktree: /abs/amend\nFix the thing.\n", "/report.md", "Worktree: /abs/slice\n", ctx, nil, "codex", "", "", false)
	if tree := DeclaredWorktree(composed); tree != "/abs/amend" {
		t.Fatalf("the amendment's own worktree must win (DeclaredWorktree=%q)", tree)
	}
	if strings.Contains(composed, "/abs/slice") {
		t.Fatalf("the original's worktree leaked into the composed amendment:\n%s", headOf(composed))
	}

	// A Worktree: line fenced in the original is not a declaration: it is
	// not preserved.
	composed = ComposeAmendment("Fix the thing.\n", "/report.md", "```\nWorktree: /abs/slice\n```\n", ctx, nil, "codex", "", "", false)
	if tree := DeclaredWorktree(composed); tree != "" {
		t.Fatalf("a fenced original worktree line must not be preserved (DeclaredWorktree=%q)", tree)
	}

	// The original declares nothing: no line is added.
	composed = ComposeAmendment("Fix the thing.\n", "/report.md", "Type: backend\n", ctx, nil, "codex", "", "", false)
	if tree := DeclaredWorktree(composed); tree != "" {
		t.Fatalf("no worktree declared anywhere, yet the composition has one (%q)", tree)
	}
	if strings.Count(composed, "Worktree:") != 0 {
		t.Fatalf("the composition invented a Worktree: line:\n%s", headOf(composed))
	}
}

func headOf(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
