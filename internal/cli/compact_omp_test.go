package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	// Minimized captures from the macOS loopback run. Keep the actual divider
	// and status lines verbatim, including the leading space on the statuses.
	ompCompactBefore  = " observations, retain the verified worktree identity,\n and continue with the next bounded verification step.\n"
	ompCompactDivider = "──────────────── 📷 soft-compacted · ⌃O ─────────────────"
	ompLinuxDivider   = "────────────── 📷 soft-compacted · Ctrl+O ───────────────"
	ompCompactEmpty   = " Warning: Nothing to compact (no messages yet)"
	ompCompactFailed  = " Error: Compaction failed: Summarization failed:"
)

var ompCompactCases = []struct {
	name, before, after, status string
	code                        int
}{
	{"fresh divider", ompCompactBefore, ompCompactBefore + "\n" + ompCompactDivider + "\n", "compacted", 0},
	{"fresh Linux divider", ompCompactBefore, ompCompactBefore + "\n" + ompLinuxDivider + "\n", "compacted", 0},
	{"new identical divider", ompCompactDivider + "\n", ompCompactDivider + "\n\n" + ompCompactDivider + "\n", "compacted", 0},
	{"fresh shorter borders", ompCompactBefore, "──── 📷 soft-compacted · ⌃O ─────\n", "compacted", 0},
	{"stale resized divider", ompCompactDivider + "\n", "──── 📷 soft-compacted · ⌃O ─────\n", "timeout", 9},
	{"stale divider with other shortcut", ompCompactDivider + "\n", ompLinuxDivider + "\n", "timeout", 9},
	{"stale divider and new echo", ompCompactDivider + "\n", "› /compact\n" + ompCompactDivider + "\n", "timeout", 9},
	{"banner and fake proof", ompCompactBefore, " OMP v19.1.1\n Compacted from 10,000 tokens\n── 📷 soft-compacted · ⌃O\n", "timeout", 9},
	{"unverified shortcut", ompCompactBefore, "──── 📷 soft-compacted · CTRL+O ─────\n", "timeout", 9},
	{"quoted divider and ending", ompCompactBefore, "> " + ompCompactDivider + "\n> " + ompCompactEmpty + "\n> " + ompCompactFailed + "\n", "timeout", 9},
	{"fenced divider and ending", ompCompactBefore, "```text\n" + ompLinuxDivider + "\n" + ompCompactEmpty + "\n~~~\n" + ompCompactFailed + "\n```\n", "timeout", 9},
	{"empty session", ompCompactBefore, ompCompactEmpty + "\n", "nothing-to-compact", 0},
	{"terminal failure", ompCompactBefore, " Error: Retry failed after 5 attempts: Retry cancelled\n\n" + ompCompactFailed + "\n Thinking loop detected: the model repeated\n", "failed", 9},
	{"terminal failure with fresh divider", ompCompactBefore, ompCompactDivider + "\n" + ompCompactFailed + "\n", "failed", 9},
	{"stale failure", ompCompactFailed + "\n", ompCompactFailed + "\n prompt redraw\n", "timeout", 9},
}

func TestCompactOMP(t *testing.T) {
	for _, tc := range ompCompactCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			f := newCompactFixture(t, "omp", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: tc.before},
				{Argv: compactReadArgv("worker"), Stdout: tc.after},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			})
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "5s")
			if code != tc.code {
				t.Fatalf("exit=%d want %d, stdout=%q stderr=%q", code, tc.code, out, stderr)
			}
			status := compactJSON(t, out)
			if status["kind"] != "omp" || status["status"] != tc.status {
				t.Fatalf("status=%v want %q, stdout=%q", status, tc.status, out)
			}
			if tc.status == "failed" && !strings.Contains(stderr, "Error: Compaction failed: Summarization failed:") {
				t.Fatalf("missing captured terminal failure: %q", stderr)
			}
			assertOMPCompactCalls(t, f.calls(t), "p1", false, tc.status == "compacted")
		})
	}
}

func TestCompactOMPProofStillNeedsIdle(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	previousSleep := compactSleep
	compactSleep = func(context.Context, time.Duration) {}
	t.Cleanup(func() { compactSleep = previousSleep })
	f := newCompactFixture(t, "omp", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("working", 2)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: ompCompactBefore},
		{Argv: compactReadArgv("worker"), Stdout: ompCompactDivider + "\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "5s")
	if code != 9 || compactJSON(t, out)["status"] != "timeout" {
		t.Fatalf("proof without idle: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertOMPCompactCalls(t, f.calls(t), "p1", false, true)
}

func TestDispatchCompactOMP(t *testing.T) {
	for _, tc := range ompCompactCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			const pane = "w0test:p0a"
			f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0",
				fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
				fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
				fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: tc.before},
				fakecli.Rule{Argv: compactReadArgv("worker"), Stdout: tc.after},
				fakecli.Rule{Argv: []string{"pane", "send-text", pane, "/compact"}, Stdout: `{"result":{}}`},
				fakecli.Rule{Argv: []string{"pane", "send-keys", pane, "Enter"}, Stdout: `{"result":{}}`},
			)
			f.env["SOHO_COMPACT_TIMEOUT"] = "5s"
			sd := filepath.Join(f.state, "ws")
			roster := "worker\t" + pane + "\tomp\timplementer\t\t0\t\tnow\t\ttask\timplementer\t\t\t\thigh\n"
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait", "--compact")
			if code != tc.code || !strings.Contains(stderr, "before sending the brief; status shows this phase") {
				t.Fatalf("exit=%d want %d, stdout=%q stderr=%q", code, tc.code, out, stderr)
			}
			if tc.code == 0 {
				if status := dispatchLastLineStatus(t, out); status != "submitted" || strings.Count(strings.TrimSpace(out), "\n") != 0 {
					t.Fatalf("expected one submitted dispatch result: %q", out)
				}
				want := "compacted 'worker' in "
				if tc.status == "nothing-to-compact" {
					want = "'worker' had nothing to compact"
				}
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr=%q missing %q", stderr, want)
				}
			} else {
				if compactJSON(t, out)["status"] != tc.status || !strings.Contains(stderr, "the brief was not sent") {
					t.Fatalf("stdout=%q stderr=%q", out, stderr)
				}
				if tc.status == "failed" && (!strings.Contains(stderr, "Error: Compaction failed: Summarization failed:") || strings.Contains(stderr, "timed out")) {
					t.Fatalf("terminal failure was misreported: %q", stderr)
				}
				compactPhaseNoTaskStateFor(t, sd, f.root)
			}
			compactPhaseMarkerAbsent(t, core.CompactPendingPath(sd, "worker"))
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			assertOMPCompactCalls(t, calls, pane, tc.code == 0, tc.status == "compacted")
		})
	}
}

func assertOMPCompactCalls(t *testing.T, calls []fakecli.Call, pane string, sentBrief, proved bool) {
	t.Helper()
	text := []string{"pane", "send-text", pane, "/compact"}
	enter := []string{"pane", "send-keys", pane, "Enter"}
	before, typed, entered, proofRead, idleChecked := false, false, false, false, false
	for _, call := range calls {
		switch {
		case slices.Equal(call.Argv, compactReadArgv("worker")):
			if entered {
				proofRead = true
			} else if !typed {
				before = true
			}
		case slices.Equal(call.Argv, text):
			typed = before
		case slices.Equal(call.Argv, enter):
			entered = typed
		case slices.Equal(call.Argv, []string{"agent", "get", "worker"}):
			idleChecked = idleChecked || proofRead
		case len(call.Argv) >= 3 && slices.Equal(call.Argv[:3], []string{"agent", "prompt", "worker"}):
			if !proofRead || (proved && !idleChecked) {
				t.Fatalf("brief sent before the proof read or idle check: %#v", calls)
			}
		}
	}
	if !proofRead || countArgv(calls, text) != 1 || countArgv(calls, enter) != 1 ||
		countArgvPrefix(calls, []string{"pane", "send-keys", pane}) != 1 || (proved && !idleChecked) {
		t.Fatalf("expected baseline, one /compact, one Enter, proof read and idle check on success: %#v", calls)
	}
	wantPrompt := 0
	if sentBrief {
		wantPrompt = 1
	}
	if countArgvPrefix(calls, []string{"agent", "prompt", "worker"}) != wantPrompt {
		t.Fatalf("expected %d brief sends: %#v", wantPrompt, calls)
	}
}
