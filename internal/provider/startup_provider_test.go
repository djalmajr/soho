package provider

import "testing"

// The two narrow startup causes are deliberately CLI-specific: a quoted copy,
// a partial pair or a healthy screen must stay provider-neutral. The shared
// working-state gate from #357 remains unchanged.
func TestNarrowStartupFailures(t *testing.T) {
	const agy = "⚠ failed to construct executor: plan model not specified; you must specify a valid model\n" +
		"⚠ Warning\n" +
		"  ⎿  model claude-sonnet-5-5-medium is not recognized as a known model or custom model in settings. Ignoring the flag.\n"
	const codex = "■ Failed to start turn: turn/start failed in TUI: turn/start failed: failed to load configuration:\n" +
		"invalid transport\n" +
		"in `mcp_servers.old-server`\n" +
		" (code -32600)\n"

	for _, tc := range []struct {
		name, kind, screen, cause string
	}{
		{name: "agy exact", screen: agy, cause: AgyModelCause, kind: "agy"},
		{name: "codex exact", screen: codex, cause: CodexConfigCause, kind: "codex"},
		{name: "agy quoted", screen: "```text\n" + agy + "```\n", kind: "agy"},
		{name: "codex quoted", screen: "```text\n" + codex + "```\n", kind: "codex"},
		{name: "agy partial", screen: "⚠ failed to construct executor: plan model not specified; you must specify a valid model\n", kind: "agy"},
		{name: "codex partial", screen: "■ Failed to start turn: turn/start failed in TUI: turn/start failed: failed to load configuration:\ninvalid transport\n", kind: "codex"},
		{name: "agy unrelated", screen: "⚠ Error compacting conversation\n", kind: "agy"},
		{name: "codex healthy", screen: "■ model sol · 32% left\n> ", kind: "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StartupProviderStop(tc.kind, "idle", tc.screen)
			if tc.cause == "" {
				if got != nil {
					t.Fatalf("detection=%#v, want none", got)
				}
				return
			}
			if got == nil || got.Status != "provider-error" || got.Cause != tc.cause || got.Auth {
				t.Fatalf("detection=%#v, want provider-error cause %q without auth", got, tc.cause)
			}
			shared := ProviderDetectTexts("idle", tc.screen, "", "")
			if shared != nil {
				t.Fatalf("kind-free detection=%#v, want none", shared)
			}
			if working := StartupProviderStop(tc.kind, "working", tc.screen); working != nil {
				t.Fatalf("working-state detection=%#v, want the #357 gate to stay quiet", working)
			}
		})
	}
}
