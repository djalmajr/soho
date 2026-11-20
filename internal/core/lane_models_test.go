package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestModelListKeysAndValidation(t *testing.T) {
	for _, key := range []string{"lane.build.models", "model_cap"} {
		if !ConfigKeyOk(key) {
			t.Fatalf("ConfigKeyOk(%q) = false", key)
		}
	}
	for _, value := range []string{"off", "1", "2", "100"} {
		if !ConfigValueOk("model_cap", value, platform.Env{}, "") {
			t.Fatalf("model_cap=%q refused", value)
		}
	}
	for _, value := range []string{"", "0", "-1", "2.5", "abc", "off ", "01"} {
		if ConfigValueOk("model_cap", value, platform.Env{}, "") {
			t.Fatalf("model_cap=%q accepted", value)
		}
	}
	for _, value := range []string{"m1", "m1,m2", "m1, m2 ,, m3", "m1:fast,m2"} {
		if !ConfigValueOk("lane.build.models", value, platform.Env{}, "") {
			t.Fatalf("lane.build.models=%q refused", value)
		}
	}
	for _, value := range []string{"", ",,,", "m1\nm2", "m1#x"} {
		if ConfigValueOk("lane.build.models", value, platform.Env{}, "") {
			t.Fatalf("lane.build.models=%q accepted", value)
		}
	}
	ctx := &Config{Entries: map[string]ConfigEntry{
		"lane_build_models": {Value: " m1 , , m2,m1:fast ,", Source: "project"},
		"lane_build_model":  {Value: "old", Source: "user"},
	}, Order: []string{"lane_build_models", "lane_build_model"}}
	env := platform.Env{"SOHO_LANES": "on"}
	if got := LaneModels(ctx, "build", env); len(got) != 3 || got[0] != "m1" || got[1] != "m2" || got[2] != "m1:fast" {
		t.Fatalf("LaneModels = %#v", got)
	}
	if got := LaneModelSpec(ctx, "build", env); got != "m1" {
		t.Fatalf("LaneModelSpec = %q, want the list's first model", got)
	}
	warnings := ModelShadowWarnings(ctx, env)
	if len(warnings) != 1 || warnings[0] != "config: lane.build.model=old is ignored: lane.build.models= m1 , , m2,m1:fast , wins" {
		t.Fatalf("warnings = %#v", warnings)
	}
	if got := LaneModels(ctx, "build", platform.Env{"SOHO_LANES": "on", "SOHO_LANE_BUILD_MODELS": "only"}); len(got) != 1 || got[0] != "only" {
		t.Fatalf("environment list = %#v", got)
	}
}

func TestModelCapOffAndOn(t *testing.T) {
	ctx := &Config{Entries: map[string]ConfigEntry{}}
	if n, capped := ModelCap(ctx, platform.Env{}); capped || n != 0 {
		t.Fatalf("default cap = (%d,%t), want off", n, capped)
	}
	ctx.Entries["model_cap"] = ConfigEntry{Value: "2", Source: "project"}
	if n, capped := ModelCap(ctx, platform.Env{}); !capped || n != 2 {
		t.Fatalf("configured cap = (%d,%t), want (2,true)", n, capped)
	}
	ctx.Entries["model_cap"] = ConfigEntry{Value: "bad", Source: "project"}
	if n, capped := ModelCap(ctx, platform.Env{}); capped || n != 0 {
		t.Fatalf("invalid file cap = (%d,%t), want off", n, capped)
	}
}

func modelCountRow(name, model string) string {
	return strings.Join([]string{name, "p-" + name, "grok", "implementer", "xai", "1", "/tmp/work", "now", model, "ask", "implementer", "build"}, "\t")
}

// The count is a server-scope fact: it reads every workspace under the state
// root and the model recorded in each roster row, never the lane column. Gone,
// released and retired rows must not consume a slot.
// The count must wait for the workspace's own roster lock (the one every
// roster writer takes) and must not probe herdr while holding it. Locking only
// the state root, or probing under the lock, fails this fixture.
func TestLiveModelCountsWaitsForWorkspaceLockWithoutProbing(t *testing.T) {
	env, repo := fixture(t)
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "live1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	ws := filepath.Join(env.Get("SOHO_DIR"), "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + modelCountRow("live1", "m1") + "\n"
	if err := os.WriteFile(filepath.Join(ws, "agents.tsv"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(ws, "agents.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	held := true
	t.Cleanup(func() {
		if held {
			_ = os.Remove(lock)
		}
	})
	type countResult struct{ counts map[string]int }
	done := make(chan countResult, 1)
	go func() {
		ctx := LoadConfig(env, repo)
		done <- countResult{LiveModelCounts(&ctx, env, repo)}
	}()
	select {
	case got := <-done:
		t.Fatalf("count returned while the workspace lock was held: %#v", got.counts)
	case <-time.After(150 * time.Millisecond):
	}
	calls, _ := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "get" {
			t.Fatalf("liveness probe ran while the workspace lock was held: %#v", call)
		}
	}
	held = false
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.counts["m1"] != 1 {
			t.Fatalf("counts after release = %#v, want m1=1", got.counts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the count did not finish after the workspace lock was released")
	}
}

func TestLiveModelCountsServerScope(t *testing.T) {
	env, repo := fixture(t)
	bin := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "live1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "get", "live2"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "get", "gone"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
		{Argv: []string{"agent", "get", "released-state"}, Stdout: `{"result":{"agent":{"agent_status":"released"}}}`},
		{Argv: []string{"agent", "get", "retired"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "get", "other"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	root := env.Get("SOHO_DIR")
	for dir, rows := range map[string]string{
		"ws1": strings.Join([]string{modelCountRow("live1", "m1"), modelCountRow("gone", "m1"), modelCountRow("released-state", "m1")}, "\n") + "\n",
		"ws2": strings.Join([]string{modelCountRow("live2", "m1"), modelCountRow("retired", "m1"), modelCountRow("other", "m2")}, "\n") + "\n",
	} {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + rows
		if err := os.WriteFile(filepath.Join(full, "agents.tsv"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ws2", "released-panes.tsv"), []byte("# name\tpane\tkind\treleased\nretired\tp-retired\tgrok\tnow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := LoadConfig(env, repo)
	counts := LiveModelCounts(&ctx, env, repo)
	if counts["m1"] != 2 || counts["m2"] != 1 {
		t.Fatalf("counts = %#v, want m1=2 m2=1", counts)
	}
	if LiveModelCount(&ctx, env, repo, "m1") != 2 {
		t.Fatalf("LiveModelCount(m1) disagrees with the map")
	}
}

func TestCmdConfigPrintsTheModelShadowWarning(t *testing.T) {
	ctx := &Config{Entries: map[string]ConfigEntry{
		"lane_build_roles":  {Value: "implementer", Source: "project"},
		"lane_build_models": {Value: "m1,m2", Source: "project"},
		"lane_build_model":  {Value: "old", Source: "user"},
	}, Order: []string{"lane_build_roles", "lane_build_models", "lane_build_model"}}
	env := platform.Env{"SOHO_LANES": "on"}
	_, stderr := capture(t, func() { CmdConfig(ctx, env, "") })
	want := "soho: warning: config: lane.build.model=old is ignored: lane.build.models=m1,m2 wins\n"
	if stderr != want {
		t.Fatalf("config stderr=%q\nwant       %q", stderr, want)
	}
	delete(ctx.Entries, "lane_build_models")
	ctx.Order = []string{"lane_build_roles", "lane_build_model"}
	_, stderr = capture(t, func() { CmdConfig(ctx, env, "") })
	if stderr != "" {
		t.Fatalf("control config stderr=%q, want none", stderr)
	}
}
