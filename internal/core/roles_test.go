package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestRoleFilesAndFrontmatter(t *testing.T) {
	// JS: "roleDirs: project dir first, $SOHO_ROLES, then the skill roles"
	// JS: "resolveRole: project role shadows the skill role"
	// JS: "fmGet: scalar, list, quoted, missing key, no frontmatter"
	// JS: "fmGet: frontmatter with CRLF lines parses like LF (decision 7)"
	// JS: "roleBody: after the closing ---, whole file without frontmatter, empty when unclosed"
	env, repo := fixture(t)
	projectRoles := filepath.Join(repo, ".agents", "herdr-roles")
	write(t, filepath.Join(projectRoles, "implementer.md"), "---\r\nname: local\r\nalternatives: [one, two]\r\nmode: edit\r\n---\r\nbody\r\n")
	projectRoles, _ = filepath.EvalSymlinks(projectRoles)
	if got := RoleFile("implementer", env, repo); got != filepath.Join(projectRoles, "implementer.md") {
		t.Fatalf("project role file = %q", got)
	}
	if got := FmGet(RoleFile("implementer", env, repo), "alternatives"); got != "one two" {
		t.Fatalf("alternatives = %q", got)
	}
	frontmatterTabs := filepath.Join(t.TempDir(), "tabs.md")
	write(t, frontmatterTabs, "---\nvalue: left\t\tright\n---\n")
	if got := FmGet(frontmatterTabs, "value"); got != "left\t\tright" {
		t.Fatalf("frontmatter collapsed non-space whitespace: %q", got)
	}
	if got := RoleBody(RoleFile("implementer", env, repo)); got != "body\n" {
		t.Fatalf("role body = %q", got)
	}
	if !RoleIsEdit("implementer", env, repo) || !HistoryHasEdit("scouter, implementer", env, repo) {
		t.Fatal("edit role was not detected")
	}
	if got := RoleBody(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Fatalf("absent body = %q", got)
	}
	plain := filepath.Join(t.TempDir(), "plain.md")
	write(t, plain, "plain\nbody\n")
	if got := RoleBody(plain); got != "plain\nbody\n" {
		t.Fatalf("plain body = %q", got)
	}
	unclosed := filepath.Join(t.TempDir(), "unclosed.md")
	write(t, unclosed, "---\nname: x\nbody\n")
	if got := RoleBody(unclosed); got != "" {
		t.Fatalf("unclosed body = %q", got)
	}
	if FmGet(plain, "name") != "" {
		t.Fatal("frontmatter parsed from plain file")
	}
	if !IsReviewRole("inspector") || IsReviewRole("documenter") {
		t.Fatal("review role set differs")
	}
	if got := ResolveRole("scouter", env, repo); got == "" {
		t.Fatal("skill role did not resolve")
	}
}

func TestResolveRoleSettingsLayers(t *testing.T) {
	// JS: "roles: role.<r>.* in a config layer beats the frontmatter"
	// JS: "roles: lane.<l>.kind/model/effort decide for the roles of the lane"
	// JS: "roles: a role model from a layer below the effective kind shows the model that is left"
	// JS: "roles: the frontmatter model is dropped when the kind comes from a config layer"
	// JS: "roles: effort.<kind> and empty values (dashes, default sources)"
	env, repo := fixture(t)
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.build.kind=pi\nlane.build.model=lane-model\nlane.build.effort=medium\nrole.implementer.model=role-model\n")
	ctx := LoadConfig(env, repo)
	r := ResolveRoleSettings("implementer", &ctx, env, repo, RoleFlags{})
	if r.Lane != "build" || r.Kind != "pi" || r.KindFrom != "lane build (project)" || r.ModelSpec != "lane-model" || r.Effort != "medium" || r.Approvals != "auto" {
		t.Fatalf("settings = %#v", r)
	}
	// The role model is configured at the same source rank as the lane kind;
	// it applies after the lane has no model value.
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.build.roles=implementer\nlane.build.kind=pi\nrole.implementer.model=role-model\n")
	ctx = LoadConfig(env, repo)
	r = ResolveRoleSettings("implementer", &ctx, env, repo, RoleFlags{})
	if r.ModelSpec != "role-model" || r.ModelFrom != "role config (project)" {
		t.Fatalf("same-layer model = %#v", r)
	}
	write(t, platform.UserConfigPath(platform.Current(), env), "role.implementer.model=lower-model\n")
	customRoles := filepath.Join(t.TempDir(), "roles")
	write(t, filepath.Join(customRoles, "implementer.md"), "---\nkind: grok\nmodel: front-model\n---\n")
	env["SOHO_ROLES"] = customRoles
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "role.implementer.kind=pi\n")
	ctx = LoadConfig(env, repo)
	r = ResolveRoleSettings("implementer", &ctx, env, repo, RoleFlags{})
	if r.Kind != "pi" || r.ModelSpec == "lower-model" || r.ModelSpec == "front-model" {
		t.Fatalf("kind-layer discard = %#v", r)
	}
	// A flag kind has rank 5 and drops all configured role/lane model specs.
	r = ResolveRoleSettings("implementer", &ctx, env, repo, RoleFlags{Kind: "grok", Model: "flag-model", Effort: "high", Approvals: "full"})
	if r.Kind != "grok" || r.KindFrom != "flag" || r.ModelSpec != "flag-model" || r.Effort != "high" || r.Approvals != "full" {
		t.Fatalf("flag settings = %#v", r)
	}
	if r.KindLayer != 5 {
		t.Fatalf("kind layer = %d", r.KindLayer)
	}
}

func TestRoleTimeoutAndRolesTable(t *testing.T) {
	// JS: "roleTimeoutMs: the frontmatter timeout scaled by the effective effort, the dispatch_timeout fallback"
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	if got := RoleTimeoutMs("reviewer", &ctx, env, repo); got <= 0 {
		t.Fatalf("review timeout = %d", got)
	}
	var output string
	out, errOut := capture(t, func() { CmdRoles(env, repo) })
	output = out
	if errOut != "" {
		t.Fatalf("roles stderr = %q", errOut)
	}
	if !strings.Contains(output, "ROLE") || !strings.Contains(output, "scouter") || !strings.Contains(output, "file: ") {
		t.Fatalf("roles output incomplete: %s", output)
	}
	// Protects the xhigh multiplier and report-time fallback from being dropped.
	custom := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(custom, "tasker.md"), "---\nkind: grok\neffort: xhigh\ntimeout: 2000\n---\n")
	env["SOHO_ROLES"] = custom
	ctx = LoadConfig(env, repo)
	if got := RoleTimeoutMs("tasker", &ctx, env, repo); got != 3000 {
		t.Fatalf("scaled timeout = %d", got)
	}
}

func TestCmdRolesUsesUTF16FilenameOrder(t *testing.T) {
	// Mutation captured: byte sorting lists the U+E000 role before the emoji role.
	env, repo := fixture(t)
	roles := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(roles, "\uE000.md"), "---\n---\n")
	write(t, filepath.Join(roles, "😀.md"), "---\n---\n")
	env["SOHO_ROLES"] = roles
	out, errOut := capture(t, func() { CmdRoles(env, repo) })
	if errOut != "" {
		t.Fatalf("roles stderr = %q", errOut)
	}
	if strings.Index(out, "😀") < 0 || strings.Index(out, "\uE000") < 0 || strings.Index(out, "😀") > strings.Index(out, "\uE000") {
		t.Fatalf("roles output has wrong UTF-16 order: %q", out)
	}
}

func TestCmdRoleAlternativesSplitOnSpacesOnly(t *testing.T) { // Mutation captured: strings.Fields treats tabs as separators and changes JS alternatives output.
	env, repo := fixture(t)
	custom := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(custom, "implementer.md"), "---\nalternatives: [one,\ttwo]\n---\n")
	env["SOHO_ROLES"] = custom
	out, stderr := capture(t, func() { CmdRole([]string{"implementer"}, env, repo) })
	if stderr != "" {
		t.Fatalf("role stderr = %q", stderr)
	}
	want := `"alternatives": [
    "one",
    "\ttwo"
  ]`
	if !strings.Contains(out, want) {
		t.Fatalf("alternatives did not match space-only parsing: %s", out)
	}
}
