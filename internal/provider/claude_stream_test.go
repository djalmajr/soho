package provider

import "testing"

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
