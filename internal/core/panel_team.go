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
		rolesCSV := SplitRoles(LaneRolesCSV(ctx, name, env))
		kind, kindFrom := panelLaneAttr(ctx, name, "kind", nil, env)
		model, modelFrom := panelLaneLayerAttr(ctx, name, "model", rolesCSV, env)
		effort, effortFrom := panelLaneLayerAttr(ctx, name, "effort", rolesCSV, env)
		approvals, _ := panelLaneAttr(ctx, name, "approvals", nil, env)
		roles := []any{}
		for _, role := range rolesCSV {
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
			"kind_from", kindFrom,
			"model_from", modelFrom,
			"effort_from", effortFrom,
		))
	}
	return lanes
}

// panelLaneAttr is one lane-level attribute with the source the panel shows:
// the value the lane sets, or ""/"default" when it sets none.
func panelLaneAttr(ctx *Config, lane, attr string, kindLayer *int, env platform.Env) (any, string) {
	value := LaneAttr(ctx, lane, attr, kindLayer, env)
	if value == "" {
		return "", "default"
	}
	return value, CfgSource(ctx, LaneKey(lane, attr), env)
}

// panelLaneLayerAttr projects a lane's model or effort exactly as the resolver
// applies it to each member: LaneAttr filtered by that member's kind layer,
// which is what spawn passes through ResolveRoleSettings (F11). A lane whose
// members receive different values announces no single value: the field is
// null with a "mixed" source, and each member's own value stays in its role
// row.
func panelLaneLayerAttr(ctx *Config, lane, attr string, roles []string, env platform.Env) (any, string) {
	if len(roles) == 0 {
		return panelLaneAttr(ctx, lane, attr, nil, env)
	}
	valueOf := func(role string) string {
		kindLayer := SpawnKindLayer(ctx, role, lane, false, env)
		return LaneAttr(ctx, lane, attr, &kindLayer, env)
	}
	first := valueOf(roles[0])
	for _, role := range roles[1:] {
		if valueOf(role) != first {
			return nil, "mixed"
		}
	}
	if first == "" {
		return "", "default"
	}
	return first, CfgSource(ctx, LaneKey(lane, attr), env)
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
