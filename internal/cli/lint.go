package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
)

const lintUsage = "usage: lint <brief.md> [--role <role>] [--agent <name>]"

// lintOwnedFilesRoleHint is appended only to the warning line of a lint run
// that omitted --role, so the hint echoes the path exactly as the user typed it.
const lintOwnedFilesRoleHint = "; a read-only role needs none: soho lint %s --role <role>"

func cmdLint(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	brief := ""
	role, agent := "implementer", ""
	explicitRole, explicitAgent := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--role", "--agent":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				platform.DieFriction(lintUsage, 2)
			}
			duplicate := false
			if arg == "--role" {
				duplicate, explicitRole = explicitRole, true
				role = args[i+1]
			} else {
				duplicate, explicitAgent = explicitAgent, true
				agent = args[i+1]
			}
			if duplicate {
				platform.DieFriction(lintUsage, 2)
			}
			i++
		default:
			if strings.HasPrefix(arg, "--") || brief != "" {
				platform.DieFriction(lintUsage, 2)
			}
			brief = arg
		}
	}
	if brief == "" {
		platform.DieFriction(lintUsage, 2)
	}
	info, err := os.Stat(brief)
	if err != nil || !info.Mode().IsRegular() {
		platform.DieFriction(fmt.Sprintf("lint: brief not found: %s", brief), 2)
	}
	if core.RoleFile(role, env, cwd) == "" {
		platform.DieFriction(fmt.Sprintf("lint: unknown role '%s'", role)+core.UnknownRoleHint(role, env, cwd), 3)
	}
	// With --role, lint reports the conflict the dispatch would refuse: a
	// read-only role and a brief that asks for edits. Without --role the
	// default edit role would make the check fire on every edit brief.
	if explicitRole && !core.RoleIsEdit(role, env, cwd) && roleDeclaredReadOnly(role, env, cwd) {
		if body, err := os.ReadFile(brief); err == nil {
			if signals, suggested, remedy := readOnlyEditSignals(string(body)); len(signals) > 0 {
				platform.DieFriction(fmt.Sprintf("lint: brief %s asks for edits (%s) but '%s' is a read-only role; the dispatch would refuse it. Lint it with --role %s to check it as an edit role, %s", brief, strings.Join(signals, ", "), role, suggested, remedy), 2)
			}
		}
	}
	options := dispatch.BriefLintOptions{ReadOnly: !core.RoleIsEdit(role, env, cwd)}
	if explicitAgent {
		// A passive reader never creates a roster, pointer or state dir.
		sd := core.StateDirForRead(ctx, env, cwd)
		line := core.RosterLine(sd, agent)
		if line == "" {
			platform.DieFriction(fmt.Sprintf("lint: no agent '%s' in the roster", text.QuoteControls(agent)), 3)
		}
		workerCwd := ""
		if fields := strings.Split(line, "\t"); len(fields) > 6 {
			workerCwd = fields[6]
		}
		body, readErr := os.ReadFile(brief)
		if readErr != nil {
			body = nil
		}
		options = dispatchLintOptions(string(body), workerCwd, cwd, sd, agent, role, ctx, env)
	} else {
		options.LintInheritHintPath = brief
	}
	result := dispatch.BriefLintFindings(brief, ctx, env, options)
	code := 0
	if result.Mode == "off" {
		fmt.Fprintf(platform.Stdout, "brief %s: lint off (brief_lint=off)\n", brief)
	} else {
		for _, warning := range result.Warnings {
			fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", warning)
		}
		if result.MissingMessage != "" && result.Mode == "strict" {
			fmt.Fprintf(platform.Stderr, "soho: %s (brief_lint=strict)\n", result.MissingMessage)
			code = 2
		} else if result.MissingMessage != "" {
			message := result.MissingMessage
			if !explicitRole && result.MissingOwnedFiles {
				message += fmt.Sprintf(lintOwnedFilesRoleHint, brief)
			}
			fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", message)
			code = 1
		} else if len(result.Warnings) > 0 {
			code = 1
		} else {
			fmt.Fprintf(platform.Stdout, "brief %s: ok\n", brief)
		}
	}
	// The Worktree: line feeds review author measurement: a review-role
	// brief without it gets the advisory, which never refuses and never
	// changes lint's exit code.
	if message := lintWorktreeWarning(brief, role); message != "" {
		fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", message)
	}
	return code
}

func lintWorktreeWarning(brief, role string) string {
	if !core.IsReviewRole(role) {
		return ""
	}
	body, err := os.ReadFile(brief)
	if err != nil {
		return ""
	}
	if dispatch.DeclaredWorktree(string(body)) != "" || dispatch.DeclaresOrchestratorAuthor(string(body)) {
		return ""
	}
	return reviewWorktreeMessage(brief)
}
