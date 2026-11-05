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
	// DJA-694: the read-only refusal counts only the literal "Owned files"
	// title - a scope list under a synonym like "Arquivos" is not an edit
	// request. Edit roles keep counting the synonyms (ownedPaths, section
	// lint), which is why this signal uses the literal matcher instead of
	// BriefHasOwnedItems.
	if dispatch.BriefHasLiteralOwnedItems(body) {
		signals = append(signals, "Owned files")
	}
	if len(signals) == 0 {
		return
	}
	wording := "runs"
	if explicit {
		wording = "was given"
	}
	// Renaming a title cannot lift a refusal the Type line caused.
	remedy := "or rename the 'Owned files' title, or drop the edit sections from the brief"
	if signals[0] != "Owned files" {
		remedy = "or drop the '" + signals[0] + "' line and the edit sections from the brief"
	}
	core.DieFriction(fmt.Sprintf("dispatch: brief %s asks for edits (%s) but '%s' %s the read-only role '%s'; nothing was sent. Pass --role %s to give it an edit role, %s", brief, strings.Join(signals, ", "), agent, wording, role, suggested, remedy), 2, frictionLogPath, "dispatch")
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

// warnSecurityBriefOnBuildLane is advisory and never blocks: a brief that
// declares Type: security routed to a build-lane worker points at the
// critical lane. It reads only the declared Type value (never free text),
// only the roster's lane column, only for the security type, and it stays
// silent when the critical lane has no capacity (strict panes=2 has no
// critical lane, flex panes=2 has one at capacity 0, and a custom team may
// have none; lanes=off rows carry no lane).
func warnSecurityBriefOnBuildLane(body, brief, agent, lane string, ctx *core.Config, env platform.Env) {
	if dispatch.BriefType(body) != "security" || lane != "build" {
		return
	}
	if !core.LaneUsable(ctx, "critical", env) {
		return
	}
	core.Warn(fmt.Sprintf("brief %s declares Type: security but '%s' is in lane build: security work goes to the critical lane (spawn critical-implementer)", brief, agent), frictionLogPath, "dispatch")
}
