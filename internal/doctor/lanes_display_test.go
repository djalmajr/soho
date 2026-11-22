package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorOnDemandLaneAndFix(t *testing.T) {
	env, root := tm6DoctorEnv(t)
	ctx := tm6DoctorConfig(map[string]string{"panes": "4", "max_workers": "4"}, "project")
	var out strings.Builder
	doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
	if strings.Contains(out.String(), "but the lanes hold") || !strings.Contains(out.String(), "note   config: max_workers=4 allows creating a new worker in an on-demand lane only when a slot is free") {
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
	if !strings.Contains(string(raw), "max_workers=5\n") {
		t.Fatalf("fix: %s", raw)
	}
}

func TestDoctorUnlimitedOnDemandLane(t *testing.T) {
	env, root := tm6DoctorEnv(t)
	ctx := tm6DoctorConfig(map[string]string{"max_workers": "0", "lane_critical_roles": "critical-implementer", "lane_critical_panes": "1"}, "project")
	var out strings.Builder
	doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
	got := out.String()
	if !strings.Contains(got, "max_workers=0 (no cap on live workers)") || strings.Contains(got, "but the lanes hold") || strings.Contains(got, "note   config: max_workers=") {
		t.Fatalf("unexpected diagnostics: %s", got)
	}
}

func TestDoctorCapacityWarningNamesLayer(t *testing.T) {
	for _, tc := range []struct{ source, fix string }{
		{"user", "soho config set max_workers 5 --user"},
		{"project", "soho config set max_workers 5"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			env, root := tm6DoctorEnv(t)
			ctx := tm6DoctorConfig(map[string]string{"panes": "4", "max_workers": "1"}, tc.source)
			var out strings.Builder
			doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
			if !strings.Contains(out.String(), "in the "+tc.source+" layer ("+tc.fix+")") {
				t.Fatalf("unexpected diagnostics: %s", out.String())
			}
		})
	}
}

func TestDoctorLaneCapacityRange(t *testing.T) {
	for _, tc := range []struct {
		limit, want   string
		warning, note bool
	}{
		{"6", "7 always-open to 9 total", true, false},
		{"7", "only when a slot is free", false, true},
		{"8", "only when a slot is free", false, true},
		{"9", "matches the lanes", false, false},
		{"10", "7 always-open to 9 total", true, false},
	} {
		t.Run(tc.limit, func(t *testing.T) {
			env, root := tm6DoctorEnv(t)
			ctx := tm6DoctorConfig(map[string]string{"max_workers": tc.limit, "lane_build_roles": "implementer", "lane_build_panes": "7", "lane_planning_roles": "sub-orchestrator,job-orchestrator", "lane_planning_panes": "2"}, "project")
			var out strings.Builder
			doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
			got := out.String()
			if !strings.Contains(got, tc.want) || strings.Contains(got, "but the lanes hold") != tc.warning || (strings.Count(got, "note   config: max_workers=") == 1) != tc.note {
				t.Fatalf("unexpected diagnostics: %s", got)
			}
		})
	}
}

func TestDoctorFlexExtraIsTemporary(t *testing.T) {
	env, root := tm6DoctorEnv(t)
	ctx := tm6DoctorConfig(map[string]string{"panes": "2", "pane_mode": "flex", "flex_extra": "1", "max_workers": "1"}, "project")
	var out strings.Builder
	doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
	got := out.String()
	if strings.Contains(got, "but the lanes hold") || !strings.Contains(got, "temporary flex worker only when a slot is free") {
		t.Fatalf("unexpected diagnostics: %s", got)
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

func TestDoctorDisplayPolicyEmptyInclude(t *testing.T) {
	env, root := tm6DoctorEnv(t)
	env["WAYLAND_DISPLAY"] = "private-display-sentinel"
	env["CODEX_HOME"] = filepath.Join(root, "codex")
	if err := os.MkdirAll(env["CODEX_HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env["CODEX_HOME"], "config.toml"), []byte("[shell_environment_policy]\ninclude_only=[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := tm6DoctorConfig(map[string]string{"lane_build_roles": "designer", "lane_build_kind": "codex"}, "project")
	var out strings.Builder
	doctorCodexRegistryPolicy(ctx, env, root, &Say{Out: &out})
	if strings.Contains(out.String(), "display variables") || strings.Contains(out.String(), "WAYLAND_DISPLAY") {
		t.Fatalf("unexpected output: %s", out.String())
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
