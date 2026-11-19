package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// A review brief names the ref it is measured against. The warning is a
// lint finding for edit and read-only roles alike, never a refusal.
func TestBriefLintBaselineLine(t *testing.T) {
	wellFormed := "# Goal\nDo it.\n# Expected result\nTests pass.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nWrite the report.\n"
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}

	writeBrief := func(t *testing.T, body string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	baselineWarnings := func(warnings []string) []string {
		var got []string
		for _, warning := range warnings {
			if strings.Contains(warning, "Baseline: <ref>") {
				got = append(got, warning)
			}
		}
		return got
	}

	t.Run("Type: review without the line warns with the exact text", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed)
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " has Type: review and no 'Baseline: <ref>' line: nothing tells the reviewer which behavior is already on the base"
		if len(got.Warnings) != 1 || got.Warnings[0] != want {
			t.Fatalf("warnings = %#v, want exactly %#v", got.Warnings, want)
		}
		if got.MissingMessage != "" {
			t.Fatalf("the baseline warning must not refuse: %q", got.MissingMessage)
		}
	})
	t.Run("Baseline: origin/main warns nothing", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed+"Baseline: origin/main\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if warned := baselineWarnings(got.Warnings); len(warned) != 0 {
			t.Fatalf("declared baseline warned: %#v", warned)
		}
		if got.MissingMessage != "" {
			t.Fatalf("a declared baseline must not refuse: %q", got.MissingMessage)
		}
	})
	t.Run("a backticked value warns nothing", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed+"Baseline: `origin/main` (branch under test)\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if warned := baselineWarnings(got.Warnings); len(warned) != 0 {
			t.Fatalf("backticked baseline warned: %#v", warned)
		}
	})
	t.Run("an empty value warns", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed+"Baseline:   \n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		warned := baselineWarnings(got.Warnings)
		want := "brief " + file + " has Type: review and no 'Baseline: <ref>' line: nothing tells the reviewer which behavior is already on the base"
		if len(warned) != 1 || warned[0] != want {
			t.Fatalf("warnings = %#v, want exactly %#v", warned, want)
		}
	})
	t.Run("a line inside a fence is not a declaration", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed+"```\nBaseline: origin/main\n```\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		warned := baselineWarnings(got.Warnings)
		want := "brief " + file + " has Type: review and no 'Baseline: <ref>' line: nothing tells the reviewer which behavior is already on the base"
		if len(warned) != 1 || warned[0] != want {
			t.Fatalf("warnings = %#v, want exactly %#v", warned, want)
		}
	})
	t.Run("Type: backend without the line warns nothing", func(t *testing.T) {
		file := writeBrief(t, "Type: backend\n"+wellFormed)
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if warned := baselineWarnings(got.Warnings); len(warned) != 0 {
			t.Fatalf("a backend brief warned about the baseline: %#v", warned)
		}
		if got.MissingMessage != "" {
			t.Fatalf("a backend brief must not refuse: %q", got.MissingMessage)
		}
	})
	t.Run("strict mode warns but never refuses", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed)
		got := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT": "strict"}, BriefLintOptions{})
		if got.Mode != "strict" {
			t.Fatalf("mode = %q, want strict", got.Mode)
		}
		if got.MissingMessage != "" {
			t.Fatalf("strict must not refuse for a missing baseline: %q", got.MissingMessage)
		}
		if warned := baselineWarnings(got.Warnings); len(warned) != 1 {
			t.Fatalf("strict warnings = %#v", got.Warnings)
		}
	})
	t.Run("a read-only role warns", func(t *testing.T) {
		body := "Type: review\n# Goal\nReview it.\n# Expected result\nReport findings.\n# Forbidden\nNo commit/push.\n# Report\nWrite the report.\n"
		file := writeBrief(t, body)
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{ReadOnly: true})
		if warned := baselineWarnings(got.Warnings); len(warned) != 1 {
			t.Fatalf("reviewer warnings = %#v", got.Warnings)
		}
	})
	t.Run("an edit role warns", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed)
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if warned := baselineWarnings(got.Warnings); len(warned) != 1 {
			t.Fatalf("edit role warnings = %#v", got.Warnings)
		}
	})
	t.Run("a control character in the value is never printed", func(t *testing.T) {
		file := writeBrief(t, "Type: review\n"+wellFormed+"Baseline: origin\x1b[31m/main\n")
		got := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if warned := baselineWarnings(got.Warnings); len(warned) != 0 {
			t.Fatalf("a declared baseline warned: %#v", warned)
		}
		for _, warning := range got.Warnings {
			if strings.ContainsRune(warning, 0x1b) {
				t.Fatalf("warning carries a control character: %#v", got.Warnings)
			}
		}
	})
}

// R595 F2: the value the parser reads, and that the first declaration wins
// even when it is empty or malformed.
func TestDeclaredBaselineValue(t *testing.T) {
	cases := []struct {
		name, body, value string
		found             bool
	}{
		{"a ref followed by a sha keeps the first field", "Baseline: origin/main d31402ee\n", "origin/main", true},
		{"a backticked ref with prose after it", "Baseline: `origin/main` (the base)\n", "origin/main", true},
		{"an unclosed backtick is absent", "Baseline: `origin/main\n", "", true},
		{"an empty first declaration wins over a valid second", "Baseline:\nBaseline: origin/main\n", "", true},
		{"an unclosed first declaration wins over a valid second", "Baseline: `x\nBaseline: origin/main\n", "", true},
		{"a valid first declaration wins over an empty second", "Baseline: origin/main\nBaseline:\n", "origin/main", true},
		{"no declaration", "Type: review\n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			value, found := declaredBaselineValue(c.body)
			if value != c.value || found != c.found {
				t.Fatalf("declaredBaselineValue(%q) = (%q, %v), want (%q, %v)", c.body, value, found, c.value, c.found)
			}
		})
	}
}
