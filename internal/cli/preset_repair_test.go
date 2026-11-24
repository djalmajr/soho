package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const predecessorReview = "lane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,validator\n"
const predecessorCritical = "lane.critical.roles=critical-implementer\n"
const predecessorStrictTwo = "lane.build.roles=implementer,designer,tasker,scouter,researcher,documenter,tester\n"
const predecessorStrict = predecessorStrictTwo + predecessorCritical + predecessorReview
const predecessorFlex = "lane.build.roles=implementer,designer,tasker,scouter,researcher,tester\n" + predecessorCritical + predecessorReview + "lane.docs.roles=documenter\n"

func TestDoctorRepairPredecessorAcrossModes(t *testing.T) {
	const olderStrictTwo = "lane.build.roles=implementer,designer,tasker,scouter,researcher,documenter\n"
	for _, source := range []struct{ name, roles, acceptedModes string }{
		{"strict-two", predecessorStrictTwo, "strict flex"},
		{"strict-with-critical", predecessorStrict, "strict flex"},
		{"flex-with-critical", predecessorFlex, "strict flex"},
		{"older-strict-two", olderStrictTwo, "strict"},
		{"custom-build-only", strings.Replace(predecessorStrictTwo, "scouter,researcher", "researcher,scouter", 1), ""},
		{"custom-with-critical", strings.Replace(predecessorFlex, "scouter,researcher", "researcher,scouter", 1), ""},
	} {
		for _, mode := range []string{"strict", "flex"} {
			for _, panes := range []string{"2", "3", "4"} {
				t.Run(source.name+"-to-"+mode+"-"+panes, func(t *testing.T) {
					env, cwd := tm6CLIEnv(t)
					conf := filepath.Join(cwd, ".agents", "soho.conf")
					savedPanes := "2"
					if strings.Contains(source.roles, predecessorReview) {
						savedPanes = "4"
					}
					input := "panes=" + savedPanes + "\npane_mode=" + mode + "\nlane.build.kind=pi\nlane.build.panes=3\n" + source.roles
					wantCapacity := 3
					if strings.Contains(source.roles, predecessorCritical) {
						input += "lane.critical.panes=1\n"
						wantCapacity++
					}
					tm6Write(t, conf, input)
					code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", panes}, env, cwd)
					got := mustRead(t, conf)
					if code != 0 {
						t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
					}
					if !strings.Contains(source.acceptedModes, mode) {
						if got != input || !strings.Contains(stderr, "left the file unchanged") {
							t.Fatalf("custom repair: conf=%q stderr=%q", got, stderr)
						}
						return
					}
					ctx := core.LoadConfig(env, cwd)
					if strings.Contains(got, "lane.build.") || strings.Contains(got, "lane.critical.") ||
						core.FileLaneSignature(conf) != "" || core.FileKeyValue(conf, "lane.builders.kind") != "pi" ||
						core.LaneCapacity(&ctx, "builders", env) != wantCapacity || core.PanesValue(&ctx, env) != panes || core.PaneMode(&ctx, env) != mode {
						t.Fatalf("predecessor did not migrate: conf=%q stderr=%q", got, stderr)
					}
					want := []string{"builders", "reviewers", "planners"}
					if mode == "strict" && panes == "2" {
						want = []string{"builders", "planners"}
					}
					if lanes := core.LaneNames(&ctx, env); !reflect.DeepEqual(lanes, want) {
						t.Errorf("destination groups=%v want=%v", lanes, want)
					}
				})
			}
		}
	}
}

func TestDoctorPresetRepairAdviceUsesDefiningLayer(t *testing.T) {
	for _, tc := range []struct{ name, layer, roles, form, status, command string }{
		{"active project", "project", predecessorStrict, "panes=4\n", "still applies", "soho doctor --fix"},
		{"ignored user", "user", "", "panes=4\n", "no longer applies", "soho doctor --fix --user"},
		{"active session", "session", predecessorFlex, "panes=4\npane_mode=flex\n", "still applies", "soho doctor --fix --session"},
		{"ignored user without panes", "user", "", "", "no longer applies", "soho doctor --fix --user --panes 4"},
		{"higher preset beside custom user", "user", "lane.tools.roles=tasker\n", "panes=4\n", "", ""},
		{"custom project", "project", "lane.tools.roles=tasker\n", "panes=4\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			project := filepath.Join(cwd, ".agents", "soho.conf")
			path := project
			switch tc.layer {
			case "user":
				path = filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
			case "session":
				path = filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
			}
			input := tc.form + tc.roles + "lane.build.kind=pi\nlane.unknown.models=model\n"
			tm6Write(t, path, input)
			if tc.name == "higher preset beside custom user" {
				tm6Write(t, project, "panes=4\nlane.tools.roles=\n")
			}
			code, out, stderr := runIn(t, []string{"doctor"}, env, cwd)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			var groupAdvice, orphanAdvice string
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "lane.build.kind") && strings.HasPrefix(line, "warn") {
					groupAdvice = line
				}
				if strings.Contains(line, "lane.unknown.models") {
					orphanAdvice = line
				}
			}
			if tc.command != "" {
				if !strings.Contains(groupAdvice, tc.status) || !strings.Contains(groupAdvice, "'"+tc.command+"'") {
					t.Errorf("group advice=%q want status %q and command %q", groupAdvice, tc.status, tc.command)
				}
				if !strings.Contains(orphanAdvice, "'"+tc.command+"'") {
					t.Errorf("orphan advice=%q want command %q", orphanAdvice, tc.command)
				}
				code, out, stderr = runIn(t, strings.Fields(tc.command)[1:], env, cwd)
				if got := mustRead(t, path); code != 0 || !strings.Contains(got, "lane.builders.kind=pi") ||
					strings.Contains(got, "lane.build.kind=") || strings.Contains(got, "lane.unknown.models=") {
					t.Fatalf("advertised repair: code=%d out=%q stderr=%q conf=%q", code, out, stderr, got)
				}
			} else if groupAdvice == "" || orphanAdvice == "" || strings.Contains(groupAdvice+orphanAdvice, "doctor --fix") {
				t.Errorf("custom advice: group=%q orphan=%q", groupAdvice, orphanAdvice)
			}
		})
	}
}

func TestDoctorRepairPreservesBuilderAssistantSettings(t *testing.T) {
	for _, tc := range []struct {
		name, input, user, wantKind, wantModel string
	}{
		{"designer only", "role.designer.kind=grok\nrole.documenter.kind=grok\n", "", "", ""},
		{"critical model only", "role.designer.kind=grok\nrole.critical-implementer.model=sol\nrole.documenter.kind=grok\n", "", "", ""},
		{"explicit critical kind", "role.designer.kind=grok\nrole.critical-implementer.kind=codex\nrole.critical-implementer.model=sol\nrole.documenter.kind=grok\n", "", "grok", ""},
		{"inheriting documenter", "role.designer.kind=grok\nrole.critical-implementer.kind=grok\n", "", "", ""},
		{"model alone would spread", "lane.builders.kind=codex\nrole.designer.model=chosen\n", "", "codex", ""},
		{"removing a pin would enable rotation", "lane.builders.kind=codex\nlane.builders.models=sol|gpt-5,second\nrole.designer.model=sol|gpt-5\n", "", "codex", ""},
		{"removing a pin would expose lower kind", "role.designer.kind=grok\nrole.critical-implementer.kind=codex\nrole.documenter.kind=codex\n", "role.designer.kind=codex\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			conf := filepath.Join(cwd, ".agents", "soho.conf")
			tm6Write(t, conf, "panes=4\n"+tc.input)
			if tc.user != "" {
				tm6Write(t, filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"), tc.user)
			}
			type settings struct{ kind, model, effort, rotation string }
			snapshot := func() map[string]settings {
				ctx := core.LoadConfig(env, cwd)
				values := map[string]settings{}
				for _, role := range []string{"implementer", "critical-implementer", "designer", "tasker", "tester", "scouter", "researcher", "documenter"} {
					rs := core.ResolveRoleSettings(role, &ctx, env, cwd, core.RoleFlags{})
					values[role] = settings{rs.Kind, rs.ModelSpec, rs.Effort, strings.Join(rs.LaneModels, ",")}
				}
				return values
			}
			before := snapshot()
			code, out, stderr := runIn(t, []string{"doctor", "--fix"}, env, cwd)
			if code != 0 {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			for role, got := range snapshot() {
				if got != before[role] {
					t.Errorf("%s assistant settings changed: before=%+v after=%+v", role, before[role], got)
				}
			}
			if kind, model := core.FileKeyValue(conf, "lane.builders.kind"), core.FileKeyValue(conf, "lane.builders.model"); kind != tc.wantKind || model != tc.wantModel {
				t.Errorf("builder defaults after repair: kind=%q model=%q; want kind=%q model=%q; conf=%q", kind, model, tc.wantKind, tc.wantModel, mustRead(t, conf))
			}
		})
	}
}

func TestDoctorMissingPaneAdviceUsesProjectTarget(t *testing.T) {
	custom := strings.Replace(predecessorStrictTwo, "implementer,designer", "designer,implementer", 1)
	for _, tc := range []struct {
		name, project, overlay string
		eligible               bool
	}{
		{"custom project masked by session preset", custom, predecessorStrict, false},
		{"blank project roles masked by session preset", "lane.build.roles=\n", predecessorStrict, false},
		{"custom project visible", custom, "", false},
		{"preset project under session preset", predecessorStrictTwo, predecessorStrict, true},
		{"preset project under custom session", predecessorStrictTwo, custom, true},
		{"no project file", "", predecessorStrict, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			project := filepath.Join(cwd, ".agents", "soho.conf")
			session := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
			if tc.project != "" {
				tm6Write(t, project, tc.project)
			}
			for _, line := range strings.Split(strings.TrimSpace(tc.overlay), "\n") {
				if key, value, ok := strings.Cut(line, "="); ok {
					if code, out, stderr := runIn(t, []string{"session", "set", key, value}, env, cwd); code != 0 {
						t.Fatalf("session set: exit=%d stdout=%q stderr=%q", code, out, stderr)
					}
				}
			}
			code, out, stderr := runIn(t, []string{"doctor"}, env, cwd)
			if code != 0 || !strings.Contains(out, "panes is not set in the project or user file (default 4)") {
				t.Fatalf("missing informational line: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "panes is not set in the project or user file") {
					if got := strings.Contains(line, "doctor --fix --panes <n>"); got != tc.eligible {
						t.Errorf("project repair advice=%t want=%t line=%q", got, tc.eligible, line)
					}
					t.Log(line)
				}
			}
			if tc.overlay != "" && mustRead(t, session) != tc.overlay {
				t.Fatalf("session set bytes: %q want %q", mustRead(t, session), tc.overlay)
			}
			code, out, stderr = runIn(t, []string{"doctor", "--fix", "--panes", "4"}, env, cwd)
			got := mustRead(t, project)
			if code != 0 || (tc.eligible && (core.FileKeyValue(project, "panes") != "4" || core.FileLaneSignature(project) != "")) ||
				(!tc.eligible && (got != tc.project || !strings.Contains(stderr, "left the file unchanged"))) {
				t.Fatalf("project repair: exit=%d stdout=%q stderr=%q conf=%q", code, out, stderr, got)
			}
			if tc.overlay != "" && mustRead(t, session) != tc.overlay {
				t.Fatal("project repair changed the session file")
			}
		})
	}
}

func TestDoctorRepairKeepsBuilderNativeArgv(t *testing.T) {
	const groupArgs = "lane.builders.args=--reasoning-effort low\n"
	const roleArgs = "role.critical-implementer.args=--reasoning-effort high\n"
	const explicitKind = "lane.builders.kind=grok\n"
	const modelList = "grok-4.7,grok-4.7-build-fast"
	for _, tc := range []struct {
		name, target, user, project, session, wantNative string
		envKey, envValue, sessionKey, sessionValue       string
		wantModel, wantModelChoice, wantStoredModel      string
		wantGroupKind                                    bool
	}{
		{name: "dormant in project", target: "project", project: groupArgs},
		{name: "migrated predecessor args", target: "project", project: "lane.build.args=--reasoning-effort low\n"},
		{name: "dormant in user", target: "project", user: groupArgs},
		{name: "dormant in higher project", target: "user", project: groupArgs},
		{name: "dormant in higher session", target: "project", session: groupArgs},
		{name: "dormant in session target", target: "session", session: groupArgs},
		{name: "dormant environment args", target: "project", envKey: "SOHO_LANE_BUILDERS_ARGS", envValue: "--reasoning-effort low"},
		{name: "blank session override", target: "session", project: groupArgs, session: "lane.builders.args=\n", wantGroupKind: true},
		{name: "scoped role args", target: "project", project: groupArgs, session: roleArgs, wantNative: "high"},
		{name: "explicit group kind", target: "project", project: groupArgs + explicitKind, wantNative: "low", wantGroupKind: true},
		{name: "no group args", target: "project", wantGroupKind: true},
		{name: "dormant session scalar model", target: "project", sessionKey: "lane.builders.model", sessionValue: "grok-4.7"},
		{name: "dormant session model rotation", target: "project", sessionKey: "lane.builders.models", sessionValue: modelList},
		{name: "dormant project rotation during user repair", target: "user", project: "lane.builders.models=" + modelList + "\n"},
		{name: "dormant environment model rotation", target: "project", envKey: "SOHO_LANE_BUILDERS_MODELS", envValue: modelList},
		{name: "dormant environment scalar model", target: "project", envKey: "SOHO_LANE_BUILDERS_MODEL", envValue: "grok-4.7"},
		{name: "active session rotation with explicit kind", target: "project", project: explicitKind, sessionKey: "lane.builders.models", sessionValue: modelList,
			wantModel: "grok-4.7", wantModelChoice: "list", wantGroupKind: true},
		{name: "blank session model rotation", target: "project", session: "lane.builders.models=\n", wantGroupKind: true},
		{name: "environment scalar during explicit kind repair", target: "project", project: explicitKind + "role.designer.model=grok\n",
			envKey: "SOHO_LANE_BUILDERS_MODEL", envValue: "grok-4.7", wantModel: "grok-4.7", wantModelChoice: "lane", wantStoredModel: "grok", wantGroupKind: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			bin := t.TempDir()
			env["PATH"], env["SOHO_FAKECLI_CONFIG"] = bin, bin
			// Re-executed fake CLIs do not need the detector's exit delay.
			env["GORACE"] = fakecli.RaceOptions(env.List())
			env["HERDR_ENV"], env["HERDR_PANE_ID"] = "1", "p1"
			env["SOHO_CALLER_CHECK"], env["SOHO_TRUST_CWD"] = "off", "off"
			env["SOHO_NO_UPDATE_CHECK"], env["SOHO_WAIT_POLL_MS"] = "1", "1"
			if tc.envKey != "" {
				env[tc.envKey] = tc.envValue
			}
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
				{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
				{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
				{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
				{Argv: []string{"pane", "get"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"workspace_id":"ws","pane_id":"p1"}}}`},
				{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
				{AnyArgs: true},
			}
			if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
				t.Fatal(err)
			}
			if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{
				"user":    filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"),
				"project": filepath.Join(cwd, ".agents", "soho.conf"),
				"session": filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf"),
			}
			input := map[string]string{"user": tc.user, "project": tc.project, "session": tc.session}
			input[tc.target] += "panes=4\nrole.designer.kind=grok\nrole.critical-implementer.kind=grok\nrole.documenter.kind=codex\n"
			for source, body := range input {
				if body != "" {
					tm6Write(t, paths[source], body)
				}
			}
			if tc.sessionKey != "" {
				code, out, stderr := runIn(t, []string{"session", "set", tc.sessionKey, tc.sessionValue}, env, cwd)
				if code != 0 {
					t.Fatalf("session set: exit=%d stdout=%q stderr=%q", code, out, stderr)
				}
				input["session"] = mustRead(t, paths["session"])
			}
			wantModel, wantModelChoice := tc.wantModel, tc.wantModelChoice
			if wantModel == "" {
				wantModel, wantModelChoice = "grok", "default"
			}
			launch := func(wantStarts int) []string {
				t.Helper()
				code, out, stderr := runIn(t, []string{"spawn", "critical-implementer", "--pane", "p1", "--fresh"}, env, cwd)
				var response struct {
					Kind, Model string
					ModelChoice string `json:"model_choice"`
				}
				if code != 0 || json.Unmarshal([]byte(out), &response) != nil || response.Kind != "grok" {
					t.Fatalf("spawn: exit=%d stdout=%q stderr=%q", code, out, stderr)
				}
				calls, err := fakecli.ReadCallsForConfig(filepath.Join(bin, "herdr.json"))
				if err != nil {
					t.Fatal(err)
				}
				var starts [][]string
				for _, call := range calls {
					if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
						starts = append(starts, call.Argv)
					}
				}
				if len(starts) != wantStarts {
					t.Fatalf("transport starts=%q want %d", starts, wantStarts)
				}
				for i, arg := range starts[wantStarts-1] {
					if arg == "--" {
						argv := starts[wantStarts-1][i+1:]
						var model string
						for j, token := range argv {
							if token == "--model" && j+1 < len(argv) {
								model = argv[j+1]
							}
						}
						if model != wantModel {
							t.Errorf("launch %d transport --model=%q want=%q argv=%q", wantStarts, model, wantModel, argv)
						}
						if response.Model != wantModel || response.ModelChoice != wantModelChoice {
							t.Errorf("launch %d response model=%q choice=%q want model=%q choice=%q", wantStarts, response.Model, response.ModelChoice, wantModel, wantModelChoice)
						}
						return argv
					}
				}
				t.Fatalf("missing native argv: %q", starts[wantStarts-1])
				return nil
			}
			before := launch(1)
			fix := []string{"doctor", "--fix"}
			if tc.target != "project" {
				fix = append(fix, "--"+tc.target)
			}
			if code, out, stderr := runIn(t, fix, env, cwd); code != 0 {
				t.Fatalf("repair: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			after := launch(2)
			t.Logf("native argv before=%q after=%q", before, after)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("repair changed native argv: before=%q after=%q", before, after)
			}
			if tc.wantNative != "" && !strings.Contains(" "+strings.Join(before, " ")+" ", " --reasoning-effort "+tc.wantNative+" ") {
				t.Errorf("expected live %s native argument before repair: %q", tc.wantNative, before)
			}
			file := paths[tc.target]
			if tc.envKey != "" && strings.Contains(mustRead(t, file), tc.envValue) {
				t.Errorf("repair copied an environment value: %q", mustRead(t, file))
			}
			if tc.wantStoredModel != "" && core.FileKeyValue(file, "lane.builders.model") != tc.wantStoredModel {
				t.Errorf("repair missed local model fold: %q", mustRead(t, file))
			}
			if kind := core.FileKeyValue(file, "lane.builders.kind"); (kind == "grok") != tc.wantGroupKind {
				t.Errorf("unexpected group kind %q for %s repair: %q", kind, tc.target, mustRead(t, file))
			}
			if !tc.wantGroupKind && core.FileKeyValue(file, "role.designer.kind") != "grok" {
				t.Errorf("repair discarded explicit role kind: %q", mustRead(t, file))
			}
			for source, body := range input {
				if source != tc.target && body != "" && mustRead(t, paths[source]) != body {
					t.Errorf("repair changed %s layer", source)
				}
			}
		})
	}
}
