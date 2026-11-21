package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

func panelParseObject(t *testing.T, out string) *jsonjs.Object {
	t.Helper()
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("panel JSON is not one object on one line: %q", out)
	}
	parsed, err := jsonjs.Parse([]byte(out))
	if err != nil {
		t.Fatalf("panel JSON does not parse: %v\n%s", err, out)
	}
	obj, ok := parsed.(*jsonjs.Object)
	if !ok {
		t.Fatalf("panel JSON root is %T, want object", parsed)
	}
	return obj
}

func panelStringArray(t *testing.T, value any, label string) []string {
	t.Helper()
	arr, ok := value.([]any)
	if !ok {
		t.Fatalf("%s = %T, want array", label, value)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("%s item = %T, want string", label, item)
		}
		out = append(out, s)
	}
	return out
}

func panelObjectField(t *testing.T, value any, label string) *jsonjs.Object {
	t.Helper()
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("%s = %T, want object", label, value)
	}
	return obj
}

const panelLaneConfig = `lanes=on
panes=2
lane.build.roles=designer,tasker
lane.build.panes=2
lane.build.kind=pi
lane.build.model=lane-model
lane.build.effort=xhigh
lane.build.approvals=ask
lane.review.roles=reviewer
lane.review.kind=codex
lane.review.model=review-model
lane.review.effort=medium
`

func panelRolesFixture(t *testing.T, config string) (platform.Env, string, *Config) {
	t.Helper()
	env, repo := fixture(t)
	if config != "" {
		write(t, filepath.Join(repo, ".agents", "soho.conf"), config)
	}
	if err := os.MkdirAll(filepath.Join(env.Get("SOHO_DIR"), "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := LoadConfig(env, repo)
	return env, repo, &ctx
}

func TestPanelTeamRolesJSONKeepsTheLegacyArray(t *testing.T) {
	env, repo, ctx := panelRolesFixture(t, panelLaneConfig)
	legacyOut, _ := capture(t, func() { CmdRolesJSON(env, repo) })
	panelOut, _ := capture(t, func() { CmdRolesPanelJSON(ctx, env, repo, true) })
	legacyRoles, _ := panelParseObject(t, legacyOut).Get("roles")
	panelObj := panelParseObject(t, panelOut)
	if keys := panelObj.Keys(); strings.Join(keys, ",") != "roles,lanes,unassigned_roles,team_exists,config_files,team_files" {
		t.Fatalf("panel roles key order = %v", keys)
	}
	panelRoles, _ := panelObj.Get("roles")
	if jsonjs.Stringify(legacyRoles) != jsonjs.Stringify(panelRoles) {
		t.Fatalf("roles array changed:\nlegacy %s\npanel  %s", jsonjs.Stringify(legacyRoles), jsonjs.Stringify(panelRoles))
	}
	// The original command keeps its one-key shape: the panel's extra keys
	// are added on the CLI path, not in the legacy Go projection.
	legacyObj := panelParseObject(t, legacyOut)
	if keys := legacyObj.Keys(); strings.Join(keys, ",") != "roles" {
		t.Fatalf("legacy roles key order changed = %v", keys)
	}
}

func TestPanelTeamLanesProjection(t *testing.T) {
	env, repo, ctx := panelRolesFixture(t, panelLaneConfig)
	out, _ := capture(t, func() { CmdRolesPanelJSON(ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	lanesValue, _ := obj.Get("lanes")
	lanes, ok := lanesValue.([]any)
	if !ok || len(lanes) != 2 {
		t.Fatalf("lanes = %#v, want two configured lanes", lanesValue)
	}
	first := panelObjectField(t, lanes[0], "lanes[0]")
	if keys := first.Keys(); strings.Join(keys, ",") != "name,roles,panes,kind,model,effort,approvals,kind_from,model_from,effort_from,approvals_from" {
		t.Fatalf("lanes[0] key order = %v", keys)
	}
	want := map[string]any{
		"name": "build", "panes": float64(2), "kind": "pi", "model": "lane-model",
		"effort": "xhigh", "approvals": "ask",
		"kind_from": "project", "model_from": "project", "effort_from": "project",
	}
	for key, value := range want {
		got, _ := first.Get(key)
		if got != value {
			t.Fatalf("lanes[0].%s = %#v, want %#v", key, got, value)
		}
	}
	rolesValue, _ := first.Get("roles")
	if got := panelStringArray(t, rolesValue, "lanes[0].roles"); !reflect.DeepEqual(got, []string{"designer", "tasker"}) {
		t.Fatalf("lanes[0].roles = %v", got)
	}
	second := panelObjectField(t, lanes[1], "lanes[1]")
	if name, _ := second.Get("name"); name != "review" {
		t.Fatalf("lanes[1].name = %v, want review", name)
	}

	// The lane order is LaneNames' order, and unassigned_roles comes from
	// each role's own resolution, not from a lane loop position.
	rolesValue, _ = obj.Get("roles")
	roleRows, _ := rolesValue.([]any)
	wantUnassigned := []string{}
	for _, row := range roleRows {
		roleObj := panelObjectField(t, row, "role row")
		lane, _ := roleObj.Get("lane")
		if lane == "" {
			name, _ := roleObj.Get("role")
			wantUnassigned = append(wantUnassigned, name.(string))
		}
	}
	unassignedValue, _ := obj.Get("unassigned_roles")
	if got := panelStringArray(t, unassignedValue, "unassigned_roles"); !reflect.DeepEqual(got, wantUnassigned) {
		t.Fatalf("unassigned_roles = %v, want %v", got, wantUnassigned)
	}
	if contains(wantUnassigned, "designer") || contains(wantUnassigned, "reviewer") {
		t.Fatalf("lane roles leaked into unassigned_roles: %v", wantUnassigned)
	}
}

func TestPanelTeamLanesOffAndNoTeam(t *testing.T) {
	env, repo, ctx := panelRolesFixture(t, "lanes=off\n")
	out, _ := capture(t, func() { CmdRolesPanelJSON(ctx, env, repo, false) })
	obj := panelParseObject(t, out)
	if lanes, _ := obj.Get("lanes"); jsonjs.Stringify(lanes) != "[]" {
		t.Fatalf("lanes with lanes=off = %s, want []", jsonjs.Stringify(lanes))
	}
	if value, _ := obj.Get("team_exists"); value != false {
		t.Fatalf("team_exists = %v, want false as passed by the CLI", value)
	}
	unassigned, _ := obj.Get("unassigned_roles")
	roles, _ := obj.Get("roles")
	if len(unassigned.([]any)) != len(roles.([]any)) {
		t.Fatalf("with lanes off every role is unassigned: %d of %d", len(unassigned.([]any)), len(roles.([]any)))
	}
}

func TestPanelTeamConfigFilesAreTheCommandDestinations(t *testing.T) {
	env, repo, ctx := panelRolesFixture(t, "")
	out, _ := capture(t, func() { CmdRolesPanelJSON(ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	files := panelObjectField(t, objValue(obj, "config_files"), "config_files")
	if keys := files.Keys(); strings.Join(keys, ",") != "project,user,session" {
		t.Fatalf("config_files key order = %v", keys)
	}
	for _, scope := range []string{"project", "user"} {
		got, _ := files.Get(scope)
		if want := ConfigFileFor(scope, env, repo); got != want {
			t.Fatalf("config_files.%s = %v, want %v", scope, got, want)
		}
	}
	if got, _ := files.Get("session"); got != SessionConfPath(ctx, env, repo) {
		t.Fatalf("config_files.session = %v, want %v", got, SessionConfPath(ctx, env, repo))
	}
	tm := panelObjectField(t, objValue(obj, "team_files"), "team_files")
	if keys := tm.Keys(); strings.Join(keys, ",") != "roster,reports" {
		t.Fatalf("team_files key order = %v", keys)
	}
	state := filepath.Join(env.Get("SOHO_DIR"), "ws")
	if got, _ := tm.Get("roster"); got != filepath.Join(state, "agents.tsv") {
		t.Fatalf("team_files.roster = %v, want %v", got, filepath.Join(state, "agents.tsv"))
	}
	if got, _ := tm.Get("reports"); got != filepath.Join(state, "reports") {
		t.Fatalf("team_files.reports = %v, want %v", got, filepath.Join(state, "reports"))
	}
	if value, _ := obj.Get("team_exists"); value != true {
		t.Fatalf("team_exists = %v, want true as passed by the CLI", value)
	}
}

func TestPanelTeamRolesReadDoesNotWriteState(t *testing.T) {
	env, repo, _ := panelRolesFixture(t, "")
	// An in-repo state location on a fresh checkout: reading roles must not
	// append .gitignore, create .soho, or leave any other status entry.
	env["SOHO_DIR"] = ".soho"
	ctx := LoadConfig(env, repo)
	out, _ := capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	tm := panelObjectField(t, objValue(obj, "team_files"), "team_files")
	for _, key := range []string{"roster", "reports"} {
		if got, _ := tm.Get(key); got != nil {
			t.Fatalf("team_files.%s with no state dir = %#v, want null", key, got)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("roles --json created .gitignore: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".soho")); !os.IsNotExist(err) {
		t.Fatalf("roles --json created .soho: err=%v", err)
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	cmd.Env = env.List()
	status, err := cmd.CombinedOutput()
	if err != nil || len(status) != 0 {
		t.Fatalf("git status after roles --json = %q, err=%v", status, err)
	}
}

func objValue(obj *jsonjs.Object, key string) any {
	value, _ := obj.Get(key)
	return value
}

func TestPanelTeamSessionAndTeamFilesOutsideAWorkspaceAreNull(t *testing.T) {
	env, repo, ctx := panelRolesFixture(t, "")
	delete(env, "HERDR_WORKSPACE_ID")
	out, _ := capture(t, func() { CmdRolesPanelJSON(ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	files := panelObjectField(t, objValue(obj, "config_files"), "config_files")
	if got, _ := files.Get("session"); got != nil {
		t.Fatalf("config_files.session outside a workspace = %#v, want null", got)
	}
	tm := panelObjectField(t, objValue(obj, "team_files"), "team_files")
	for _, key := range []string{"roster", "reports"} {
		if got, _ := tm.Get(key); got != nil {
			t.Fatalf("team_files.%s outside a workspace = %#v, want null", key, got)
		}
	}
	for _, scope := range []string{"project", "user"} {
		if got, _ := files.Get(scope); got != ConfigFileFor(scope, env, repo) {
			t.Fatalf("config_files.%s outside a workspace = %v", scope, got)
		}
	}
}

func TestPanelTeamEmptyConfigAndLaneWithNoRoles(t *testing.T) {
	env, repo := fixture(t)
	probe := func(label string, exists bool) {
		t.Helper()
		ctx := LoadConfig(env, repo)
		out, _ := capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, false) })
		obj := panelParseObject(t, out)
		files := panelObjectField(t, objValue(obj, "config_files"), "config_files")
		if got, _ := files.Get("project"); got != ConfigFileFor("project", env, repo) {
			t.Fatalf("%s project file = %v", label, got)
		}
		if got, _ := files.Get("user"); got != ConfigFileFor("user", env, repo) {
			t.Fatalf("%s user file = %v", label, got)
		}
		if _, err := os.Stat(filepath.Join(repo, ".agents", "soho.conf")); (err == nil) != exists {
			t.Fatalf("%s config existence = %v, want %v", label, err == nil, exists)
		}
	}
	probe("missing config", false)
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "\n")
	probe("empty config", true)
	// A configured lane whose roles list is blank still gets a row with an
	// empty roles array; no membership is invented from another lane.
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.empty.roles=,\n")
	ctx := LoadConfig(env, repo)
	out, _ := capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	lanesValue, _ := obj.Get("lanes")
	lanes, ok := lanesValue.([]any)
	if !ok || len(lanes) != 1 {
		t.Fatalf("lanes = %#v, want the one blank lane", lanesValue)
	}
	row := panelObjectField(t, lanes[0], "lanes[0]")
	if name, _ := row.Get("name"); name != "empty" {
		t.Fatalf("lanes[0].name = %v, want empty", name)
	}
	if roles, _ := row.Get("roles"); jsonjs.Stringify(roles) != "[]" {
		t.Fatalf("lanes[0].roles = %s, want []", jsonjs.Stringify(roles))
	}
}

func TestPanelTeamShowsConfiguredGroupAndEachRoleSource(t *testing.T) {
	env, repo := fixture(t)
	write(t, ConfigFileFor("user", env, repo), "lane.build.models=group-first,group-second\nlane.build.effort=high\nrole.tester.effort=low\n")
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lanes=on\nlane.build.roles=tester,designer,implementer\nlane.build.kind=pi\nlane.build.model=shadowed\nlane.build.approvals=full\nrole.tester.kind=grok\nrole.tester.model=role-model\nrole.tester.approvals=ask\n")
	ctx := LoadConfig(env, repo)
	out, _ := capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	lanes, _ := objValue(obj, "lanes").([]any)
	if len(lanes) != 1 {
		t.Fatalf("lanes=%v, want one build lane", lanes)
	}
	check := func(row *jsonjs.Object, want map[string]any) {
		t.Helper()
		for key, value := range want {
			if got, _ := row.Get(key); got != value {
				t.Errorf("%s = %#v, want %#v", key, got, value)
			}
		}
	}
	check(panelObjectField(t, lanes[0], "lane"), map[string]any{
		"kind": "pi", "kind_from": "project", "model": "group-first", "model_from": "user",
		"effort": "high", "effort_from": "user", "approvals": "full", "approvals_from": "project",
	})
	roles, _ := objValue(obj, "roles").([]any)
	seen := map[string]bool{}
	for _, value := range roles {
		row := panelObjectField(t, value, "role")
		name, _ := row.Get("role")
		switch name {
		case "tester":
			seen["tester"] = true
			check(row, map[string]any{
				"kind": "grok", "kind_from": "role", "model": "role-model", "model_from": "role",
				"effort": "low", "effort_from": "role", "approvals": "ask", "approvals_from": "role",
			})
		case "designer":
			seen["designer"] = true
			check(row, map[string]any{
				"kind": "pi", "kind_from": "group", "model": "group-first", "model_from": "group",
				"effort": "high", "effort_from": "group", "approvals": "full", "approvals_from": "group",
			})
		}
	}
	if !seen["tester"] || !seen["designer"] {
		t.Fatalf("missing role rows: %v", seen)
	}

	write(t, ConfigFileFor("user", env, repo), "")
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lanes=on\nlane.build.roles=tester,designer\nrole.tester.kind=codex\nrole.designer.kind=opencode\n")
	ctx = LoadConfig(env, repo)
	out, _ = capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	lanes, _ = objValue(panelParseObject(t, out), "lanes").([]any)
	check(panelObjectField(t, lanes[0], "lane"), map[string]any{
		"kind": "", "kind_from": "default", "model": "", "model_from": "default",
		"effort": "", "effort_from": "default", "approvals": "", "approvals_from": "default",
	})
}
