package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-531's saved pre-send screen, with machine paths replaced by <project>.
// Keep the stale compaction proof, empty composer and real truncated footer.
const codexLowContextBefore = `
• Ran sed -n '1,58p' .soho/w6X/reports/review-5-20261025T175254.pr…
  └ 6ca9ca9d2e62ddb8dee83bbde1c7dd5f10f207c4
    finished: 2026-10-25T18:29:03-03:00
    exit_status: 0
    + 105 lines (⌃t to expand)

• Context compacted · 1m 19s

• Explored
  └ List review-5-20261025T175254.probe.preview-content.md
    Read review-5-20261025T175254.probe.preview-content.md
    + Show details

• Ran cat <project>/.soho/w6X/…
  └ 6ca9ca9d2e62ddb8dee83bbde1c7dd5f10f207c4
    finished: 2026-10-25T18:29:03-03:00
    exit_status: 0
    + 24 lines (⌃t to expand)

• Explored
  └ Read review-5-20261025T175254.probe.report-v3.go
    Search watchdog|30m|outside|\[partial\]|native|source_manifest|
           copy_manifest|sha256|report.*publish|func publish|case
    + Show details

• Ran GOCACHE=$TMPDIR/gocache-review GOTMPDIR=$TMPDIR go run /User…
  └ (no output)
    + Show details

• <project>/.soho/w6X/reports/
  review-5-20261025T175254.md

  Worked for 39m 27s • 18:32


› Ask Codex to do anything

  GPT-6.1-Sol xhigh · Context 7% used · 475K window · weekly 81% l…
  ← for agents · ? for shortcuts         ⚠ 2 warnings · f2 to view

`

func compactLowContextEnv(t *testing.T, env platform.Env) {
	t.Helper()
	for key := range env {
		switch strings.ToUpper(key) {
		case "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH", "GORACE", "TEMP", "TMP", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT":
		default:
			if !strings.HasPrefix(key, "SOHO_") && !strings.HasPrefix(key, "HERDR_") {
				delete(env, key)
			}
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(key, env[key])
	}
}

func assertCompactNoInput(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	for _, prefix := range [][]string{{"pane", "send-text"}, {"pane", "send-keys"}, {"agent", "send-text"}, {"agent", "send-keys"}} {
		if n := countArgvPrefix(calls, prefix); n != 0 {
			t.Errorf("input %v called %d times; want zero: %#v", prefix, n, calls)
		}
	}
}

func TestCompactCodexLowContextSkipsInput(t *testing.T) {
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Stdout: codexLowContextBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	compactLowContextEnv(t, f.env)
	t.Chdir(f.root)
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "1000")
	calls := f.calls(t)
	assertCompactNoInput(t, calls)
	value := compactJSON(t, out)
	if code != 0 || stderr != "" || value["agent"] != "worker" || value["kind"] != "codex" || value["status"] != "nothing-to-compact" || len(value) != 4 {
		t.Fatalf("code=%d stdout=%q stderr=%q calls=%#v", code, out, stderr, calls)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 0 {
		t.Fatalf("elapsed_ms=%v; stdout=%q", value["elapsed_ms"], out)
	}
	if n := countArgv(calls, compactReadArgv("worker")); n != 1 {
		t.Fatalf("screen reads=%d want pre-send only; calls=%#v", n, calls)
	}
}

func TestCompactCodexLowContextBoundaryStillSends(t *testing.T) {
	for _, tc := range []struct{ name, before string }{
		{"base-10", strings.Replace(codexLowContextBefore, "Context 7%", "Context 10%", 1)},
		{"unreadable", strings.Replace(codexLowContextBefore, "Context 7% used · 475K window", "Context unavailable", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.before
			f := newCompactFixture(t, "codex", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
				{Argv: compactReadArgv("worker"), Stdout: "› /compact\n• Context compacted\n› Ask Codex to do anything\n"},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			})
			compactLowContextEnv(t, f.env)
			t.Chdir(f.root)
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "5000")
			calls := f.calls(t)
			if code != 0 || stderr != "" || compactJSON(t, out)["status"] != "compacted" || countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}) != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q calls=%#v before=%q", code, out, stderr, calls, before)
			}
		})
	}
}

func TestDispatchCompactCodexLowContextSendsBrief(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0",
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexLowContextBefore},
	)
	compactLowContextEnv(t, f.env)
	if runtime.GOOS != "windows" {
		// Model only this fixture process's identity; the real marker claim,
		// removal and dispatch still run through Run. No host process is read.
		if _, err := fakecli.Install(t, f.bin, "ps", []fakecli.Rule{{
			Argv:   []string{"-o", "lstart=,state=,comm=", "-p", strconv.Itoa(os.Getpid())},
			Stdout: "Sun Oct 25 20:00:00 2026 S compact-fixture\n",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	assertCompactNoInput(t, calls)
	if code != 0 || !strings.Contains(stderr, "soho: dispatch: 'worker' had nothing to compact (context 7% is already low)\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q calls=%#v HOME=%q PATH=%q TMPDIR=%q", code, out, stderr, calls, f.env["HOME"], f.env["PATH"], f.env["TMPDIR"])
	}
	if dispatchLastLineStatus(t, out) != "submitted" || countArgvPrefix(calls, []string{"agent", "prompt", "worker"}) != 1 {
		t.Fatalf("brief not submitted exactly once: stdout=%q calls=%#v", out, calls)
	}
	if _, err := os.Stat(core.CompactPendingPath(filepath.Join(f.env["SOHO_DIR"], "ws"), "worker")); !os.IsNotExist(err) {
		t.Fatalf("compact marker not removed: %v", err)
	}
}
