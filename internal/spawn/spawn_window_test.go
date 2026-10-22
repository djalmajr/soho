package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnPostStartJavaScriptCases(t *testing.T) {
	const auth = "Error: 401 Unauthorized: Incorrect API key provided token=sk_test_fakestartup1\n"
	cases := []struct {
		js, state, screen     string
		startCode, wantCode   int
		wantReady, wantRoster bool
	}{
		// JS: "spawn: an agent that exits right after start without the marker dies 4, no roster line"
		{js: "spawn: an agent that exits right after start without the marker dies 4, no roster line", state: "gone", screen: "Error: process exited\n", wantCode: 4},
		// JS: "spawn: a stopped auth screen after a successful start dies 14, no roster line, pane open"
		{js: "spawn: a stopped auth screen after a successful start dies 14, no roster line, pane open", state: "gone", screen: auth, wantCode: 14},
		// JS: "spawn: a blocked post-start state with an auth screen dies 14"
		{js: "spawn: a blocked post-start state with an auth screen dies 14", state: "blocked", screen: auth, wantCode: 14},
		// JS: "spawn: a blocked post-start state without auth keeps the ready path and the roster line"
		{js: "spawn: a blocked post-start state without auth keeps the ready path and the roster line", state: "blocked", screen: "screen line 1\nscreen line 2\n", wantReady: true, wantRoster: true},
		// JS: "spawn: a still-working worker is never classified from an old error line"
		{js: "spawn: a still-working worker is never classified from an old error line", state: "working", screen: auth, wantReady: true, wantRoster: true},
		// JS: "spawn: a transient provider screen at start still proceeds to ready"
		{js: "spawn: a transient provider screen at start still proceeds to ready", state: "working", screen: "Error: Connection error.\n", wantReady: true, wantRoster: true},
		// JS: "spawn: an immediate exit with an auth screen dies 14, not 4"
		{js: "spawn: an immediate exit with an auth screen dies 14, not 4", state: "gone", screen: auth, wantCode: 14},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.js, func(t *testing.T) {
			// JS: title is fixed in the table entry for this individual subtest.
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
				{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stderr: "agent_not_ready: login prompt\n", Code: tc.startCode},
				{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"` + tc.state + `"}}}`},
				{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: tc.screen},
			}
			f := newSpawnFixture(t, rules)
			configureSpawnFixture(t, &f)
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--name", "worker", "--pane", "w0test:p0a"})
			if code != tc.wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if tc.wantReady {
				if !strings.Contains(stdout, `"status": "ready"`) {
					t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
				}
			} else if strings.Contains(stdout, `"status": "ready"`) {
				t.Fatalf("unexpected ready state: %q", stdout)
			}
			roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
			present := err == nil && strings.Contains(string(roster), "worker\tw0test:p0a")
			if present != tc.wantRoster {
				t.Fatalf("roster present=%v data=%q err=%v", present, roster, err)
			}
			if tc.wantCode == 14 && strings.Contains(stderr, "sk_test_fakestartup1") {
				t.Fatalf("secret was not redacted: %q", stderr)
			}
			if tc.wantCode == 14 {
				for _, call := range calls {
					if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
						t.Fatalf("auth pane was closed: %#v", calls)
					}
				}
			}
		})
	}
}

func TestBusyRetryDelayDefaultsToOneSecond(t *testing.T) {
	if busyRetryDelay != time.Second {
		t.Fatalf("busy retry delay=%v, want %v", busyRetryDelay, time.Second)
	}
}

func TestSpawnRetryAndUpdateJavaScriptCases(t *testing.T) {
	t.Run("spawn: agent_pane_busy gets 15 retries and the 16th failure ends without a 17th agent start", func(t *testing.T) {
		// JS: "spawn: agent_pane_busy twice, then success (15×1s retry budget)"
		// Mutation captured: lowering the tries < 15 retry bound stops before the 16th start.
		previousDelay := busyRetryDelay
		busyRetryDelay = 0
		t.Cleanup(func() { busyRetryDelay = previousDelay })
		rules := make([]fakecli.Rule, 0, 16)
		for i := 1; i <= 16; i++ {
			rules = append(rules, fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Call: i, Stderr: `{"error":{"code":"agent_pane_busy"}}`, Code: 1})
		}
		f := newSpawnFixture(t, rules)
		o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1000"}
		if code := catchExitCode(func() { startAgent(o, "grok", f.env, false, nil) }); code != 4 {
			t.Fatalf("final agent_pane_busy exit=%d, want 4", code)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		starts := 0
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				starts++
			}
		}
		if starts != 16 {
			t.Fatalf("agent start attempts=%d, want initial try plus 15 retries; calls=%#v", starts, calls)
		}
	})

	t.Run("spawn: a CLI that updates itself at start is relaunched once, then ready", func(t *testing.T) {
		// JS: "spawn: a CLI that updates itself at start is relaunched once, then ready"
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`, Code: 1},
			{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Call: 1, Stdout: "❯ codex -s workspace-write\nUpdating Codex via npm install\nPlease restart Codex\n"},
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Call: 2, Stdout: "ready\n"},
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Call: 3, Stdout: "ready\n"},
		})
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1"}
		if startAgent(o, "codex", f.env, false, nil) {
			t.Fatal("initial start blocked")
		}
		oldErr := platform.Stderr
		var stderr strings.Builder
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		state, blocked := "", false
		code := catchExitCode(func() { state, _, blocked = checkStartWindow(o, "codex", f.env, false, nil, f.ctx) })
		if code != 0 {
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			t.Fatalf("exit=%d stderr=%q calls=%#v", code, stderr.String(), calls)
		}
		if state != "working" || blocked || !strings.Contains(stderr.String(), "updated itself at start and exited; started it again") {
			t.Fatalf("state=%q blocked=%v stderr=%q", state, blocked, stderr.String())
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		starts := 0
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				starts++
			}
		}
		if starts != 2 {
			t.Fatalf("relaunch start count=%d calls=%#v", starts, calls)
		}
	})

	t.Run("spawn: the relaunch that exits again dies 4 (no second relaunch)", func(t *testing.T) {
		// JS: "spawn: the relaunch that exits again dies 4 (no second relaunch)"
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 1},
			{Argv: []string{"agent", "get", "worker"}, Call: 3, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 1},
			{Argv: []string{"agent", "get", "worker"}, Call: 4, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 1},
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Call: 1, Stdout: "❯ codex -s workspace-write\nUpdating Codex via npm install\nPlease restart Codex\n"},
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Call: 2, Stdout: "❯ codex -s workspace-write\nUpdating Codex via npm install\nPlease restart Codex\n"},
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Call: 3, Stdout: "❯ codex -s workspace-write\nUpdating Codex via npm install\nPlease restart Codex\n"},
		})
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		o := spawnOptions{name: "worker", pane: "p", timeout: "1"}
		if startAgent(o, "codex", f.env, false, nil) {
			t.Fatal("initial start blocked")
		}
		code := 0
		func() {
			defer func() {
				if e, ok := recover().(*platform.ExitError); ok {
					code = e.Code
				}
			}()
			_, _, _ = checkStartWindow(o, "codex", f.env, false, nil, f.ctx)
		}()
		if code != 4 {
			t.Fatalf("exit=%d", code)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		starts := 0
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				starts++
			}
		}
		if starts != 2 {
			t.Fatalf("unexpected extra relaunch: %d calls=%#v", starts, calls)
		}
	})

	t.Run("spawn: an alive agent without the marker proceeds after the first probe", func(t *testing.T) {
		// JS: "spawn: an alive agent without the marker proceeds after the first probe"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}, {Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Stdout: "ready\n"}})
		state, screen, blocked := checkStartWindow(spawnOptions{name: "worker", timeout: "1"}, "codex", f.env, false, nil, f.ctx)
		if state != "working" || screen != "ready\n" || blocked {
			t.Fatalf("state=%q screen=%q blocked=%v", state, screen, blocked)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil || len(calls) != 2 {
			t.Fatalf("probe calls=%#v err=%v", calls, err)
		}
	})
}

func TestSpawnRemainingReuseJavaScriptCases(t *testing.T) {
	t.Run("spawn: lanes=off refuses an idle same-role worker without recorded effort (--name given)", func(t *testing.T) {
		// JS: "spawn: lanes=off refuses an idle same-role worker without recorded effort (--name given)"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
		f.roster(t, "worker\tw0test:p0a\tgrok\tworker\txai\t1\t/tmp/work\tnow")
		name, unavailable, blocked := FindReusable("worker", "grok", "/tmp/work", "worker", "", "ask", "high", f.ctx, f.env, f.cwd)
		if name != "" || unavailable != "" || blocked {
			t.Fatalf("name=%q unavailable=%q blocked=%v", name, unavailable, blocked)
		}
	})

	t.Run("spawn: lanes=off — the same role on another recorded model spawns a new worker", func(t *testing.T) {
		// JS: "spawn: lanes=off — the same role on another recorded model spawns a new worker"
		f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
		f.roster(t, spawnRosterRow("worker", "worker", "grok-4", "full", ""))
		name, _, blocked := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd)
		if name != "" || blocked {
			t.Fatalf("mismatched model reused: name=%q blocked=%v", name, blocked)
		}
	})

	t.Run("spawn: lanes=off — role.<role>.args reaches only that role", func(t *testing.T) {
		// JS: "spawn: lanes=off — role.<role>.args reaches only that role"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--worker-only", Source: "project"}
		if got := ConfigNativeArgs("grok", "", "worker", f.ctx, f.env, f.cwd, nil); got != "--worker-only" {
			t.Fatalf("worker args=%q", got)
		}
		if got := ConfigNativeArgs("grok", "", "other", f.ctx, f.env, f.cwd, nil); got != "" {
			t.Fatalf("other role args=%q", got)
		}
	})

	t.Run("spawn: lanes on — lane.<lane>.args reaches every worker of the lane; role args stay ignored", func(t *testing.T) {
		// JS: "spawn: lanes on — lane.<lane>.args reaches every worker of the lane; role args stay ignored"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.ctx.Entries["lane_build_args"] = core.ConfigEntry{Value: "--lane-only", Source: "project"}
		f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--role-only", Source: "project"}
		if got := ConfigNativeArgs("grok", "build", "worker", f.ctx, f.env, f.cwd, nil); got != "--lane-only" {
			t.Fatalf("lane args=%q", got)
		}
	})

	t.Run("spawn: lanes=off — a worker opened with different native args is not reused; equal args are", func(t *testing.T) {
		// JS: "spawn: lanes=off — a worker opened with different native args is not reused; equal args are"
		f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
		f.roster(t, spawnRosterRow("worker", "implementer", "grok-4.7", "full", "--role"))
		if got, _, _ := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "" {
			t.Fatalf("different native args reused %q", got)
		}
		f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--role", Source: "project"}
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "worker" {
			t.Fatalf("equal native args not reused: %q", got)
		}
	})

	t.Run("enforceWorkerCap: a dead last roster worker is not counted and does not stop spawn", func(t *testing.T) {
		// JS: "enforceWorkerCap: a dead last roster worker is not counted and does not stop spawn"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}})
		f.ctx.Entries["max_workers"] = core.ConfigEntry{Value: "1", Source: "user"}
		f.roster(t, spawnRosterRow("dead", "worker", "", "", ""))
		if code := catchExitCode(func() { core.EnforceWorkerCap(f.ctx, f.env, f.cwd) }); code != 0 {
			t.Fatalf("dead worker stopped cap: exit=%d", code)
		}
	})
}

func TestSpawnRemainingIntegrationJavaScriptCases(t *testing.T) {
	t.Run("spawn: another live editor in the same cwd warns (one per editor)", func(t *testing.T) {
		// JS: "spawn: another live editor in the same cwd warns (one per editor)"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"old","pane_id":"p-old","agent_status":"working"},{"name":"reader","pane_id":"p-reader","agent_status":"working"}]}}`}})
		f.roster(t,
			strings.Join([]string{"old", "p-old", "grok", "implementer", "xai", "1", "/tmp/work", "now"}, "\t"),
			strings.Join([]string{"reader", "p-reader", "grok", "scouter", "xai", "1", "/tmp/work", "now"}, "\t"),
		)
		oldErr := platform.Stderr
		var stderr strings.Builder
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		sameTreeEditors("new", "implementer", "/tmp/work", filepath.Join(f.state, "ws"), f.env, f.cwd, f.ctx)
		if strings.Count(stderr.String(), "both edit /tmp/work") != 1 || !strings.Contains(stderr.String(), "'new' and 'old'") {
			t.Fatalf("warnings=%q", stderr.String())
		}
	})

	t.Run("spawn: a failing agent list after the start never fails the spawn", func(t *testing.T) {
		// JS: "spawn: a failing agent list after the start never fails the spawn"
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Call: 1, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "list"}, Call: 2, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "list"}, Call: 3, Stderr: "list unavailable\n", Code: 3},
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
		})
		configureSpawnFixture(t, &f)
		f.roster(t, strings.Join([]string{"old", "p-old", "grok", "implementer", "xai", "1", f.cwd, "now"}, "\t"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--name", "new", "--pane", "w0test:p0a", "--fresh"})
		if code != 0 || !strings.Contains(stdout, `"status": "ready"`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		lists := 0
		for _, call := range calls {
			if strings.Join(call.Argv, " ") == "agent list" {
				lists++
			}
		}
		if lists != 3 {
			t.Fatalf("post-start list probe missing: lists=%d calls=%#v", lists, calls)
		}
	})

	t.Run("spawn: codex effort max reaches the CLI when the model advertises it", func(t *testing.T) {
		// JS: "spawn: codex effort max reaches the CLI when the model advertises it"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: codex\nmodel: large\neffort: max\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.root, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		cache := `{"models":[{"slug":"large","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"}]}]}`
		if err := os.WriteFile(filepath.Join(f.root, ".codex", "models_cache.json"), []byte(cache), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--name", "worker", "--pane", "w0test:p0a"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		found := false
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" && strings.Contains(strings.Join(call.Argv, " "), `model_reasoning_effort="max"`) {
				found = true
			}
		}
		if !found {
			t.Fatalf("max effort not passed to CLI: %#v", calls)
		}
	})

	t.Run("spawn: a cursor model that already encodes the effort — silent; another effort — warns", func(t *testing.T) {
		// JS: "spawn: a cursor model that already encodes the effort — silent; another effort — warns"
		f := newSpawnFixture(t, nil)
		oldErr := platform.Stderr
		var stderr strings.Builder
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		buildArgs("cursor", "ask", "cursor-model-high", "high", f.ctx, f.env)
		if stderr.Len() != 0 {
			t.Fatalf("matching suffix warned: %q", stderr.String())
		}
		buildArgs("cursor", "ask", "cursor-model-high", "low", f.ctx, f.env)
		if !strings.Contains(stderr.String(), "already encodes effort 'high'") {
			t.Fatalf("missing mismatch warning: %q", stderr.String())
		}
	})

	t.Run("spawn: a reused name replaces the stale roster lines (name and pane) with a warning", func(t *testing.T) {
		// JS: "spawn: a reused name replaces the stale roster lines (name and pane) with a warning"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.roster(t,
			strings.Join([]string{"worker", "p-old", "grok", "worker", "xai", "1", "/tmp/work", "old"}, "\t"),
			strings.Join([]string{"old-name", "w0test:p0a", "grok", "worker", "xai", "1", "/tmp/work", "old"}, "\t"),
		)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--name", "worker", "--pane", "w0test:p0a"})
		if code != 0 || !strings.Contains(stderr, "replaced the stale roster line") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(roster), "\n") != 2 || !strings.Contains(string(roster), "worker\tw0test:p0a") {
			t.Fatalf("roster=%q", roster)
		}
	})

	t.Run("spawn: panes=2 — the review roles have no lane (exit 3, the orchestrator reviews)", func(t *testing.T) {
		// JS: "spawn: panes=2 — the review roles have no lane (exit 3, the orchestrator reviews)"
		f := newSpawnFixture(t, nil)
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.env["SOHO_PANES"] = "2"
		code, _, _, _ := runCmdSpawn(t, f, []string{"reviewer"})
		if code != 3 {
			t.Fatalf("reviewer exit=%d", code)
		}
	})
}

func TestSpawnSessionAfterSpawnJavaScriptCases(t *testing.T) {
	t.Run("spawn: a session set after the spawn blocks the reuse", func(t *testing.T) {
		// JS: "spawn: a session set after the spawn blocks the reuse"
		t.Run("same role", func(t *testing.T) {
			// JS: "same role"
			f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
			f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--new", Source: "session"}
			if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f.roster(t, spawnRosterRow("worker", "worker", "grok-4.7", "ask", ""))
			if got, _, _ := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "" {
				t.Fatalf("same-role worker reused: %q", got)
			}
		})
		t.Run("cross-role", func(t *testing.T) {
			// JS: "cross-role"
			f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
			f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--new", Source: "session"}
			if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f.roster(t, spawnRosterRow("worker", "implementer", "grok-4.7", "full", ""))
			if got, _, _ := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "" {
				t.Fatalf("cross-role worker reused: %q", got)
			}
		})
		t.Run("lane", func(t *testing.T) {
			// JS: "lane"
			f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
			configureSpawnFixture(t, &f)
			f.env["SOHO_LANES"] = "on"
			f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
			f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
			f.ctx.Entries["lane_build_model"] = core.ConfigEntry{Value: "grok-4.7", Source: "project"}
			f.ctx.Entries["lane_build_args"] = core.ConfigEntry{Value: "--new", Source: "session"}
			f.ctx.Order = []string{"lane_build_roles", "lane_build_kind", "lane_build_model", "lane_build_args"}
			f.roster(t, strings.Join([]string{"build", "p-build", "grok", "worker", "xai", "1", f.cwd, "now", "grok-4.7", "ask", "worker", "build", "", "", "high"}, "\t"))
			code, stdout, _, calls := runCmdSpawn(t, f, []string{"worker"})
			if code != 13 || !strings.Contains(stdout, `"status":"kind-mismatch"`) {
				t.Fatalf("exit=%d stdout=%q calls=%#v", code, stdout, calls)
			}
		})
	})
}

func catchExitCode(fn func()) (code int) {
	defer func() {
		if value := recover(); value != nil {
			if e, ok := value.(*platform.ExitError); ok {
				code = e.Code
				return
			}
			panic(value)
		}
	}()
	fn()
	return 0
}
