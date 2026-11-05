package dispatch

import (
	"path/filepath"
	"testing"
)

// TestDeclaredReview (DJA-684 slice C, scenario 1): the first Review: line
// outside fenced code blocks, read the way DeclaredWorktree reads Worktree:
// the path between backticks when the value is quoted, else the first field.
// A relative or malformed path has no review.
func TestDeclaredReview(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "review with spaces.md")
	other := filepath.Join(t.TempDir(), "other.md")
	for _, tc := range []struct {
		name, body, want string
	}{
		{name: "quoted-example", body: "Review: `/a/b/review.md` (round 2)", want: "/a/b/review.md"},
		{name: "bare-example", body: "Review: /a/b/c.md", want: "/a/b/c.md"},
		{name: "relative", body: "Review: ../review.md"},
		{name: "case-sensitive", body: "review: /a/b.md"},
		{name: "native-quoted-spaces", body: "Review: `" + abs + "`", want: abs},
		{name: "native-bare", body: "Review: " + other + " (the last round)", want: other},
		{name: "backtick-fence", body: "```\nReview: `" + other + "`\n```\nReview: `" + abs + "`", want: abs},
		{name: "tilde-fence", body: "~~~\nReview: `" + other + "`\n~~~"},
		{name: "fence-closed-with-tab", body: "```\nReview: `" + other + "`\n```\t\nReview: `" + abs + "`", want: abs},
		{name: "first-declaration", body: "Review: `" + abs + "`\nReview: `" + other + "`", want: abs},
		{name: "invalid-first-declaration", body: "Review: ../review.md\nReview: `" + abs + "`"},
		{name: "quoted-relative", body: "Review: `../review.md`"},
		{name: "column-zero", body: " Review: `" + abs + "`"},
		{name: "empty", body: "Review:"},
		{name: "empty-quote", body: "Review: ``"},
		{name: "unclosed-quote", body: "Review: `" + abs},
		{name: "crlf", body: "```\r\nReview: `" + other + "`\r\n```\r\nReview: `" + abs + "`\r\n", want: abs},
		{name: "windows-drive", body: "Review: `C:\\repo\\review.md`", want: `C:\repo\review.md`},
		{name: "bare-then-backticks", body: "Review: " + other + " (see `the report` for detail)", want: other},
		{name: "quoted-then-backticks", body: "Review: `" + abs + "` (branch `feat/x`)", want: abs},
		{name: "no-review-line", body: "Worktree: /a/b\n\n## Goal\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if !filepath.IsAbs(want) {
				want = ""
			}
			if got := DeclaredReview(tc.body); got != want {
				t.Fatalf("DeclaredReview(%q) = %q, want %q", tc.body, got, want)
			}
		})
	}
}
