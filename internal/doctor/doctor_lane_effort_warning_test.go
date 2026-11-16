package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDoctorLaneEffortWarning(t *testing.T) {
	for _, request := range []string{"xhigh", "high", ""} {
		t.Run("request-"+request, func(t *testing.T) {
			root := t.TempDir()
			roles := filepath.Join(root, "roles")
			if err := os.Mkdir(roles, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(roles, "security-reviewer.md"), []byte("---\nkind: codex\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": root, "SOHO_SKILL_DIR": root, "SOHO_ROLES": roles, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_LANES": "on"}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
			for k, v := range map[string]string{"lane_review_roles": "security-reviewer", "lane_review_kind": "codex", "lane_review_effort": "high", "role_security_reviewer_effort": request} {
				if v != "" {
					ctx.Entries[k] = core.ConfigEntry{Value: v, Source: "project"}
					ctx.Order = append(ctx.Order, k)
				}
			}
			var out strings.Builder
			DoctorCheck(ctx, env, root, &out)
			want := "lane review runs security-reviewer at high; the role asks for xhigh"
			if request == "xhigh" {
				if strings.Count(out.String(), want) != 1 {
					t.Fatalf("missing or repeated pair: %s", out.String())
				}
			} else if strings.Contains(out.String(), "the role asks for") {
				t.Fatalf("non-conflicting lane listed: %s", out.String())
			}
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "the role asks for") {
					t.Log(line)
				}
			}
		})
	}
}

// SOH-299: a lane effort the user or project chose wins over the role
// file's suggestion silently; a lane effort from the skill defaults still
// warns, and so does an explicit role effort (TestDoctorLaneEffortWarning).
func TestDoctorLaneEffortChosenSilencesTheRoleFile(t *testing.T) {
	for _, laneSource := range []string{"user", "project", "defaults"} {
		t.Run(laneSource, func(t *testing.T) {
			root := t.TempDir()
			roles := filepath.Join(root, "roles")
			if err := os.Mkdir(roles, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(roles, "security-reviewer.md"), []byte("---\nkind: codex\neffort: xhigh\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": root, "SOHO_SKILL_DIR": root, "SOHO_ROLES": roles, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_LANES": "on"}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
			ctx.Entries["lane_review_roles"] = core.ConfigEntry{Value: "security-reviewer", Source: "project"}
			ctx.Order = append(ctx.Order, "lane_review_roles")
			// The lane effort only applies from the lane kind's layer or above
			// (core.LaneAttr), so both come from the same layer here.
			for k, v := range map[string]string{"lane_review_kind": "codex", "lane_review_effort": "high"} {
				ctx.Entries[k] = core.ConfigEntry{Value: v, Source: laneSource}
				ctx.Order = append(ctx.Order, k)
			}
			var out strings.Builder
			DoctorCheck(ctx, env, root, &out)
			warned := strings.Contains(out.String(), "lane review runs security-reviewer at high; the role asks for xhigh")
			if laneSource == "defaults" && !warned {
				t.Fatalf("a default lane effort must still warn: %s", out.String())
			}
			if laneSource != "defaults" && warned {
				t.Fatalf("a chosen lane effort must not warn about the role file: %s", out.String())
			}
		})
	}
}
