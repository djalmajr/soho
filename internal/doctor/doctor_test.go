package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

func TestDoctorReportsUsedKindsInUTF16Order(t *testing.T) {
	// Mutation captured: byte sorting reports U+E000 before the emoji kind.
	root := t.TempDir()
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "first.md"), []byte("---\nkind: \uE000\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "second.md"), []byte("---\nkind: 😀\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME": root, "USERPROFILE": root, "PATH": bin,
		"HERDR_ENV": "1", "SOHO_DIR": filepath.Join(root, "state"),
		"SOHO_LANES": "off", "SOHO_ROLES": roles,
		"SOHO_SKILL_DIR": root,
	}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	if !strings.Contains(out.String(), "kinds in use but not in PATH: 😀 \uE000") {
		t.Fatalf("doctor did not report kinds in UTF-16 order: %s", out.String())
	}
}

func TestProjectIsFirstRunRequiresTeamChoiceOrRoster(t *testing.T) { // JS: "projectIsFirstRun: the team-choice and roster tests"
	root := t.TempDir()
	env := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	conf := filepath.Join(root, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectIsFirstRun(&ctx, env, root); !got {
		t.Fatal("fresh project should be first run")
	}
	if err := os.WriteFile(conf, []byte("# team later\nmax_workers=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ProjectIsFirstRun(&ctx, env, root); !got {
		t.Fatal("max_workers alone is not a team choice")
	}
	if err := os.WriteFile(conf, []byte("lane.build.kind=grok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ProjectIsFirstRun(&ctx, env, root); got {
		t.Fatal("lane kind should end first run")
	}
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state", "ws")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\nworker\tpane-1\tcodex\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ProjectIsFirstRun(&ctx, env, root); got {
		t.Fatal("roster row should end first run")
	}
}

func TestDoctorFixRejectsInvalidPanesBeforeWriting(t *testing.T) { // JS: "doctorFix: the dies 2 (bad flag, no panes anywhere, bad file panes)"
	root := t.TempDir()
	// The skill must stay outside the project root: inside it, doctor --fix
	// now refuses before the panes validation this test checks.
	env := platform.Env{"HOME": root, "SOHO_SKILL_DIR": filepath.Join(root, "skill")}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	defer func() {
		if recover() == nil {
			t.Fatal("expected doctor --fix to reject missing panes")
		}
		if _, err := os.Stat(filepath.Join(root, ".agents", "herdr-soho.conf")); !os.IsNotExist(err) {
			t.Fatalf("created config before refusal: %v", err)
		}
	}()
	doctorFix("project", "", &ctx, env, root)
}

func TestDoctorFixUsesLastNonCommentPanesValue(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		fails             bool
	}{
		{"comment then later assignment", "# panes=2\npanes=4\n", "4", false},
		{"duplicate assignments", "panes=2\npanes=4\n", "4", false},
		{"empty then valid assignment", "panes=\npanes=3\n", "3", false},
		{"comment only", "# panes=3\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// The skill must stay outside the project root: inside it, doctor
			// --fix now refuses before the panes parsing this test checks.
			env := platform.Env{"HOME": root, "USERPROFILE": root, "SOHO_SKILL_DIR": filepath.Join(root, "skill")}
			ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
			conf := filepath.Join(root, ".agents", "soho.conf")
			if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(conf, []byte(tc.input), 0o600); err != nil {
				t.Fatal(err)
			}
			oldOut := platform.Stdout
			var out strings.Builder
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = oldOut })

			var recovered any
			func() {
				defer func() { recovered = recover() }()
				doctorFix("project", "", &ctx, env, root)
			}()
			if tc.fails {
				if recovered == nil {
					t.Fatal("comment-only panes assignment should be rejected")
				}
				got, err := os.ReadFile(conf)
				if err != nil || string(got) != tc.input {
					t.Fatalf("rejected fix changed file: %q, %v", got, err)
				}
				return
			}
			if recovered != nil {
				t.Fatalf("doctor --fix unexpectedly failed: %v", recovered)
			}
			got, err := os.ReadFile(conf)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "panes="+tc.want+"\n") {
				t.Fatalf("doctor --fix wrote wrong preset, want panes=%s: %q", tc.want, got)
			}
		})
	}
}

func TestDoctorDiscardedLaneModelIsSilentWhenLanesAreOff(t *testing.T) {
	root := t.TempDir()
	ctx := core.Config{Entries: map[string]core.ConfigEntry{
		"lanes":            {Value: "off", Source: "project"},
		"lane_build_kind":  {Value: "grok", Source: "project"},
		"lane_build_model": {Value: "claude-opus", Source: "user"},
	}}
	var out strings.Builder
	doctorDiscardedModels(&ctx, platform.Env{"SOHO_SKILL_DIR": root}, root, &Say{Out: &out})
	if strings.Contains(out.String(), "lane.build.model=") {
		t.Fatalf("unused lane model was reported while lanes=off: %s", out.String())
	}
}

func TestDoctorLaneRolesSplitOnlyOnCommas(t *testing.T) {
	root := t.TempDir()
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	for role, kind := range map[string]string{"implementer": "grok", "designer": "agy"} {
		if err := os.WriteFile(filepath.Join(roles, role+".md"), []byte("---\nkind: "+kind+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{"SOHO_ROLES": roles, "SOHO_SKILL_DIR": root}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{
		"lanes":                          {Value: "on", Source: "project"},
		"panes":                          {Value: "4", Source: "project"},
		"lane_build_roles":               {Value: "implementer designer", Source: "project"},
		"role_implementer designer_args": {Value: "--verbose", Source: "project"},
	}}
	ctx.Order = []string{"lanes", "panes", "lane_build_roles", "role_implementer designer_args"}
	var out strings.Builder
	doctorLaneWarnings(&ctx, env, root, &Say{Out: &out})
	if !strings.Contains(out.String(), "lanes: unknown roles: build:implementer designer") {
		t.Fatalf("space-separated text was treated as two roles: %s", out.String())
	}
	if strings.Contains(out.String(), "roles disagree") {
		t.Fatalf("space-separated text was treated as two lane roles: %s", out.String())
	}
	if got := usedKinds(&ctx, env, root); len(got) != 0 {
		t.Fatalf("usedKinds split a role at whitespace: %#v", got)
	}
	out.Reset()
	doctorLaneArgs(&ctx, env, &Say{Out: &out})
	if !strings.Contains(out.String(), "role.implementer designer.args is ignored") {
		t.Fatalf("lane args check split a role at whitespace: %s", out.String())
	}
	if got := doctorLaneRoles("  implementer\u00a0, designer\ufeff "); len(got) != 2 || got[0] != "implementer" || got[1] != "designer" {
		t.Fatalf("role trim does not match JavaScript trim: %#v", got)
	}
}

func TestDoctorChecksWritabilityWithoutCreatingFiles(t *testing.T) {
	root := t.TempDir()
	originalAccess := writableAccess
	t.Cleanup(func() { writableAccess = originalAccess })
	called := false
	writableAccess = func(path string) error {
		called = true
		if path != root {
			t.Fatalf("checked unexpected path %q", path)
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			t.Fatalf("writability probe created files: %v", entries)
		}
		return nil
	}
	if !isWritable(root) || !called {
		t.Fatal("writable directory was not checked through the non-writing access probe")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("writability check left directory entries: %v, %v", entries, err)
	}
}

func TestDoctorPlannerWarningsUseSortedKeys(t *testing.T) {
	root := t.TempDir()
	ctx := core.Config{
		Entries: map[string]core.ConfigEntry{
			"role_planner_model": {Value: "fable", Source: "project"},
			"role_planner_kind":  {Value: "codex", Source: "project"},
		},
		Order: []string{"role_planner_model", "role_planner_kind"},
	}
	var out strings.Builder
	doctorLaneWarnings(&ctx, platform.Env{"SOHO_SKILL_DIR": root}, root, &Say{Out: &out})
	got := out.String()
	kind := strings.Index(got, "config: role_planner_kind is set")
	model := strings.Index(got, "config: role_planner_model is set")
	if kind < 0 || model < 0 || kind > model {
		t.Fatalf("planner warnings are not sorted by key: %s", got)
	}
}

func TestDoctorWarnsWhenOfficialSkillDiffersFromHerdr(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "herdr"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".agents", "skills", "herdr", "SKILL.md"), []byte("installed\n"), 0o600)
	bin := filepath.Join(root, "bin")
	rules := []fakecli.Rule{{Argv: []string{"--version"}, Stdout: "herdr 1.0.0\n"}, {Argv: []string{"status", "server"}, Stdout: "server 1.0.0\n"}, {Argv: []string{"--skill"}, Stdout: "different\n"}}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": home, "USERPROFILE": home, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_FAKECLI_CONFIG": bin, "SOHO_SKILL_DIR": root, "PATH": bin, "HERDR_ENV": "1"}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	if !strings.Contains(out.String(), "differs from 'herdr --skill'") {
		t.Fatalf("expected official skill warning, got %s", out.String())
	}
}

func TestDoctorOwnProviderLiteralAPIKey(t *testing.T) {
	t.Run(`doctor: own provider with a literal apiKey warns; the env reference does not`, func(t *testing.T) { // JS: "doctor: own provider with a literal apiKey warns; the env reference does not"
		// Mutation captured: treating the $-prefixed API key as literal also warns on the env-reference fixture.
		env, root := tm6DoctorEnv(t)
		pi := filepath.Join(env.Get("HOME"), ".pi", "agent", "models.json")
		if err := os.MkdirAll(filepath.Dir(pi), 0o700); err != nil {
			t.Fatal(err)
		}
		write := func(apiKey string) {
			t.Helper()
			data := `{"providers":{"local":{"apiKey":"` + apiKey + `","models":[{"id":"m","maxTokens":32768}]}}}`
			if err := os.WriteFile(pi, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("literal-secret")
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if !strings.Contains(out, "own provider 'local' (pi) has a literal apiKey") || strings.Contains(out, "literal-secret") {
			t.Fatalf("literal key diagnostic=%s", out)
		}
		write("$MY_API_KEY")
		out = tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if strings.Contains(out, "literal apiKey") {
			t.Fatalf("environment reference was diagnosed as a literal key: %s", out)
		}
	})
}

func TestDoctorReadsHerdrVersionAndLargeSkillFromPath(t *testing.T) { // Mutation captured: passing the already-resolved absolute path back to RunCli makes it search PATH for that path and silently return no output.
	root := t.TempDir()
	home := filepath.Join(root, "home")
	skillDir := filepath.Join(home, ".agents", "skills", "herdr")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Herdr's installed skill is 13,867 bytes; matching it exercises the real
	// large-output comparison as well as the version path.
	skill := strings.Repeat("x", 13867)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 0.9.1\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 0.9.1\n"},
		{Argv: []string{"--skill"}, Stdout: skill},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": home, "USERPROFILE": home, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_FAKECLI_CONFIG": bin, "SOHO_SKILL_DIR": root, "PATH": bin, "HERDR_ENV": "1"}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	got := out.String()
	if !strings.Contains(got, "ok     herdr 0.9.1\n") {
		t.Fatalf("version output was not read from herdr on PATH: %s", got)
	}
	if !strings.Contains(got, "ok     official herdr skill matches the binary (") || strings.Contains(got, "differs from 'herdr --skill'") {
		t.Fatalf("matching large skill was reported stale: %s", got)
	}
}

func TestDoctorRecognizesNativeAndOldSessionStartHooks(t *testing.T) { // JS: "doctor requires the current SessionStart command"
	for _, tc := range []struct {
		name     string
		command  string
		wantLine string
	}{{
		"the exact native hook is recognized as present", setuptext.SetupHookDoctor(),
		"ok     Claude hooks present in .claude/settings.json",
	}, {
		"the recognized previous herdr-soho shell hook warns for the setup replacement", setuptext.PreviousHookDoctor(),
		"old herdr-soho shell hooks in .claude/settings.json: run '",
	}, {
		"the exact pre-rename legacy hook warns for the setup replacement", setuptext.LegacyHookDoctor(),
		"an older version of the hooks in .claude/settings.json: run '",
	}, {
		"a foreign command that only mentions herdr-soho is not a soho hook", "sh -c herdr-soho doctor",
		"warn   no soho hooks in .claude/settings.json",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			env := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root, "PATH": "", "HERDR_ENV": "1"}
			ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
			settings := filepath.Join(root, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":`+quoteCommand(tc.command)+`}]}]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			DoctorCheck(&ctx, env, root, &out)
			got := out.String()
			if !strings.Contains(got, tc.wantLine) {
				t.Fatalf("expected %q in:\n%s", tc.wantLine, got)
			}
			if tc.command == setuptext.PreviousHookDoctor() {
				// The setup guidance names the native launcher, never the
				// obsolete skill scripts path.
				launcher := platform.LauncherPath(env)
				if !strings.Contains(got, "to replace them with the native hook commands") || !strings.Contains(got, launcher+" setup'") || strings.Contains(got, "scripts/herdr-soho") {
					t.Fatalf("old-hook guidance is not the native launcher:\n%s", got)
				}
			}
		})
	}
}

func TestDoctorWarnsWhenNativeAndPreviousHooksCoexist(t *testing.T) {
	for _, obsolete := range []struct{ name, command, warning string }{
		{"previous", setuptext.PreviousHookDoctor(), "old herdr-soho shell hooks"},
		{"legacy", setuptext.LegacyHookDoctor(), "an older version of the hooks"},
	} {
		for _, nativeFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/native-first=%v", obsolete.name, nativeFirst), func(t *testing.T) {
				root := t.TempDir()
				env := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root, "PATH": "", "HERDR_ENV": "1"}
				ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
				commands := []string{setuptext.SetupHookDoctor(), obsolete.command}
				if !nativeFirst {
					commands[0], commands[1] = commands[1], commands[0]
				}
				settings := filepath.Join(root, ".claude", "settings.json")
				if err := os.MkdirAll(filepath.Dir(settings), 0755); err != nil {
					t.Fatal(err)
				}
				body := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":` + quoteCommand(commands[0]) + `},{"type":"command","command":` + quoteCommand(commands[1]) + `}]}]}}`
				if err := os.WriteFile(settings, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				var out strings.Builder
				DoctorCheck(&ctx, env, root, &out)
				if !strings.Contains(out.String(), obsolete.warning) || strings.Contains(out.String(), "ok     Claude hooks present") {
					t.Fatalf("mixed hook settings were accepted without migration warning:\n%s", out.String())
				}
				after, err := os.ReadFile(settings)
				if err != nil || string(after) != body {
					t.Fatalf("doctor mutated settings: %q err=%v", after, err)
				}
			})
		}
	}
}

func quoteCommand(command string) string {
	b, err := json.Marshal(command)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestDoctorSetupGuidanceUsesTheNativeLauncher proves the setup guidance
// lines carry the platform.LauncherPath binary (the actual native executable
// when no SOHO_BIN override exists), never the obsolete scripts path.
func TestDoctorSetupGuidanceUsesTheNativeLauncher(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root, "PATH": "", "HERDR_ENV": "1"}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	got := out.String()
	launcher := platform.LauncherPath(env)
	for _, line := range []string{"then run '" + launcher + " doctor --fix --panes <n>' with their answer.", "run '" + launcher + " setup' (writes the delegation rules"} {
		if !strings.Contains(got, line) {
			t.Fatalf("setup guidance line missing (%s):\n%s", line, got)
		}
	}
	if strings.Contains(got, "scripts/soho") {
		t.Fatalf("obsolete scripts path leaked into the setup guidance:\n%s", got)
	}
	// A hostile SOHO_BIN override is refused by LauncherPath (code 2)
	// before any guidance is emitted: the doctor never runs it.
	hostile := platform.Env{"HOME": root, "SOHO_SKILL_DIR": root, "PATH": "", "SOHO_BIN": "relative-bin"}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		var out2 strings.Builder
		DoctorCheck(&ctx, hostile, root, &out2)
	}()
	exit, ok := recovered.(*platform.ExitError)
	if !ok || exit.Code != 2 {
		t.Fatalf("hostile SOHO_BIN: recovered %#v; want ExitError code 2 (fail closed, no guidance)", recovered)
	}
}

func TestDoctorCodexUIWarningsFollowNetworkToken(t *testing.T) {
	root := t.TempDir()
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(roles, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"designer", "inspector"} {
		if err := os.WriteFile(filepath.Join(roles, role+".md"), []byte("---\nkind: codex\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{"SOHO_ROLES": roles, "SOHO_SKILL_DIR": root}
	t.Run(`doctorCodexNetworkWarnings: codex UI roles without a network token warn, lifted by the spawn tokens`, func(t *testing.T) { // JS: "doctorCodexNetworkWarnings: codex UI roles without a network token warn, lifted by the spawn tokens"
		// Mutation captured: Contains instead of HasSuffix accepts a suffixed network token and hides both warnings.
		exact := func(role, key string) string {
			return "warn   config: " + role + " runs on codex without network: it cannot open a local port, so the UI and e2e tests do not run (listen EPERM); set " + key + "=-c sandbox_workspace_write.network_access=true, or run the e2e yourself\n"
		}
		check := func(values map[string]string, want string) {
			t.Helper()
			entries := make(map[string]core.ConfigEntry, len(values))
			for key, value := range values {
				entries[key] = core.ConfigEntry{Value: value, Source: "project"}
			}
			var out strings.Builder
			doctorCodexNetwork(&core.Config{Entries: entries}, env, root, &Say{Out: &out})
			if out.String() != want {
				t.Fatalf("network warning output=%q, want %q", out.String(), want)
			}
		}
		base := map[string]string{"lanes": "off", "role_designer_kind": "codex", "role_inspector_kind": "codex"}
		check(base, exact("designer", "role.designer.args")+exact("inspector", "role.inspector.args"))
		for _, token := range []string{"sandbox_workspace_write.network_access=trueno", "xdanger-full-access"} {
			values := map[string]string{"lanes": "off", "role_designer_kind": "codex", "role_inspector_kind": "codex", "args_codex": token}
			check(values, exact("designer", "role.designer.args")+exact("inspector", "role.inspector.args"))
		}
		check(map[string]string{"lanes": "off", "role_designer_kind": "codex", "role_inspector_kind": "codex", "args_codex": "-c sandbox_workspace_write.network_access=true"}, "")
		for _, token := range []string{"--sandbox danger-full-access", "--dangerously-bypass-approvals-and-sandbox"} {
			check(map[string]string{"lanes": "off", "role_designer_kind": "codex", "role_inspector_kind": "codex", "args_codex": token}, "")
		}
		check(map[string]string{"lanes": "off", "role_designer_kind": "codex", "role_inspector_kind": "codex", "role_designer_args": "-c sandbox_workspace_write.network_access=true"}, exact("inspector", "role.inspector.args"))
		check(map[string]string{"panes": "4", "lane_build_kind": "codex", "lane_review_kind": "codex"}, exact("designer", "lane.build.args")+exact("inspector", "lane.review.args"))
		check(map[string]string{"panes": "4", "lane_build_kind": "codex", "lane_review_kind": "codex", "lane_review_args": "-c sandbox_workspace_write.network_access=true", "role_designer_args": "-c sandbox_workspace_write.network_access=true"}, exact("designer", "lane.build.args"))
	})
}

func TestDoctorWarnsAboutFormerProjectConfig(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root, "PATH": "", "HERDR_ENV": "1"}
	legacy := filepath.Join(root, ".agents", "herdr-soho.conf")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("panes=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	got := out.String()
	want := "former project config " + legacy + " is no longer read: run 'soho setup' to move it to " + filepath.Join(root, ".agents", "soho.conf")
	if !strings.Contains(got, want) {
		t.Fatalf("leftover warning missing (want %q): %s", want, got)
	}
}

func TestDoctorWarnsWhenSplitCapExceedsTheTeam(t *testing.T) { // A9: the reference is 1 + the effective max_workers, not panes (the frozen JS text was panes-based)
	root := t.TempDir()
	env := platform.Env{"HOME": root, "SOHO_SKILL_DIR": root}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{"panes": {Value: "3", Source: "project"}, "split_max_panes": {Value: "8", Source: "project"}}, Order: []string{"panes", "split_max_panes"}}
	var out strings.Builder
	s := &Say{Out: &out}
	doctorLaneWarnings(&ctx, env, root, s)
	if !strings.Contains(out.String(), "split_max_panes=8 is greater than 1 + max_workers=2. Set split_max_panes=3 (doctor --fix aligns it).") {
		t.Fatalf("split cap mismatch warning missing: %s", out.String())
	}
}

func TestDoctorSplitCapChecksTheWholeTeam(t *testing.T) { // A9: an explicit split_max_panes is checked against 1 + the effective max_workers
	root := t.TempDir()
	env := platform.Env{"HOME": root, "SOHO_SKILL_DIR": root}
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"a team of six fits under split_max_panes=7 without a split cap warning",
			map[string]string{"lane_build_panes": "5", "lane_review_panes": "1", "split_max_panes": "7"},
			""},
		{"split_max_panes=9 is greater than 1 + max_workers=6",
			map[string]string{"lane_build_panes": "5", "lane_review_panes": "1", "split_max_panes": "9"},
			"config: split_max_panes=9 is greater than 1 + max_workers=6. Set split_max_panes=7 (doctor --fix aligns it)."},
		{"split_max_panes=3 leaves no room for a team of five",
			map[string]string{"lane_build_panes": "4", "lane_review_panes": "1", "split_max_panes": "3"},
			"config: split_max_panes=3 leaves no room for the whole team (1 + max_workers=5); the last workers will open in a herd tab. Remove split_max_panes or set 6."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &core.Config{Entries: make(map[string]core.ConfigEntry, len(tc.values))}
			for key, value := range tc.values {
				ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
			}
			var out strings.Builder
			doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
			got := out.String()
			if tc.want == "" {
				if strings.Contains(got, "is greater than 1 + max_workers") || strings.Contains(got, "leaves no room for the whole team") {
					t.Fatalf("unexpected split cap warning: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("missing %q in %s", tc.want, got)
			}
		})
	}
}

func TestDoctorReportsRawPaneMode(t *testing.T) { // Mutation captured: normalizing an invalid pane_mode to strict hides the raw configuration error.
	root := t.TempDir()
	env := platform.Env{"SOHO_SKILL_DIR": root}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{
		"panes":     {Value: "4", Source: "project"},
		"pane_mode": {Value: "weird", Source: "project"},
	}}
	var out strings.Builder
	doctorLaneWarnings(&ctx, env, root, &Say{Out: &out})
	if !strings.Contains(out.String(), "warn   config: pane_mode='weird' is not strict|flex\n") {
		t.Fatalf("raw pane_mode warning missing: %s", out.String())
	}
	if strings.Contains(out.String(), "config: pane_mode=strict") {
		t.Fatalf("invalid pane_mode was reported as strict: %s", out.String())
	}
}

func TestDoctorReportsDiscardedLaneEffort(t *testing.T) { // Mutation captured: omitting the ignored-effort message hides a layer-precedence decision.
	root := t.TempDir()
	env := platform.Env{"SOHO_SKILL_DIR": root}
	ctx := core.Config{
		Entries: map[string]core.ConfigEntry{
			"panes":              {Value: "4", Source: "project"},
			"lane_review_roles":  {Value: "reviewer", Source: "project"},
			"lane_review_kind":   {Value: "codex", Source: "project"},
			"lane_review_effort": {Value: "low", Source: "user"},
		},
		Order: []string{"panes", "lane_review_roles", "lane_review_kind", "lane_review_effort"},
	}
	var out strings.Builder
	doctorLaneWarnings(&ctx, env, root, &Say{Out: &out})
	want := "ok     lanes: lane 'review' kind codex (project); ignored lane effort low from user (another kind)\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("discarded lane effort line missing: %s", out.String())
	}
}

func TestDoctorNamesAllLegacyPresetLanes(t *testing.T) { // Mutation captured: reporting only the first legacy lane loses the preset's full identity.
	root := t.TempDir()
	env := platform.Env{"SOHO_SKILL_DIR": root}
	ctx := core.Config{
		Entries: map[string]core.ConfigEntry{
			"panes":            {Value: "3", Source: "project"},
			"lane_build_roles": {Value: "implementer,designer,tasker", Source: "project"},
			"lane_read_roles":  {Value: "scouter,researcher,reviewer,security-reviewer,ui-reviewer,inspector", Source: "project"},
		},
		Order: []string{"panes", "lane_build_roles", "lane_read_roles"},
	}
	var out strings.Builder
	doctorLaneWarnings(&ctx, env, root, &Say{Out: &out})
	if !strings.Contains(out.String(), "old preset (build, read)") {
		t.Fatalf("legacy preset lane names missing: %s", out.String())
	}
}

func TestDoctorSortsOrphanLaneWarnings(t *testing.T) { // Mutation captured: using config insertion order instead of key order reverses orphan diagnostics.
	root := t.TempDir()
	env := platform.Env{"SOHO_SKILL_DIR": root}
	ctx := core.Config{
		Entries: map[string]core.ConfigEntry{
			"panes":           {Value: "4", Source: "project"},
			"lane_ops_roles":  {Value: "implementer", Source: "project"},
			"lane_zebra_kind": {Value: "grok", Source: "project"},
			"lane_alpha_kind": {Value: "claude", Source: "project"},
		},
		Order: []string{"panes", "lane_ops_roles", "lane_zebra_kind", "lane_alpha_kind"},
	}
	var out strings.Builder
	doctorLaneWarnings(&ctx, env, root, &Say{Out: &out})
	got := out.String()
	alpha := strings.Index(got, "config: lane.alpha.kind=claude")
	zebra := strings.Index(got, "config: lane.zebra.kind=grok")
	if alpha < 0 || zebra < 0 || alpha > zebra {
		t.Fatalf("orphan lane warnings are missing or unsorted: %s", got)
	}
}

func TestDoctorNamesSeparateClaudeMdWithLegacyBlock(t *testing.T) { // JS: "setup + doctor (launcher): a separate CLAUDE.md that keeps the legacy block is named"
	legacy := setuptext.LegacySetupStart + "\nold\n" + setuptext.LegacySetupEnd + "\n"
	current := setuptext.SetupStart + "\nnew\n" + setuptext.SetupEnd + "\n"
	for _, tc := range []struct {
		name, agents, claude string
		want                 bool
	}{
		{"separate CLAUDE.md with only the legacy block", "# A\n\n" + current, "# C\n\n" + legacy, true},
		{"separate CLAUDE.md already renamed", "# A\n\n" + current, "# C\n\n" + current, false},
		{"CLAUDE.md is the setup target", "# A\n", "# C\n\n" + legacy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			env := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": root, "PATH": "", "HERDR_ENV": "1"}
			ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
			if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(tc.agents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(tc.claude), 0o600); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			DoctorCheck(&ctx, env, root, &out)
			got := strings.Contains(out.String(), "setup --target CLAUDE.md' to rename it in place")
			if got != tc.want {
				t.Fatalf("separate CLAUDE.md warning=%v, want %v:\n%s", got, tc.want, out.String())
			}
		})
	}
}

func TestDoctorDiscardedModelsNameTheHigherConfigLayer(t *testing.T) {
	for _, tc := range []struct{ name, lanes, kindKey, modelKey, want string }{
		{"lane", "on", "lane_build_kind", "lane_build_model", "warn   config: lane.build.model=grok-4.7 (user) is ignored: lane.build.kind=codex comes from a higher layer (project) without a model\n"},
		{"role", "off", "role_implementer_kind", "role_implementer_model", "warn   config: role.implementer.model=grok-4.7 (user) is ignored: role.implementer.kind=codex comes from a higher layer (project) without a model\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			roles := filepath.Join(root, "roles")
			if err := os.Mkdir(roles, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nmode: edit\n---\nImplement.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"SOHO_SKILL_DIR": root, "SOHO_ROLES": roles}
			ctx := core.Config{Entries: map[string]core.ConfigEntry{
				"lanes": {Value: tc.lanes, Source: "project"}, "panes": {Value: "4", Source: "project"}, "lane_build_roles": {Value: "implementer", Source: "project"},
				tc.kindKey: {Value: "codex", Source: "project"}, tc.modelKey: {Value: "grok-4.7", Source: "user"},
			}}
			var out strings.Builder
			doctorDiscardedModels(&ctx, env, root, &Say{Out: &out})
			if out.String() != tc.want {
				t.Fatalf("discarded model warning=%q; want %q", out.String(), tc.want)
			}
		})
	}
}

func TestDoctorReportsRetiredFamilyConfigInEveryLayer(t *testing.T) {
	env, root := tm6DoctorEnv(t)
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["XDG_CONFIG_HOME"] = filepath.Join(root, "config")
	files := map[string]string{
		"user":    core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}),
		"project": filepath.Join(root, ".agents", "soho.conf"),
	}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("family_check=strict\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := core.LoadConfig(env, root)
	files["session"] = core.SessionConfPath(&ctx, env, root)
	if files["session"] == "" {
		t.Fatal("fixture did not resolve session.conf")
	}
	if err := os.MkdirAll(filepath.Dir(files["session"]), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files["session"], []byte("family_check=off\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx = core.LoadConfig(env, root)
	output := tm6DoctorOutput(t, &ctx, env, root)
	for source, file := range files {
		want := "family_check is retired (" + source + "); delete the family_check line from " + file
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %s", want, output)
		}
		raw, err := os.ReadFile(file)
		value := "strict"
		if source == "session" {
			value = "off"
		}
		if err != nil || string(raw) != "family_check="+value+"\n" {
			t.Fatalf("doctor changed %s: %q %v", source, raw, err)
		}
	}
}
