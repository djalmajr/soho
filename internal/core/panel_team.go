package core

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// panelLaneRows projects the configured lanes for the SoHo panel. Membership,
// capacity and each attribute come from the same lane helpers the resolution
// and the plugin use; no lane is paired with a role by list position. Lanes
// off is an explicitly empty array, never an omission.
//
// The kind, model and effort are the values the lane's members really receive,
// resolved with spawn's own per-member resolver (ResolveRoleSettings): a field
// the members resolve differently is null with a "mixed" source, and a field
// they agree on shows that value (F4).
func panelLaneRows(ctx *Config, env platform.Env, cwd string) []any {
	lanes := []any{}
	if !LanesEnabled(ctx, env) {
		return lanes
	}
	for _, name := range LaneNames(ctx, env) {
		rolesCSV := SplitRoles(LaneRolesCSV(ctx, name, env))
		members := make([]RoleSettings, 0, len(rolesCSV))
		for _, role := range rolesCSV {
			members = append(members, ResolveRoleSettings(role, ctx, env, cwd, RoleFlags{}))
		}
		kind, kindFrom := panelLaneField(ctx, name, "kind", rolesCSV, members, env)
		model, modelFrom := panelLaneField(ctx, name, "model", rolesCSV, members, env)
		effort, effortFrom := panelLaneField(ctx, name, "effort", rolesCSV, members, env)
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

// panelLaneField is one lane-level field the panel shows as the members'
// resolved value: null with "mixed" when the members resolve it differently.
// When they agree, the source is the lane override's when the lane override is
// what every member receives, else the members' own source (their common
// layer; a lane override that sets the displayed value for some member stands
// for it when the members' layers differ). A lane with no member keeps its
// own override row.
func panelLaneField(ctx *Config, lane, attr string, roles []string, members []RoleSettings, env platform.Env) (any, string) {
	if len(members) == 0 {
		return panelLaneAttr(ctx, lane, attr, nil, env)
	}
	valueOf := func(res RoleSettings) (string, string) {
		switch attr {
		case "kind":
			return res.Kind, res.KindFrom
		case "model":
			return res.ModelSpec, res.ModelFrom
		case "effort":
			return res.Effort, res.EffortFrom
		}
		return "", ""
	}
	first, firstFrom := valueOf(members[0])
	fromLane := strings.HasPrefix(firstFrom, "lane "+lane+" (")
	for _, res := range members[1:] {
		value, from := valueOf(res)
		if value != first {
			return nil, "mixed"
		}
		if !strings.HasPrefix(from, "lane "+lane+" (") {
			fromLane = false
		}
	}
	if fromLane {
		return first, CfgSource(ctx, LaneKey(lane, attr), env)
	}
	source := panelSourceLayer(firstFrom)
	common := true
	laneSets := false
	for _, res := range members {
		_, from := valueOf(res)
		if layer := panelSourceLayer(from); layer != source {
			common = false
		}
		if strings.HasPrefix(from, "lane "+lane+" (") {
			laneSets = true
		}
	}
	if !common && laneSets {
		source = CfgSource(ctx, LaneKey(lane, attr), env)
	}
	return first, source
}

// panelSourceLayer is the config layer a resolver "from" label names:
// "lane build (project)" and "role config (project)" are "project",
// "model.codex.worker (defaults)" is "defaults", and a label without a
// parenthesized layer ("role file", "default", "flag") is the layer itself.
func panelSourceLayer(from string) string {
	if i := strings.LastIndex(from, "("); i >= 0 && strings.HasSuffix(from, ")") && i+1 < len(from)-1 {
		return from[i+1 : len(from)-1]
	}
	if from == "" {
		return "default"
	}
	return from
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
		"lanes", panelLaneRows(ctx, env, cwd),
		"unassigned_roles", panelUnassignedRoles(rows),
		"team_exists", teamExists,
		"config_files", jsonjs.O(
			"project", ConfigFileFor("project", env, cwd),
			"user", ConfigFileFor("user", env, cwd),
		),
	)))
}
