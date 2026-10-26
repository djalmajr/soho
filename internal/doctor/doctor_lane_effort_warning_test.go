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
