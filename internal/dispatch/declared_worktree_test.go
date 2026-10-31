package dispatch

import (
	"path/filepath"
	"testing"
)

func TestDeclaredWorktree(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "tree with spaces")
	other := filepath.Join(t.TempDir(), "other")
	for _, tc := range []struct {
		name, body, want string
	}{
		{name: "quoted-example", body: "Worktree: `/a/b/.worktrees/x` (branch y)", want: "/a/b/.worktrees/x"},
		{name: "bare-example", body: "Worktree: /a/b/c", want: "/a/b/c"},
		{name: "relative", body: "Worktree: .worktrees/x"},
		{name: "case-sensitive", body: "worktree: /a/b"},
		{name: "native-quoted-spaces", body: "Worktree: `" + abs + "` (branch y)", want: abs},
		{name: "native-bare", body: "Worktree: " + other + " (branch y)", want: other},
		{name: "backtick-fence", body: "```md\nWorktree: `" + other + "`\n```\nWorktree: `" + abs + "`", want: abs},
		{name: "tilde-fence", body: "~~~md\nWorktree: `" + other + "`\n~~~"},
		{name: "fence-closed-with-tab", body: "```md\nWorktree: `" + other + "`\n```\t\nWorktree: `" + abs + "`", want: abs},
		{name: "first-declaration", body: "Worktree: `" + abs + "`\nWorktree: `" + other + "`", want: abs},
		{name: "invalid-first-declaration", body: "Worktree: relative\nWorktree: `" + abs + "`"},
		{name: "quoted-relative", body: "Worktree: `.worktrees/x` (branch y)"},
		{name: "column-zero", body: " Worktree: `" + abs + "`"},
		{name: "empty", body: "Worktree:"},
		{name: "empty-quote", body: "Worktree: ``"},
		{name: "unclosed-quote", body: "Worktree: `" + abs},
		{name: "crlf", body: "```md\r\nWorktree: `" + other + "`\r\n```\r\nWorktree: `" + abs + "`\r\n", want: abs},
		{name: "windows-drive", body: "Worktree: `C:\\repo\\worktree`", want: `C:\repo\worktree`},
		{name: "windows-unc", body: "Worktree: `\\\\server\\share\\tree`", want: `\\server\share\tree`},
		{name: "bare-then-backticks", body: "Worktree: " + other + " (branch `feat/x`, commit `a4224d0`, on `main`). Work only in `web/`.", want: other},
		{name: "quoted-then-backticks", body: "Worktree: `" + abs + "` (branch `feat/x`)", want: abs},
		{name: "relative-then-backticks", body: "Worktree: .worktrees/x (branch `feat/x`)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A path only declares a tree when absolute on the running platform.
			want := tc.want
			if !filepath.IsAbs(want) {
				want = ""
			}
			if got := DeclaredWorktree(tc.body); got != want {
				t.Fatalf("DeclaredWorktree(%q) = %q, want %q", tc.body, got, want)
			}
		})
	}
}
