package dispatch

import "testing"

// SOH-317: "Author: orchestrator" marks a review of the orchestrator's code.
func TestDeclaresOrchestratorAuthor(t *testing.T) {
	for body, want := range map[string]bool{
		"# Brief\nAuthor: orchestrator\n":    true,
		"Author: `Orchestrator`\n":           true,
		"Author:   orchestrator  \n":         true,
		"Author: implementer\n":              false,
		"Author: orchestrator-2\n":           false,
		"```\nAuthor: orchestrator\n```\n":   false,
		"Author: me\nAuthor: orchestrator\n": false,
		"no author line\n":                   false,
		"  Author: orchestrator\n":           false,
	} {
		if got := DeclaresOrchestratorAuthor(body); got != want {
			t.Fatalf("%q: got %v want %v", body, got, want)
		}
	}
}
