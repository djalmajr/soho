package kinds

import (
	"strings"
	"testing"
)

// TestKindFromProgramHintsCatalog pins every catalog kind and executable:
// each one resolves through its own name, its executable name, each
// recognized suffix, folded case, and both path separator forms.
func TestKindFromProgramHintsCatalog(t *testing.T) {
	for _, kind := range KnownKinds {
		exe := KindExe(kind)
		cases := map[string][]string{
			"bare kind":         {kind},
			"bare executable":   {exe},
			"exe suffix":        {exe + ".exe"},
			"cmd suffix":        {exe + ".cmd"},
			"bat suffix":        {exe + ".bat"},
			"upper exe":         {strings.ToUpper(exe + ".EXE")},
			"unix path":         {"/usr/local/bin/" + exe},
			"windows backslash": {"C:\\" + "\\Program Files\\" + strings.ToUpper(exe) + ".exe"},
			"windows slash":     {"C:/Program Files/" + exe + ".CMD"},
			"after unknown":     {"node.exe", exe + ".exe"},
		}
		for name, hints := range cases {
			t.Run(kind+"/"+name, func(t *testing.T) {
				if got := KindFromProgramHints(hints); got != kind {
					t.Fatalf("KindFromProgramHints(%q) = %q, want %q", hints, got, kind)
				}
			})
		}
	}
}

func TestKindFromProgramHintsUnknown(t *testing.T) {
	cases := []struct {
		name  string
		hints []string
	}{
		{"no hints", nil},
		{"empty hint", []string{""}},
		{"node alone", []string{"node"}},
		{"node exe alone", []string{"node.exe"}},
		{"shell command unparsed", []string{"codex --flag"}},
		{"flag only", []string{"--flag"}},
		{"model string", []string{"claude-3-opus"}},
		{"model id", []string{"gpt-5.2"}},
		{"versioned model", []string{"claude-3.5-sonnet"}},
		{"model path", []string{"anthropic/claude-opus-4"}},
		{"unknown suffix not removed", []string{"claude.sh"}},
		{"bare exe suffix", []string{".exe"}},
		{"double suffix not removed twice", []string{"cursor-agent.exe.exe"}},
		{"near miss", []string{"claude2"}},
		{"windows root", []string{"C:\\"}},
		{"separator only", []string{"/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := KindFromProgramHints(tc.hints); got != "" {
				t.Fatalf("KindFromProgramHints(%q) = %q, want the empty kind", tc.hints, got)
			}
		})
	}
}

func TestKindFromProgramHintsPriority(t *testing.T) {
	// The first recognized hint in source order wins.
	if got := KindFromProgramHints([]string{"node", "cursor-agent", "codex"}); got != "cursor" {
		t.Fatalf("priority got %q, want cursor", got)
	}
	if got := KindFromProgramHints([]string{"codex", "cursor-agent"}); got != "codex" {
		t.Fatalf("priority got %q, want codex", got)
	}
	if got := KindFromProgramHints([]string{"unknown", "", "pi"}); got != "pi" {
		t.Fatalf("priority got %q, want pi", got)
	}
	// The cursor executable resolves to the cursor kind, never to another.
	if got := KindFromProgramHints([]string{"cursor-agent"}); got != "cursor" {
		t.Fatalf("cursor executable got %q, want cursor", got)
	}
	// A later recognized hint still wins once the earlier ones are unknown.
	if got := KindFromProgramHints([]string{"--flag", "pi"}); got != "pi" {
		t.Fatalf("later hint got %q, want pi", got)
	}
	// No model, name or role inference: a kind never comes from a model id.
	if got := KindFromProgramHints([]string{"anthropic/claude-opus-4"}); got != "" {
		t.Fatalf("model id got %q, want the empty kind", got)
	}
}
