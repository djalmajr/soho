package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnTruncatedRosterApprovalRequests(t *testing.T) {
	for _, lanes := range []string{"off", "on"} {
		for _, role := range []string{"worker", "scouter"} {
			for _, requested := range []string{"", "ask", "edits", "auto", "full", "unsandboxed"} {
				name := requested
				if name == "" {
					name = "default"
				}
				t.Run(lanes+"/"+role+"/"+name, func(t *testing.T) {
					held := "held"
					if lanes == "on" {
						held = "build"
					} // Historical lane rows use the lane name.
					f := newSpawnFixture(t, append([]fakecli.Rule{idleHerdrRule(held)}, freshSpawnRules()...))
					configureSpawnFixture(t, &f)
					for _, r := range []string{"worker", "scouter"} {
						if err := os.WriteFile(filepath.Join(f.root, "roles", r+".md"), []byte("---\nkind: grok\neffort: high\n---\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					f.env["SOHO_LANES"] = lanes
					f.env["SOHO_LANE_BUILD_ROLES"] = "worker,scouter"
					f.env["SOHO_LANE_BUILD_KIND"] = "grok"
					f.env["SOHO_LANE_BUILD_PANES"] = "2"
					code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--name", held, "--fresh", "--approvals", "ask"})
					if code != 0 {
						t.Fatalf("initial ask: code=%d out=%s stderr=%s", code, out, stderr)
					}
					firstStarts := 0
					for _, call := range calls {
						if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
							firstStarts++
							for _, arg := range call.Argv {
								if arg == "--permission-mode" || arg == "--always-approve" {
									t.Fatalf("initial ask args=%q", call.Argv)
								}
							}
						}
					}
					row := strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), held), "\t")
					if firstStarts != 1 || len(row) < 10 || row[9] != "ask" {
						t.Fatalf("initial starts=%d roster=%q", firstStarts, row)
					}
					truncated := strings.Join(row[:8], "\t")
					if len(strings.Split(truncated, "\t")) != 8 {
						t.Fatal("fixture must have exactly eight columns")
					}
					f.roster(t, truncated)
					before := len(calls)
					args := []string{role}
					if requested != "" {
						args = append(args, "--approvals", requested)
					}
					code, out, stderr, calls = runCmdSpawn(t, f, args)
					// An eight-column row cannot establish opening effort,
					// even for explicit ask or the same role.
					reuse := false
					starts := 0
					for _, call := range calls[before:] {
						if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
							starts++
						}
					}
					var result struct {
						Name, Approvals string
						Reused          bool
					}
					if err := json.Unmarshal([]byte(out), &result); err != nil {
						t.Fatalf("code=%d JSON=%q stderr=%s: %v", code, out, stderr, err)
					}
					wantStarts := 1
					if reuse {
						wantStarts = 0
					}
					if code != 0 || result.Reused != reuse || starts != wantStarts {
						t.Fatalf("requested=%q lanes=%s role=%s code=%d reused=%v want=%v starts=%d want=%d out=%s stderr=%s", requested, lanes, role, code, result.Reused, reuse, starts, wantStarts, out, stderr)
					}
					if !reuse {
						mode := requested
						if mode == "" {
							mode = "auto"
						}
						if result.Approvals != mode {
							t.Fatalf("new worker mode=%q want=%q out=%s", result.Approvals, mode, out)
						}
						newRow := strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), result.Name), "\t")
						if len(newRow) < 10 || newRow[9] != mode {
							t.Fatalf("new worker roster=%q want=%s", newRow, mode)
						}
					}
				})
			}
		}
	}
}

func TestReuseUnsandboxedApprovalRank(t *testing.T) {
	for _, role := range []string{"implementer", "scouter"} {
		for _, tc := range []struct {
			held, requested string
			reuse           bool
		}{
			{"edits", "", false},
			{"auto", "", true},
			{"edits", "auto", false},
			{"", "auto", false},
			{"legacy", "auto", false},
			{"full", "auto", true},
			{"auto", "full", false},
			{"auto", "auto", true},
			{"full", "unsandboxed", false},
			{"unsandboxed", "full", true},
			{"unsandboxed", "unsandboxed", true},
		} {
			t.Run(role+"/"+tc.held+"-to-"+tc.requested, func(t *testing.T) {
				f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
				f.roster(t, spawnRosterRow("worker", "implementer", "grok-4.7", tc.held, ""))
				got, unavailable, blocked := FindReusable(role, "grok", "/tmp/work", "", "grok-4.7", tc.requested, "high", f.ctx, f.env, f.cwd)
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
		{"default-opens-new", "edits", "", "2", false, false, true, 0},
		{"default-missing-approval-opens-new", "", "", "2", false, false, true, 0},
		{"auto-opens-new", "edits", "auto", "2", false, false, true, 0},
		{"auto-missing-approval-opens-new", "", "auto", "2", false, false, true, 0},
		{"auto-unknown-approval-opens-new", "legacy", "auto", "2", false, false, true, 0},
		{"legacy-missing-approval-still-reuses-full", "", "full", "1", false, false, false, 0},
		{"auto-reuses-full", "full", "auto", "1", false, false, false, 0},
		{"auto-cannot-satisfy-full", "auto", "full", "1", false, false, false, 10},
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
			front := "---\nkind: codex\napprovals: ask\n---\n"
			if tc.requested == "" {
				front = "---\nkind: codex\n---\n"
			}
			if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte(front), 0o600); err != nil {
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
