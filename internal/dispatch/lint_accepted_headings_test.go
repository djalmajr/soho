package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestAcceptedHeadingsWarning(t *testing.T) {
	file := filepath.Join(t.TempDir(), "brief.md")
	body := "## Expected result\nDone\n## Owned files\n- a.go\n## Report\nNo commit.\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT_ALIASES": "Goal=Pedido do dono|Goal|objetivo"}, BriefLintOptions{}).MissingMessage
	want := "brief " + file + " is missing sections: [Goal] [Forbidden] — the worker does not know what the slice is for; nothing keeps the worker out of other files — accepted headings: Goal: Goal, Objetivo, Meta, Pedido do dono; Forbidden: Forbidden, Non-goals, Constraints, Proibido, Proibidos, Fora do escopo, Restrições; add your own with brief_lint_aliases=<Section>=<Heading>|<Heading>"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// R566 F1: a padded alias is listed as the spelling a pasted heading matches,
// and that heading clears the warning.
func TestPaddedAliasListedAsItMatches(t *testing.T) {
	sections, ignored := ParseBriefLintAliases("Goal=  Pedido do dono|   ,Owned files=Edit ")
	if len(ignored) != 0 || len(sections["Goal"]) != 1 || sections["Goal"][0] != "Pedido do dono" {
		t.Fatalf("sections=%q ignored=%q", sections, ignored)
	}
	// R568: a trailing space is a word delimiter and stays.
	if got := sections["Owned files"]; len(got) != 1 || got[0] != "Edit " {
		t.Fatalf("trailing delimiter: %q", got)
	}
	if !BriefSectionTitleMatches("Edit scope", "Owned files", sections) || BriefSectionTitleMatches("Editorial notes", "Owned files", sections) {
		t.Fatal("the trailing space no longer delimits the alias")
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	env := platform.Env{"SOHO_BRIEF_LINT_ALIASES": "Goal= Pedido do dono"}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.md")
	if err := os.WriteFile(missing, []byte("## Expected result\nDone\n## Owned files\n- a.go\n## Forbidden\nx\n## Report\nNo commit.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := BriefLintFindings(missing, ctx, env, BriefLintOptions{}).MissingMessage; !strings.Contains(got, "Goal: Goal, Objetivo, Meta, Pedido do dono;") {
		t.Fatalf("listed spelling: %q", got)
	}
	pasted := filepath.Join(dir, "pasted.md")
	if err := os.WriteFile(pasted, []byte("##  Pedido do dono\nDo it\n## Expected result\nDone\n## Owned files\n- a.go\n## Forbidden\nx\n## Report\nNo commit.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := BriefLintFindings(pasted, ctx, env, BriefLintOptions{}).MissingMessage; got != "" {
		t.Fatalf("pasted heading still missing: %q", got)
	}
}

func TestForbiddenPluralAndLabels(t *testing.T) {
	base := "## Objetivo\nDo it\n## Expected result\nDone\n## Arquivos\n%s\n## Report\nNo commit.\n"
	for _, tc := range []struct {
		name, content string
		missing       bool
	}{
		{"plural heading", "## Proibidos\nx", false},
		{"strict word boundary", "## Proibidos de tocar\nx", true},
		{"punctuation boundary", "## Proibidos?\nx", false},
		{"plural label", "**Proibidos:** a.go", false},
		{"english label", "Forbidden: y", false},
		{"list label", "- Fora do escopo: z", false},
		{"fenced label", "```\nForbidden: y\n```", true},
		{"prose label", "não é proibido: x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(base, "%s", tc.content, 1)
			got := strings.Contains(BriefMissingSections(body, false, nil), "[Forbidden]")
			if got != tc.missing {
				t.Fatalf("body %q missing Forbidden = %v", body, got)
			}
		})
	}
	lines, _ := OwnedSectionLines([]string{"**Proibidos:** a.go", "- b.go", "Owned: c.go"})
	if strings.Contains(strings.Join(lines, "\n"), "a.go") || strings.Contains(strings.Join(lines, "\n"), "b.go") || !strings.Contains(strings.Join(lines, "\n"), "c.go") {
		t.Fatalf("owned lines = %q", lines)
	}
	if BriefHasOwnedItems(strings.Replace(base, "%s", "**Proibidos:** a.go", 1), nil) {
		t.Fatal("forbidden path counted as owned")
	}
}
