package core

import (
	"fmt"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// panelLaneRows projects the configured lanes for the SoHo panel. Membership,
// capacity and each attribute come from the same lane helpers the resolution
// and the plugin use; no lane is paired with a role by list position. Lanes
// off is an explicitly empty array, never an omission.
func panelLaneRows(ctx *Config, env platform.Env) []any {
	lanes := []any{}
	if !LanesEnabled(ctx, env) {
		return lanes
	}
	for _, name := range LaneNames(ctx, env) {
		kind := LaneAttr(ctx, name, "kind", nil, env)
		model := LaneAttr(ctx, name, "model", nil, env)
		effort := LaneAttr(ctx, name, "effort", nil, env)
		approvals := LaneAttr(ctx, name, "approvals", nil, env)
		from := func(attr, value string) string {
			if value == "" {
				return "default"
			}
			return CfgSource(ctx, LaneKey(name, attr), env)
		}
		roles := []any{}
		for _, role := range SplitRoles(LaneRolesCSV(ctx, name, env)) {
			roles = append(roles, role)
		}
		lanes = append(lanes, jsonjs.O(
			"name", name,
			"roles", roles,
			"panes", LaneCapacity(ctx, name, env),
			"kind", kind,
			"model", model,
			"effort", effort,
			"approvals", approvals,
			"kind_from", from("kind", kind),
			"model_from", from("model", model),
			"effort_from", from("effort", effort),
		))
	}
	return lanes
}

// panelUnassignedRoles lists, in roles order, the roles with no effective
// lane. The lane comes from each row's resolution, not from a lane loop.
func panelUnassignedRoles(rows []roleTableRow) []any {
	out := []any{}
	for _, row := range rows {
		if row.Res.Lane == "" {
			out = append(out, row.Role)
		}
	}
	return out
}

// CmdRolesPanelJSON prints the panel's roles read on one line. The legacy
// "roles" array is built by the same projection as roles --json; teamExists
// is passed in by the CLI from doctor.ProjectIsFirstRun, the predicate init
// and doctor already use, so there is no second first-run rule here.
func CmdRolesPanelJSON(ctx *Config, env platform.Env, cwd string, teamExists bool) {
	if ctx == nil {
		loaded := LoadConfig(env, cwd)
		ctx = &loaded
	}
	rows := collectRoles(env, cwd)
	fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(
		"roles", rolesJSONArray(rows),
		"lanes", panelLaneRows(ctx, env),
		"unassigned_roles", panelUnassignedRoles(rows),
		"team_exists", teamExists,
		"config_files", jsonjs.O(
			"project", ConfigFileFor("project", env, cwd),
			"user", ConfigFileFor("user", env, cwd),
		),
	)))
}
