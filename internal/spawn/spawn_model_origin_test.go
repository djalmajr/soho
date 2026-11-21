package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// SOH-473: the advisory states only that soho passes no model argument and,
// with lanes off, what lane configuration exists. It predicts neither the
// assistant's model nor the model a lane would choose.
func modelOriginFixture(t *testing.T) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	if err := os.WriteFile(filepath.Join(f.root, "roles", "validator.md"), []byte("---\nkind: pi\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "pi", nil); err != nil {
		t.Fatal(err)
	}
	f.roster(t)
	return f
}

func modelOriginConfig(f *spawnFixture, key, value string) {
	f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
	f.ctx.Order = append(f.ctx.Order, key)
}

func modelOriginWarned(stderr string) bool {
	return strings.Contains(stderr, "soho: warning: spawn: soho passes no model") || strings.Contains(stderr, "soho: warning: spawn: the configured model")
}

func modelOriginStartArgs(calls []fakecli.Call) []string {
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			return call.Argv
		}
	}
	return nil
}

func TestSpawnNoModelWarning(t *testing.T) {
	const line = "soho: warning: spawn: soho passes no model to this pi worker ('validator'); it starts on the model its native arguments or the assistant's own settings choose"
	for _, tc := range []struct {
		name, suffix string
		lanes        string
		config       map[string]string
		args         []string
		wantLine     bool
	}{
		{name: "lanes off", lanes: "off", wantLine: true},
		{name: "lanes on without lane model", lanes: "on", config: map[string]string{"lane_build_roles": "validator", "lane_build_kind": "pi"}, wantLine: true},
		{name: "lanes off with lane model", lanes: "off", config: map[string]string{"lane_build_roles": "validator", "lane_build_model": "lane-default"}, suffix: "; lanes are off: lane 'build' configures lane.build.model=lane-default; pass --model to choose", wantLine: true},
		{name: "lanes off with lane models list", lanes: "off", config: map[string]string{"lane_build_roles": "validator", "lane_build_models": "first-lane,second-lane"}, suffix: "; lanes are off: lane 'build' configures lane.build.models=first-lane,second-lane; pass --model to choose", wantLine: true},
		{name: "raw config value", lanes: "off", config: map[string]string{"lane_build_roles": "validator", "lane_build_models": " ^m1$, m2 "}, suffix: "; lanes are off: lane 'build' configures lane.build.models= ^m1$, m2 ; pass --model to choose", wantLine: true},
		{name: "explicit model", lanes: "off", args: []string{"--model", "explicit-model"}},
		{name: "lanes on with lane model", lanes: "on", config: map[string]string{"lane_build_roles": "validator", "lane_build_kind": "pi", "lane_build_model": "lane-default"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := modelOriginFixture(t)
			f.env["SOHO_LANES"] = tc.lanes
			for key, value := range tc.config {
				modelOriginConfig(&f, key, value)
			}
			code, stdout, stderr, calls := runCmdSpawn(t, f, append([]string{"validator", "--fresh"}, tc.args...))
			if code != 0 || len(calls) == 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			want := line + tc.suffix + "\n"
			if got := strings.Count(stderr, want); (got == 1) != tc.wantLine {
				t.Fatalf("line %q count=%d wantLine=%t stderr=%q", want, got, tc.wantLine, stderr)
			}
			if modelOriginWarned(stderr) != tc.wantLine || strings.Contains(stderr, "would use") {
				t.Fatalf("unexpected advisory in %q", stderr)
			}
			if !spawnJSONHasField(t, stdout, `"status": "ready"`) {
				t.Fatalf("spawn result=%q", stdout)
			}
		})
	}
}

func TestSpawnNoModelWarningCodex(t *testing.T) {
	f := modelOriginFixture(t)
	if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
		t.Fatal(err)
	}
	const line = "soho: warning: spawn: soho passes no model to this codex worker ('validator'); it starts on the model its native arguments or the assistant's own settings choose"
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"validator", "--fresh", "--kind", "codex", "--mcp", "none"})
	if code != 0 || strings.Count(stderr, line+"\n") != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSpawnNativeModelStillGetsNoModelLine(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		extra      []string
	}{
		{name: "pi", kind: "pi", extra: []string{"--", "--model", "worker-model"}},
		{name: "codex long", kind: "codex", extra: []string{"--", "--model", "worker-model"}},
		{name: "codex config", kind: "codex", extra: []string{"--", "-c", `model="worker-model"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := modelOriginFixture(t)
			if tc.kind == "codex" {
				if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"validator", "--fresh", "--kind", tc.kind, "--mcp", "none"}, tc.extra...)
			code, stdout, stderr, calls := runCmdSpawn(t, f, args)
			start := modelOriginStartArgs(calls)
			if code != 0 || len(start) == 0 {
				t.Fatalf("exit=%d start=%q stdout=%q stderr=%q", code, start, stdout, stderr)
			}
			if !strings.Contains(strings.Join(start, " "), "worker-model") {
				t.Fatalf("native model did not reach the worker: %q", start)
			}
			// The line describes soho's own action, so it stays true even
			// though the native arguments supply the model.
			if !modelOriginWarned(stderr) {
				t.Fatalf("missing no-model line: %q", stderr)
			}
		})
	}
}

func TestSpawnNativeModelStillGetsNoModelLineHC(t *testing.T) {
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "validator", "pi")
	rules := append(hcFixtureRules(), fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker})
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn()
	exit, out, stderr, _ := spawnHcExitWithLaunch(t, bin, []string{"validator", "--fresh", "--", "--model", "worker-model"}, ctx, env, cwd, conn, nil)
	if exit != nil || !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
		t.Fatalf("exit=%v stdout=%q stderr=%q", exit, out.String(), stderr.String())
	}
	_, params := parseTerminalRunReq(t, conn.getWritten())
	argv, err := json.Marshal(params["argv"])
	if err != nil || !strings.Contains(string(argv), `"--model","worker-model"`) {
		t.Fatalf("native model missing from terminal.run argv=%s", argv)
	}
	if !modelOriginWarned(stderr.String()) {
		t.Fatalf("missing no-model line: %q", stderr.String())
	}
}

func TestSpawnModelOriginUnresolvedSpec(t *testing.T) {
	const line = "spawn: the configured model 'unmatched' does not resolve for cursor, so soho passes no model to this worker ('validator'); it starts on the model its native arguments or the assistant's own settings choose"
	for _, tc := range []struct {
		name, spec, listing string
		wantText            string
	}{
		{name: "cursor unmatched", spec: "unmatched", listing: "gemini-test-high - Google\n", wantText: line},
		{name: "cursor exact", spec: "gemini-test-high", listing: "gemini-test-high - Google\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := modelOriginFixture(t)
			if _, err := fakecli.Install(t, f.bin, "cursor-agent", []fakecli.Rule{{Argv: []string{"--list-models"}, Stdout: tc.listing}}); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_MODELS_TIMEOUT"] = "5"
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"validator", "--fresh", "--kind", "cursor", "--model", tc.spec, "--effort", "low"})
			start := modelOriginStartArgs(calls)
			if code != 0 || len(start) == 0 || !spawnJSONHasField(t, stdout, `"model_choice": "flag"`) {
				t.Fatalf("exit=%d start=%q stdout=%q stderr=%q", code, start, stdout, stderr)
			}
			if tc.wantText != "" && strings.Count(stderr, "soho: warning: "+tc.wantText+"\n") != 1 {
				t.Fatalf("missing unresolved-model line: %q", stderr)
			}
			if modelOriginWarned(stderr) != (tc.wantText != "") {
				t.Fatalf("warned=%v stderr=%q", modelOriginWarned(stderr), stderr)
			}
		})
	}
}

// The lanes-off suffix is literal configuration, not a prediction: caps,
// effective kind and effort cannot change it.
func TestSpawnModelOriginLaneConfigIsLiteral(t *testing.T) {
	for _, tc := range []struct {
		name, cap, models string
		live              []string
		enabledModel      string
		enabledExit       int
	}{
		{name: "cap off", cap: "off", models: "m1,m2", enabledModel: "m1", live: []string{"m1", "m2"}},
		{name: "all capped", cap: "1", models: "m1,m2", live: []string{"m1", "m2"}, enabledExit: 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, lanes := range []string{"on", "off"} {
				t.Run(lanes, func(t *testing.T) {
					f := laneModelFixture(t, tc.cap, tc.models)
					f.env["SOHO_LANES"] = lanes
					for i, model := range tc.live {
						appendRosterRow(t, f, []string{"a1", "a2"}[i], model, "other")
					}
					code, stdout, stderr, _, _ := runLaneModelSpawn(t, f, []string{"scouter", "--fresh"})
					if lanes == "on" {
						if code != tc.enabledExit || (code == 0 && !spawnJSONHasField(t, stdout, `"model": "`+tc.enabledModel+`"`)) {
							t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, tc.enabledExit, stdout, stderr)
						}
						return
					}
					want := "soho: warning: spawn: soho passes no model to this grok worker ('scouter'); it starts on the model its native arguments or the assistant's own settings choose; lanes are off: lane 'build' configures lane.build.models=m1,m2; pass --model to choose\n"
					if code != 0 || strings.Count(stderr, want) != 1 {
						t.Fatalf("exit=%d want line %q, stderr=%q", code, want, stderr)
					}
				})
			}
		})
	}
}

// A lane value with a line break (an environment value split across lines)
// is shown escaped, so the warning stays one line.
func TestSpawnModelOriginLaneConfigOneLine(t *testing.T) {
	f := laneModelFixture(t, "off", "m1,\nm2")
	f.env["SOHO_LANES"] = "off"
	code, _, stderr, _, _ := runLaneModelSpawn(t, f, []string{"scouter", "--fresh"})
	want := "soho: warning: spawn: soho passes no model to this grok worker ('scouter'); it starts on the model its native arguments or the assistant's own settings choose; lanes are off: lane 'build' configures lane.build.models=m1,\\nm2; pass --model to choose\n"
	if code != 0 || strings.Count(stderr, want) != 1 {
		t.Fatalf("exit=%d want line %q, stderr=%q", code, want, stderr)
	}
}

func TestSpawnModelOriginLaneConfigIgnoresKindLayer(t *testing.T) {
	for _, lanes := range []string{"on", "off"} {
		t.Run(lanes, func(t *testing.T) {
			f := modelOriginFixture(t)
			f.env["SOHO_LANES"] = lanes
			f.env["SOHO_ROLE_VALIDATOR_KIND"] = "pi"
			modelOriginConfig(&f, "lane_build_model", "m1")
			modelOriginConfig(&f, "lane_build_roles", "validator")
			code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"validator", "--fresh"})
			suffix := "; lanes are off: lane 'build' configures lane.build.model=m1; pass --model to choose"
			if code != 0 || !spawnJSONHasField(t, stdout, `"model": "default"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if strings.Count(stderr, suffix) != map[string]int{"on": 0, "off": 1}[lanes] {
				t.Fatalf("suffix %q count wrong in %q", suffix, stderr)
			}
		})
	}
}

func TestSpawnKindModelSettingsAreSilent(t *testing.T) {
	for _, key := range []string{"model_pi_worker", "model_pi"} {
		t.Run(key, func(t *testing.T) {
			f := modelOriginFixture(t)
			modelOriginConfig(&f, key, "worker-model")
			code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"validator", "--fresh"})
			if code != 0 || !spawnJSONHasField(t, stdout, `"model": "worker-model"`) || modelOriginWarned(stderr) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestSpawnRoleModelWithLanesOffPassesModel(t *testing.T) {
	f := modelOriginFixture(t)
	modelOriginConfig(&f, "role_validator_model", "role-model")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"validator", "--fresh"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"model": "role-model"`) || modelOriginWarned(stderr) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSpawnModelOriginIdleReuseSilent(t *testing.T) {
	f := newSpawnFixture(t, append([]fakecli.Rule{idleHerdrRule("validator")}, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	if err := os.WriteFile(filepath.Join(f.root, "roles", "validator.md"), []byte("---\nkind: pi\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "pi", nil); err != nil {
		t.Fatal(err)
	}
	f.roster(t, strings.Join([]string{"validator", "p2", "pi", "validator", "unknown", "1", f.cwd, "now", "", "auto", "validator", "", "", "", "", core.McpCanonical("inherit")}, "\t"))
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"validator", "--reuse"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"reused": true`) || modelOriginWarned(stderr) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertNoStart(t, calls)
}

func TestSpawnModelOriginAdmissionSilent(t *testing.T) {
	f := modelOriginFixture(t)
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code, message := configuredExit(func() { PreflightFreshSpawn([]string{"validator", "--fresh"}, f.ctx, f.env, f.cwd) })
	if code != 0 || message != "" || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d message=%q stdout=%q stderr=%q", code, message, out.String(), stderr.String())
	}
}
