package core

import (
	"os"
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
	ctx := LoadConfig(env, repo)
	return env, repo, &ctx
}

func TestPanelTeamRolesJSONKeepsTheLegacyArray(t *testing.T) {
	env, repo, ctx := panelRolesFixture(t, panelLaneConfig)
	legacyOut, _ := capture(t, func() { CmdRolesJSON(env, repo) })
	panelOut, _ := capture(t, func() { CmdRolesPanelJSON(ctx, env, repo, true) })
	legacyRoles, _ := panelParseObject(t, legacyOut).Get("roles")
	panelObj := panelParseObject(t, panelOut)
	if keys := panelObj.Keys(); strings.Join(keys, ",") != "roles,lanes,unassigned_roles,team_exists,config_files" {
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
	if keys := first.Keys(); strings.Join(keys, ",") != "name,roles,panes,kind,model,effort,approvals,kind_from,model_from,effort_from" {
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
	if keys := files.Keys(); strings.Join(keys, ",") != "project,user" {
		t.Fatalf("config_files key order = %v", keys)
	}
	for _, scope := range []string{"project", "user"} {
		got, _ := files.Get(scope)
		if want := ConfigFileFor(scope, env, repo); got != want {
			t.Fatalf("config_files.%s = %v, want %v", scope, got, want)
		}
	}
	if value, _ := obj.Get("team_exists"); value != true {
		t.Fatalf("team_exists = %v, want true as passed by the CLI", value)
	}
}

func objValue(obj *jsonjs.Object, key string) any {
	value, _ := obj.Get(key)
	return value
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

func TestPanelTeamLaneFieldsUseTheMembersResolvedValues(t *testing.T) {
	// One member whose own project settings outrank a user-layer lane
	// override: the lane shows the value the member really receives.
	env, repo := fixture(t)
	write(t, ConfigFileFor("user", env, repo), "lane.build.model=user-model\nlane.build.effort=high\n")
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lanes=on\nlane.build.roles=tester\nrole.tester.kind=codex\nrole.tester.model=project-choice\nrole.tester.effort=low\n")
	ctx := LoadConfig(env, repo)
	out, _ := capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	obj := panelParseObject(t, out)
	lanes, ok := objValue(obj, "lanes").([]any)
	if !ok || len(lanes) != 1 {
		t.Fatalf("lanes = %#v, want the one build lane", objValue(obj, "lanes"))
	}
	lane := panelObjectField(t, lanes[0], "lanes[0]")
	for key, want := range map[string]any{
		"kind": "codex", "kind_from": "project",
		"model": "project-choice", "model_from": "project",
		"effort": "low", "effort_from": "project",
	} {
		if got, _ := lane.Get(key); got != want {
			t.Fatalf("lanes[0].%s = %#v, want %#v", key, got, want)
		}
	}
	// The member's own resolution keeps its project values.
	roles, _ := objValue(obj, "roles").([]any)
	var tester *jsonjs.Object
	for _, value := range roles {
		row := panelObjectField(t, value, "role row")
		if role, _ := row.Get("role"); role == "tester" {
			tester = row
		}
	}
	if tester == nil {
		t.Fatal("no tester role row")
	}
	for key, want := range map[string]any{"model": "project-choice", "effort": "low"} {
		if got, _ := tester.Get(key); got != want {
			t.Fatalf("tester.%s = %#v, want %#v", key, got, want)
		}
	}

	// No override and members that resolve differently: null with "mixed" for
	// kind, model and effort alike, never one member's value or a default.
	write(t, ConfigFileFor("user", env, repo), "")
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lanes=on\nlane.build.roles=tester,designer\nrole.tester.kind=codex\nrole.designer.kind=opencode\nrole.tester.model=one\nrole.designer.model=two\nrole.tester.effort=low\nrole.designer.effort=max\n")
	ctx = LoadConfig(env, repo)
	out, _ = capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	lanes, _ = objValue(panelParseObject(t, out), "lanes").([]any)
	lane = panelObjectField(t, lanes[0], "lanes[0]")
	for _, field := range []string{"kind", "model", "effort"} {
		if got, _ := lane.Get(field); got != nil {
			t.Fatalf("mixed lane %s = %#v, want null", field, got)
		}
		if got, _ := lane.Get(field + "_from"); got != "mixed" {
			t.Fatalf("mixed lane %s_from = %#v, want mixed", field, got)
		}
	}

	// A lane override reaches one member while the other member's own role
	// settings give the same value: the lane shows the value, not "mixed".
	write(t, ConfigFileFor("user", env, repo), "lane.build.model=shared-model\nlane.build.effort=high\n")
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lanes=on\nlane.build.roles=tester,designer\nrole.tester.kind=codex\nrole.tester.model=shared-model\nrole.tester.effort=high\n")
	ctx = LoadConfig(env, repo)
	out, _ = capture(t, func() { CmdRolesPanelJSON(&ctx, env, repo, true) })
	lanes, _ = objValue(panelParseObject(t, out), "lanes").([]any)
	lane = panelObjectField(t, lanes[0], "lanes[0]")
	for key, want := range map[string]any{"model": "shared-model", "model_from": "user", "effort": "high", "effort_from": "user"} {
		if got, _ := lane.Get(key); got != want {
			t.Fatalf("agreed lane %s = %#v, want %#v", key, got, want)
		}
	}
	// The kinds still differ (codex against the designer role file's agy).
	if got, _ := lane.Get("kind"); got != nil {
		t.Fatalf("lane kind = %#v, want null (the members resolve different kinds)", got)
	}
}
