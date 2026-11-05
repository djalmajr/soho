package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// DJA-685: the per-`Type:` sections and the named mutations in the brief
// lint. Warnings only: the lint never refuses on them, even with
// brief_lint=strict; read-only roles and amendments (PathsOnly) get none.

func TestTypeSectionWarnings(t *testing.T) {
	const path = "brief.md"
	allMarkers := "## Risk markers\n- [boundary] b\n- [lifetime] l\n- [shared] s\n- [identity] i\n- [hostile] h\n"
	mutationsRow := "## Mutations\n| Mutation | Case |\n|---|---|\n| m | c |\n"
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			"backend-missing-both-sections",
			"Type: backend\n## Goal\nDo it.\n",
			[]string{
				"brief " + path + " has Type: backend and no 'Risk markers' section: nothing makes the slice state its risks",
				"brief " + path + " has Type: backend and no 'Mutations' section: name the mutation and the case that must fail, for each rule",
			},
		},
		{
			"backend-all-markers-missing-mutations",
			"Type: backend\n" + allMarkers,
			[]string{
				"brief " + path + " has Type: backend and no 'Mutations' section: name the mutation and the case that must fail, for each rule",
			},
		},
		{
			"backend-missing-one-marker-names-it",
			"Type: backend\n## Risk markers\n- [boundary] b\n- [lifetime] l\n- [identity] i\n- [hostile] h\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
			},
		},
		{
			"backend-complete",
			"Type: backend\n" + allMarkers + mutationsRow,
			nil,
		},
		{
			"backend-mutations-list-item",
			"Type: backend\n" + allMarkers + "## Mutations\n- flip the check; the fixture stops being caught\n",
			nil,
		},
		{
			"backend-mutations-na",
			"Type: backend\n" + allMarkers + "## Mutations\nn/a: no new rule in this slice\n",
			nil,
		},
		{
			"backend-mutations-header-and-separator-only",
			"Type: backend\n" + allMarkers + "## Mutations\n| Mutation | Case |\n|---|---|\n",
			[]string{"brief " + path + " 'Mutations' section is empty"},
		},
		{
			"backend-risk-markers-whole-na",
			"Type: backend\n## Risk markers\nn/a: nothing at risk in this slice\n" + mutationsRow,
			nil,
		},
		{
			"backend-risk-markers-na-without-reason",
			"Type: backend\n## Risk markers\nn/a:\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
		{
			"backend-risk-markers-na-punctuation-only",
			"Type: backend\n## Risk markers\nn/a: —.\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
		{
			"backend-marker-na-with-reason-counts",
			"Type: backend\n## Risk markers\n- [boundary] b\n- [lifetime] l\n- [shared] n/a: nothing shared\n- [identity] i\n- [hostile] h\n" + mutationsRow,
			nil,
		},
		{
			"backend-marker-na-without-reason-does-not-count",
			"Type: backend\n## Risk markers\n- [boundary] b\n- [lifetime] l\n- [shared] n/a:\n- [identity] i\n- [hostile] h\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
			},
		},
		{
			"portuguese-synonyms-and-level-1",
			"Type: backend\n# Marcadores de risco\n- [boundary] b\n- [lifetime] l\n- [shared] s\n- [identity] i\n- [hostile] h\n# Mutações\n| m | c |\n",
			nil,
		},
		{
			"lowercase-english-and-level-3",
			"Type: backend\n### risk markers\n- [boundary] b\n- [lifetime] l\n- [shared] s\n- [identity] i\n- [hostile] h\n### mutations\n| m | c |\n",
			nil,
		},
		{
			"security-portuguese-synonyms",
			"Type: security\n## Entrada e resposta\n- in -> out\n## Mutações\n| m | c |\n",
			nil,
		},
		{
			"ui-portuguese-synonym",
			"Type: ui\n# Ciclo de vida da interface\n- [async] a\n- [test-env] e\n",
			nil,
		},
		{
			"ui-english",
			"Type: ui\n## UI lifecycle\n- [async] a\n- [test-env] e\n",
			nil,
		},
		{
			"marker-only-in-fenced-block-does-not-count",
			"Type: backend\n## Risk markers\n```\n[boundary]\n[lifetime]\n[shared]\n[identity]\n[hostile]\n```\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
		{
			"marker-in-another-section-does-not-count",
			"Type: backend\n## Goal\n[boundary] [lifetime] [shared] [identity] [hostile]\n## Risk markers\nnothing here\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
		{
			"fenced-title-is-not-a-section",
			"Type: backend\n```\n## Risk markers\n- [boundary] b\n- [lifetime] l\n- [shared] s\n- [identity] i\n- [hostile] h\n```\n" + mutationsRow,
			[]string{
				"brief " + path + " has Type: backend and no 'Risk markers' section: nothing makes the slice state its risks",
			},
		},
		{
			"security-missing-section",
			"Type: security\n## Goal\nDo it.\n",
			[]string{
				"brief " + path + " has Type: security and no 'Input and response' section: nothing lists the inputs and the expected response",
				"brief " + path + " has Type: security and no 'Mutations' section: name the mutation and the case that must fail, for each rule",
			},
		},
		{
			"security-header-and-separator-only-is-empty",
			"Type: security\n## Input and response\n| Input | Expected |\n|---|---|\n" + mutationsRow,
			[]string{"brief " + path + " 'Input and response' section is empty"},
		},
		{
			"security-one-data-row",
			"Type: security\n## Input and response\n| Input | Expected |\n|---|---|\n| hostile link | refused, exit 2 |\n" + mutationsRow,
			nil,
		},
		{
			"security-list-item",
			"Type: security\n## Input and response\n- path outside the tree -> refused\n" + mutationsRow,
			nil,
		},
		{
			"security-whole-na-and-mutations-na",
			"Type: security\n## Input and response\nn/a: no input surface\n## Mutations\nn/a: no rule\n",
			nil,
		},
		{
			"ui-missing-section-no-mutations-required",
			"Type: ui\n## Goal\nDo it.\n",
			[]string{
				"brief " + path + " has Type: ui and no 'UI lifecycle' section: nothing covers async states or what the tests cannot reproduce",
			},
		},
		{
			"ui-missing-one-marker",
			"Type: ui\n## UI lifecycle\n- [async] a\n",
			[]string{
				"brief " + path + " 'UI lifecycle' section has no [test-env]: what the test environment does not reproduce is not named",
			},
		},
		{
			"ui-mutations-section-optional",
			"Type: ui\n## UI lifecycle\n- [async] a\n- [test-env] e\n## Mutations\n| m | c |\n",
			nil,
		},
		{"mechanical-no-sections", "Type: mechanical\n## Goal\nDo it.\n", nil},
		{"docs-no-sections", "Type: docs\n## Goal\nDo it.\n", nil},
		{"review-no-sections", "Type: review\n## Goal\nDo it.\n", nil},
		{
			"title-with-the-template-annotation-counts",
			"Type: ui\n## UI lifecycle (Type: ui)\n- [async] a\n- [test-env] b\n",
			nil,
		},
		{
			"title-that-only-begins-with-the-required-one-does-not-count",
			"Type: ui\n## UI lifecycle review\n- [async] a\n- [test-env] b\n",
			[]string{"brief brief.md has Type: ui and no 'UI lifecycle' section: nothing covers async states or what the tests cannot reproduce"},
		},
		{
			"no-type-line",
			"## Goal\nDo it.\n## Risk markers\n- [boundary] b\n",
			nil,
		},
		{
			"unknown-type-value",
			"Type: quantum\n## Risk markers\n- [boundary] b\n",
			nil,
		},
		{
			"type-line-inside-fence-only",
			"```\nType: backend\n```\n## Goal\nDo it.\n",
			nil,
		},
		{
			"type-line-extra-word-invalid",
			"Type: backend extra\n## Goal\nDo it.\n",
			nil,
		},
		{
			"duplicated-title-first-wins",
			"Type: backend\n## Risk markers\n## Risk markers\n- [boundary] b\n- [lifetime] l\n- [shared] s\n- [identity] i\n- [hostile] h\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
		{
			"marker-form-is-exact-case",
			"Type: backend\n## Risk markers\n- [Boundary] b\n- [LIFETIME] l\n- [shared] s\n- [identity] i\n- [hostile] h\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
			},
		},
		{
			"risk-markers-for-mechanical-type",
			"Type: mechanical\n## Risk markers\n- [boundary] b\n",
			nil,
		},
		{
			"whole-na-must-be-first-non-empty-line",
			"Type: backend\n## Risk markers\nintro line\nn/a: nothing at risk\n" + mutationsRow,
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
		{
			"section-ends-at-next-heading-of-equal-level",
			"Type: backend\n## Risk markers\n## Mutations\n| m | c |\n- [boundary] b\n- [lifetime] l\n- [shared] s\n- [identity] i\n- [hostile] h\n",
			[]string{
				"brief " + path + " 'Risk markers' section has no [boundary]: limits, empties and sizes are not covered",
				"brief " + path + " 'Risk markers' section has no [lifetime]: what opens and who closes it is not covered",
				"brief " + path + " 'Risk markers' section has no [shared]: state other callers touch is not covered",
				"brief " + path + " 'Risk markers' section has no [identity]: who owns what, and a reused id, are not covered",
				"brief " + path + " 'Risk markers' section has no [hostile]: input built to break it is not covered",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TypeSectionWarnings(path, tc.body)
			if !equalStringSlices(got, tc.want) {
				t.Fatalf("warnings = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBriefLintFindingsTypeSectionGating(t *testing.T) {
	// All standard sections present, so only the new check speaks.
	body := "Type: backend\n# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nWrite it.\n"
	run := func(t *testing.T, env platform.Env, options BriefLintOptions) BriefLintResult {
		t.Helper()
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		return BriefLintFindings(file, ctx, env, options)
	}
	t.Run("edit role gets the warnings and no refusal message", func(t *testing.T) {
		got := run(t, platform.Env{}, BriefLintOptions{})
		if got.MissingMessage != "" {
			t.Fatalf("the new check must not refuse: missing=%q", got.MissingMessage)
		}
		if len(got.Warnings) != 2 {
			t.Fatalf("warnings = %q", got.Warnings)
		}
		for _, fragment := range []string{
			"has Type: backend and no 'Risk markers' section: nothing makes the slice state its risks",
			"has Type: backend and no 'Mutations' section: name the mutation and the case that must fail, for each rule",
		} {
			if !strings.Contains(got.Warnings[0], fragment) && !strings.Contains(got.Warnings[1], fragment) {
				t.Fatalf("fragment %q not in %q", fragment, got.Warnings)
			}
		}
	})
	t.Run("read-only role gets none", func(t *testing.T) {
		got := run(t, platform.Env{}, BriefLintOptions{ReadOnly: true})
		if len(got.Warnings) != 0 {
			t.Fatalf("read-only warnings = %q", got.Warnings)
		}
	})
	t.Run("amendment (PathsOnly) gets none", func(t *testing.T) {
		got := run(t, platform.Env{}, BriefLintOptions{PathsOnly: true})
		if len(got.Warnings) != 0 {
			t.Fatalf("amendment warnings = %q", got.Warnings)
		}
	})
	t.Run("off mode warns nothing", func(t *testing.T) {
		got := run(t, platform.Env{"SOHO_BRIEF_LINT": "off"}, BriefLintOptions{})
		if len(got.Warnings) != 0 || got.MissingMessage != "" {
			t.Fatalf("off findings = %#v", got)
		}
	})
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestTypeSectionsFilledTemplateBriefWarnsNothing(t *testing.T) {
	// DJA-685 scenario 13: a brief generated from the shipped template and
	// filled in (every section kept, a Type: line added) produces no
	// warning from this slice, for each Type the lint covers.
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "templates", "brief.md"))
	if err != nil {
		t.Fatalf("shipped template unreadable from the test directory: %v", err)
	}
	body := string(data)
	for _, typ := range []string{"backend", "security", "ui"} {
		filled := body + "\nType: " + typ + "\n"
		if warnings := TypeSectionWarnings("brief.md", filled); len(warnings) != 0 {
			t.Fatalf("filled %s template brief warns: %v", typ, warnings)
		}
	}
}
