package provider

import (
	"os"
	"strings"
	"testing"
)

func TestProviderRefusal(t *testing.T) {
	// The cybersecurity fixture is reconstructed from the two reported lines.
	refusal, err := os.ReadFile("testdata/codex-cyber-refusal.txt")
	if err != nil {
		t.Fatal(err)
	}
	reconnect, err := os.ReadFile("testdata/codex-reconnect-failed.txt")
	if err != nil {
		t.Fatal(err)
	}
	phrase := "We take extra care with some cybersecurity requests"
	for _, tc := range []struct{ name, state, screen, status string }{
		{"two lines", "idle", string(refusal), "provider-refusal"},
		{"second only", "idle", "  ┃ ■ " + phrase, "provider-refusal"},
		{"case insensitive", "idle", strings.ToUpper(phrase) + ". More information", "provider-refusal"},
		{"first only", "idle", "■ This content can't be shown", ""},
		{"curly first only", "idle", "■ This content can’t be shown", ""},
		{"quoted bullet", "idle", "• the screen said \"" + phrase + "\"", ""},
		{"working refusal", "working", string(refusal), ""},
		{"outside tail", "idle", phrase + "\n" + strings.Repeat("ordinary output\n", 10), ""},
		{"reconnect working", "working", string(reconnect), "provider-error"},
		{"reconnect idle", "idle", string(reconnect), "provider-error"},
		{"automatic only", "working", "  ■ AUTOMATIC RECONNECT COULD NOT RESTORE THIS SESSION. Your draft is", "provider-error"},
		{"server only", "working", "■ Server connection could not be restored", "provider-error"},
		{"reconnect quoted", "working", "• the screen said \"Server connection could not be restored\"", ""},
		{"automatic quoted", "idle", "• the screen said \"Automatic reconnect could not restore this session\"", ""},
		{"reconnect outside tail", "working", "■ Server connection could not be restored\n" + strings.Repeat("ordinary output\n", 10), ""},
		{"ordinary retry", "working", "Working\nReconnecting… 2/5", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ProviderDetectTexts(tc.state, tc.screen, "", "")
			if tc.status == "" {
				if d != nil {
					t.Fatalf("unexpected detection: %#v", d)
				}
				return
			}
			if d == nil || d.Status != tc.status {
				t.Fatalf("detection=%#v want %s", d, tc.status)
			}
			if tc.status == "provider-error" && d.Cause != LostConnectionCause {
				t.Fatalf("cause=%q", d.Cause)
			}
			if tc.status == "provider-refusal" && !strings.HasPrefix(strings.ToLower(d.Cause), strings.ToLower(phrase)) {
				t.Fatalf("cause=%q", d.Cause)
			}
		})
	}
	if RetryLine("working", "Reconnecting… 2/5") == "" {
		t.Fatal("retry rule changed")
	}
}
