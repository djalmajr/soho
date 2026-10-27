package cli

import (
	"fmt"
	"os"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
)

const lintUsage = "usage: lint <brief.md> [--role <role>]"

func cmdLint(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	brief := ""
	if len(args) > 0 {
		brief = args[0]
	}
	if brief == "" || len(brief) >= 2 && brief[:2] == "--" {
		platform.DieFriction(lintUsage, 2)
	}
	role := "implementer"
	if len(args) == 3 && args[1] == "--role" && args[2] != "" && !(len(args[2]) >= 2 && args[2][:2] == "--") {
		role = args[2]
	} else if len(args) != 1 {
		platform.DieFriction(lintUsage, 2)
	}
	info, err := os.Stat(brief)
	if err != nil || !info.Mode().IsRegular() {
		platform.DieFriction(fmt.Sprintf("lint: brief not found: %s", brief), 2)
	}
	if core.RoleFile(role, env, cwd) == "" {
		platform.DieFriction(fmt.Sprintf("lint: unknown role '%s'", role)+core.UnknownRoleHint(role, env, cwd), 3)
	}
	result := dispatch.BriefLintFindings(brief, ctx, env, dispatch.BriefLintOptions{ReadOnly: !core.RoleIsEdit(role, env, cwd)})
	if result.Mode == "off" {
		fmt.Fprintf(platform.Stdout, "brief %s: lint off (brief_lint=off)\n", brief)
		return 0
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", warning)
	}
	if result.MissingMessage != "" && result.Mode == "strict" {
		fmt.Fprintf(platform.Stderr, "soho: %s (brief_lint=strict)\n", result.MissingMessage)
		return 2
	}
	if result.MissingMessage != "" {
		fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", result.MissingMessage)
	}
	if len(result.Warnings) > 0 || result.MissingMessage != "" {
		return 1
	}
	fmt.Fprintf(platform.Stdout, "brief %s: ok\n", brief)
	return 0
}
