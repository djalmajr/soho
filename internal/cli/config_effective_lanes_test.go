package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func configRows(out, key string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == key {
			rows = append(rows, fields)
		}
	}
	return rows
}

func TestConfigEffectiveLanes(t *testing.T) {
	for _, tc := range []struct {
		name, extra, wantValue, wantSource string
		wantPlanners                       bool
	}{
		{"derived", "", "5", "lanes", true},
		{"explicit", "max_workers=3\n", "3", "user", true},
		{"disabled", "lanes=off\n", "3", "defaults", false},
		{"flex", "pane_mode=flex\nflex_extra=2\n", "7", "lanes", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := commandFixture(t)
			// Doctor cannot discover or execute a real Herdr or hc binary.
			env["PATH"] = t.TempDir()
			writeConfigFile(t, platform.UserConfigPath(platform.Current(), env), "lane.builders.kind=pi\nlane.reviewers.kind=pi\n"+tc.extra)
			code, listing, errOut := runIn(t, []string{"config"}, env, cwd)
			if code != 0 || errOut != "" {
				t.Fatalf("config code=%d err=%q", code, errOut)
			}
			rows := configRows(listing, "max_workers")
			if len(rows) != 1 || len(rows[0]) != 3 || rows[0][1] != tc.wantValue || rows[0][2] != tc.wantSource {
				t.Errorf("max_workers rows=%v want value=%s source=%s", rows, tc.wantValue, tc.wantSource)
			}
			planners := configRows(listing, "lane.planners.roles")
			if tc.wantPlanners {
				if len(planners) != 1 || strings.Join(planners[0], " ") != "lane.planners.roles planner,sub-orchestrator,job-orchestrator skill default" {
					t.Errorf("planners rows=%v", planners)
				}
			} else if len(planners) != 0 || len(configRows(listing, "lane.builders.roles")) != 0 || len(configRows(listing, "lane.reviewers.roles")) != 0 {
				t.Errorf("disabled lanes added preset rows: %v", planners)
			}
			for _, key := range []string{"lane.builders.kind", "lane.reviewers.kind"} {
				if got := configRows(listing, key); len(got) != 1 || strings.Join(got[0], " ") != key+" pi user" {
					t.Errorf("configured row %s = %v", key, got)
				}
			}
			if len(configRows(listing, "lane.builders.roles")) != 0 || len(configRows(listing, "lane.reviewers.roles")) != 0 {
				t.Error("preset row duplicated an already listed lane")
			}
			code, out, errOut := runIn(t, []string{"config", "get", "max_workers"}, env, cwd)
			if code != 0 || out != tc.wantValue+"\n" || errOut != "" {
				t.Errorf("config get code=%d out=%q err=%q want=%s", code, out, errOut, tc.wantValue)
			}
			if tc.name == "derived" {
				code, out, errOut = runIn(t, []string{"doctor"}, env, cwd)
				want := "config: max_workers=5 matches the lanes (builders=3 reviewers=1 planners=1)"
				if code != 0 || !strings.Contains(out, want) {
					t.Errorf("doctor code=%d err=%q missing %q in %q", code, errOut, want, out)
				}
			}
		})
	}
}

func TestConfigLaneRowsRespectDottedEnvAndCustomLanes(t *testing.T) {
	t.Run("env lane key", func(t *testing.T) {
		env, cwd := commandFixture(t)
		env["PATH"] = t.TempDir()
		env["SOHO_LANE_PLANNERS_KIND"] = "pi"
		code, listing, errOut := runIn(t, []string{"config"}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("config code=%d err=%q", code, errOut)
		}
		if rows := configRows(listing, "lane.planners.kind"); len(rows) != 1 || strings.Join(rows[0], " ") != "lane.planners.kind pi env" {
			t.Errorf("env row=%v", rows)
		}
		if rows := configRows(listing, "lane.planners.roles"); len(rows) != 0 {
			t.Errorf("duplicated lane=%v", rows)
		}
		if rows := configRows(listing, "lane.builders.roles"); len(rows) != 1 || rows[0][len(rows[0])-2] != "skill" || rows[0][len(rows[0])-1] != "default" {
			t.Errorf("missing default builders row=%v", rows)
		}
	})
	t.Run("custom lanes", func(t *testing.T) {
		env, cwd := commandFixture(t)
		env["PATH"] = t.TempDir()
		writeConfigFile(t, filepath.Join(cwd, ".agents", "soho.conf"), "lane.my-team.roles=implementer\n")
		code, listing, errOut := runIn(t, []string{"config"}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("config code=%d err=%q", code, errOut)
		}
		if rows := configRows(listing, "lane.my-team.roles"); len(rows) != 1 || strings.Join(rows[0], " ") != "lane.my-team.roles implementer project" {
			t.Errorf("custom row=%v", rows)
		}
		if rows := configRows(listing, "lane.planners.roles"); len(rows) != 0 {
			t.Errorf("preset row for custom lanes=%v", rows)
		}
	})
}
