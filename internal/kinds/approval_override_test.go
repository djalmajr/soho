package kinds

import "testing"

func TestCodexApprovalOverrideFlag(t *testing.T) {
	for _, flag := range []string{"-s", "--sandbox", "-a", "--ask-for-approval", "--dangerously-bypass-approvals-and-sandbox", "--full-auto", "--approve-for-me"} {
		t.Run(flag, func(t *testing.T) {
			if got := CodexApprovalOverrideFlag([]string{"--verbose", flag, "value"}); got != flag {
				t.Fatalf("flag=%q got=%q", flag, got)
			}
		})
		if len(flag) > 2 {
			t.Run(flag+"=", func(t *testing.T) {
				for _, value := range []string{"", "workspace-write", "never", "false"} {
					if got := CodexApprovalOverrideFlag([]string{flag + "=" + value}); got != flag {
						t.Fatalf("arg=%q got=%q", flag+"="+value, got)
					}
				}
			})
		}
	}
	for _, args := range [][]string{nil, {}, {"--verbose", "value"}, {"sandbox", "danger-full-access", "never"}, {"prefix--sandbox", "--sandbox-extra", "--full-automatic", "--approve-for-me-extra"}, {"-s=workspace-write", "-a=never", "-sfoo", "-abc"}, {"text --sandbox=value", "--prompt=--full-auto"}} {
		if got := CodexApprovalOverrideFlag(args); got != "" {
			t.Fatalf("non-flags=%q got=%q", args, got)
		}
	}
	if got := CodexApprovalOverrideFlag([]string{"--full-auto", "-s", "workspace-write"}); got != "--full-auto" {
		t.Fatalf("first flag=%q", got)
	}
}
