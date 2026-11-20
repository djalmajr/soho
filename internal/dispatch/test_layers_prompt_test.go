package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// SOH-442: a brief's declared `Test layers:` line reaches the worker's prompt
// and is not a brief-lint finding. The names are copied as written and never
// validated or run.

const declaredLayersPromptLine = "- Test layers that apply: unit, mutation; mark the others n/a: <why>.\n"

func declaredLayersRole(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "role.md")
	if err := os.WriteFile(file, []byte("---\nname: Implementer\n---\nRole.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestDeclaredTestLayersPromptLine(t *testing.T) {
	role := declaredLayersRole(t)
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	env := composeEnv(t)
	for _, tc := range []struct {
		name, brief string
		want        bool
	}{
		{"declared", "# Goal\nrun\n\nTest layers: unit, mutation\n", true},
		{"no line", "# Goal\nrun\n", false},
		{"empty list", "# Goal\nrun\n\nTest layers:\n", false},
		{"fenced line", "# Goal\nrun\n\n```md\nTest layers: unit, mutation\n```\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := ComposePrompt(role, "implementer", "worker", tc.brief, "/report.md", ctx, env, "codex", "", "", false)
			if got := strings.Count(prompt, declaredLayersPromptLine); got != boolCount(tc.want) {
				t.Fatalf("prompt line count=%d want %d: %q", got, boolCount(tc.want), prompt)
			}
			if tc.want && strings.Index(prompt, "- Write the report in one go") < strings.Index(prompt, declaredLayersPromptLine) {
				t.Fatal("declared test-layers line is outside the report contract")
			}
		})
	}
	t.Run("amendment", func(t *testing.T) {
		prompt := ComposeAmendment("# Amend\nchange\n\nTest layers: unit, mutation\n", "/report.md", "", "implementer", ctx, env, "codex", "", "", false)
		if strings.Count(prompt, declaredLayersPromptLine) != 1 {
			t.Fatalf("amendment lost the declared test-layers line: %q", prompt)
		}
	})
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestDeclaredTestLayersLintStaysQuiet(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "brief.md")
	body := "# Goal\nDo it.\n\nTest layers: not-a-layer, ANY Name, 42\n\n# Expected result\nDone.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit.\n# Report\n\ndone\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	result := BriefLintFindings(brief, ctx, platform.Env{"SOHO_BRIEF_LINT": "warn"}, BriefLintOptions{})
	if len(result.Warnings) != 0 || result.MissingMessage != "" {
		t.Fatalf("lint warned about the declared layers: warnings=%q missing=%q", result.Warnings, result.MissingMessage)
	}
}
