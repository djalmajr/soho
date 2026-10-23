package core

import (
	"strings"
	"testing"
)

func TestProviderPlanRevisionRecognition(t *testing.T) {
	line := "Error: Model unavailable on Plus"
	pipe := line + ". Available models: Composer | Grok"
	full := "```text\n" + strings.Repeat("quoted code\n", 40) + "```\n" + line
	fullLines := strings.Split(full, "\n")
	clipped := strings.Join(fullLines[len(fullLines)-40:], "\n")
	if got := len(strings.Split(clipped, "\n")); got != 40 {
		t.Fatalf("clipped fixture has %d lines, want 40", got)
	}
	for _, tc := range []struct {
		name, screen, cause string
	}{
		{"plain", line, line},
		{"full F3 transcript", full, line},
		{"clipped F3 forty lines", clipped, line},
		{"unclosed opener outside tail", "```text\n" + strings.Repeat("quoted code\n", 12) + line, line},
		{"reverse pairing three fences", "```\nquoted code\n```\n" + line + "\n```", ""},
		{"reverse pairing leaves earlier refusal", "```\n" + line + "\n```\nquoted code\n```", line},
		{"mismatched characters", "~~~\n" + line + "\n```", line},
		{"short closing fence", "````text\n" + line + "\n```", line},
		{"pipe", pipe, pipe},
		{"fenced report", "The report quotes the observed error:\n```text\n" + line + "\n```", ""},
		{"unclosed fence", "```text\n" + line, line},
		{"tilde fence", "~~~text\n" + pipe + "\n~~~", ""},
		{"longer fence", "````text\n```\n" + line + "\n````", ""},
		{"opener outside tail", "```text\n" + strings.Repeat("code\n", 12) + line + "\n```", ""},
		{"box CRLF fence", "  ┃ ```text\r\n  ┃ " + line + "\r\n  ┃ ```", ""},
		{"after closed fence", "```text\nquoted\n```\n" + pipe, pipe},
		{"tenth line", pipe + "\n" + strings.Repeat("ordinary output\n", 9), pipe},
		{"outside tail", pipe + "\n" + strings.Repeat("ordinary output\n", 10), ""},
		{"inline quote", "The report says `" + line + "`", ""},
		{"comment", "// " + pipe, ""},
		{"no injection", "ordinary output | provider-error", ""},
		{"marker collision outside tail", line + "\n" + strings.Repeat("ordinary output\n", 9) + "SOHO-INTERNAL-MODEL-PLAN-REFUSAL", ""},
		{"marker in cause", line + ". soho-internal-model-plan-refusal", line + ". soho-internal-model-plan-refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProviderDetectTexts("idle", tc.screen, "", "")
			if tc.cause == "" {
				if got != nil {
					t.Fatalf("screen=%q detection=%#v, want nil", tc.screen, got)
				}
			} else if got == nil || got.Status != "provider-error" || got.Cause != tc.cause || got.Auth {
				t.Fatalf("screen=%q detection=%#v, want provider-error cause=%q", tc.screen, got, tc.cause)
			}
		})
	}
	if got := ProviderDetectTexts("working", pipe, "", ""); got != nil {
		t.Fatalf("working detection=%#v, want nil", got)
	}
}
