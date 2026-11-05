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
