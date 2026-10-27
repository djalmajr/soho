package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func codexThresholdScreens(t *testing.T) (string, string) {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(packageDir, "testdata", "codex-0.164-compact-scrolled-"+name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before, after := read("before"), read("after")
	if n, ok := compactCodexContext(before); !ok || n != 11 {
		t.Fatalf("before footer=%d readable=%t", n, ok)
	}
	if n, ok := compactCodexContext(after); !ok || n != 6 {
		t.Fatalf("after footer=%d readable=%t", n, ok)
	}
	return before, after
}

func TestCompactCodexThresholdDrop(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		code        int
		status      string
	}{
		{"idle", "idle", 0, "compacted"},
		{"done", "done", 0, "compacted"},
		{"working", "working", 9, "timeout"},
		{"threshold-equal", "idle", 9, "timeout"},
		{"running-marker", "idle", 9, "timeout"},
		{"marker-at-15", "idle", 9, "timeout"},
		{"marker-at-16", "idle", 0, "compacted"},
		{"working-then-idle-without-footer", "working", 9, "timeout"},
		{"history-only", "idle", 9, "timeout"},
		{"low-base", "idle", 0, "nothing-to-compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			before, after := codexThresholdScreens(t)
			switch tc.name {
			case "threshold-equal":
				after = strings.Replace(after, "Context 6% used", "Context 10% used", 1)
			case "running-marker":
				after = strings.Replace(after, "\n› Ask Codex", "\n• Compacting context (17s • esc to interrupt)\n› Ask Codex", 1)
			case "marker-at-15", "marker-at-16":
				boundary := 15
				if tc.name == "marker-at-16" {
					boundary = 16
				}
				lines, nonEmpty := strings.Split(after, "\n"), 0
				for i := len(lines) - 1; i >= 0; i-- {
					if strings.TrimSpace(lines[i]) == "" {
						continue
					}
					nonEmpty++
					if nonEmpty == boundary-1 {
						lines[i] = "• Compacting context (17s • esc to interrupt)\n" + lines[i]
						break
					}
				}
				if nonEmpty != boundary-1 {
					t.Fatal("fixture lacks marker boundary")
				}
				after = strings.Join(lines, "\n")
			case "history-only":
				after = strings.Replace(after, "Context 6% used", "No gauge", 1) + "\n"
				after = "History: Context 6% used · 475K window\n" + after
			case "low-base":
				before = strings.Replace(before, "Context 11% used", "Context 9% used", 1)
			}
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON(tc.state, 2)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
				{Argv: compactReadArgv("worker"), Stdout: after},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			}
			if tc.name == "working-then-idle-without-footer" {
				rules = append([]fakecli.Rule{
					{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
					{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 3)},
					{Argv: compactReadArgv("worker"), Call: 2, Stdout: after},
					{Argv: compactReadArgv("worker"), Call: 3, Stdout: "› Ask Codex\nNo footer\n"},
				}, rules...)
			}
			f := newCompactFixture(t, "codex", rules)
			compactLowContextEnv(t, f.env)
			t.Chdir(f.root)
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "5000")
			value := compactJSON(t, out)
			if code != tc.code || value["status"] != tc.status {
				t.Fatalf("case=%s state=%s exit=%d want=%d stdout=%q stderr=%q", tc.name, tc.state, code, tc.code, out, stderr)
			}
			calls := f.calls(t)
			if tc.name == "low-base" {
				assertCompactNoInput(t, calls)
			} else {
				if countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}) != 1 {
					t.Fatalf("input mismatch: %#v", calls)
				}
			}
			for _, call := range calls {
				if len(call.Argv) >= 4 && call.Argv[0] == "agent" && call.Argv[1] == "read" {
					if strings.Contains(strings.Join(call.Argv, " "), "history") {
						t.Fatalf("history read: %v", call.Argv)
					}
				}
			}
			t.Logf("case=%s state=%s exit=%d stdout=%s stderr=%s", tc.name, tc.state, code, out, stderr)
		})
	}
}

func TestDispatchCompactCodexThresholdSendsBriefOnce(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	before, after := codexThresholdScreens(t)
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0",
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		fakecli.Rule{Argv: compactReadArgv("worker"), Stdout: after},
		fakecli.Rule{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		fakecli.Rule{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
	)
	f.env["SOHO_COMPACT_TIMEOUT"] = "5000"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	compactLowContextEnv(t, f.env)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 || dispatchLastLineStatus(t, out) != "submitted" || !strings.Contains(stderr, "dispatch: compacted 'worker' in ") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if countArgvPrefix(calls, []string{"agent", "prompt", "worker"}) != 1 || countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "w0test:p0a", "Enter"}) != 1 {
		t.Fatalf("expected one compaction and one brief: %#v", calls)
	}
	t.Logf("exit=%d brief_prompts=1 stdout=%s stderr=%s", code, out, stderr)
}
