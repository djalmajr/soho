package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorOnDemandLaneAndFix(t *testing.T) {
	env, root := tm6DoctorEnv(t)
	ctx := tm6DoctorConfig(map[string]string{"panes": "4", "max_workers": "3"}, "project")
	var out strings.Builder
	doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
	if strings.Contains(out.String(), "but the lanes hold") || !strings.Contains(out.String(), "on-demand lane critical would make 4 workers") {
		t.Fatalf("unexpected diagnostics: %s", out.String())
	}
	file := filepath.Join(root, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("panes=4\nmax_workers=9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	doctorFix("project", "4", ctx, env, root)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "max_workers=3\n") {
		t.Fatalf("fix: %s", raw)
	}
}

func TestDoctorDisplayPolicyUIRole(t *testing.T) {
	for _, tc := range []struct {
		name, roles, kind string
		want              bool
	}{
		{"lane UI", "designer", "codex", true},
		{"no UI", "implementer", "codex", false},
		{"UI other kind", "designer", "grok", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, root := tm6DoctorEnv(t)
			env["WAYLAND_DISPLAY"] = "private-display-sentinel"
			env["CODEX_HOME"] = filepath.Join(root, "codex")
			if err := os.MkdirAll(env["CODEX_HOME"], 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(env["CODEX_HOME"], "config.toml"), []byte("[shell_environment_policy]\ninclude_only=['PATH']\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := tm6DoctorConfig(map[string]string{"lane_build_roles": tc.roles, "lane_build_kind": tc.kind}, "project")
			var out strings.Builder
			doctorCodexRegistryPolicy(ctx, env, root, &Say{Out: &out})
			if strings.Contains(out.String(), "WAYLAND_DISPLAY") != tc.want || strings.Contains(out.String(), env["WAYLAND_DISPLAY"]) {
				t.Fatalf("unexpected output: %s", out.String())
			}
		})
	}
}

func TestDoctorDisplayPolicyRoleConfig(t *testing.T) {
	for _, lanes := range []string{"on", "off"} {
		t.Run(lanes, func(t *testing.T) {
			env, root := tm6DoctorEnv(t)
			env["WAYLAND_DISPLAY"] = "private-display-sentinel"
			env["CODEX_HOME"] = filepath.Join(root, "codex")
			if err := os.MkdirAll(env["CODEX_HOME"], 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(env["CODEX_HOME"], "config.toml")
			if err := os.WriteFile(path, []byte("[shell_environment_policy]\ninclude_only=['PATH']\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := tm6DoctorConfig(map[string]string{"lanes": lanes, "lane_build_roles": "implementer", "lane_build_kind": "grok", "role_inspector_kind": "codex"}, "project")
			got := tm6DoctorOutput(t, ctx, env, root)
			if !strings.Contains(got, path+" drops display variables needed by UI roles: WAYLAND_DISPLAY") || strings.Contains(got, env["WAYLAND_DISPLAY"]) {
				t.Fatalf("lanes=%s output=%s", lanes, got)
			}
		})
	}
}
