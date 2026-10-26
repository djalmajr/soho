package startup

import (
	"encoding/json"
	"testing"
)

func TestGrokAnswerHintActiveDialog(t *testing.T) {
	for _, tc := range []struct{ name, screen, key string }{
		{"bottom", grokTrustScreen, "y"},
		{"composer", grokTrustScreen + "❯ Continue reviewing\n", ""},
		{"later-output", grokTrustScreen + "Build complete\nReview continues\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, option, ok := AnswerHint("grok", tc.screen)
			if key != tc.key || ok != (tc.key != "") || (tc.key == "" && option != "") {
				t.Fatalf("screen=%q key=%q option=%q ok=%v want key=%q", tc.screen, key, option, ok, tc.key)
			}
			t.Logf("key=%q option=%q ok=%v", key, option, ok)
		})
	}
}

func TestStartupTrustAnswerHints(t *testing.T) {
	// Codex menu copied from codexhooks/codexhooks_test.go's menuScreen.
	menu := "  Hooks need review\n  3 hooks are new or changed.\n  Hooks can run outside the sandbox after you trust them.\n\n\n› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)\n\n  enter confirm · esc skip\n"
	for _, tc := range []struct{ name, kind, screen, key, option string }{
		{"cursor", "cursor", nativeTrustScreen(t), "a", "Trust this workspace"},
		{"cursor-macos", "cursor", darwinTrustMcpScreen(t), "a", "Trust this workspace"},
		{"grok", "grok", grokTrustScreen, "y", "Yes, proceed (y)"},
		{"codex-hooks", "codex", menu, "2", "Trust all and continue"},
		{"shell", "cursor", "$ cursor-agent\n", "", ""},
		{"grok-lookalike", "grok", "  Yes, proceed (y)\n  No, keep going (n)\n", "", ""},
		{"mcp-approval", "cursor", "MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\n", "", ""},
		{"no-shortcut", "cursor", "Do you trust the contents of this directory?\nTrust this workspace\nQuit\n", "", ""},
		{"wrong-kind", "claude", nativeTrustScreen(t), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, option, ok := AnswerHint(tc.kind, tc.screen)
			if key != tc.key || option != tc.option || ok != (tc.key != "") {
				t.Fatalf("AnswerHint(%q, %q)=%q,%q,%t; want %q,%q", tc.kind, tc.screen, key, option, ok, tc.key, tc.option)
			}
		})
	}
}

func TestStartupTrustAssessmentNeverHintsMCPApproval(t *testing.T) {
	ev := Evaluate("cursor", "darwin", "idle", nativeTrustScreen(t), true)
	want := `ask the user before answering; to trust it: herdr agent send-keys worker a (answers "Trust this workspace"); then herdr agent wait worker --timeout 60000`
	if hint := ev.TrustHint("worker"); hint != want {
		t.Fatalf("hint=%q want %q", hint, want)
	}
	ev.Reason = "mcp-approval"
	if hint := ev.TrustHint("worker"); hint != "" {
		t.Fatalf("permission approval acquired trust hint: %q", hint)
	}
}

func TestReview322ActiveHintBoundaries(t *testing.T) {
	menu := "Hooks need review\n3 hooks are new or changed.\n› 1. Review hooks\n2. Trust all and continue\n3. Continue without trusting (hooks won't run)\nenter confirm · esc skip\n"
	for _, tc := range []struct{ kind, screen string }{{"cursor", nativeTrustScreen(t)}, {"grok", grokTrustScreen}, {"codex", menu}} {
		t.Run(tc.kind, func(t *testing.T) {
			key, _, ok := AnswerHint(tc.kind, tc.screen)
			if !ok {
				t.Fatal("active fixture not recognized")
			}
			t.Logf("active fixture: kind=%s key=%q", tc.kind, key)
			quoted := "The report quotes this old dialog:\n" + tc.screen + "\n❯ Continue reviewing\n"
			key, option, ok := AnswerHint(tc.kind, quoted)
			t.Logf("quoted dialog above composer: kind=%s key=%q option=%q ok=%v", tc.kind, key, option, ok)
			if ok || key != "" || option != "" {
				t.Fatal("quoted dialog acquired actionable shortcut")
			}
		})
	}
}

func TestReview322HintLeavesEvaluationJSONUnchanged(t *testing.T) {
	ev := Evaluate("cursor", "darwin", "idle", nativeTrustScreen(t), true)
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"Verdict\":\"blocked\",\"Reason\":\"workspace-trust\",\"Source\":\"visible\"}"
	if string(b) != want {
		t.Fatalf("JSON=%s want=%s", b, want)
	}
	t.Logf("evaluation JSON=%s", b)
}
