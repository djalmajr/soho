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
	if got := LaneNames(&ctx, env); len(got) != 3 || got[0] != "build" || got[1] != "critical" || got[2] != "review" {
		t.Fatalf("preset lanes = %#v", got)
	}
	if got := LaneRolesCSV(&ctx, "build", env); got != "implementer,designer,tasker,scouter,researcher,documenter,tester" {
		t.Fatalf("build roles = %q", got)
	}
	if got := LaneRolesCSV(&ctx, "critical", env); got != "critical-implementer" {
		t.Fatalf("critical roles = %q", got)
	}
	if LaneCapacity(&ctx, "build", env) != 2 || LaneCapacity(&ctx, "critical", env) != 1 || LaneCapacity(&ctx, "review", env) != 1 || MaxWorkers(&ctx, env) != "4" {
		t.Fatal("4-panel capacity derivation changed")
	}
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "panes=2\n")
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); len(got) != 1 || LaneOfRole(&ctx, "reviewer", env) != "" || LaneOfRole(&ctx, "critical-implementer", env) != "" || LaneCapacity(&ctx, "build", env) != 1 || MaxWorkers(&ctx, env) != "1" {
		t.Fatalf("2-panel preset invalid: lanes=%v reviewer=%q critical=%q max=%s", got, LaneOfRole(&ctx, "reviewer", env), LaneOfRole(&ctx, "critical-implementer", env), MaxWorkers(&ctx, env))
	}
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "panes=2\npane_mode=flex\n")
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); len(got) != 4 || LaneCapacity(&ctx, "critical", env) != 0 || LaneCapacity(&ctx, "review", env) != 0 || LaneCapacity(&ctx, "docs", env) != 0 || MaxWorkers(&ctx, env) != "2" {
		t.Fatalf("flex preset invalid: lanes=%v max=%s", got, MaxWorkers(&ctx, env))
	}
	if PresetSignature("2", "flex") != PresetSignature("4", "flex") || PresetRolesFlat("3", "flex") != "implementer,designer,tasker,scouter,researcher,tester,critical-implementer,reviewer,security-reviewer,ui-reviewer,inspector,validator,documenter" {
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
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build,critical,review" {
		t.Fatalf("overlapping file key lanes=%v", got)
	}
	env["SOHO_LANE_ROLES"] = "x"
	ctx = LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build,critical,review" {
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
	write(t, file, "# keep\npanes=4\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter,tester\nlane.critical.roles=critical-implementer\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,validator\nlane.read.kind=codex\nlane.explore.model=gpt\nmax_workers=8\nsplit_max_panes=9\n")
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

func TestLegacyPresetSignaturesMigrate(t *testing.T) {
	// DJA-686 round 2: a file that carries exactly one of the preset shapes an
	// earlier soho version wrote is a preset file, not a custom one: setup
	// drops its lane roles (keeping the lane kind/model lines), writes panes,
	// and the current preset gives critical-implementer a slot. A file with
	// genuinely different roles stays custom with the warning.
	for _, tc := range []struct {
		name, panes, file string
		keep              []string
	}{
		{name: "a: strict old roles", panes: "3", file: "pane_mode=strict\npanes=3\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector\nlane.build.kind=grok\nlane.review.kind=codex\n", keep: []string{"lane.build.kind=grok", "lane.review.kind=codex"}},
		{name: "a: strict old roles, panes 4", panes: "4", file: "pane_mode=strict\npanes=4\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector\n"},
		{name: "b: strict two-panel shape", panes: "3", file: "pane_mode=strict\npanes=2\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.build.kind=grok\n", keep: []string{"lane.build.kind=grok"}},
		{name: "c: flex old roles", panes: "3", file: "pane_mode=flex\npanes=3\nlane.build.roles=implementer,designer,tasker,scouter,researcher\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector\nlane.docs.roles=documenter\nlane.build.kind=grok\nlane.review.kind=codex\n", keep: []string{"lane.build.kind=grok", "lane.review.kind=codex"}},
		{name: "d: the pre-critical preset (old fixture)", panes: "3", file: "panes=4\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter,tester\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,validator\nlane.read.kind=codex\n", keep: []string{"lane.review.kind=codex"}},
		{name: "e: flex pre-critical roles", panes: "3", file: "pane_mode=flex\npanes=4\nlane.build.roles=implementer,designer,tasker,scouter,researcher,tester\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,validator\nlane.docs.roles=documenter\nlane.build.kind=grok\nlane.review.kind=codex\n", keep: []string{"lane.build.kind=grok", "lane.review.kind=codex"}},
		{name: "a: strict old roles with a lane capacity", panes: "3", file: "pane_mode=strict\npanes=3\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector\nlane.build.panes=2\n", keep: []string{"lane.build.panes=2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := fixture(t)
			file := filepath.Join(repo, ".agents", "soho.conf")
			write(t, file, tc.file)
			oldErr := platform.Stderr
			var buf strings.Builder
			platform.Stderr = &buf
			lines := ApplyLaneFile(file, tc.panes, env, repo)
			platform.Stderr = oldErr
			if strings.Contains(buf.String(), "custom") {
				t.Fatalf("legacy preset was treated as custom: %q", buf.String())
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if FileLaneSignature(file) != "" || FileLaneCount(file) != 0 {
				t.Fatalf("lane roles were not dropped: file=%s", text)
			}
			for _, k := range tc.keep {
				if !containsText(text, k) {
					t.Fatalf("%s was not kept: file=%s", k, text)
				}
			}
			if !containsString(lines, "set panes="+tc.panes) {
				t.Fatalf("panes was not written: lines=%v", lines)
			}
			ctx := LoadConfig(env, repo)
			if got := LaneCapacity(&ctx, "critical", env); got != 1 {
				t.Fatalf("critical-implementer has no slot after the migration: capacity=%d file=%s", got, text)
			}
		})
	}
	t.Run("genuinely different roles stay custom with the warning", func(t *testing.T) {
		env, repo := fixture(t)
		file := filepath.Join(repo, ".agents", "soho.conf")
		write(t, file, "pane_mode=strict\npanes=3\nlane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\nlane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,qa\n")
		oldErr := platform.Stderr
		var buf strings.Builder
		platform.Stderr = &buf
		lines := ApplyLaneFile(file, "3", env, repo)
		platform.Stderr = oldErr
		if !strings.Contains(buf.String(), "custom") {
			t.Fatalf("the custom warning is missing: %q lines=%v", buf.String(), lines)
		}
		raw, _ := os.ReadFile(file)
		if !containsText(string(raw), "lane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,qa") {
			t.Fatalf("the custom roles were not kept: %s", raw)
		}
	})
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

func TestLaneOfRoleForTesterAndValidatorPresets(t *testing.T) {
	// DJA-695: the tester sits in the build lane and the validator in the review
	// lane of every preset that has those lanes (strict and flex, panes 2/3/4);
	// the strict 2-panel preset has no review lane, so the validator stays out
	// of the lanes exactly like the other review roles.
	env, repo := fixture(t)
	for _, tc := range []struct {
		panes, mode, role, want string
	}{
		{"4", "strict", "tester", "build"},
		{"4", "strict", "validator", "review"},
		{"3", "strict", "tester", "build"},
		{"3", "strict", "validator", "review"},
		{"2", "strict", "tester", "build"},
		{"2", "strict", "validator", ""},
		{"4", "flex", "tester", "build"},
		{"4", "flex", "validator", "review"},
		{"3", "flex", "tester", "build"},
		{"3", "flex", "validator", "review"},
		{"2", "flex", "tester", "build"},
		{"2", "flex", "validator", "review"},
	} {
		t.Run(tc.panes+"/"+tc.mode+"/"+tc.role, func(t *testing.T) {
			conf := "panes=" + tc.panes
			if tc.mode == "flex" {
				conf += "\npane_mode=flex"
			}
			write(t, filepath.Join(repo, ".agents", "soho.conf"), conf)
			ctx := LoadConfig(env, repo)
			if got := LaneOfRole(&ctx, tc.role, env); got != tc.want {
				t.Fatalf("LaneOfRole(%s) panes=%s mode=%s = %q want %q", tc.role, tc.panes, tc.mode, got, tc.want)
			}
		})
	}
}

func TestReviewLaneLocksAnEditorForTheValidator(t *testing.T) {
	// DJA-695: the validator is a review role (ReviewRolesAll), so the
	// edit-history lock refuses it on a worker that already wrote code, and a
	// worker without an edit history is a valid candidate (control).
	env, repo := fixture(t)
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.review.roles=reviewer,validator\n")
	bin := t.TempDir()
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "get", "fresh"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
	})
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
	decision := LaneDecide(&ctx, "review", "validator", env, repo, true)
	if decision.Decision != "locked" || decision.Name != "worker" || decision.N != 1 || len(decision.Occupants) != 1 || decision.Occupants[0] != "worker" {
		t.Fatalf("validator reuse decision = %#v", decision)
	}
	RosterAppend(state, []string{"fresh", "pane-2", "codex", "reviewer", "openai", "pane-2", repo, "now", "gpt-5", "task", "reviewer", "review"})
	decision = LaneDecide(&ctx, "review", "validator", env, repo, true)
	if decision.Decision != "reuse" || decision.Name != "fresh" || decision.Candidate != "fresh" {
		t.Fatalf("validator control decision = %#v", decision)
	}
}

func TestCriticalLanePresetsMatrix(t *testing.T) {
	// DJA-686: the critical lane ships with the strict 3- and 4-panel presets
	// and with every flex preset, holding only critical-implementer at
	// capacity 1 (0 in flex panes=2, the review rule). Strict panes=2 keeps
	// the minimal team: the lane is absent there, and MaxWorkers follows the
	// capacities without any explicit value.
	env, repo := fixture(t)
	for _, tc := range []struct {
		panes, mode string
		present     bool
		capacity    int
		workers     string
	}{
		{"4", "strict", true, 1, "4"},
		{"3", "strict", true, 1, "3"},
		{"2", "strict", false, 0, "1"},
		{"4", "flex", true, 1, "5"},
		{"3", "flex", true, 1, "4"},
		{"2", "flex", true, 0, "2"},
	} {
		t.Run(tc.panes+"/"+tc.mode, func(t *testing.T) {
			conf := "panes=" + tc.panes
			if tc.mode == "flex" {
				conf += "\npane_mode=flex"
			}
			write(t, filepath.Join(repo, ".agents", "soho.conf"), conf)
			ctx := LoadConfig(env, repo)
			found := ""
			for _, lane := range LaneNames(&ctx, env) {
				if lane == "critical" {
					found = lane
				}
			}
			if (found != "") != tc.present {
				t.Fatalf("critical lane present=%v lanes=%v", found != "", LaneNames(&ctx, env))
			}
			if got := LaneOfRole(&ctx, "critical-implementer", env); tc.present && got != "critical" {
				t.Fatalf("LaneOfRole(critical-implementer)=%q want critical", got)
			}
			if tc.present {
				if got := LaneRolesCSV(&ctx, "critical", env); got != "critical-implementer" {
					t.Fatalf("critical roles=%q", got)
				}
				if got := LaneCapacity(&ctx, "critical", env); got != tc.capacity {
					t.Fatalf("critical capacity=%d want %d", got, tc.capacity)
				}
			}
			if got := MaxWorkers(&ctx, env); got != tc.workers {
				t.Fatalf("MaxWorkers=%q want %q", got, tc.workers)
			}
		})
	}
}

func TestCriticalLaneFileMigrationAndKindResolution(t *testing.T) {
	// DJA-686: the critical lane is single-role, so MigrateLaneAttr treats
	// critical-implementer like planner and documenter: doctor --fix / setup
	// never write lane.critical.* for it on a clean project (first_run stays
	// true), and a user-written lane.critical.kind survives the fix and
	// resolves the spawn kind.
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	clean := ApplyLaneFile(file, "4", env, repo)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if containsText(string(raw), "lane.critical") {
		t.Fatalf("clean project fix wrote the critical lane: file=%s lines=%v", raw, clean)
	}
	if !containsText(string(raw), "panes=4") || !containsText(string(raw), "reuse_workers=on") {
		t.Fatalf("fix output missing the base keys: file=%s", raw)
	}
	write(t, file, "lane.critical.kind=claude\n")
	lines := ApplyLaneFile(file, "4", env, repo)
	raw, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(string(raw), "lane.critical.kind=claude") {
		t.Fatalf("user-set lane.critical.kind removed: file=%s lines=%v", raw, lines)
	}
	for _, line := range lines {
		if containsText(line, "removed lane.critical") {
			t.Fatalf("user-set lane.critical.kind reported removed: %v", lines)
		}
	}
	ctx := LoadConfig(env, repo)
	res := ResolveRoleSettings("critical-implementer", &ctx, env, repo, RoleFlags{})
	if res.Lane != "critical" || res.Kind != "claude" || !strings.HasPrefix(res.KindFrom, "lane critical") {
		t.Fatalf("resolved settings=%+v", res)
	}
}

func TestReviewLaneLocksACriticalImplementer(t *testing.T) {
	// DJA-686: a worker that edited as critical-implementer never takes a
	// review role. The skill role dir is emptied so the EditRoles word list,
	// not the role file's mode: edit, is the mechanism under test; a worker
	// without an edit history stays a valid candidate (control).
	env, repo := fixture(t)
	env["SOHO_SKILL_DIR"] = t.TempDir()
	write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.review.roles=reviewer\n")
	bin := t.TempDir()
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "get", "fresh"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
	})
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
	RosterAppend(state, []string{"worker", "pane-1", "codex", "critical-implementer", "openai", "pane-1", repo, "now", "gpt-5", "task", "critical-implementer", "review"})
	decision := LaneDecide(&ctx, "review", "reviewer", env, repo, true)
	if decision.Decision != "locked" || decision.Name != "worker" || decision.N != 1 || len(decision.Occupants) != 1 || decision.Occupants[0] != "worker" {
		t.Fatalf("critical-implementer reuse decision = %#v", decision)
	}
	RosterAppend(state, []string{"fresh", "pane-2", "codex", "reviewer", "openai", "pane-2", repo, "now", "gpt-5", "task", "reviewer", "review"})
	decision = LaneDecide(&ctx, "review", "reviewer", env, repo, true)
	if decision.Decision != "reuse" || decision.Name != "fresh" || decision.Candidate != "fresh" {
		t.Fatalf("control decision = %#v", decision)
	}
}

func TestCustomCriticalLaneLeftInPlace(t *testing.T) {
	// DJA-686: a project with its own critical lane (lane.critical.roles, the
	// Kashes shape) stays custom: the preset never applies, the fix keeps
	// every lane.critical.* key, and the derived limits follow the file's
	// capacities, exactly as before the lane shipped in the preset.
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "panes=4\nlane.build.roles=implementer,designer,tasker\nlane.critical.roles=critical-implementer\nlane.critical.panes=2\nlane.critical.kind=codex\nlane.critical.model=gpt-6.1-sol\nlane.critical.effort=medium\nlane.review.roles=reviewer,security-reviewer\nmax_workers=9\nsplit_max_panes=10\n")
	lines := ApplyLaneFile(file, "4", env, repo)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lane.critical.roles=critical-implementer", "lane.critical.panes=2", "lane.critical.kind=codex", "lane.critical.model=gpt-6.1-sol", "lane.critical.effort=medium", "max_workers=4", "split_max_panes=5"} {
		if !containsText(string(raw), key) {
			t.Fatalf("custom critical file lost %q: file=%s lines=%v", key, raw, lines)
		}
	}
	ctx := LoadConfig(env, repo)
	if got := LaneNames(&ctx, env); strings.Join(got, ",") != "build,critical,review" {
		t.Fatalf("custom lanes=%v", got)
	}
	if got := LaneOfRole(&ctx, "critical-implementer", env); got != "critical" {
		t.Fatalf("LaneOfRole(critical-implementer)=%q", got)
	}
	if got := LaneCapacity(&ctx, "critical", env); got != 2 {
		t.Fatalf("critical capacity=%d want 2", got)
	}
}

func TestProjectCriticalRoleWinsOverSkill(t *testing.T) {
	// DJA-686: a project .agents/soho-roles/critical-implementer.md wins over
	// the shipped skill role, and the project append still attaches.
	env, repo := fixture(t)
	rolesDir := filepath.Join(repo, ".agents", "soho-roles")
	project := filepath.Join(rolesDir, "critical-implementer.md")
	write(t, project, "---\nname: critical-implementer\nkind: grok\nmode: edit\n---\nProject body wins.\n")
	if got := RoleFile("critical-implementer", env, repo); got != project {
		t.Fatalf("RoleFile=%q want %q", got, project)
	}
	if got := RoleBodyFor("critical-implementer", env, repo); got != "Project body wins.\n" {
		t.Fatalf("body=%q", got)
	}
	write(t, filepath.Join(rolesDir, "critical-implementer.append.md"), "---\n---\nAppend block.\n")
	if got := RoleBodyFor("critical-implementer", env, repo); got != "Project body wins.\n\nAppend block.\n" {
		t.Fatalf("appended body=%q", got)
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
