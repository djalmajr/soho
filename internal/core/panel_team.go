package core

import (
	"fmt"
	"path/filepath"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// panelLaneRows reports each lane's own configuration and its source. Member
// settings are projected separately on the role rows.
func panelLaneRows(ctx *Config, env platform.Env) []any {
	lanes := []any{}
	if !LanesEnabled(ctx, env) {
		return lanes
	}
	for _, name := range LaneNames(ctx, env) {
		kind, kindFrom := panelLaneAttr(ctx, name, "kind", env)
		model, modelKey, _ := LaneModelConfig(ctx, name, env)
		modelFrom := panelConfigSource(ctx, modelKey, model, env)
		effort, effortFrom := panelLaneAttr(ctx, name, "effort", env)
		approvals, approvalsFrom := panelLaneAttr(ctx, name, "approvals", env)
		lanes = append(lanes, jsonjs.O(
			"name", name,
			"roles", stringsToAny(SplitRoles(LaneRolesCSV(ctx, name, env))),
			"panes", LaneCapacity(ctx, name, env),
			"kind", kind,
			"model", model,
			"effort", effort,
			"approvals", approvals,
			"kind_from", kindFrom,
			"model_from", modelFrom,
			"effort_from", effortFrom,
			"approvals_from", approvalsFrom,
		))
	}
	return lanes
}

func panelLaneAttr(ctx *Config, lane, attr string, env platform.Env) (string, string) {
	key := LaneKey(lane, attr)
	value := Cfg(ctx, key, "", env)
	return value, panelConfigSource(ctx, key, value, env)
}

func panelConfigSource(ctx *Config, key, value string, env platform.Env) string {
	if value == "" {
		return "default"
	}
	return CfgSource(ctx, key, env)
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
	rows := collectRoles(ctx, env, cwd)
	// The session file is the one `soho session set` resolves for this caller;
	// without a workspace there is neither a session nor a state dir to name.
	session := SessionConfPath(ctx, env, cwd)
	var roster, reports any
	if session != "" {
		// A passive reader never creates or dirties state: StateDirForRead
		// resolves over a read-only env clone and requires an existing dir.
		if dir := StateDirForRead(ctx, env, cwd); dir != "" {
			roster = filepath.Join(dir, rosterFileName)
			reports = filepath.Join(dir, "reports")
		}
	}
	fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(
		"roles", rolesJSONArray(rows),
		"lanes", panelLaneRows(ctx, env),
		"unassigned_roles", panelUnassignedRoles(rows),
		"team_exists", teamExists,
		"config_files", jsonjs.O(
			"project", ConfigFileFor("project", env, cwd),
			"user", ConfigFileFor("user", env, cwd),
			"session", panelPath(session),
		),
		"team_files", jsonjs.O(
			"roster", roster,
			"reports", reports,
		),
	)))
}

// panelPath turns an absent path into JSON null, never an empty string.
func panelPath(path string) any {
	if path == "" {
		return nil
	}
	return path
}
