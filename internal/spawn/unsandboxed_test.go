package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestReuseUnsandboxedApprovalRank(t *testing.T) {
	for _, role := range []string{"implementer", "scouter"} {
		for _, tc := range []struct {
			held, requested string
			reuse           bool
		}{
			{"full", "unsandboxed", false},
			{"unsandboxed", "full", true},
			{"unsandboxed", "unsandboxed", true},
		} {
			t.Run(role+"/"+tc.held+"-to-"+tc.requested, func(t *testing.T) {
				f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
				f.roster(t, spawnRosterRow("worker", "implementer", "grok-4.7", tc.held, ""))
				got, unavailable, blocked := FindReusable(role, "grok", "/tmp/work", "", "grok-4.7", tc.requested, f.ctx, f.env, f.cwd)
				want := ""
				if tc.reuse {
					want = "worker"
				}
				if got != want || unavailable != "" || blocked {
					t.Fatalf("held=%s requested=%s got=%q want=%q unavailable=%q blocked=%v", tc.held, tc.requested, got, want, unavailable, blocked)
				}
			})
		}
	}
}

func TestSpawnLaneUnsandboxedApprovalReuse(t *testing.T) {
	for _, tc := range []struct {
		name, held, requested, capacity string
		named, alternative, fresh       bool
		code                            int
	}{
		{"opens-new", "full", "unsandboxed", "2", false, false, true, 0},
		{"full-lane-refuses", "full", "unsandboxed", "1", false, false, false, 10},
		{"named-refuses", "full", "unsandboxed", "2", true, false, false, 10},
		{"reuses-stronger", "unsandboxed", "full", "1", false, false, false, 0},
		{"named-reuses-stronger", "unsandboxed", "full", "1", true, false, false, 0},
		{"picks-stronger-alternative", "full", "unsandboxed", "2", false, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []fakecli.Rule{idleHerdrRule("held"), idleHerdrRule("stronger")}
			f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: codex\napprovals: ask\n---\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_LANES"] = "on"
			f.env["SOHO_LANE_BUILD_ROLES"] = "worker"
			f.env["SOHO_LANE_BUILD_KIND"] = "codex"
			f.env["SOHO_LANE_BUILD_PANES"] = tc.capacity
			f.env["SOHO_LANE_BUILD_APPROVALS"] = tc.requested
			row := func(name, mode string) string {
				return strings.Join([]string{name, "p-" + name, "codex", "worker", "openai", "1", f.cwd, "now", "", mode, "worker", "build", "", "", "", "inherit"}, "\t")
			}
			rows := []string{row("held", tc.held)}
			if tc.alternative {
				rows = append(rows, row("stronger", "unsandboxed"))
			}
			f.roster(t, rows...)
			args := []string{"worker"}
			if tc.named {
				args = append(args, "--name", "held")
			}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != tc.code {
				t.Fatalf("code=%d want=%d out=%s stderr=%s", code, tc.code, out, stderr)
			}
			started := false
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
					started = true
				}
			}
			if started != tc.fresh {
				t.Fatalf("started=%v want=%v out=%s stderr=%s", started, tc.fresh, out, stderr)
			}
			if tc.code == 0 && !tc.fresh {
				want := "held"
				if tc.alternative {
					want = "stronger"
				}
				if !strings.Contains(out, `"reused": true`) || !strings.Contains(out, `"name": "`+want+`"`) {
					t.Fatalf("out=%s", out)
				}
			}
		})
	}
}
