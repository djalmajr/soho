package spawn

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-416: an agent that never registered has nothing for `herdr agent read`,
// so a failed start (or an agent gone right after start) reads the pane itself
// with `herdr pane read <pane> --source recent-unwrapped --lines 40` for the
// last screen lines. The messages do not change; only their lines do.

const paneBootstrapScreen = "codex 0.163.0\n\nloading\nstep 1\nstep 2\nError: account/read failed during TUI bootstrap\n  failed to load workspace requirements (code -32603)\n\n"
const paneBootstrapTail = "loading / step 1 / step 2 / Error: account/read failed during TUI bootstrap / failed to load workspace requirements (code -32603)"

var paneReadArgv = []string{"pane", "read", "w0test:p0a", "--source", "recent-unwrapped", "--lines", "40"}

func startTimeoutRule() fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stderr: `{"error":{"code":"timeout","message":"agent did not register within 1s"}}`, Code: 1}
}

func agentReadNotFound() fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stderr: `{"error":{"code":"agent_not_found","message":"no agent"}}`, Code: 1}
}

// herdrCallIndex returns the index of the first herdr call with exactly argv
// (prefix when prefix is true), or -1.
func herdrCallIndex(calls []fakecli.Call, argv []string, prefix bool) int {
	for i, c := range calls {
		if prefix && len(c.Argv) >= len(argv) && strings.Join(c.Argv[:len(argv)], "\x00") == strings.Join(argv, "\x00") {
			return i
		}
		if !prefix && strings.Join(c.Argv, "\x00") == strings.Join(argv, "\x00") {
			return i
		}
	}
	return -1
}

func TestStartAgentFailedStartReadsThePane(t *testing.T) {
	const closedMsg = "agent start failed for worker (codex) in pane w0test:p0a; closed the pane this spawn opened (last screen lines: "

	t.Run("agent read fails, pane read has the screen: the message carries the pane's last lines", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{
			startTimeoutRule(),
			agentReadNotFound(),
			{Argv: paneReadArgv, Stdout: paneBootstrapScreen},
			{Argv: []string{"pane", "close", "w0test:p0a"}},
		})
		o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1"}
		code, msg := startAgentExit(t, o, "codex", f.env)
		if want := closedMsg + paneBootstrapTail + ")"; code != 4 || msg != want {
			t.Fatalf("exit=%d msg=%q want %q", code, msg, want)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		read, closed := herdrCallIndex(calls, paneReadArgv, false), herdrCallIndex(calls, []string{"pane", "close", "w0test:p0a"}, false)
		if read < 0 || closed < 0 || read > closed {
			t.Fatalf("the pane read is not before the close: read=%d close=%d calls=%#v", read, closed, calls)
		}
	})

	for _, tc := range []struct {
		name string
		rule fakecli.Rule
	}{
		{"pane read fails", fakecli.Rule{Argv: paneReadArgv, Stderr: `{"error":{"code":"pane_not_found","message":"no pane"}}`, Code: 1}},
		{"pane read is empty", fakecli.Rule{Argv: paneReadArgv, Stdout: "\n  \n"}},
	} {
		t.Run(tc.name+": today's agent read stands in", func(t *testing.T) {
			f := newSpawnFixture(t, []fakecli.Rule{
				startTimeoutRule(),
				tc.rule,
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible", "--lines", "40"}, Stdout: "agent line 1\nagent line 2\n"},
				{Argv: []string{"pane", "close", "w0test:p0a"}},
			})
			o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1"}
			code, msg := startAgentExit(t, o, "codex", f.env)
			if want := closedMsg + "agent line 1 / agent line 2)"; code != 4 || msg != want {
				t.Fatalf("exit=%d msg=%q want %q", code, msg, want)
			}
		})
	}
}

// checkStartWindowExit runs checkStartWindow and returns the DieFriction exit
// and message. created says whether this spawn opened the pane.
func checkStartWindowExit(t *testing.T, o spawnOptions, kind string, f spawnFixture, created bool) (code int, msg string) {
	t.Helper()
	oldErr := platform.Stderr
	platform.Stderr = &strings.Builder{}
	t.Cleanup(func() { platform.Stderr = oldErr })
	func() {
		defer func() {
			value := recover()
			if value == nil {
				t.Fatal("checkStartWindow returned without dying")
			}
			if e, ok := value.(*platform.ExitError); ok {
				code, msg = e.Code, e.Msg
				return
			}
			t.Fatalf("recovered=%#v", value)
		}()
		_, _, _ = checkStartWindow(o, kind, f.env, created, nil, f.ctx)
	}()
	return code, msg
}

func TestCheckStartWindowExitedRightAfterStartReadsThePane(t *testing.T) {
	gone := fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`, Code: 1}
	o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1"}

	t.Run("an empty agent screen: the pane's last lines", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{
			gone,
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
			{Argv: paneReadArgv, Stdout: paneBootstrapScreen},
		})
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		code, msg := checkStartWindowExit(t, o, "codex", f, true)
		if want := "agent 'worker' (codex) exited right after start; last screen lines: " + paneBootstrapTail; code != 4 || msg != want {
			t.Fatalf("exit=%d msg=%q want %q", code, msg, want)
		}
	})

	t.Run("a given --pane this spawn did not open is never read", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{
			gone,
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
			{Argv: paneReadArgv, Stdout: "previous occupant's screen\n"},
		})
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		code, msg := checkStartWindowExit(t, o, "codex", f, false)
		if want := "agent 'worker' (codex) exited right after start; last screen lines: "; code != 4 || msg != want {
			t.Fatalf("exit=%d msg=%q want %q", code, msg, want)
		}
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if herdrCallIndex(calls, []string{"pane", "read"}, true) >= 0 {
			t.Fatalf("a pane this spawn did not open was read: %#v", calls)
		}
	})

	t.Run("control: a screen from the agent read is kept and the pane is not read", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{
			gone,
			{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Stdout: "agent said bye\n"},
			{Argv: paneReadArgv, Stdout: paneBootstrapScreen},
		})
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		code, msg := checkStartWindowExit(t, o, "codex", f, true)
		if want := "agent 'worker' (codex) exited right after start; last screen lines: agent said bye"; code != 4 || msg != want {
			t.Fatalf("exit=%d msg=%q want %q", code, msg, want)
		}
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if herdrCallIndex(calls, []string{"pane", "read"}, true) >= 0 {
			t.Fatalf("the pane was read with a screen in hand: %#v", calls)
		}
	})
}
