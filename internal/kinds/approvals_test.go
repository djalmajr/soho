package kinds

import (
	"reflect"
	"testing"
)

func TestKindUnsandboxedApprovalArgs(t *testing.T) {
	full := map[string][]string{
		"codex":    {"-s", "workspace-write", "-a", "never"},
		"claude":   {"--permission-mode", "bypassPermissions", "--settings", `{"enableAllProjectMcpServers":true}`},
		"grok":     {"--permission-mode", "bypassPermissions", "--always-approve"},
		"cursor":   {"--trust", "--force", "--approve-mcps"},
		"agy":      {"--dangerously-skip-permissions"},
		"gemini":   {"--dangerously-skip-permissions"},
		"opencode": {"--auto"}, "pi": nil,
	}
	edits := map[string][]string{
		"claude": {"--permission-mode", "acceptEdits"},
		"codex":  {"-s", "workspace-write", "-a", "on-request"},
		"grok":   {"--permission-mode", "acceptEdits"},
		"agy":    {"--mode", "accept-edits"}, "gemini": {"--mode", "accept-edits"},
		"cursor": {"--trust", "--auto-review"}, "pi": nil, "opencode": nil,
	}
	auto := map[string][]string{
		"claude": {"--permission-mode", "auto"}, "grok": {"--permission-mode", "auto"},
		"codex": {"--approve-for-me"}, "cursor": {"--trust", "--auto-review"},
		"agy": {"--mode", "auto"}, "opencode": {"--auto"}, "pi": nil,
		"gemini": {"--mode", "accept-edits"},
	}
	for kind, args := range full {
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []string{"ask", "edits", "auto", "full", "unsandboxed"} {
				want, warning := args, ""
				if mode == "ask" {
					want = nil
				}
				if mode == "auto" {
					want = auto[kind]
					if kind == "gemini" {
						warning = "gemini has no verified auto mode; approvals=auto runs as edits"
					}
				}
				if mode == "edits" {
					want = edits[kind]
					switch kind {
					case "pi":
						warning = "pi has no approval prompts (its tools run as-is); approvals=edits is a no-op (restrict tools with --tools/--exclude-tools after --)"
					case "opencode":
						warning = "opencode has no edits approvals flag; use approvals=full (--auto) or per-tool permissions in opencode.json"
					}
				}
				if mode == "unsandboxed" {
					switch kind {
					case "codex":
						want = []string{"-s", "danger-full-access", "-a", "never"}
					case "cursor":
						want = []string{"--trust", "--force", "--approve-mcps", "--sandbox", "disabled"}
					case "claude":
					default:
						warning = kind + " has no separate sandbox switch; approvals=unsandboxed runs as full"
					}
				}
				var warnings []string
				got, err := KindApprovalArgs(kind, mode, func(s string) { warnings = append(warnings, s) })
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s args=%q want=%q err=%v", mode, got, want, err)
				}
				if warning == "" && len(warnings) != 0 || warning != "" && !reflect.DeepEqual(warnings, []string{warning}) {
					t.Fatalf("%s warnings=%q want=%q", mode, warnings, warning)
				}
			}
		})
	}
}
