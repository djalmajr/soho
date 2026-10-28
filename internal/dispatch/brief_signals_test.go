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
