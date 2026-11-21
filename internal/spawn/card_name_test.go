package spawn

import (
	"fmt"
	"strings"
	"testing"
)

func TestCardAgentName(t *testing.T) {
	cases := []struct {
		card, role, want, wantErr string
	}{
		{card: "SOH-427", role: "implementer", want: "soh427-impl"},
		{card: "KAS-309", role: "reviewer", want: "kas309-rev"},
		{card: "soh-427", role: "implementer", want: "soh427-impl"},
		{card: "SOH-427!", role: "implementer", want: "soh427-impl"},
		{card: "", role: "implementer", wantErr: "spawn: --card '' gives no usable name; expected a card key such as SOH-427"},
		{card: "427-SOH", role: "implementer", wantErr: "spawn: --card '427-SOH' gives no usable name; expected a card key such as SOH-427"},
		{card: "!!!", role: "implementer", wantErr: "spawn: --card '!!!' gives no usable name; expected a card key such as SOH-427"},
		{card: "SOH-427", role: "worker", want: "soh427-worker"},
		{card: "SOH-427", role: "critical-implementer", want: "soh427-crit"},
		{card: "SOH-427", role: "security-reviewer", want: "soh427-sec"},
		{card: "SOH-427", role: "scouter", want: "soh427-scout"},
		{card: "SOH-427", role: "researcher", want: "soh427-res"},
		{card: "SOH-427", role: "tester", want: "soh427-test"},
		{card: "SOH-427", role: "documenter", want: "soh427-doc"},
		{card: "SOH-427", role: "designer", want: "soh427-ui"},
		{card: "SOH-427", role: "ui-reviewer", want: "soh427-ui"},
		{card: "SOH-427", role: "validator", want: "soh427-val"},
		{card: "SOH-427", role: "tasker", want: "soh427-task"},
		{card: "SOH-427", role: "inspector", want: "soh427-insp"},
		{card: "SOH-427", role: "sub-orchestrator", want: "soh427-orch"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%s", tc.card, tc.role), func(t *testing.T) {
			got, err := CardAgentName(tc.card, tc.role)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err=%v, want %q", err, tc.wantErr)
				}
				if got != "" {
					t.Fatalf("name=%q, want empty on error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("CardAgentName(%q, %q)=%q, %v; want %q", tc.card, tc.role, got, err, tc.want)
			}
		})
	}
}

func TestNameOutsidePattern(t *testing.T) {
	for _, name := range []string{"soh427-impl", "kas309-rev", "soh427-impl-2", "s1-impl-12"} {
		if got := NameOutsidePattern(name); got != "" {
			t.Errorf("NameOutsidePattern(%q)=%q, want silent", name, got)
		}
	}
	cases := map[string]string{
		"build":     fmt.Sprintf("spawn: agent name 'build' is not <subject>-<function>[-<n>] (for example soh427-impl); pass --card <KEY> to derive it"),
		"review":    fmt.Sprintf("spawn: agent name 'review' is not <subject>-<function>[-<n>] (for example soh427-impl); pass --card <KEY> to derive it"),
		"my-worker": fmt.Sprintf("spawn: agent name 'my-worker' is not <subject>-<function>[-<n>] (for example soh427-impl); pass --card <KEY> to derive it"),
		"wake-x":    "spawn: agent name 'wake-x' uses the reserved prefix 'wake-' (soho's own wake and helper terminals); name workers <subject>-<function>, for example soh427-impl, or pass --card <KEY>",
		"soho-x":    "spawn: agent name 'soho-x' uses the reserved prefix 'soho-' (soho's own wake and helper terminals); name workers <subject>-<function>, for example soh427-impl, or pass --card <KEY>",
	}
	for name, want := range cases {
		got := NameOutsidePattern(name)
		if got != want {
			t.Errorf("NameOutsidePattern(%q)=%q, want %q", name, got, want)
		}
		if !strings.Contains(got, name) {
			t.Errorf("NameOutsidePattern(%q) does not name the agent: %q", name, got)
		}
	}
}
