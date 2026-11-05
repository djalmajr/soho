package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// Scenario 14: a Review: line with a relative path or a file that does not
// exist is a lint warning in Warnings, never a refusal — even in strict
// mode. A valid absolute path that exists warns nothing.
func TestBriefLintReviewLine(t *testing.T) {
	// A well-formed brief body so no other finding fires; the Review: line
	// is appended at the end, outside any fence.
	wellFormed := "# Goal\nDo it.\n# Expected result\nTests pass.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nWrite the report.\n"

	writeBrief := func(t *testing.T, body string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}

	t.Run("a relative Review: path warns is-not-an-absolute-path", func(t *testing.T) {
		file := writeBrief(t, wellFormed+"Review: ../review.md\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " has a Review: line that is not an absolute path: ../review.md"
		if len(got.Warnings) != 1 || got.Warnings[0] != want {
			t.Fatalf("warnings = %#v, want exactly %#v", got.Warnings, want)
		}
		if got.MissingMessage != "" {
			t.Fatalf("a relative Review: path must not refuse: %q", got.MissingMessage)
		}
	})
	t.Run("the value in the warning is filtered", func(t *testing.T) {
		file := writeBrief(t, wellFormed+"Review: ../a\x1b[31m.md\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if len(got.Warnings) != 1 || strings.ContainsRune(got.Warnings[0], 0x1b) {
			t.Fatalf("warnings = %#v", got.Warnings)
		}
	})
	t.Run("a quoted relative Review: path warns is-not-an-absolute-path", func(t *testing.T) {
		file := writeBrief(t, wellFormed+"Review: `review with space.md`\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " has a Review: line that is not an absolute path: review with space.md"
		if len(got.Warnings) != 1 || got.Warnings[0] != want {
			t.Fatalf("warnings = %#v, want exactly %#v", got.Warnings, want)
		}
	})
	t.Run("an absent Review: file warns names-a-file-that-does-not-exist", func(t *testing.T) {
		review := filepath.Join(t.TempDir(), "absent.md")
		file := writeBrief(t, wellFormed+"Review: "+review+"\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " has a Review: line that names a file that does not exist: " + review
		if len(got.Warnings) != 1 || got.Warnings[0] != want {
			t.Fatalf("warnings = %#v, want exactly %#v", got.Warnings, want)
		}
		if got.MissingMessage != "" {
			t.Fatalf("an absent Review: file must not refuse: %q", got.MissingMessage)
		}
	})
	t.Run("a valid Review: path warns nothing", func(t *testing.T) {
		review := filepath.Join(t.TempDir(), "review.md")
		if err := os.WriteFile(review, []byte("findings: 0 (p0 0, p1 0, p2 0, p3 0) | verdict: pass\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		file := writeBrief(t, wellFormed+"Review: "+review+"\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		for _, warning := range got.Warnings {
			if strings.Contains(warning, "Review:") {
				t.Fatalf("a valid Review: line warned: %#v", got.Warnings)
			}
		}
		if got.MissingMessage != "" {
			t.Fatalf("a valid Review: line must not refuse: %q", got.MissingMessage)
		}
	})
	t.Run("strict mode warns but never refuses", func(t *testing.T) {
		file := writeBrief(t, wellFormed+"Review: ../review.md\n")
		warn := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		strict := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT": "strict"}, BriefLintOptions{})
		if strict.Mode != "strict" {
			t.Fatalf("mode = %q, want strict", strict.Mode)
		}
		if strict.MissingMessage != warn.MissingMessage || strict.MissingMessage != "" {
			t.Fatalf("strict must not turn the Review: warning into a refusal: warn=%q strict=%q", warn.MissingMessage, strict.MissingMessage)
		}
		want := "brief " + file + " has a Review: line that is not an absolute path: ../review.md"
		if len(strict.Warnings) != 1 || strict.Warnings[0] != want {
			t.Fatalf("strict warnings = %#v, want exactly %#v", strict.Warnings, want)
		}
	})
	t.Run("a Review: line inside a fence is not a declaration", func(t *testing.T) {
		file := writeBrief(t, wellFormed+"```\nReview: ../review.md\n```\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		for _, warning := range got.Warnings {
			if strings.Contains(warning, "Review:") {
				t.Fatalf("a fenced Review: line warned: %#v", got.Warnings)
			}
		}
	})
	t.Run("off mode warns nothing", func(t *testing.T) {
		file := writeBrief(t, wellFormed+"Review: ../review.md\n")
		got := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT": "off"}, BriefLintOptions{})
		if len(got.Warnings) != 0 {
			t.Fatalf("off mode warned: %#v", got.Warnings)
		}
	})
}
