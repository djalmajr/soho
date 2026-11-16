package dispatch

import "testing"

func TestBriefTypeOutsideFences(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"Type: backend\n", "backend"}, {"```md\nType: ui\n```\nType: docs\n", "docs"}, {"~~~\nType: backend\n~~~\nType: review\n", "review"}, {"Type: other\nType: backend\n", ""}, {"Type: security\n", "security"}, {"Type: backend other\n", ""},
	} {
		if got := BriefType(tc.body); got != tc.want {
			t.Fatalf("BriefType(%q)=%q want=%q", tc.body, got, tc.want)
		}
	}
}

func TestBriefOwnedSignalAliases(t *testing.T) {
	aliases := map[string][]string{"Owned files": {"My paths"}}
	for _, heading := range []string{"Owned files", "Owned", "Arquivos", "My paths"} {
		if !BriefHasOwnedItems("## "+heading+"\n- app.go\n", aliases) {
			t.Fatalf("alias not recognized: %s", heading)
		}
	}
	for _, body := range []string{"# Owned files\n\n# Report\nDone\n", "# Owned files\n- \n* \n+ \n", "```\n# Owned files\n- app.go\n```\n", "# Owned files\n~~~\napp.go\n~~~\n"} {
		if BriefHasOwnedItems(body, aliases) {
			t.Fatalf("false ownership: %q", body)
		}
	}
}

// DJA-694: the read-only refusal signal counts only the literal "Owned
// files" title (the lint's own title comparison: normalized, prefix, no
// strict boundary) - never the synonyms, so a scope list under "Arquivos"
// refuses nothing. The title comparison keeps its case-insensitive
// behavior: "owned files" still counts.
func TestBriefOwnedSignalReadOnlyLiteral(t *testing.T) {
	for _, heading := range []string{"Owned files", "owned files"} {
		if !BriefHasLiteralOwnedItems("## " + heading + "\n- app.go\n") {
			t.Fatalf("literal title not recognized: %s", heading)
		}
	}
	for _, body := range []string{
		"## Arquivos\n- app.go\n",
		"# Owned\n- app.go\n",
		"# My paths\n- app.go\n",
		"# Arquivos de teste\n- app.go\n",
		"# Owned files\n\n# Report\nDone\n",
		"# Owned files\n- \n* \n+ \n",
		"```\n# Owned files\n- app.go\n```\n",
		"# Owned files\n~~~\napp.go\n~~~\n",
	} {
		if BriefHasLiteralOwnedItems(body) {
			t.Fatalf("false literal ownership: %q", body)
		}
	}
	if BriefHasLiteralOwnedItems("# My paths\n- app.go\n") {
		t.Fatal("a heading the lint does not know as Owned files must not count for the literal signal")
	}
}

// SOH-287: an Owned files section whose every item declares no files is not
// an edit request for the read-only refusal; one other item still is, and the
// edit-role signal (BriefHasOwnedItems) is unchanged.
func TestBriefOwnedSignalReadOnlyNoneDeclaration(t *testing.T) {
	for _, items := range []string{
		"None.", "- None", "* none", "+ NONE", "Nenhum. Só leitura.", "Nenhuma.", "n/a", "N/A", "—", "- —", "nada", "NOTHING: read only", "None: read-only", "-\n- None\n",
	} {
		if BriefHasLiteralOwnedItems("## Owned files\n" + items + "\n") {
			t.Fatalf("none declaration counted as an edit request: %q", items)
		}
		if !BriefHasOwnedItems("## Owned files\n"+items+"\n", nil) {
			t.Fatalf("edit-role signal changed for %q", items)
		}
	}
	for _, items := range []string{
		"nonexistent.go", "`none.txt`", "- internal/x.go", "None\n- internal/x.go", "- internal/x.go\nNone", "nothingness.go", "Nenhumas.go", "- --", "Not applicable",
	} {
		if !BriefHasLiteralOwnedItems("## Owned files\n" + items + "\n") {
			t.Fatalf("owned item ignored: %q", items)
		}
	}
	if BriefHasLiteralOwnedItems("```md\n## Owned files\n- app.go\n```\n## Owned files\nNone.\n") {
		t.Fatal("fenced example counted next to a none declaration")
	}
}
