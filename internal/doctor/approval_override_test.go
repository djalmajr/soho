package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDoctorCodexArgsOverrideApprovals(t *testing.T) {
	for _, tc := range []struct{ name, kind, mode, args, flag, layer string }{
		{"auto", "codex", "auto", "-s danger-full-access", "-s", "kind"},
		{"ask", "codex", "ask", "-s danger-full-access", "", "kind"},
		{"no-flag", "codex", "auto", "--verbose", "", "kind"},
		{"claude", "claude", "auto", "-s danger-full-access", "", "kind"},
		{"equals-role", "codex", "full", "--sandbox=workspace-write", "--sandbox", "role"},
		{"lane", "codex", "edits", "-a never", "-a", "lane"},
		{"lane-ask", "codex", "ask", "-a never", "", "lane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			roles := filepath.Join(root, "roles")
			bin := filepath.Join(root, "bin")
			for _, p := range []string{roles, bin} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte(fmt.Sprintf("---\nkind: %s\napprovals: auto\n---\n", tc.kind)), 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_ROLES": roles, "SOHO_SKILL_DIR": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_LANES": "off", "HERDR_ENV": "1", "SOHO_APPROVALS": tc.mode, "SOHO_ROLE_WORKER_APPROVALS": tc.mode}
			switch tc.layer {
			case "kind":
				env["SOHO_ARGS_"+strings.ToUpper(tc.kind)] = tc.args
			case "role":
				env["SOHO_ROLE_WORKER_ARGS"] = tc.args
			case "lane":
				env["SOHO_LANES"] = "on"
				env["SOHO_PANES"] = "3"
				env["SOHO_LANE_BUILD_ROLES"] = "worker"
				env["SOHO_LANE_BUILD_KIND"] = tc.kind
				env["SOHO_LANE_BUILD_ARGS"] = tc.args
				env["SOHO_LANE_BUILD_APPROVALS"] = tc.mode
			}
			var out strings.Builder
			DoctorCheck(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root, &out)
			if tc.flag != "" {
				want := fmt.Sprintf("warn   config: worker runs codex with %s in its args; approvals=%s is not added (the args win)\n", tc.flag, tc.mode)
				if strings.Count(out.String(), want) != 1 {
					t.Fatalf("missing/duplicate warning %q output=%s", want, out.String())
				}
			} else if strings.Contains(out.String(), "(the args win)") {
				t.Fatalf("unexpected warning: %s", out.String())
			}
		})
	}
}
