package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// shippedBriefLintDefaults reads the two brief-lint keys from the shipped
// config.defaults, so the tests pin the same values both runtimes load.
func shippedBriefLintDefaults(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "config.defaults"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if hash := strings.IndexByte(line, '#'); hash >= 0 {
			line = line[:hash]
		}
		line = strings.TrimSpace(line)
		for _, key := range []string{"brief_lint_aliases", "brief_lint_placeholders"} {
			if strings.HasPrefix(line, key+"=") {
				got[key] = line[len(key)+1:]
			}
		}
	}
	for _, key := range []string{"brief_lint_aliases", "brief_lint_placeholders"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("config.defaults missing key %s", key)
		}
	}
	return got
}

// defaultsCtx builds a config with the shipped defaults as the defaults
// layer; overrides replace an entry the way a user or project file would.
func defaultsCtx(t *testing.T, overrides map[string]string) *core.Config {
	t.Helper()
	entries := map[string]core.ConfigEntry{}
	for key, value := range shippedBriefLintDefaults(t) {
		entries[key] = core.ConfigEntry{Value: value, Source: "defaults"}
	}
	for key, value := range overrides {
		entries[key] = core.ConfigEntry{Value: value, Source: "project"}
	}
	return &core.Config{Entries: entries}
}

// ptBRBrief uses the five Portuguese headings of the shipped default
// (Arquivos próprios only the default accepts) plus the "Sem commit" line.
const ptBRBrief = "## Objetivo\nFazer a fatia.\n## Resultado esperado\nFeito.\n## Arquivos próprios\nsrc/a.go\n## Proibido\nSem commit.\n## Relatório\nFeito.\n"

func TestShippedPortugueseDefaultAliases(t *testing.T) {
	t.Run("the shipped default adds the Portuguese headings the built-in map rejects", func(t *testing.T) {
		// The built-in map already takes single-word Portuguese titles (Objetivo,
		// Proibido, Relatório...) on a word boundary; the default only adds the
		// multi-word ones it rejects.
		aliases, ignored := ParseBriefLintAliases(shippedBriefLintDefaults(t)["brief_lint_aliases"])
		if len(ignored) != 0 {
			t.Fatalf("shipped default has malformed items: %#v", ignored)
		}
		if len(aliases["Owned files"]) == 0 || len(aliases["Expected result"]) == 0 {
			t.Fatalf("shipped default aliases = %#v", aliases)
		}
	})
	t.Run("the five Portuguese titles plus the 'Sem commit' line leave no mark", func(t *testing.T) {
		if got := BriefMissingSections(ptBRBrief, false, nil); got != " [Owned files]" {
			t.Fatalf("control (built-in map only) = %q, want the word continuation 'Arquivos próprios' to stay missing", got)
		}
		aliases, _ := ParseBriefLintAliases(shippedBriefLintDefaults(t)["brief_lint_aliases"])
		if got := BriefMissingSections(ptBRBrief, false, aliases); got != "" {
			t.Fatalf("shipped default left a mark: %q", got)
		}
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(ptBRBrief), 0o600); err != nil {
			t.Fatal(err)
		}
		findings := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		if len(findings.Warnings) != 0 || findings.MissingMessage != "" {
			t.Fatalf("findings = warnings=%#v missing=%q", findings.Warnings, findings.MissingMessage)
		}
	})
	t.Run("'Arquivos proibidos' does not stand for Owned files", func(t *testing.T) {
		// The default has no bare 'Arquivos' alias: a prefix match would let the
		// forbidden-files heading satisfy the owned-files section.
		body := strings.Replace(ptBRBrief, "## Arquivos próprios\nsrc/a.go\n", "## Arquivos proibidos\nsrc/b.go\n", 1)
		aliases, _ := ParseBriefLintAliases(shippedBriefLintDefaults(t)["brief_lint_aliases"])
		if got := BriefMissingSections(body, false, aliases); got != " [Owned files]" {
			t.Fatalf("missing = %q, want [Owned files]", got)
		}
	})
	t.Run("a project value for the key replaces the shipped default", func(t *testing.T) {
		body := strings.Replace(ptBRBrief, "## Resultado esperado", "## Aceite", 1)
		aliases, _ := ParseBriefLintAliases(shippedBriefLintDefaults(t)["brief_lint_aliases"])
		if got := BriefMissingSections(body, false, aliases); got != "" {
			t.Fatalf("'Aceite' is not a shipped alias: %q", got)
		}
		// The project file wins over the default: only Goal has an alias now,
		// so 'Aceite' no longer satisfies Expected result.
		overrides := map[string]string{"brief_lint_aliases": "Goal=Task"}
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		findings := BriefLintFindings(file, defaultsCtx(t, overrides), platform.Env{}, BriefLintOptions{})
		if !strings.Contains(findings.MissingMessage, "[Expected result]") {
			t.Fatalf("project value did not replace the default: %q", findings.MissingMessage)
		}
		// The environment still wins over the project value: the env value
		// (the shipped default, which carries 'Aceite') replaces 'Goal=Task'.
		findings = BriefLintFindings(file, defaultsCtx(t, overrides), platform.Env{"SOHO_BRIEF_LINT_ALIASES": shippedBriefLintDefaults(t)["brief_lint_aliases"]}, BriefLintOptions{})
		if strings.Contains(findings.MissingMessage, "[Expected result]") {
			t.Fatalf("env value did not replace the project value: %q", findings.MissingMessage)
		}
	})
	t.Run("the comparison folds case and accents on both sides", func(t *testing.T) {
		aliases, _ := ParseBriefLintAliases(shippedBriefLintDefaults(t)["brief_lint_aliases"])
		base := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
		for _, tc := range []struct{ name, section, heading string }{
			{"uppercase and accentless 'ARQUIVOS PROPRIOS'", "Owned files", "ARQUIVOS PROPRIOS"},
			{"uppercase 'ACEITE'", "Expected result", "ACEITE"},
			{"uppercase 'RELATORIO'", "Report", "RELATORIO"},
		} {
			body := strings.Replace(base, "# "+tc.section+"\n", "## "+tc.heading+"\n", 1)
			if got := BriefMissingSections(body, false, aliases); got != "" {
				t.Errorf("%s: heading %q left a mark: %q", tc.name, tc.heading, got)
			}
		}
	})
}

func TestUnfilledPlaceholderMarks(t *testing.T) {
	const body = "# Goal\nDispatch to AGENT_NAME.\n# Expected result\nObserved.\n# Owned files\nnone\n# Forbidden\nNo commit or push.\n# Report\nDone.\nworktree: WORKTREE_PATH\n"
	write := func(t *testing.T, value string) string {
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	t.Run("each built-in marker carries the mark with the right line", func(t *testing.T) {
		file := write(t, body)
		findings := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		if len(findings.Warnings) != 0 {
			t.Fatalf("marks ride the missing list, not warnings: %#v", findings.Warnings)
		}
		want := "brief " + file + " is missing sections: [unfilled placeholder 'AGENT_NAME' (line 2)] [unfilled placeholder 'WORKTREE_PATH' (line 11)]"
		if findings.MissingMessage != want {
			t.Fatalf("MissingMessage = %q, want %q", findings.MissingMessage, want)
		}
	})
	t.Run("sections come first, then the marks, and known reasons keep their tail", func(t *testing.T) {
		file := write(t, strings.Replace(body, "# Report\nDone.\n", "", 1))
		findings := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " is missing sections: [Report] [unfilled placeholder 'AGENT_NAME' (line 2)] [unfilled placeholder 'WORKTREE_PATH' (line 9)] — without a report section the worker may never write one"
		if findings.MissingMessage != want {
			t.Fatalf("MissingMessage = %q, want %q", findings.MissingMessage, want)
		}
	})
	t.Run("the same marker inside a fenced code block is not counted", func(t *testing.T) {
		file := write(t, "# Goal\n```\nAGENT_NAME and WORKTREE_PATH\n```\n# Expected result\nObserved.\n# Owned files\nnone\n# Forbidden\nNo commit or push.\n# Report\nDone.\n")
		findings := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		if findings.MissingMessage != "" {
			t.Fatalf("fenced markers were counted: %q", findings.MissingMessage)
		}
	})
	t.Run("a marker on the fence line itself is not counted", func(t *testing.T) {
		file := write(t, "# Goal\n```bash AGENT_NAME\nWORKTREE_PATH\n```\n# Expected result\nObserved.\n# Owned files\nnone\n# Forbidden\nNo commit or push.\n# Report\nDone.\n")
		findings := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		if findings.MissingMessage != "" {
			t.Fatalf("fence-line markers were counted: %q", findings.MissingMessage)
		}
	})
	t.Run("matching is case-sensitive: lowercase agent_name is not counted", func(t *testing.T) {
		file := write(t, strings.Replace(body, "AGENT_NAME", "agent_name", 1))
		findings := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		if strings.Contains(findings.MissingMessage, "AGENT_NAME") {
			t.Fatalf("lowercase marker was counted: %q", findings.MissingMessage)
		}
	})
	t.Run("an identifier-like marker counts only as a whole word", func(t *testing.T) {
		file := write(t, "# Goal\nForward SOHO_AGENT_NAME and AGENT_NAMES, set MY_SLOT_DIR.\nKeep `AGENT_NAME`, (AGENT_NAME) and AGENT_NAME.\n# Expected result\nObserved.\n# Owned files\nnone\n# Forbidden\nNo commit or push.\n# Report\nDone.\nworktree: /tmp/WORKTREE_PATH_OLD\n")
		ctx := defaultsCtx(t, map[string]string{"brief_lint_placeholders": "MY_SLOT, Other slot"})
		findings := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " is missing sections: [unfilled placeholder 'AGENT_NAME' (line 3)]"
		if findings.MissingMessage != want {
			t.Fatalf("MissingMessage = %q, want %q", findings.MissingMessage, want)
		}
	})
	t.Run("a marker with other characters stays a substring", func(t *testing.T) {
		file := write(t, "# Goal\nsee xOther slotx here\n# Expected result\nObserved.\n# Owned files\nnone\n# Forbidden\nNo commit or push.\n# Report\nDone.\n")
		ctx := defaultsCtx(t, map[string]string{"brief_lint_placeholders": "Other slot"})
		findings := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " is missing sections: [unfilled placeholder 'Other slot' (line 2)]"
		if findings.MissingMessage != want {
			t.Fatalf("MissingMessage = %q, want %q", findings.MissingMessage, want)
		}
	})
	t.Run("the brief_lint_placeholders list adds trimmed items in order and drops duplicates", func(t *testing.T) {
		file := write(t, "# Goal\nslot MY_SLOT then Other slot\n# Expected result\nObserved.\n# Owned files\nnone\n# Forbidden\nNo commit or push.\n# Report\nDone.\n")
		ctx := defaultsCtx(t, map[string]string{"brief_lint_placeholders": "MY_SLOT, Other slot, MY_SLOT, AGENT_NAME"})
		findings := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		want := "brief " + file + " is missing sections: [unfilled placeholder 'MY_SLOT' (line 2)] [unfilled placeholder 'Other slot' (line 2)]"
		if findings.MissingMessage != want {
			t.Fatalf("MissingMessage = %q, want %q", findings.MissingMessage, want)
		}
	})
	t.Run("warn and strict agree on the mark; off produces nothing", func(t *testing.T) {
		file := write(t, body)
		warn := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{}, BriefLintOptions{})
		strict := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{"SOHO_BRIEF_LINT": "strict"}, BriefLintOptions{})
		if strict.MissingMessage != warn.MissingMessage || strict.Mode != "strict" || warn.Mode != "warn" {
			t.Fatalf("warn/strict differ: warn=%#v strict=%#v", warn, strict)
		}
		off := BriefLintFindings(file, defaultsCtx(t, nil), platform.Env{"SOHO_BRIEF_LINT": "off"}, BriefLintOptions{})
		if off.MissingMessage != "" || len(off.Warnings) != 0 {
			t.Fatalf("off findings = %#v", off)
		}
	})
}

func TestUnfilledSlotAndCRFence(t *testing.T) {
	markers := briefLintPlaceholderMarkers("")
	t.Run("<slot> inside a path is flagged, as a label it is not", func(t *testing.T) {
		body := "cache em `/tmp/hs-go/<slot>/cache`\n(`<slot>` = `build`)\nsrc/<slot>/main.go\n"
		got := UnfilledPlaceholders(body, markers)
		want := []string{"unfilled placeholder '<slot>' (line 1)", "unfilled placeholder '<slot>' (line 3)"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("got %#v want %#v", got, want)
		}
	})
	t.Run("a fence closed by a CR-terminated line closes", func(t *testing.T) {
		body := "```\nAGENT_NAME inside\n```\r\nAGENT_NAME after\n"
		got := UnfilledPlaceholders(body, markers)
		if len(got) != 1 || got[0] != "unfilled placeholder 'AGENT_NAME' (line 4)" {
			t.Fatalf("got %#v", got)
		}
	})
}
