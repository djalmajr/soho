package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"

	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestTM9CoreCases(t *testing.T) {
	t.Run("brief_lint_aliases: a scalar key with free one-line text (empty default)", func(t *testing.T) { // JS: "brief_lint_aliases: a scalar key with free one-line text (empty default)"
		env, repo := fixture(t)
		ctx := LoadConfig(env, repo)
		if !strings.HasPrefix(Cfg(&ctx, "brief_lint_aliases", "", env), "Expected result=Aceite") || !ConfigKeyOk("brief_lint_aliases") || !ConfigValueOk("brief_lint_aliases", "Goal, Acceptance", env, repo) || ConfigValueOk("brief_lint_aliases", "a#b", env, repo) || ConfigValueOk("brief_lint_aliases", "a\nb", env, repo) {
			t.Fatal("brief_lint_aliases scalar/default/one-line contract changed")
		}
	})
	t.Run("configValueOk: the args keys take any one-line value, dash-led included", func(t *testing.T) { // JS: "configValueOk: the args keys take any one-line value, dash-led included"
		env, repo := fixture(t)
		if !ConfigValueOk("role.reviewer.args", "-c a=b", env, repo) || !ConfigValueOk("args.codex", "-c a=b", env, repo) || ConfigValueOk("lane.build.args", "a#b", env, repo) || ConfigValueOk("args.codex", "a\nb", env, repo) {
			t.Fatal("args config values no longer preserve one-line dash-led input")
		}
	})
	t.Run("configFileFor resolves user vs project", func(t *testing.T) { // JS: "configFileFor resolves user vs project"
		env, repo := fixture(t)
		repo, err := filepath.EvalSymlinks(repo)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := ConfigFileFor("user", env, repo), filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"); got != want {
			t.Fatalf("user config path=%q want %q", got, want)
		}
		if got, want := ConfigFileFor("project", env, repo), filepath.Join(repo, ".agents", "soho.conf"); got != want {
			t.Fatalf("project config path=%q want %q", got, want)
		}
	})
	t.Run("lanes only via the SOHO_LANE_<X>_ROLES env var", func(t *testing.T) { // JS: "lanes only via the HERDR_SOHO_LANE_<X>_ROLES env var"
		env, repo := fixture(t)
		env["SOHO_LANE_OPS_ROLES"] = "implementer"
		ctx := LoadConfig(env, repo)
		if got := strings.Join(LaneNames(&ctx, env), ","); got != "ops" || LaneOfRole(&ctx, "implementer", env) != "ops" {
			t.Fatalf("env lane resolution=%q role lane=%q", got, LaneOfRole(&ctx, "implementer", env))
		}
	})
	t.Run("laneNames: sort -u order (C locale) and only effective non-empty roles", func(t *testing.T) { // JS: "laneNames: sort -u order (C locale) and only effective non-empty roles"
		env, repo := fixture(t)
		write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.z.roles=tasker\nlane.a.roles=\nlane.b.roles=implementer\n")
		ctx := LoadConfig(env, repo)
		if got := strings.Join(LaneNames(&ctx, env), ","); got != "b,z" {
			t.Fatalf("effective lane names=%q", got)
		}
	})
	t.Run("panesValue: 2, 3 and 4 pass, anything else is 4", func(t *testing.T) { // JS: "panesValue: 2, 3 and 4 pass, anything else is 4"
		env, repo := fixture(t)
		for _, value := range []string{"2", "3", "4", "0", "5", "bad"} {
			env["SOHO_PANES"] = value
			want := value
			if value == "0" || value == "5" || value == "bad" {
				want = "4"
			}
			ctx := LoadConfig(env, repo)
			if got := PanesValue(&ctx, env); got != want {
				t.Errorf("panes=%q got %q want %q", value, got, want)
			}
		}
	})
	t.Run("maxWorkers: explicit value per layer (user, project, env, session)", func(t *testing.T) { // JS: "maxWorkers: explicit value per layer (user, project, env, session)"
		env, repo := fixture(t)
		write(t, platform.UserConfigPath(platform.Current(), env), "max_workers=4\n")
		write(t, filepath.Join(repo, ".agents", "soho.conf"), "max_workers=5\n")
		write(t, filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf"), "max_workers=6\n")
		ctx := LoadConfig(env, repo)
		if got := MaxWorkers(&ctx, env); got != "6" {
			t.Fatalf("session max workers=%q", got)
		}
		env["SOHO_MAX_WORKERS"] = "7"
		if got := MaxWorkers(&ctx, env); got != "7" {
			t.Fatalf("env max workers=%q", got)
		}
	})
	t.Run("maxWorkers: the sum of the lane capacities (3/2/1 for panes 4/3/2)", func(t *testing.T) { // JS: "maxWorkers: the sum of the lane capacities (3/2/1 for panes 4/3/2)"
		env, repo := fixture(t)
		for _, tc := range []struct{ panes, want string }{{"4", "3"}, {"3", "2"}, {"2", "1"}} {
			env["SOHO_PANES"] = tc.panes
			ctx := LoadConfig(env, repo)
			if got := MaxWorkers(&ctx, env); got != tc.want {
				t.Errorf("panes %s max workers=%s want %s", tc.panes, got, tc.want)
			}
		}
	})
	t.Run("applyLaneFile propagates write failures and leaves the source file untouched", func(t *testing.T) { // JS: "applyLaneFile propagates write failures and leaves the source file untouched"
		// The JS injects a failing renameSync; here the directory is made
		// read-only, so the rewrite of the same file fails the same way.
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("a read-only directory does not block writes on this host")
		}
		env, repo := fixture(t)
		dest := filepath.Join(t.TempDir(), "dest", "soho.conf")
		const original = "role.implementer.kind=grok\n"
		write(t, dest, original)
		if err := os.Chmod(filepath.Dir(dest), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(dest), 0o755) })
		var caught any
		func() {
			defer func() { caught = recover() }()
			ApplyLaneFile(dest, "4", env, repo)
		}()
		exit, ok := caught.(*platform.ExitError)
		if !ok || exit.Code != 4 || exit.Msg != "could not rewrite "+dest+" (file left untouched)" {
			t.Fatalf("caught=%#v", caught)
		}
		if got, err := os.ReadFile(dest); err != nil || string(got) != original {
			t.Fatalf("dest=%q err=%v", got, err)
		}
		entries, err := os.ReadDir(filepath.Dir(dest))
		if err != nil || len(entries) != 1 || entries[0].Name() != "soho.conf" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})
	t.Run("laneDecide: absent, reuse, gone (open), unavailable, capacity open", func(t *testing.T) { // JS: "laneDecide: absent, reuse, gone (open), unavailable, capacity open"
		env, repo := fixture(t)
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "get", "gone"}, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`, Code: 1},
			{Argv: []string{"agent", "get", "unavailable"}, Stderr: `{"error":{"code":"permission_denied","message":"no access"}}`, Code: 13},
		}); err != nil {
			t.Fatal(err)
		}
		env["PATH"] = bin
		env["SOHO_FAKECLI_CONFIG"] = bin
		env["HERDR_SOCKET_PATH"] = filepath.Join(t.TempDir(), "missing.sock")
		ctx := LoadConfig(env, repo)
		if got := LaneDecide(&ctx, "build", "implementer", env, repo, true).Decision; got != "absent" {
			t.Fatalf("empty lane=%q", got)
		}
		state := StateDir(&ctx, env, repo)
		RosterAppend(state, []string{"worker", "p1", "grok", "implementer", "xai", "p1", repo, "now", "grok-4.7", "full", "implementer", "build"})
		if got := LaneDecide(&ctx, "build", "implementer", env, repo, true); got.Decision != "reuse" || got.Name != "worker" {
			t.Fatalf("idle candidate=%#v", got)
		}
		if got := LaneDecide(&ctx, "build", "implementer", env, repo, false); got.Decision != "open" {
			t.Fatalf("capacity open=%#v", got)
		}
		rows := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + "gone\tp2\tgrok\timplementer\txai\tp2\t" + repo + "\tnow\tgrok-4.7\tfull\timplementer\tbuild\n"
		if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(rows), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LaneDecide(&ctx, "build", "implementer", env, repo, true); got.Decision != "open" || strings.Join(got.Gone, ",") != "gone" {
			t.Fatalf("gone worker decision=%#v", got)
		}
		rows = strings.Replace(rows, "gone\tp2", "unavailable\tp2", 1)
		if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(rows), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LaneDecide(&ctx, "build", "implementer", env, repo, true); got.Decision != "unavailable" || got.Name != "unavailable" {
			t.Fatalf("unavailable worker decision=%#v", got)
		}
	})
	t.Run("roles: frontmatter-only role — values from the role file and the config defaults", func(t *testing.T) { // JS: "roles: frontmatter-only role — values from the role file and the config defaults"
		env, repo := fixture(t)
		ctx := LoadConfig(env, repo)
		resolved := ResolveRoleSettings("scouter", &ctx, env, repo, RoleFlags{})
		if resolved.Kind != "grok" || resolved.ModelSpec != "grok" || resolved.Effort != "xhigh" || resolved.KindFrom != "role file" || resolved.ModelFrom != "model.grok.worker (defaults)" {
			t.Fatalf("resolved frontmatter/defaults=%#v", resolved)
		}
	})
	t.Run("roles: the process check confirms PID, start time and command (no ps -axo)", func(t *testing.T) {
		for _, name := range []string{"implementer", "tasker", "designer", "reviewer", "security-reviewer"} {
			body, err := os.ReadFile(filepath.Join(testSkillDir(t), "roles", name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "ps -axo") || !strings.Contains(string(body), "ps -p <pid> -o lstart=,comm=") {
				t.Errorf("%s lacks the process identity check", name)
			}
			// Edit roles may also read the registry; read-only roles must not
			// run soho procs, which can write state in the repository.
			readOnly := name == "reviewer" || name == "security-reviewer"
			affirms := strings.Contains(string(body), "`soho procs` shows it `running`")
			if readOnly != strings.Contains(string(body), "not `soho procs`") || readOnly == affirms || !readOnly && !strings.Contains(string(body), "`reused`") {
				t.Errorf("%s: wrong soho procs guidance for its mode", name)
			}
		}
	})
}

func TestTM9LiveWorkerCases(t *testing.T) {
	t.Run("enforceWorkerCap: at the cap (code 8, the bash message) and below it", func(t *testing.T) { // JS: "enforceWorkerCap: at the cap (code 8, the bash message) and below it"
		env, cwd, state := tm9LiveWorkerFixture(t, []string{
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\ttasker\txai\t1\t/repo\tnow\tgrok-4.7\tfull\ttasker\treview",
			"w3\tp3\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\texplore",
		}, []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Call: 1, Stdout: `{"result":{"agents":[{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"},{"name":"w3","pane_id":"p3"}]}}`},
			{Argv: []string{"agent", "list"}, Call: 2, Stdout: `{"result":{"agents":[{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"},{"name":"w3","pane_id":"p3"}]}}`},
			{Argv: []string{"agent", "list"}, Call: 3, Stdout: `{"result":{"agents":[{"name":"w1","pane_id":"p1"}]}}`},
		})
		ctx := LoadConfig(env, cwd)
		if got := LiveWorkerNames(state, env); strings.Join(got, ",") != "w1,w2,w3" {
			t.Fatalf("live names=%v", got)
		}
		var recovered any
		func() { defer func() { recovered = recover() }(); EnforceWorkerCap(&ctx, env, cwd) }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: w1 w2 w3)") {
			t.Fatalf("cap result=%#v", recovered)
		}
		func() {
			defer func() {
				if value := recover(); value != nil {
					t.Fatalf("below cap panicked: %v", value)
				}
			}()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
	})
	t.Run("enforceWorkerCap: a roster whose workers are all gone counts none live and does not cap", func(t *testing.T) { // JS: "enforceWorkerCap: a roster whose workers are all gone counts none live and does not cap"
		env, cwd, state := tm9LiveWorkerFixture(t, []string{"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild"}, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}})
		ctx := LoadConfig(env, cwd)
		if len(LiveWorkerNames(state, env)) != 0 {
			t.Fatal("gone roster row counted as live")
		}
		if got := MaxWorkers(&ctx, env); got != "3" {
			t.Fatalf("default cap=%q", got)
		}
	})
	t.Run("liveBurstWorkers: only the temporary (burst) workers that are live", func(t *testing.T) { // JS: "liveBurstWorkers: only the temporary (burst) workers that are live"
		env, _, state := tm9LiveWorkerFixture(t, []string{
			"b1\tp1\tgrok\tdocumenter\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tdocumenter\tdocs\tburst",
			"w1\tp2\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
		}, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"b1","pane_id":"p1"},{"name":"w1","pane_id":"p2"}]}}`}})
		if got := strings.Join(LiveBurstWorkers(state, env), ","); got != "b1" {
			t.Fatalf("burst workers=%q", got)
		}
		if got := strings.Join(LiveWorkerNames(state, env), ","); got != "b1,w1" {
			t.Fatalf("all live workers=%q", got)
		}
	})
	t.Run("liveWorkerNames: a row counts only when its name is live in the same pane", func(t *testing.T) { // JS: "liveWorkerNames: a row counts only when its name is live in the same pane"
		env, _, state := tm9LiveWorkerFixture(t, []string{"same\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild", "other\tp2\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild"}, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"same","pane_id":"p1"},{"name":"other","pane_id":"different"}]}}`}})
		if got := strings.Join(LiveWorkerNames(state, env), ","); got != "same" {
			t.Fatalf("same-pane workers=%q", got)
		}
	})
	t.Run("enforceWorkerCap: a name alive in another pane does not count toward the cap", func(t *testing.T) { // JS: "enforceWorkerCap: a name alive in another pane does not count toward the cap"
		env, cwd, state := tm9LiveWorkerFixture(t, []string{"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild", "w2\tp2\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild"}, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"elsewhere"}]}}`}})
		ctx := LoadConfig(env, cwd)
		if got := strings.Join(LiveWorkerNames(state, env), ","); got != "w1" {
			t.Fatalf("live names=%q", got)
		}
		defer func() {
			if value := recover(); value != nil {
				t.Fatalf("other-pane row hit cap: %v", value)
			}
		}()
		EnforceWorkerCap(&ctx, env, cwd)
	})
	t.Run("liveBurstWorkers: the same name-and-pane live rule as liveWorkerNames", func(t *testing.T) { // JS: "liveBurstWorkers: the same name-and-pane live rule as liveWorkerNames"
		env, _, state := tm9LiveWorkerFixture(t, []string{"b1\tp1\tgrok\tdocumenter\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tdocumenter\tdocs\tburst", "b2\tp2\tgrok\tdocumenter\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tdocumenter\tdocs\tburst"}, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"b1","pane_id":"p1"},{"name":"b2","pane_id":"elsewhere"}]}}`}})
		if got := strings.Join(LiveBurstWorkers(state, env), ","); got != "b1" {
			t.Fatalf("live burst workers=%q", got)
		}
	})
}

func tm9LiveWorkerFixture(t *testing.T, rows []string, rules []fakecli.Rule) (platform.Env, string, string) {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range testutil.CleanEnv(t) {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	env["SOHO_SKILL_DIR"] = testSkillDir(t)
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["HOME"] = filepath.Join(root, "home")
	env["USERPROFILE"] = env.Get("HOME")
	env["XDG_CONFIG_HOME"] = filepath.Join(root, "config")
	env["SOHO_DIR"] = filepath.Join(root, "state")
	env["TMPDIR"] = filepath.Join(root, "tmp")
	for _, dir := range []string{env.Get("HOME"), env.Get("XDG_CONFIG_HOME"), env.Get("SOHO_DIR"), env.Get("TMPDIR")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env["PATH"] = bin
	env["SOHO_FAKECLI_CONFIG"] = bin
	env["HERDR_SOCKET_PATH"] = filepath.Join(t.TempDir(), "missing.sock")
	ctx := LoadConfig(env, cwd)
	state := StateDir(&ctx, env, cwd)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	return env, cwd, state
}

func TestTM9ParentTitleSubtests(t *testing.T) {
	cases := []struct{ title, test string }{
		{"config shows defaults for unset keys (multi_role on defaults)", "TestConfigOutputSources"},
		{"config prints the dotted keys as written in the files, rebuilt for env-only keys", "TestDottedKeyNameRestoresKnownRoleAndLaneSpellings"},
		{"config set preserves whole-line and trailing comments, collapses duplicates", "TestConfigWritePair"},
		{"config set appends a missing key and does not duplicate existing ones", "TestConfigWritePair"},
		{"key normalization matches the bash sed/tr pipeline", "TestNormalizeKey"},
		{"configKeyOk: scalar plus dotted patterns", "TestConfigValidation"},
		{"configValueOk: enums, ladders and role resolution", "TestConfigValidation"},
		{"a CRLF config file loads the same as the same file with LF (decision 7)", "TestConfigFileParsingAndEmptySource"},
		{"empty file values fall back but keep their layer as source", "TestConfigFileParsingAndEmptySource"},
		{"surrounding double quotes are stripped once (no escape handling)", "TestConfigFileParsingAndEmptySource"},
		{"gitignoreAfter: the entry always lands on a line of its own", "TestGitignoreAfter"},
		{"session set writes the session file in the state dir", "TestSessionCommands"},
		{"precedence: session > project, env > session", "TestLoadConfigPrecedence"},
		{"session show lists the session entries", "TestSessionCommands"},
		{"session clear <key> drops one key only", "TestSessionCommands"},
		{"session clear (no key) removes the layer; config falls back to project", "TestSessionCommands"},
		{"without a resolvable workspace, set refuses; config still works", "TestSessionWithoutWorkspace"},
		{"session path falls back to the workspace herdr reports", "TestSessionPathUsesHerdrFallback"},
		{"presets 2/3/4: lanes, roles, capacities, and custom lanes", "TestLanePresetsCapacityAndNames"},
		{"applyLaneFile: a preset file drops the roles, the old lanes and the derived keys", "TestLaneFileMigrationAndSignatures"},
		{"applyLaneFile: lane.read.<attr> moves to lane.review.<attr>, an existing one wins", "TestLaneFileMigrationAndSignatures"},
		{"applyLaneFile: a custom file keeps its lanes and aligns max_workers to the capacities", "TestLaneFileMigrationAndSignatures"},
		{"applyLaneFile: any other lane outside the preset is removed, comments stay", "TestLaneFileMigrationAndSignatures"},
		{"applyLaneFile: the old presets are treated as preset files (not custom)", "TestLaneFileMigrationAndSignatures"},
		{"layer rule: cases 1-4, 7, 8 and the --kind flag", "TestLaneLayerRulesAndSpecs"},
		{"setupLaneSpec: valid specs and every rejection (code 2)", "TestLaneLayerRulesAndSpecs"},
		{"laneDecide: full lane (busy, --fresh candidate, locked), pending-report, reuse by name", "TestReviewLaneLocksAnEditor"},
		{"roleDirs: project dir first, $SOHO_ROLES, then the skill roles", "TestRoleFilesAndFrontmatter"},
		{"resolveRole: project role shadows the skill role", "TestRoleFilesAndFrontmatter"},
		{"fmGet: scalar, list, quoted, missing key, no frontmatter", "TestRoleFilesAndFrontmatter"},
		{"fmGet: frontmatter with CRLF lines parses like LF (decision 7)", "TestRoleFilesAndFrontmatter"},
		{"roleBody: after the closing ---, whole file without frontmatter, empty when unclosed", "TestRoleFilesAndFrontmatter"},
		{"roles: role.<r>.* in a config layer beats the frontmatter", "TestResolveRoleSettingsLayers"},
		{"roles: lane.<l>.kind/model/effort decide for the roles of the lane", "TestResolveRoleSettingsLayers"},
		{"roles: a role model from a layer below the effective kind shows the model that is left", "TestResolveRoleSettingsLayers"},
		{"roles: the frontmatter model is dropped when the kind comes from a config layer", "TestResolveRoleSettingsLayers"},
		{"roles: effort.<kind> and empty values (dashes, default sources)", "TestResolveRoleSettingsLayers"},
		{"roleTimeoutMs: the frontmatter timeout scaled by the effective effort, the dispatch_timeout fallback", "TestRoleTimeoutAndRolesTable"},
		{"parity: lane_names / lane_of_role / max_workers matrix", "TestParityLanesGolden"},
		{"parity: apply_lane_file (nine seeds: bytes, modes, printed lines, warnings, no temps)", "TestApplyLaneFileGolden"},
		{"parity: setup_lane_spec over valid and rejected specs", "TestSetupLaneSpecGolden"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.title, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run", "^"+tc.test+"$")
			cmd.Env = os.Environ()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("existing Go mirror %s failed: %v\n%s", tc.test, err, output)
			}
		})
	}
}
