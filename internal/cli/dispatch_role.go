package cli

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func dispatchSidecarRole(path string) string {
	raw, err := core.ReadScratchReport(path)
	if err != nil {
		return ""
	}
	v, err := jsonjs.Parse(raw)
	if err != nil {
		return ""
	}
	if obj, ok := v.(*jsonjs.Object); ok {
		if role, _ := obj.Get("role"); role != nil {
			if s, ok := role.(string); ok {
				return s
			}
		}
	}
	return ""
}

func openDispatchRole(sd, agent string) string {
	return waitpkg.RecordedTaskRole(sd, agent)
}

func refuseReadOnlyEditBrief(body, brief, agent, role string, explicit bool, ctx *core.Config, env platform.Env, cwd string) {
	if core.RoleIsEdit(role, env, cwd) || !roleDeclaredReadOnly(role, env, cwd) {
		return
	}
	typeValue := dispatch.BriefType(body)
	suggested := "implementer"
	signals := []string{}
	switch typeValue {
	case "mechanical", "backend", "ui", "docs":
		signals = append(signals, "Type: "+typeValue)
		suggested = map[string]string{"mechanical": "tasker", "backend": "implementer", "ui": "designer", "docs": "documenter"}[typeValue]
	}
	aliases, _ := dispatch.ParseBriefLintAliases(core.Cfg(ctx, "brief_lint_aliases", "", env))
	if dispatch.BriefHasOwnedItems(body, aliases) {
		signals = append(signals, "Owned files")
	}
	if len(signals) == 0 {
		return
	}
	wording := "runs"
	if explicit {
		wording = "was given"
	}
	core.DieFriction(fmt.Sprintf("dispatch: brief %s asks for edits (%s) but '%s' %s the read-only role '%s'; nothing was sent. Pass --role %s to give it an edit role, or drop the edit sections from the brief", brief, strings.Join(signals, ", "), agent, wording, role, suggested), 2, frictionLogPath, "dispatch")
}

func amendmentRoleHeader(roleFile, role, agent string) string {
	return fmt.Sprintf("The role instructions below replace the role instructions of your previous dispatch.\n\n# Role: %s\n\nYou are running as the `%s` role, agent name `%s`.\n\n%s\n\n", core.FmGet(roleFile, "name"), role, agent, core.RoleBody(roleFile))
}

// roleDeclaredReadOnly reports a role whose file declares mode: read-only.
// Only that explicit header contradicts an edit brief; a role without a
// declared mode (a custom project role) is not refused.
func roleDeclaredReadOnly(role string, env platform.Env, cwd string) bool {
	file := core.RoleFile(role, env, cwd)
	return file != "" && core.FmGet(file, "mode") == "read-only"
}
