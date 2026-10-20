package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestDispatchTM3bAmendmentContract(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	// JS: "composeAmendment: the report_language line, in order, only when set"
	t.Run("composeAmendment: the report_language line, in order, only when set", func(t *testing.T) {
		ctx.Entries["report_language"] = core.ConfigEntry{Value: "pt-BR"}
		got := ComposeAmendment("# Amend\n\nDo X.\n", "/tmp/report.md", ctx, nil, "codex", "", "", false)
		lang := "- Write the report in pt-BR.\n"
		if strings.Count(got, lang) != 1 || strings.Index(got, lang) < strings.Index(got, "Write your report as Markdown") || strings.Index(got, lang) > strings.Index(got, "Write the report in one go") {
			t.Fatalf("report_language line missing or out of order:\n%s", got)
		}
		ctx.Entries = map[string]core.ConfigEntry{}
		plain := ComposeAmendment("# Amend\n", "/tmp/report.md", ctx, nil, "codex", "", "", false)
		if strings.Contains(plain, "Write the report in pt-BR") {
			t.Fatalf("unset report_language leaked into contract:\n%s", plain)
		}
	})

	// JS: "dispatchPairSuffix: no collision keeps the plain name; each cause takes -2"
	t.Run("dispatchPairSuffix: no collision keeps the plain name; each cause takes -2", func(t *testing.T) {
		composed := func(s string) string { return "brief" + s }
		report := func(s string) string { return "report" + s }
		if got := DispatchPairSuffix(composed, report, func(string) bool { return false }, ""); got != "" {
			t.Fatalf("free pair suffix=%q", got)
		}
		for _, exists := range []func(string) bool{
			func(p string) bool { return p == "brief" },
			func(p string) bool { return p == "report" },
		} {
			if got := DispatchPairSuffix(composed, report, exists, ""); got != "-2" {
				t.Fatalf("collision suffix=%q, want -2", got)
			}
		}
		if got := DispatchPairSuffix(composed, report, func(string) bool { return false }, "report"); got != "-2" {
			t.Fatalf("last-report collision suffix=%q, want -2", got)
		}
	})
}

func TestDispatchTM3bPromptContracts(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	// JS: "compose: command output in the report is pasted from the run (D32 line, in order)"
	t.Run("compose: command output in the report is pasted from the run (D32 line, in order)", func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\nRole body.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := ComposePrompt(role, "implementer", "build", "# Goal\nDo it.\n", "/tmp/report.md", ctx, composeEnv(t), "codex", "", "", false)
		for _, part := range []string{"# Role: Implementer\n", "# Brief\n\n# Goal\nDo it.\n", "Write your report as Markdown to `/tmp/report.md`", "Command output you put in the report is pasted from the run"} {
			if !strings.Contains(got, part) {
				t.Errorf("composed prompt lacks %q", part)
			}
		}
	})
	// JS: "compose: the network note asks for the integration tests even unrunnable (D29)"
	t.Run("compose: the network note asks for the integration tests even unrunnable (D29)", func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Codex\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := ComposePrompt(role, "implementer", "build", "brief", "/tmp/report.md", ctx, composeEnv(t), "codex", "", "", false)
		if !strings.Contains(got, "tests that start a local server fail with \"Operation not permitted\"") || !strings.Contains(got, "Still write the integration tests the brief asks for") {
			t.Fatalf("Codex sandbox note missing its local-network guidance:\n%s", got)
		}
	})
}
