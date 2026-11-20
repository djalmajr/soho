package dispatch

// Review round 2 (forms-r-20261119T180825 F2): the audit reads the same
// positive and negative section labels the lint reads. This keeps the two
// vocabularies from drifting apart.

import (
	"testing"

	"github.com/djalmajr/soho/internal/gittree"
)

func TestOwnedItemLabelVocabularyMatchesLint(t *testing.T) {
	for _, line := range []string{
		"- Forbidden: `src/a.ts`",
		"- Proibido: `src/a.ts`",
		"- Proibidos: `src/a.ts`",
		"- NÃO TOCAR: `src/a.ts`",
		"- Do not touch: `src/a.ts`",
		"- Fora do escopo: `src/a.ts`",
		"- Out of scope: `src/a.ts`",
		"* Owned: src/a.ts",
		"1. Owned: src/a.ts",
		"- **Owned:** src/a.ts",
		"- May edit: src/a.ts",
		"- Pode tocar: src/a.ts",
		"- Arquivos próprios: src/a.ts",
		"- Note: src/a.ts",
		"- src/a.ts",
		"Forbidden: `src/a.ts`",
	} {
		negative, positive, _ := ownedLabel(line)
		auditNegative, auditPositive := gittree.OwnedItemLabel(line)
		if negative != auditNegative || positive != auditPositive {
			t.Errorf("%q: lint=(negative %t, positive %t), audit=(negative %t, positive %t)", line, negative, positive, auditNegative, auditPositive)
		}
	}
}
