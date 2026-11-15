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

// TestKindFromProgramHintsReporterApp pins the one reporter-app alias:
// claude-code resolves to claude through the same normalization as the
// catalog names, and through the live hint order a Claude Code terminal
// reports (status app, version-named foreground executable, argv[0]).
func TestKindFromProgramHintsReporterApp(t *testing.T) {
	cases := []struct {
		name  string
		hints []string
	}{
		{"bare", []string{"claude-code"}},
		{"upper case", []string{"Claude-Code"}},
		{"unix path", []string{"/opt/x/claude-code"}},
		{"exe suffix", []string{"claude-code.exe"}},
		{"windows backslash path", []string{"C:\\x\\claude-code.exe"}},
		{"live hint order", []string{"claude-code", "2.1.325", "env"}},
		{"after unknown", []string{"node", "claude-code"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := KindFromProgramHints(tc.hints); got != "claude" {
				t.Fatalf("KindFromProgramHints(%q) = %q, want claude", tc.hints, got)
			}
		})
	}
}

// TestKindFromProgramHintsReporterAppExactOnly proves the alias is an exact
// match on the normalized name: no prefix, suffix, substring, separator
// rewriting or version-named executable resolves.
func TestKindFromProgramHintsReporterAppExactOnly(t *testing.T) {
	for _, hint := range []string{
		"claude-code-x", "claude-codex", "xclaude-code", "claude_code",
		"claude code", "code", "2.1.325", "env", "claude-code/evil",
		"claude-code.sh", "claude-code.exe.exe", "claude-code ",
		" claude-code", "claude-code\n",
	} {
		t.Run(hint, func(t *testing.T) {
			if got := KindFromProgramHints([]string{hint}); got != "" {
				t.Fatalf("KindFromProgramHints(%q) = %q, want the empty kind", hint, got)
			}
		})
	}
	if got := KindFromProgramHints([]string{"2.1.325", "env"}); got != "" {
		t.Fatalf("version-named executable and env resolved to %q, want the empty kind", got)
	}
}

// TestReporterAppAliasTargetsAreKnownKinds fails when an alias value is not
// a catalog kind, and pins the table to its single evidenced entry.
func TestReporterAppAliasTargetsAreKnownKinds(t *testing.T) {
	if len(reporterAppKinds) != 1 || reporterAppKinds["claude-code"] != "claude" {
		t.Fatalf("reporterAppKinds = %v, want exactly claude-code -> claude", reporterAppKinds)
	}
	for app, kind := range reporterAppKinds {
		if app != strings.ToLower(app) {
			t.Errorf("alias key %q is not lower case", app)
		}
		known := false
		for _, k := range KnownKinds {
			known = known || k == kind
		}
		if !known {
			t.Errorf("alias %q -> %q: target is not in KnownKinds", app, kind)
		}
	}
}
