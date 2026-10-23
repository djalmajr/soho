package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const reviewMCPScreen = "MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\n"

func reviewCursorRoster(t *testing.T, f *dispatchArrivalFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte("worker\tw0test:p0a\tcursor\timplementer\txai\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// Ported from the review's wait probe: the first prompt is safe and the
// approval arrives only when the blocking dispatch enters WaitFor.
func TestReviewDJA481BlockingDispatchMCPAutoApprove(t *testing.T) {
	idle := `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`
	blocked := `{"result":{"agent":{"agent_status":"blocked","state_change_seq":2}}}`
	visible := []string{"agent", "read", "worker", "--source", "visible"}
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0",
		fakecli.Rule{Argv: visible, Call: 1, Stdout: cursorTUIScreen},
		fakecli.Rule{Argv: visible, Call: 2, Stdout: cursorTUIScreen},
		fakecli.Rule{Argv: visible, ArgvPrefix: true, Stdout: reviewMCPScreen},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: idle},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: idle},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: blocked},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true})
	reviewCursorRoster(t, f)
	f.env["SOHO_PROMPT_CHECK_SECONDS"], f.env["SOHO_AUTO_APPROVE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "on", "1"
	code, out, errText := f.run(t, "worker", f.brief, "--timeout", "2s")
	calls := dispatchCalls(t, f)
	if code != 7 || !strings.Contains(out, `"wait_status":"blocked"`) || !strings.Contains(errText, "approval of MCP servers") {
		t.Fatalf("exit=%d out=%s stderr=%q calls=%#v", code, out, errText, calls)
	}
	if countDispatchCalls(calls, "prompt") != 1 || countDispatchCalls(calls, "send-keys") != 0 || !strings.Contains(out, `"auto_approved":0`) {
		t.Fatalf("expected one safe initial prompt and no approval keys: out=%s calls=%#v", out, calls)
	}
}

func TestReviewDJA481LatestFallbackBeforePrompt(t *testing.T) {
	for _, dialog := range []bool{true, false} {
		t.Run(map[bool]string{true: "MCP", false: "safe control"}[dialog], func(t *testing.T) {
			visible := []string{"agent", "read", "worker", "--source", "visible"}
			latest := cursorTUIScreen
			if dialog {
				latest = reviewMCPScreen
			}
			f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0",
				fakecli.Rule{Argv: visible, Call: 1, Stdout: cursorTUIScreen},
				fakecli.Rule{Argv: visible, Call: 2, Stdout: cursorTUIScreen},
				fakecli.Rule{Argv: visible, ArgvPrefix: true, Stdout: latest},
				fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"agent_not_idle","message":"busy"}}`},
				fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
				fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 5, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`})
			reviewCursorRoster(t, f)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			calls := dispatchCalls(t, f)
			if dialog {
				if code != 7 || !strings.Contains(out, `"wait_status":"blocked"`) {
					t.Fatalf("exit=%d out=%s stderr=%q calls=%#v", code, out, errText, calls)
				}
				dispatchAssertNoInput(t, calls)
			} else if code != 0 || countDispatchCalls(calls, "prompt") != 1 {
				t.Fatalf("control exit=%d out=%s stderr=%q calls=%#v", code, out, errText, calls)
			}
			reads := 0
			for _, c := range calls {
				if strings.Join(c.Argv, "\x00") == strings.Join(visible, "\x00") {
					reads++
				}
			}
			if reads < 4 {
				t.Fatalf("fallback and final visible read were not exercised: calls=%#v", calls)
			}
		})
	}
}

func TestReviewDJA481RollbackMarkers(t *testing.T) {
	read := []string{"agent", "read", "worker", "--source", "visible"}
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0",
		fakecli.Rule{Argv: read, Call: 1, Stdout: cursorTUIScreen},
		fakecli.Rule{Argv: read, ArgvPrefix: true, Stdout: reviewMCPScreen})
	reviewCursorRoster(t, f)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	files := map[string]string{"wait/worker.queued": "retained queued task\n", "wait/worker.not-received": "retained failed delivery\n", "wait/worker.approvals": "2\n", "task-worker": "implementer: Previous task ✓\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(f.state, "ws", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 {
		t.Fatalf("exit=%d out=%s stderr=%q", code, out, errText)
	}
	dispatchAssertNoInput(t, dispatchCalls(t, f))
	for name, want := range files {
		b, e := os.ReadFile(filepath.Join(f.state, "ws", name))
		if e != nil || string(b) != want {
			t.Errorf("%s=%q error=%v want=%q", name, b, e, want)
		}
	}
}
