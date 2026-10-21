package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestBriefMissingSections(t *testing.T) {
	const full = "# Goal\n\nDo the slice.\n# Expected result\n\nDone.\n# Owned files\n\nsrc/a.go\n# Forbidden\n\nNo commit or push.\n# Report\n\nDone.\n"
	t.Run("// JS: \"lint: every missing section in order\"", func(t *testing.T) {
		if got, want := BriefMissingSections("Just do it.\n", false, nil), " [Goal] [Expected result] [Owned files] [Forbidden] [Report] [no-git line: say 'no commit/push']"; got != want {
			t.Fatalf("BriefMissingSections = %q, want %q", got, want)
		}
	})
	t.Run("// JS: \"lint: each section absent, each alternative accepted\"", func(t *testing.T) {
		cases := []struct {
			label string
			alts  []string
		}{
			{"Goal", []string{"Goal"}},
			{"Expected result", []string{"Expected result", "Acceptance", "Definition of done"}},
			{"Owned files", []string{"Owned files", "Owned", "Scope"}},
			{"Forbidden", []string{"Forbidden", "Non-goals", "Constraints"}},
			{"Report", []string{"Report"}},
		}
		for _, tc := range cases {
			for _, heading := range tc.alts {
				t.Run(tc.label+"/"+heading, func(t *testing.T) {
					headings := map[string]string{
						"Goal": "Goal", "Expected result": "Expected result", "Owned files": "Owned files",
						"Forbidden": "Forbidden", "Report": "Report",
					}
					headings[tc.label] = heading
					body := "# " + headings["Goal"] + "\nDo the slice.\n# " + headings["Expected result"] + "\nDone.\n# " + headings["Owned files"] + "\nsrc/a.go\n# " + headings["Forbidden"] + "\nNo commit/push.\n# " + headings["Report"] + "\nDone.\n"
					if got := BriefMissingSections(body, false, nil); got != "" {
						t.Fatalf("heading %q for %s did not satisfy the section: %s", heading, tc.label, got)
					}
				})
			}
		}
		for _, tc := range cases {
			body := strings.Replace(full, "# "+tc.label, "# Unrelated", 1)
			if got, want := BriefMissingSections(body, false, nil), " ["+tc.label+"]"; got != want {
				t.Errorf("missing %s = %q, want %q", tc.label, got, want)
			}
		}
	})
	t.Run("// JS: \"lint: header level 1-3 only, case-insensitive, a word mid-paragraph does not count\"", func(t *testing.T) {
		for _, tc := range []struct{ name, body, want string }{
			{"level two", strings.Replace(full, "# Goal", "## gOaL", 1), ""},
			{"level three", strings.Replace(full, "# Goal", "### Goal", 1), ""},
			{"level four", strings.Replace(full, "# Goal", "#### Goal", 1), " [Goal]"},
			{"no space", strings.Replace(full, "# Goal", "#Goal", 1), " [Goal]"},
			{"paragraph", strings.Replace(full, "# Goal", "Goal: do it.", 1), " [Goal]"},
			{"long s is not ascii case folding", strings.Replace(full, "# Goal", "# Gſal", 1), " [Goal]"},
			{"no git accepts either ascii marker", strings.Replace(full, "No commit or push.", "NO PUSH at all.", 1), ""},
			{"long s does not satisfy no git", strings.Replace(full, "No commit or push.", "No coſmit and no puſh.", 1), " [no-git line: say 'no commit/push']"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := BriefMissingSections(tc.body, false, nil); got != tc.want {
					t.Fatalf("BriefMissingSections = %q, want %q", got, tc.want)
				}
			})
		}
	})
	t.Run("// JS: \"lint: built-in Portuguese section headings and accentless headings pass\"", func(t *testing.T) {
		pt := "## Objetivo\nDo it.\n## Resultado esperado\nDone.\n## Arquivos\nsrc/a.go\n## Proibido\nNo commit/push.\n## Relatório\nDone.\n"
		if got := BriefMissingSections(pt, false, nil); got != "" {
			t.Fatalf("Portuguese headings missing: %s", got)
		}
		if got := BriefMissingSections(strings.Replace(pt, "Relatório", "Relatorio", 1), false, nil); got != "" {
			t.Fatalf("accentless heading missing: %s", got)
		}
	})
	t.Run("// JS: \"lint: canonical NFD headings match before JavaScript lowercasing\"", func(t *testing.T) {
		for _, tc := range []struct{ name, body string }{
			{"macron Goal", strings.Replace(full, "# Goal", "# Gōal", 1)},
			{"caron Scope", strings.Replace(full, "# Owned files", "# Šcope", 1)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := BriefMissingSections(tc.body, false, nil); got != "" {
					t.Fatalf("canonical NFD heading missing: %s", got)
				}
			})
		}
		aliases := map[string][]string{"Goal": {"Task"}}
		if got := BriefMissingSections(strings.Replace(full, "# Goal", "# TasK", 1), false, aliases); got != "" {
			t.Fatalf("Kelvin sign alias heading missing: %s", got)
		}
	})
	t.Run("// JS: \"lint: Portuguese section prefixes reject word continuations\"", func(t *testing.T) {
		body := strings.Replace(full, "# Owned files", "## Arquivos proibidos", 1)
		if got := BriefMissingSections(body, false, nil); got != " [Owned files]" {
			t.Fatalf("Portuguese continuation was accepted: %q", got)
		}
	})
	t.Run("// JS: \"lint: Meta is a Portuguese Goal heading but Metadata and Metadados are not\"", func(t *testing.T) {
		for _, tc := range []struct{ title, want string }{{"Meta", ""}, {"Metadata", " [Goal]"}, {"Metadados", " [Goal]"}} {
			body := strings.Replace(full, "# Goal", "## "+tc.title, 1)
			if got := BriefMissingSections(body, false, nil); got != tc.want {
				t.Errorf("title %q: got %q, want %q", tc.title, got, tc.want)
			}
		}
	})
	t.Run("// JS: \"lint: a read-only brief needs no Owned files; the other sections still hold\"", func(t *testing.T) {
		body := strings.Replace(full, "# Owned files\n\nsrc/a.go\n", "", 1)
		if got := BriefMissingSections(body, false, nil); got != " [Owned files]" {
			t.Fatalf("edit brief missing = %q", got)
		}
		if got := BriefMissingSections(body, true, nil); got != "" {
			t.Fatalf("read-only brief missing = %q", got)
		}
	})
}

func TestBriefLintAliases(t *testing.T) {
	const full = "# Goal\n\nDo the slice.\n# Expected result\n\nDone.\n# Owned files\n\nsrc/a.go\n# Forbidden\n\nNo commit/push.\n# Report\n\nDone.\n"
	t.Run("// JS: \"aliases: the parser applies the valid items and warns once per malformed one\"", func(t *testing.T) {
		sections, ignored := ParseBriefLintAliases("Goal=Parte A|Contexto,Expected result=Entrega")
		if len(ignored) != 0 || strings.Join(sections["Goal"], "|") != "Parte A|Contexto" || strings.Join(sections["Expected result"], "|") != "Entrega" {
			t.Fatalf("parsed aliases = %#v, ignored %#v", sections, ignored)
		}
		sections, ignored = ParseBriefLintAliases("Goal=OK,nodash,PartX=Foo,Report=")
		if len(sections["Goal"]) != 1 || sections["Goal"][0] != "OK" || strings.Join(ignored, "|") != "nodash|PartX=Foo|Report=" {
			t.Fatalf("parsed aliases = %#v, ignored %#v", sections, ignored)
		}
	})
	t.Run("// JS: \"lint: an alias heading covering the section passes, a mid-title one does not\"", func(t *testing.T) {
		body := "## Criterios\nDo the slice.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
		aliases := map[string][]string{"Goal": {"Critérios"}}
		if got := BriefMissingSections(body, false, aliases); got != "" {
			t.Fatalf("accent-insensitive alias missing: %s", got)
		}
		body = strings.Replace(body, "## Criterios", "## Another Criterios", 1)
		if got := BriefMissingSections(body, false, aliases); got != " [Goal]" {
			t.Fatalf("mid-title alias matched: %q", got)
		}
		readOnly := strings.Replace(full, "# Goal\n\nDo the slice.\n", "## Parte A\nDo the slice.\n", 1)
		readOnly = strings.Replace(readOnly, "# Owned files\n\nsrc/a.go\n", "", 1)
		aliases = map[string][]string{"Goal": {"Parte A"}}
		if got := BriefMissingSections(readOnly, false, aliases); got != " [Owned files]" {
			t.Fatalf("edit alias result = %q", got)
		}
		if got := BriefMissingSections(readOnly, true, aliases); got != "" {
			t.Fatalf("read-only alias result = %q", got)
		}
	})
}

func TestMissingSectionsReasons(t *testing.T) {
	t.Run("// JS: \"lint: each missing section carries its own reason, alone and combined\"", func(t *testing.T) {
		cases := []struct{ label, reason string }{
			{"Goal", "the worker does not know what the slice is for"},
			{"Expected result", "nothing says when the slice is done"},
			{"Owned files", "workers without owned files collide"},
			{"Forbidden", "nothing keeps the worker out of other files"},
			{"Report", "without a report section the worker may never write one"},
			{"no-git line: say 'no commit/push'", "the worker may commit or push"},
		}
		for _, tc := range cases {
			if got := MissingSectionsReasons(" [" + tc.label + "]"); got != tc.reason {
				t.Errorf("reason for %s = %q, want %q", tc.label, got, tc.reason)
			}
		}
		if got, want := MissingSectionsReasons(" [Goal] [Report]"), cases[0].reason+"; "+cases[4].reason; got != want {
			t.Fatalf("combined reasons = %q, want %q", got, want)
		}
	})
}

func TestFailureMatrixMissing(t *testing.T) {
	t.Run("// JS: \"lint: checks only the optional failure matrix and explains missing markers in order\"", func(t *testing.T) {
		body := "# Failure matrix\n- [retry] retry\n\n# Later\n[clock] outside\n"
		if got, want := strings.Join(FailureMatrixMissing(body), " "), "crash clock"; got != want {
			t.Fatalf("FailureMatrixMissing = %q, want %q", got, want)
		}
		if got := FailureMatrixMissing("# Ordinary\n[crash]\n"); len(got) != 0 {
			t.Fatalf("ordinary section returned markers: %#v", got)
		}
		if got, want := strings.Join(FailureMatrixMissing("### failure MATRIX variants\n[crash] [retry] [clock]\n## end\n[clock]\n"), " "), ""; got != want {
			t.Fatalf("complete matrix = %q, want %q", got, want)
		}
	})
}

func TestEmptyCodeLines(t *testing.T) {
	t.Run("// JS: \"lint: the empty-code warn caps at three lines plus one summary\"", func(t *testing.T) {
		body := "``\nabc `` xyz\n```js\n``\n```\n~~~\n``\n~~~\n````\n"
		got := EmptyCodeLines(body)
		if len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Fatalf("EmptyCodeLines = %#v, want [1 2]", got)
		}
	})
	t.Run("// JS: \"lint: emits dispatch warnings unchanged and creates no state\"", func(t *testing.T) {
		body := "# Goal\n# Expected result\n# Owned files\n# Forbidden\nNo commit/push.\n# Report\n"
		body += "``\n``\n``\n``\n```go\n``\n```\n"
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		findings := BriefLintFindings(file, &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{}, BriefLintOptions{})
		if len(findings.Warnings) != 4 || !strings.Contains(findings.Warnings[3], "and 1 more line(s)") {
			t.Fatalf("warnings = %#v", findings.Warnings)
		}
	})
}

func TestEmptyCodeLinesDoubleBacktickSpans(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		empty      bool
	}{
		{name: "table-code-span", line: "| ``Worktree: `/a` (b)`` | x |"},
		{name: "reported-table-row", line: "| ``Worktree: `/a/b/.worktrees/x` (branch y)`` | `/a/b/.worktrees/x` |"},
		{name: "heredoc-empty", line: "run `` now", empty: true},
		{name: "spaces-only", line: "a ``   `` b", empty: true},
		{name: "tabs-only", line: "a `` \t `` b", empty: true},
		{name: "valid-then-unclosed", line: "use ``x`` and `` then", empty: true},
		{name: "single-code-span", line: "plain `code` here"},
		{name: "multiple-valid-spans", line: "use ``x`` and ``y``"},
		{name: "valid-then-whitespace", line: "use ``x`` and `` \t ``", empty: true},
		{name: "whitespace-before-valid", line: "``   `` and ``ok``", empty: true},
		{name: "other-runs-inside-span", line: "use ``one ` two ``` three``"},
		{name: "longer-inline-runs", line: "use ```x``` and ````y````"},
		{name: "non-ascii-content", line: "use ``\u00a0``"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One result per line, numbered from one; a second line cannot
			// close the double-backtick run on the preceding line.
			got := EmptyCodeLines("prefix\n" + tc.line + "\nsuffix")
			if tc.empty {
				if len(got) != 1 || got[0] != 2 {
					t.Fatalf("EmptyCodeLines(%q) = %v, want [2]", tc.line, got)
				}
			} else if len(got) != 0 {
				t.Fatalf("EmptyCodeLines(%q) = %v, want no lines", tc.line, got)
			}
		})
	}
	t.Run("cross-line-pair", func(t *testing.T) {
		got := EmptyCodeLines("prefix\n``x\nother``\nsuffix")
		if len(got) != 2 || got[0] != 2 || got[1] != 3 {
			t.Fatalf("cross-line double backticks = %v, want [2 3]", got)
		}
	})
}

func TestBriefLintFindingsModesAndWarnings(t *testing.T) {
	t.Run("// JS: \"lint: emits dispatch warnings unchanged and creates no state\"", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte("# Goal\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\n``\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"SOHO_BRIEF_LINT_ALIASES": "not-an-alias"}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		got := BriefLintFindings(file, ctx, env, BriefLintOptions{})
		if got.Mode != "warn" || len(got.Warnings) != 2 || got.Warnings[0] != "brief_lint_aliases: ignored 'not-an-alias' (use Section=Heading|Heading)" || !strings.Contains(got.Warnings[1], "line 7 has empty inline code") || !strings.Contains(got.MissingMessage, "[Expected result]") {
			t.Fatalf("findings = %#v", got)
		}
	})
	t.Run("// JS: \"lint: strict mode reports missing sections while warn continues\"", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte("Just do it."), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		warn := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		strict := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT": "strict"}, BriefLintOptions{})
		off := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT": "off"}, BriefLintOptions{})
		if warn.MissingMessage == "" || strict.MissingMessage != warn.MissingMessage || strict.Mode != "strict" {
			t.Fatalf("warn/strict findings differ: warn=%#v strict=%#v", warn, strict)
		}
		if off.Mode != "off" || off.MissingMessage != "" || len(off.Warnings) != 0 {
			t.Fatalf("off findings = %#v", off)
		}
	})
}

type lintCorpusRow struct {
	Path     string   `json:"path"`
	Aliases  string   `json:"aliases,omitempty"`
	Warnings []string `json:"warnings"`
	Missing  string   `json:"missingMessage"`
	Mode     string   `json:"mode"`
}

var unfilledPlaceholderBracket = regexp.MustCompile(` \[unfilled placeholder '[^']*' \(line [0-9]+\)\]`)

// stripUnfilledPlaceholderMarks removes the Go-only unfilled placeholder
// marks from a missing-sections message before the JS corpus comparison:
// the corpus rows predate the feature (PLAN: new functions are Go-only
// after command parity, the JS is frozen). The marks are dropped from the
// list and a message that held only marks becomes empty; everything else —
// sections, reasons, warnings, mode — is compared strictly.
func stripUnfilledPlaceholderMarks(message string) string {
	const separator = "is missing sections:"
	idx := strings.Index(message, separator)
	if idx < 0 {
		return message
	}
	rest := unfilledPlaceholderBracket.ReplaceAllString(message[idx+len(separator):], "")
	if strings.TrimSpace(rest) == "" {
		return ""
	}
	return message[:idx] + separator + rest
}

func TestLintCorpusMatchesJavaScript(t *testing.T) {
	t.Run("// JS: \"gen_lint.mjs generated brief corpus parity\"", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "lint_corpus.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rows []lintCorpusRow
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		warned, clean := 0, 0
		for _, row := range rows {
			t.Run(row.Path, func(t *testing.T) {
				env := platform.Env{"SOHO_BRIEF_LINT": "warn"}
				if row.Aliases != "" {
					env["SOHO_BRIEF_LINT_ALIASES"] = row.Aliases
				}
				got := BriefLintFindings(row.Path, ctx, env, BriefLintOptions{})
				got.MissingMessage = stripUnfilledPlaceholderMarks(got.MissingMessage)
				if got.Mode != row.Mode || got.MissingMessage != row.Missing || strings.Join(got.Warnings, "\x00") != strings.Join(row.Warnings, "\x00") {
					t.Fatalf("Go findings differ from JS: got=%#v want=%#v", got, row)
				}
				if len(row.Warnings) > 0 || row.Missing != "" {
					warned++
				} else {
					clean++
				}
			})
		}
		t.Logf("JS corpus parity: %d briefs with warnings; %d clean; %d total; zero divergences", warned, clean, len(rows))
	})
}
