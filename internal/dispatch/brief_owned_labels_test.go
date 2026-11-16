package dispatch

import (
	"strings"
	"testing"
)

func TestBriefOwnedLabelsItems(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"negative only", "## Owned files\nForbidden:\n- `src/no.go`\n", false},
		{"negative same line", "## Owned files\n- Forbidden: `src/no.go`\n", false},
		{"positive after negative", "## Owned files\nProibido:\n- `src/no.go`\nPode tocar: `src/yes.go`\n", true},
		{"empty positive", "## Owned files\nProibido:\n- `src/no.go`\nPode tocar:\n", false},
		{"negative consecutive deeper", "## Owned files\nForbidden:\n### detail\nNÃO TOCAR:\n- `src/no.go`\n", false},
		{"crlf", "## Owned files\r\nForbidden:\r\n- `src/no.go`\r\n", false},
		{"fence", "## Owned files\n```\nForbidden:\n```\n- `src/yes.go`\n", true},
		{"nonlabel", "## Owned files\n- `src/forbidden:file.go`\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BriefHasOwnedItems(tc.body, nil); got != tc.want {
				t.Fatalf("BriefHasOwnedItems=%v want %v", got, tc.want)
			}
			if got := BriefHasLiteralOwnedItems(tc.body); got != tc.want {
				t.Fatalf("BriefHasLiteralOwnedItems=%v want %v", got, tc.want)
			}
		})
	}
}

// Review R379: a positive label's value with its own list marker or an
// emphasis pair still declares no files when it says so, for the read-only
// signal.
func TestBriefOwnedLabelValueDeclaresNoFiles(t *testing.T) {
	for _, body := range []string{
		"## Owned files\nOwned: - None.\n",
		"## Owned files\nOwned: **None.**\n",
		"## Owned files\n**Owned:** nenhum\n",
	} {
		if BriefHasLiteralOwnedItems(body) {
			t.Fatalf("BriefHasLiteralOwnedItems(%q) = true, want false", body)
		}
	}
	if !BriefHasLiteralOwnedItems("## Owned files\nOwned: - src/ui/app.go\n") {
		t.Fatal("a real path after a positive label must stay an edit request")
	}
}

func TestBriefForbiddenLabelLint(t *testing.T) {
	prefix := "## Goal\nGoal\n## Expected result\nDone\n## Arquivos\nPode tocar:\n- `src/ui/**`\n"
	suffix := "## Report\nNo commit.\n"
	for _, label := range []string{"Proibido:", "**Proibido:**", "- Forbidden: `src/no.go`", "NÃO TOCAR:", "Do not touch:", "Fora do escopo:", "Out of scope:"} {
		t.Run(label, func(t *testing.T) {
			if got := BriefMissingSections(prefix+label+"\n- `src/**`\n"+suffix, false, nil); strings.Contains(got, "[Forbidden]") {
				t.Fatalf("missing Forbidden: %q", got)
			}
		})
	}
	for _, text := range []string{"Proibidos\n", "- `src/forbidden:file.go`\n", "```md\nForbidden:\n```\n", "~~~\nProibido:\n~~~\n"} {
		t.Run("not label "+text, func(t *testing.T) {
			if got := BriefMissingSections(prefix+text+suffix, false, nil); !strings.Contains(got, "[Forbidden]") {
				t.Fatalf("incorrect Forbidden match: %q", got)
			}
		})
	}
}
