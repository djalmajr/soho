package job

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// R413 F1: a max_workers that soho config derives from the lanes (source
// lanes) already counts the job lane, so EnsureJobLane must not raise it.
func TestEnsureJobLaneKeepsALaneDerivedMaxWorkers(t *testing.T) {
	f := newTeamOpsFix(t, nil)
	f.env["HOME"] = t.TempDir()
	f.env["XDG_CONFIG_HOME"] = t.TempDir()
	f.env["SOHO_DIR"] = t.TempDir()
	f.env["SOHO_SKILL_DIR"] = filepath.Join(f.dir, "missing-skill")
	ctx := core.Config{Entries: map[string]core.ConfigEntry{
		"max_workers":      {Value: "3", Source: "defaults", Original: "max_workers"},
		"lane_build_roles": {Value: "implementer", Source: "project", Original: "lane.build.roles"},
		"lane_job_roles":   {Value: "job-orchestrator", Source: "session", Original: "lane.job.roles"},
		"lane_job_panes":   {Value: "1", Source: "session", Original: "lane.job.panes"},
	}, Order: []string{"max_workers", "lane_build_roles", "lane_job_roles", "lane_job_panes"}}
	var out bytes.Buffer
	saved := platform.Stdout
	platform.Stdout = &out
	core.CmdConfig(&ctx, f.env, f.dir)
	platform.Stdout = saved
	if n, explicit, err := parseMaxWorkers(out.String()); err != nil || explicit || n != 2 {
		t.Fatalf("parseMaxWorkers(real listing) = %d explicit=%v err=%v; want 2, derived", n, explicit, err)
	}
	consumer := newTeamOpsFix(t, []fakecli.Rule{
		{Argv: []string{"session", "set", "lane.job.roles", "job-orchestrator"}},
		{Argv: []string{"session", "set", "lane.job.panes", "1"}},
		{Argv: []string{"config"}, Stdout: out.String()},
		{Argv: []string{"session", "set", "max_workers", "3"}},
	})
	if err := consumer.cli().EnsureJobLane(); err != nil {
		t.Fatal(err)
	}
	if calls := consumer.calls(); len(calls) != 3 {
		t.Fatalf("a lane-derived max_workers must stay derived; last call %v", calls[len(calls)-1].Argv)
	}
}
