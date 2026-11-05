package core

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// callerCapFixture extends the live-worker fixture with a caller pane: the
// fake "herdr agent get <pane>" answers with the calling agent's name.
func callerCapFixture(t *testing.T, rows []string, callerPane, callerName string, callerNameRule *fakecli.Rule, agentList string) (platform.Env, string, string) {
	t.Helper()
	rules := []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":` + agentList + `}}`}}
	if callerNameRule != nil {
		rules = append(rules, *callerNameRule)
	}
	env, cwd, state := tm9LiveWorkerFixture(t, rows, rules)
	// DJA-686: the preset's lane sum grew to 4 for panes=4; these cases pin
	// the cap at the old 3 so the caller-exclusion mechanics stay the point.
	env["SOHO_MAX_WORKERS"] = "3"
	if callerPane != "" {
		env["HERDR_PANE_ID"] = callerPane
	}
	return env, cwd, state
}

func TestEnforceWorkerCapExcludesTheCallingAgent(t *testing.T) {
	t.Run(`enforceWorkerCap: a missing caller pane never frees a worker slot`, func(t *testing.T) {
		// An unscoped agent lookup must not authorize excluding an arbitrary
		// worker returned by the transport when no caller identity is present.
		env, cwd, _ := callerCapFixture(t, []string{
			"sub\tpc\tgrok\tsub-orchestrator\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tsub-orchestrator\tbuild",
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
		}, "", "sub", &fakecli.Rule{Argv: []string{"agent", "get", ""}, Stdout: `{"result":{"agent":{"name":"sub","agent_status":"working"}}}`}, `[{"name":"sub","pane_id":"pc"},{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"}]`)
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() { defer func() { recovered = recover() }(); EnforceWorkerCap(&ctx, env, cwd) }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: sub w1 w2)") {
			t.Fatalf("cap result=%#v, want all three workers counted without a caller identity", recovered)
		}
	})
	t.Run(`enforceWorkerCap: the calling registered sub-orchestrator does not count against its own cap`, func(t *testing.T) {
		// Mutation captured: counting the caller's own roster row makes a
		// promoted sub-orchestrator hit max_workers one worker early.
		env, cwd, state := callerCapFixture(t, []string{
			"sub\tpc\tgrok\tsub-orchestrator\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tsub-orchestrator\tbuild",
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
		}, "pc", "sub", &fakecli.Rule{Argv: []string{"agent", "get", "pc"}, Stdout: `{"result":{"agent":{"name":"sub","agent_status":"working"}}}`}, `[{"name":"sub","pane_id":"pc"},{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"}]`)
		ctx := LoadConfig(env, cwd)
		// The roster row is still a live worker for the general API:
		if got := LiveWorkerNames(state, env); strings.Join(got, ",") != "sub,w1,w2" {
			t.Fatalf("live names=%v, want sub,w1,w2", got)
		}
		func() {
			defer func() {
				if value := recover(); value != nil {
					t.Fatalf("the caller counted against its own cap: %v", value)
				}
			}()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
	})
	t.Run(`enforceWorkerCap: the other genuine workers, including another sub-orchestrator, still count`, func(t *testing.T) {
		// Mutation captured: excluding every sub-orchestrator (not just the
		// caller) lets the team grow past max_workers.
		env, cwd, _ := callerCapFixture(t, []string{
			"sub\tpc\tgrok\tsub-orchestrator\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tsub-orchestrator\tbuild",
			"s1\tq1\tgrok\tsub-orchestrator\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tsub-orchestrator\tbuild",
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
		}, "pc", "sub", &fakecli.Rule{Argv: []string{"agent", "get", "pc"}, Stdout: `{"result":{"agent":{"name":"sub","agent_status":"working"}}}`}, `[{"name":"sub","pane_id":"pc"},{"name":"s1","pane_id":"q1"},{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"}]`)
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() { defer func() { recovered = recover() }(); EnforceWorkerCap(&ctx, env, cwd) }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: s1 w1 w2)") {
			t.Fatalf("cap result=%#v, want the three genuine workers listed", recovered)
		}
	})
	t.Run(`enforceWorkerCap: an unresolvable caller keeps the old count (fail closed)`, func(t *testing.T) {
		// Mutation captured: excluding a row that cannot be identified as the
		// caller undercounts the team when the agent lookup fails.
		env, cwd, _ := callerCapFixture(t, []string{
			"sub\tpc\tgrok\tsub-orchestrator\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tsub-orchestrator\tbuild",
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
		}, "pc", "", &fakecli.Rule{Argv: []string{"agent", "get", "pc"}, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 1}, `[{"name":"sub","pane_id":"pc"},{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"}]`)
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() { defer func() { recovered = recover() }(); EnforceWorkerCap(&ctx, env, cwd) }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: sub w1 w2)") {
			t.Fatalf("cap result=%#v, want all three rows counted", recovered)
		}
	})
	t.Run(`enforceWorkerCap: a caller without a roster row changes nothing`, func(t *testing.T) {
		// Control: the exclusion must not drop a name that is not on the roster.
		env, cwd, _ := callerCapFixture(t, []string{
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w3\tp3\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
		}, "pc", "orch", &fakecli.Rule{Argv: []string{"agent", "get", "pc"}, Stdout: `{"result":{"agent":{"name":"orch","agent_status":"working"}}}`}, `[{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"},{"name":"w3","pane_id":"p3"}]`)
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() { defer func() { recovered = recover() }(); EnforceWorkerCap(&ctx, env, cwd) }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: w1 w2 w3)") {
			t.Fatalf("cap result=%#v, want the unchanged count and list", recovered)
		}
	})
	t.Run(`enforceWorkerCap: the exclusion frees exactly the caller's slot`, func(t *testing.T) {
		// Caller plus three genuine workers at the default cap 3: the caller's
		// row must not push the team over, and the third genuine worker still
		// holds its slot (subtest 1 passes with two, this one dies with three).
		env, cwd, _ := callerCapFixture(t, []string{
			"sub\tpc\tgrok\tsub-orchestrator\txai\t1\t/repo\tnow\tgrok-4.7\tfull\tsub-orchestrator\tbuild",
			"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
			"w2\tp2\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
			"w3\tp3\tgrok\ttasker\txai\t1\t/repo\tnow\tgrok-4.7\tfull\ttasker\tbuild",
		}, "pc", "sub", &fakecli.Rule{Argv: []string{"agent", "get", "pc"}, Stdout: `{"result":{"agent":{"name":"sub","agent_status":"working"}}}`}, `[{"name":"sub","pane_id":"pc"},{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"},{"name":"w3","pane_id":"p3"}]`)
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() { defer func() { recovered = recover() }(); EnforceWorkerCap(&ctx, env, cwd) }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: w1 w2 w3)") {
			t.Fatalf("cap result=%#v, want the three genuine workers only", recovered)
		}
	})
}
