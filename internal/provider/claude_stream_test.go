package provider

import "testing"

// TestProviderDetectStreamScanGate proves the gate affects only the built-in
// stream branch: a rejected stream leaves same-line configured matches and
// earlier ordinary lines in the same bounded scan, while an accepted stream
// supplies the fixed visible-frame cause without a new detection field.
func TestProviderDetectStreamScanGate(t *testing.T) {
	const auth = "Error: incorrect API key\n"
	const stream = "Error: The response stopped arriving.\n"
	const fixed = "API Error: The response stopped arriving. The response above may be incomplete."
	for _, tc := range []struct {
		name, screen, configured, gateCause, status, cause string
		auth, generic                                      bool
	}{
		{"generic behavior without a gate", stream, "", "", "provider-error", "Error: The response stopped arriving.", false, true},
		{"accepted signed cause", stream, "", fixed, "provider-error", fixed, false, false},
		{"untrusted stream rejected", stream, "", "", "", "", false, false},
		{"auth before stream log", auth + stream, "", "", "provider-error", "Error: incorrect API key", true, false},
		{"auth after stream log", stream + auth, "", "", "provider-error", "Error: incorrect API key", true, false},
		{"configured stream text on the same line", stream, "The response stopped arriving.", "", "provider-error", "Error: The response stopped arriving.", false, false},
		{"other generic rule on the same line", "Error: The response stopped arriving. connection error\n", "", "", "provider-error", "Error: The response stopped arriving. connection error", false, false},
		{"signed stream newer than capacity", "Error: 529 at capacity\n" + stream, "", fixed, "provider-error", fixed, false, false},
		{"redacted generic cause", "API Error: The response stopped arriving. token=sk_live_fakekey000\n", "", "", "provider-error", "API Error: The response stopped arriving. token=[redacted]", false, true},
		{"output bullet after stream", stream + "● continued output\n", "", "", "", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gate func(string) string
			if !tc.generic {
				gate = func(string) string { return tc.gateCause }
			}
			got := ProviderDetectTextsWithStreamGate("idle", tc.screen, "", tc.configured, gate)
			if tc.status == "" {
				if got != nil {
					t.Fatalf("detection = %#v, want nil", got)
				}
				return
			}
			if got == nil || got.Status != tc.status || got.Cause != tc.cause || got.Auth != tc.auth {
				t.Fatalf("detection = %#v, want status=%q cause=%q auth=%v", got, tc.status, tc.cause, tc.auth)
			}
		})
	}
}

func TestClaudeStreamStopFrame(t *testing.T) {
	const signed = "● API Error: The response stopped arriving. The response above may be incomplete."
	const done = "\n✻ Cogitated for 23m 53s · done 7:12 PM\n❯"
	const cause = "API Error: The response stopped arriving. The response above may be incomplete."
	for _, tc := range []struct {
		name, frame, history, cause string
	}{
		{"signed at empty composer", "● prior output\n\n" + signed + done, "● prior output", cause},
		{"unsigned", "API Error: The response stopped arriving. The response above may be incomplete." + done, "", ""},
		{"quoted", "● explaining: \"" + signed + "\"" + done, "", ""},
		{"fenced output", "~~~text\n" + signed + done, "", ""},
		{"continuing output", signed + "\n● another token" + done, "", ""},
		{"retry active", signed + "\nRetrying in 2 seconds" + done, "", ""},
		{"nonempty composer", signed + done + " draft", "", ""},
		{"no completed-turn footer", signed + "\n❯", "", ""},
		{"active footer", signed + "\n✻ Cogitating… (23m 53s · esc to interrupt)\n❯", "", ""},
		{"no visible composer", signed, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history, detected := ClaudeStreamStop(tc.frame)
			if history != tc.history || detected != tc.cause {
				t.Fatalf("ClaudeStreamStop = (%q, %q), want (%q, %q)", history, detected, tc.history, tc.cause)
			}
		})
	}
}
