package dispatch

import "testing"

// SOH-472: the line helper and the read-only signal must be the same matcher,
// so the refusal cannot name a line that was not counted (or miss one).
func TestBriefFirstLiteralOwnedItem(t *testing.T) {
	for _, tc := range []struct {
		name, body, text string
		line             int
	}{
		{name: "reported", body: "Type: review\n## Owned files\n\n- None.\n\nSem commit.\n\n## Report\nDone.\n", text: "Sem commit.", line: 6},
		{name: "first-of-two", body: "## Owned files\nSem commit.\n- src/app.go\n", text: "Sem commit.", line: 2},
		{name: "skips-none", body: "## Owned files\n- None.\n\n- src/app.go\n", text: "- src/app.go", line: 4},
		{name: "label", body: "## Owned files\nOwned: - src/app.go\n", text: "Owned: - src/app.go", line: 2},
		{name: "crlf", body: "## Owned files\r\n- src/app.go\r\n", text: "- src/app.go", line: 2},
		{name: "fenced", body: "```\n## Owned files\n- src/app.go\n```\n", line: 0},
		{name: "alias-only", body: "## Arquivos\n- src/app.go\n", line: 0},
		{name: "none-only", body: "## Owned files\n- None.\n", line: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, text := BriefFirstLiteralOwnedItem(tc.body)
			if line != tc.line || text != tc.text {
				t.Fatalf("BriefFirstLiteralOwnedItem = (%d, %q), want (%d, %q)", line, text, tc.line, tc.text)
			}
			if got := BriefHasLiteralOwnedItems(tc.body); got != (line > 0) {
				t.Fatalf("BriefHasLiteralOwnedItems = %v, line = %d", got, line)
			}
		})
	}
}
