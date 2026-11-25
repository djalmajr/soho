package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const legacyPiStartupPane = "p-new"
const legacyPiSession = "/sessions/worker.jsonl"
const legacyPiGone = `{"error":{"code":"agent_not_found","message":"no agent"}}`

var legacyPiGetArgv = []string{"agent", "get", "worker"}

func legacyPiIdentityRule(call int, pane, session string) fakecli.Rule {
	rule := piHerdrIdentityRule("worker", pane, session)
	rule.Call = call
	return rule
}

func newLegacyPiStartupFixture(t *testing.T, overrides ...fakecli.Rule) spawnFixture {
	t.Helper()
	// Call 1 is the original state observation. Calls 2 and 3 read the
	// startup identity. A failure injected at call 3 leaves both earlier
	// observations and the visible screen successful.
	observation := idleHerdrRule("worker")
	observation.Call = 1
	rules := append(overrides,
		observation,
		legacyPiIdentityRule(2, legacyPiStartupPane, legacyPiSession),
		legacyPiIdentityRule(3, legacyPiStartupPane, legacyPiSession),
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "model • high\n"},
	)
	f := newSpawnFixture(t, freshSpawnRules(rules...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_LAYOUT"] = "tab"
	f.roster(t, spawnRosterRow("other", "worker", "old-model", "ask", ""))
	return f
}

func assertLegacyPiStartupCalls(t *testing.T, f spawnFixture, wantGets int, wantArgs []string) {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var getIndices []int
	start, observeScreen := -1, -1
	for i, call := range calls {
		switch {
		case slices.Equal(call.Argv, legacyPiGetArgv):
			getIndices = append(getIndices, i)
		case slices.Equal(call.Argv, []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}):
			observeScreen = i
		case len(call.Argv) >= 2 && slices.Equal(call.Argv[:2], []string{"agent", "start"}):
			if start >= 0 {
				t.Fatalf("spawn launched twice: %v", calls)
			}
			start = i
			delimiter := slices.Index(call.Argv, "--")
			if delimiter != 9 ||
				!slices.Equal(call.Argv[:7], []string{"agent", "start", "worker", "--kind", "pi", "--pane", legacyPiStartupPane}) ||
				!slices.Equal(call.Argv[delimiter+1:], wantArgs) {
				t.Fatalf("launch argv=%q, want worker in %s with assistant argv %q", call.Argv, legacyPiStartupPane, wantArgs)
			}
		case len(call.Argv) >= 2 && (slices.Equal(call.Argv[:2], []string{"pane", "close"}) ||
			slices.Equal(call.Argv[:2], []string{"agent", "kill"}) ||
			slices.Equal(call.Argv[:2], []string{"agent", "stop"}) ||
			slices.Equal(call.Argv[:2], []string{"agent", "prompt"})):
			t.Fatalf("spawn called %q", call.Argv)
		}
	}
	if len(getIndices) != wantGets || start < 0 || getIndices[0] <= start ||
		observeScreen <= getIndices[0] || wantGets > 1 && getIndices[1] <= observeScreen {
		t.Fatalf("start=%d get=%v visible=%d, want launch, first get and visible read, then %d identity gets; calls=%v",
			start, getIndices, observeScreen, wantGets-1, calls)
	}
}

func assertLegacyPiSpawnResult(t *testing.T, f spawnFixture, stdout string, ready bool, mcp string) {
	t.Helper()
	other := spawnRosterRow("other", "worker", "old-model", "ask", "")
	roster := core.RosterRows(filepath.Join(f.state, "ws"))
	if !ready {
		if stdout != "" || !slices.Equal(roster, []string{other}) {
			t.Fatalf("refused spawn wrote stdout=%q or changed the roster=%q", stdout, roster)
		}
		return
	}
	obj := parseOneSpawnJSONLine(t, stdout)
	spawnOneLineField(t, obj, "status", "ready")
	spawnOneLineField(t, obj, "name", "worker")
	spawnOneLineField(t, obj, "kind", "pi")
	spawnOneLineField(t, obj, "pane_id", legacyPiStartupPane)
	spawnOneLineField(t, obj, "created_pane", true)
	if len(roster) != 2 || roster[0] != other {
		t.Fatalf("roster=%q, want the original row and the worker", roster)
	}
	fields := strings.Split(roster[1], "\t")
	if len(fields) < 16 || !slices.Equal(fields[:4], []string{"worker", legacyPiStartupPane, "pi", "worker"}) || fields[15] != mcp {
		t.Fatalf("worker row=%q, want worker in %s with mcp=%s", roster[1], legacyPiStartupPane, mcp)
	}
}

func TestLegacyPiNoMcpStartupIdentity(t *testing.T) {
	goneStderr := fakecli.Rule{Argv: legacyPiGetArgv, Call: 3, Stderr: legacyPiGone, Code: 1}
	for _, tc := range []struct {
		name, wantError string
		policy          bool
		override        fakecli.Rule
		wantGets        int
	}{
		{name: "explicit mcp none then gone", override: goneStderr, wantGets: 3, wantError: "exited right after start; last screen lines: model • high"},
		{name: "role policy then gone", policy: true, override: goneStderr, wantGets: 3, wantError: "exited right after start; last screen lines: model • high"},
		{name: "gone on raw stdout", override: fakecli.Rule{Argv: legacyPiGetArgv, Call: 3, Stdout: legacyPiGone, Code: 1}, wantGets: 3, wantError: "exited right after start"},
		{name: "other transport error mentions gone", override: fakecli.Rule{Argv: legacyPiGetArgv, Call: 3, Stderr: `{"error":{"code":"unavailable","message":"no agent_not_found response"}}`, Code: 1},
			wantGets: 3, wantError: "could not be confirmed after start in pane p-new (unavailable)"},
		{name: "wrong pane", override: legacyPiIdentityRule(2, "p-other", legacyPiSession), wantGets: 2, wantError: "could not be confirmed after start in pane p-new (mismatch)"},
		{name: "session changes", override: legacyPiIdentityRule(3, legacyPiStartupPane, "/sessions/replacement.jsonl"), wantGets: 3, wantError: "the worker identity changed"},
		{name: "ready metadata without session identity", override: fakecli.Rule{Argv: legacyPiGetArgv,
			Stdout: `{"result":{"agent":{"name":"worker","pane_id":"p-new","terminal_id":"term_pane","agent":"pi","agent_status":"idle","interactive_ready":true,"state_change_seq":42}}}`},
			wantGets: 2, wantError: "Herdr reported no Pi session identity"},
		{name: "no reported session identity", override: legacyPiIdentityRule(3, legacyPiStartupPane, ""), wantGets: 3, wantError: "Herdr reported no Pi session identity"},
		{name: "valid continued liveness", wantGets: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var overrides []fakecli.Rule
			if len(tc.override.Argv) != 0 {
				overrides = append(overrides, tc.override)
			}
			f := newLegacyPiStartupFixture(t, overrides...)
			args := []string{"worker", "--kind", "pi", "--effort", "high"}
			if tc.policy {
				f.ctx.Entries["role_worker_mcp"] = core.ConfigEntry{Value: "none", Source: "project"}
			} else {
				args = append(args, "--mcp", "none")
			}
			code, message, stdout, stderr, _ := runCmdSpawnWithExitMessage(t, f, args)
			ready := tc.wantError == ""
			wantCode := 4
			if ready {
				wantCode = 0
			}
			if code != wantCode || !strings.Contains(message, tc.wantError) {
				t.Fatalf("exit=%d want=%d message=%q want=%q stdout=%q stderr=%q", code, wantCode, message, tc.wantError, stdout, stderr)
			}
			assertLegacyPiStartupCalls(t, f, tc.wantGets, []string{"--thinking", "high", "--no-mcp"})
			assertLegacyPiSpawnResult(t, f, stdout, ready, "none")
		})
	}
}

func TestLegacyPiNativeArgFlagIsNotStartupGate(t *testing.T) {
	f := newLegacyPiStartupFixture(t, fakecli.Rule{Argv: legacyPiGetArgv, Call: 2, Stderr: legacyPiGone, Code: 1})
	// Soho consumes the first delimiter. The second is Pi's delimiter.
	args := []string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "inherit", "--", "--", "--no-mcp"}
	code, message, stdout, stderr, _ := runCmdSpawnWithExitMessage(t, f, args)
	if code != 0 {
		t.Fatalf("exit=%d message=%q stdout=%q stderr=%q", code, message, stdout, stderr)
	}
	assertLegacyPiStartupCalls(t, f, 1, []string{"--thinking", "high", "--", "--no-mcp"})
	assertLegacyPiSpawnResult(t, f, stdout, true, "inherit")
}

func TestPiNoMcpCapabilityConsumer(t *testing.T) {
	f := newLegacyPiStartupFixture(t)
	if _, err := fakecli.Install(t, f.bin, "pi", []fakecli.Rule{
		{Argv: []string{"--help"}, Stdout: "Usage: pi [options]\n  --model <id>   Choose a model\n"},
	}); err != nil {
		t.Fatal(err)
	}
	// The caller's Pi help can differ from the pane's executable. Doctor's
	// advisory cannot suppress the launch flag or replace the worker lookup.
	code, message, stdout, stderr, _ := runCmdSpawnWithExitMessage(t, f,
		[]string{"worker", "--kind", "pi", "--effort", "high", "--mcp", "inherit", "--", "--no-mcp"})
	if code != 0 {
		t.Fatalf("exit=%d message=%q stdout=%q stderr=%q", code, message, stdout, stderr)
	}
	assertLegacyPiStartupCalls(t, f, 3, []string{"--thinking", "high", "--no-mcp"})
	assertLegacyPiSpawnResult(t, f, stdout, true, "inherit")
	if calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "pi.calls.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("spawn probed the caller's Pi: calls=%v err=%v", calls, err)
	}
}
