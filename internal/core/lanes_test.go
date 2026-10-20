package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestLanePresetsCapacityAndNames(t *testing.T) {
	// JS: "presets 2/3/4: lanes, roles, capacities, and custom lanes"
	// Mutation captured: changing the 4-panel build lane capacity or moving review roles into the build lane changes these observable assignments.
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); len(got) != 2 || got[0] != "build" || got[1] != "review" {
		t.Fatalf("preset lanes = %#v", got)
	}
	if got := LaneRolesCSV(&ctx, "build", env); got != "implementer,designer,tasker,scouter,researcher,documenter" {
		t.Fatalf("build roles = %q", got)
	}
	if LaneCapacity(&ctx, "build", env) != 2 || LaneCapacity(&ctx, "review", env) != 1 || MaxWorkers(&ctx, env) != "3" {
		t.Fatal("4-panel capacity derivation changed")
	}
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "panes=2\n")
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); len(got) != 1 || LaneOfRole(&ctx, "reviewer", env) != "" || LaneCapacity(&ctx, "build", env) != 1 || MaxWorkers(&ctx, env) != "1" {
		t.Fatalf("2-panel preset invalid: lanes=%v reviewer=%q max=%s", got, LaneOfRole(&ctx, "reviewer", env), MaxWorkers(&ctx, env))
	}
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "panes=2\npane_mode=flex\n")
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); len(got) != 3 || LaneCapacity(&ctx, "review", env) != 0 || LaneCapacity(&ctx, "docs", env) != 0 || MaxWorkers(&ctx, env) != "2" {
		t.Fatalf("flex preset invalid: lanes=%v max=%s", got, MaxWorkers(&ctx, env))
	}
	if PresetSignature("2", "flex") != PresetSignature("4", "flex") || PresetRolesFlat("3", "flex") != "implementer,designer,tasker,scouter,researcher,reviewer,security-reviewer,ui-reviewer,inspector,documenter" {
		t.Fatal("flex preset signature/role flattening changed")
	}
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.ops.roles=implementer tasker\nlane.ops.panes=3\n")
	ctx = LoadConfig(env, repo)
	if LaneOfRole(&ctx, "tasker", env) != "ops" || LaneOfRole(&ctx, "scouter", env) != "" || LaneCapacity(&ctx, "ops", env) != 3 {
		t.Fatal("custom lane did not replace preset or apply capacity")
	}
	env["SOHO_LANE_B_ROLES"] = "scouter"
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); len(got) != 2 || got[0] != "b" || got[1] != "ops" {
		t.Fatalf("file/env custom lane sort = %#v", got)
	}
}

func TestLaneNamesRequireNonOverlappingRoleSuffix(t *testing.T) {
	// Mutation captured: prefix/suffix matching without a middle lane name suppresses preset lanes.
	env, repo := fixture(t)
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.roles=x\n")
	ctx := LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build,review" {
		t.Fatalf("overlapping file key lanes=%v", got)
	}
	env["SOHO_LANE_ROLES"] = "x"
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build,review" {
		t.Fatalf("overlapping env key lanes=%v", got)
	}
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.build.roles=x\n")
	delete(env, "SOHO_LANE_ROLES")
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build" {
		t.Fatalf("valid file lane=%v", got)
	}
	env["SOHO_LANE_BUILD_ROLES"] = "x"
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build" {
		t.Fatalf("valid env lane=%v", got)
	}
}

func TestLaneLayerRulesAndSpecs(t *testing.T) {
	// JS: "layer rule: cases 1-4, 7, 8 and the --kind flag"
	// JS: "setupLaneSpec: valid specs and every rejection (code 2)"
	// Mutation captured: a model or effort from below the effective kind layer must be discarded, and an explicit kind flag has rank 5.
	env, repo := fixture(t)
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.ops.roles=implementer\nlane.ops.kind=pi\nlane.ops.model=project-model\n")
	ctx := LoadConfig(env, repo)
	rank := SpawnKindLayer(&ctx, "implementer", "ops", false, env)
	if rank != 2 || LaneAttr(&ctx, "ops", "model", nil, env) != "project-model" {
		t.Fatalf("lane layer = %d model=%q", rank, LaneAttr(&ctx, "ops", "model", nil, env))
	}
	user := platform.UserConfigPath(platform.Current(), env)
	write(t, user, "lane.ops.model=user-model\n")
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.ops.roles=implementer\nlane.ops.kind=pi\n")
	ctx = LoadConfig(env, repo)
	if LaneAttr(&ctx, "ops", "model", &rank, env) != "" {
		t.Fatal("lower-layer model was not dropped")
	}
	rank = SpawnKindLayer(&ctx, "implementer", "ops", true, env)
	if rank != 5 || LaneAttr(&ctx, "ops", "model", &rank, env) != "" {
		t.Fatal("flag kind did not suppress lower model")
	}
	for _, spec := range []string{"review=claude:model:high", "docs=pi::xhigh", "ops=grok:model::"} {
		got, err := SetupLaneSpec(spec)
		if err != nil || got.Name == "" || got.Kind == "" {
			t.Fatalf("SetupLaneSpec(%q) = %#v, %v", spec, got, err)
		}
	}
	for _, spec := range []string{"build", "Build=codex", "build=", "build=nope", "build=codex:m:huge", "build=codex:g#x", "build=codex:m:f:extra"} {
		if _, err := SetupLaneSpec(spec); err == nil {
			t.Errorf("SetupLaneSpec(%q) accepted invalid value", spec)
		}
	}
}

func TestLaneFileMigrationAndSignatures(t *testing.T) {
	// JS: "applyLaneFile: a preset file drops the roles, the old lanes and the derived keys"
	// JS: "applyLaneFile: lane.read.<attr> moves to lane.review.<attr>, an existing one wins"
	// JS: "applyLaneFile: a custom file keeps its lanes and aligns max_workers to the capacities"
	// JS: "applyLaneFile: any other lane outside the preset is removed, comments stay"
	// JS: "applyLaneFile: the old presets are treated as preset files (not custom)"
	// Mutation captured: a preset reconfiguration must stop freezing lane roles, while custom lanes retain roles and capacity.
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "# keep\npanes=4\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector\nlane.read.kind=codex\nlane.explore.model=gpt\nmax_workers=8\nsplit_max_panes=9\n")
	lines := ApplyLaneFile(file, "3", env, repo)
	raw, _ := os.ReadFile(file)
	text := string(raw)
	if FileLaneCount(file) != 0 || FileLaneSignature(file) != "" || !containsString(lines, "set panes=3") || containsText(text, "lane.build.roles=") || !containsText(text, "lane.review.kind=codex") || containsText(text, "max_workers=") || !containsText(text, "# keep") {
		t.Fatalf("preset migration failed: lines=%v file=%s", lines, text)
	}
	write(t, file, "lane.ops.roles=implementer,tasker\nlane.ops.panes=2\n")
	// A custom lane of two workers: the caller's tab holds the caller plus the team (1 + 2).
	lines = ApplyLaneFile(file, "4", env, repo)
	raw, _ = os.ReadFile(file)
	text = string(raw)
	if FileLaneCount(file) != 1 || !containsText(text, "lane.ops.roles=implementer,tasker") || !containsText(text, "max_workers=2") || !containsText(text, "split_max_panes=3") || !containsString(lines, "set max_workers=2") || !containsString(lines, "set split_max_panes=3") {
		t.Fatalf("custom migration failed: lines=%v file=%s", lines, text)
	}
}

func TestApplyLaneFileCreatesConfigWithDefaultFileMode(t *testing.T) {
	// Mutation captured: creating a missing lane config with 0600 diverges from Node's append-created file mode.
	env, repo := fixture(t)
	dest := filepath.Join(repo, "dest", "herdr-soho.conf")
	ApplyLaneFile(dest, "4", env, repo)
	got, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(repo, "dest", "append-mode-control")
	f, err := os.OpenFile(control, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(control)
	if err != nil || got.Mode().Perm() != want.Mode().Perm() {
		t.Fatalf("config mode=%o, append-created control mode=%o, err=%v", got.Mode().Perm(), want.Mode().Perm(), err)
	}
}

// TestApplyLaneFileGolden used these seeds and checked for leftover temp files
// independently of the historical output/file comparison.
func TestApplyLaneFileRetirementTempContract(t *testing.T) {
	seeds := []struct{ name, panes, file string }{
		{"empty", "4", ""},
		{"preset3-to-4", "4", "lane.build.roles=implementer,designer,tasker\nlane.read.roles=scouter,researcher,reviewer,security-reviewer,ui-reviewer,inspector\n"},
		{"preset4-to-3", "3", "lane.build.roles=implementer,designer,tasker\nlane.explore.roles=scouter,researcher\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector\n"},
		{"custom", "4", "lane.ops.roles=implementer,tasker\n"},
		{"unanimous-kind", "4", "role.implementer.kind=grok\nrole.designer.kind=grok\nrole.tasker.kind=grok\n"},
		{"divergent-kind", "4", "role.implementer.kind=grok\nrole.designer.kind=agy\n"},
		{"planner-keys", "4", "role.planner.kind=codex\nrole.planner.model=gpt-5\nrole.implementer.kind=grok\nrole.designer.kind=grok\nrole.tasker.kind=grok\n"},
		{"existing-lane-kind", "4", "lane.build.kind=codex\nrole.implementer.kind=grok\nrole.designer.kind=grok\nrole.tasker.kind=grok\n"},
		{"unanimous-model", "4", "lane.build.kind=agy\nrole.implementer.model=m1\nrole.designer.model=m1\nrole.tasker.model=m1\n"},
		{"custom-space-roles", "4", "lane.x.roles=implementer designer\nrole.implementer.kind=grok\nrole.designer.kind=grok\n"},
	}
	for _, sc := range seeds {
		t.Run(sc.name, func(t *testing.T) {
			env, repo := fixture(t)
			env["USERPROFILE"] = env.Get("HOME")
			env["TMPDIR"] = t.TempDir()
			dest := filepath.Join(filepath.Dir(repo), "dest", "herdr-soho.conf")
			if sc.file != "" {
				write(t, dest, sc.file)
			} else if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			ApplyLaneFile(dest, sc.panes, env, repo)
			for _, dir := range []string{filepath.Dir(dest), env.Get("TMPDIR")} {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "soho-conf.") || (strings.HasPrefix(entry.Name(), ".soho.conf.") && strings.HasSuffix(entry.Name(), ".tmp")) {
						t.Fatalf("temporary file remains: %s", filepath.Join(dir, entry.Name()))
					}
				}
			}
		})
	}
}

func TestReviewLaneLocksAnEditor(t *testing.T) {
	// JS: "laneDecide: full lane (busy, --fresh candidate, locked), pending-report, reuse by name"
	// Mutation captured: removing the edit-history lock would incorrectly allow a reviewer to reuse a worker that wrote code.
	env, repo := fixture(t)
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.review.roles=reviewer\n")
	bin := t.TempDir()
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	ctx := LoadConfig(env, repo)
	state := StateDir(&ctx, env, repo)
	RosterAppend(state, []string{"worker", "pane-1", "grok", "implementer", "xai", "pane-1", repo, "now", "grok-4.7", "task", "implementer", "review"})
	decision := LaneDecide(&ctx, "review", "reviewer", env, repo, true)
	if decision.Decision != "locked" || decision.Name != "worker" || decision.N != 1 || len(decision.Occupants) != 1 || decision.Occupants[0] != "worker" {
		t.Fatalf("review reuse decision = %#v", decision)
	}
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
func containsText(value, needle string) bool { return strings.Contains(value, needle) }

func TestRosterRenameChangesOnlyTheNameColumn(t *testing.T) {
	// A4.1: the roster half of the spawn rename keeps every column after the name.
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	state := StateDir(&ctx, env, repo)
	row := "review-2\tw0test:p0a\tcodex\treviewer\topenai\t1\t/tmp/work\tnow\t\task\treviewer\treview\t\t"
	RosterAppend(state, strings.Split(row, "\t"))
	RosterRename(state, "review-2", "review")
	got := RosterLine(state, "review")
	if got == "" || strings.Split(got, "\t")[0] != "review" {
		t.Fatalf("renamed line missing: %q", got)
	}
	if want := "review\tw0test:p0a\tcodex\treviewer\topenai\t1\t/tmp/work\tnow\t\task\treviewer\treview\t\t"; got != want {
		t.Fatalf("roster line=%q want %q", got, want)
	}
	if RosterLine(state, "review-2") != "" {
		t.Fatalf("old name still in roster: %q", RosterLine(state, "review-2"))
	}
}
