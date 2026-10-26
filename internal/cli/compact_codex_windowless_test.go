package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func codexWindowlessScreen(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(packageDir, "testdata/codex-0.164-compacted-windowless.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCompactCodexWindowless(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		base        int
		marker      int
		stale       bool
		code        int
		status      string
	}{
		{"idle", "idle", 38, 0, false, 0, "compacted"},
		{"done", "done", 38, 0, false, 0, "compacted"},
		{"working", "working", 38, 0, false, 9, "timeout"},
		{"blocked", "blocked", 38, 0, false, 9, "timeout"},
		{"running-marker", "idle", 38, 15, false, 9, "timeout"},
		{"older-marker", "idle", 38, 16, false, 0, "compacted"},
		{"working-then-idle-without-footer", "working", 38, 0, true, 9, "timeout"},
		{"low-base", "idle", 7, 0, false, 0, "nothing-to-compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			screen := codexWindowlessScreen(t)
			if tc.marker > 0 {
				lines := strings.Split(screen, "\n")
				nonEmpty := 0
				for i := len(lines) - 1; i >= 0; i-- {
					if strings.TrimSpace(lines[i]) == "" {
						continue
					}
					nonEmpty++
					if nonEmpty == tc.marker-1 {
						lines[i] = "History: • Compacting context (17s • esc to interrupt)\n" + lines[i]
						break
					}
				}
				if nonEmpty != tc.marker-1 {
					t.Fatal("fixture lacks marker placement boundary")
				}
				screen = strings.Join(lines, "\n")
			}
			before := strings.Replace(codexScrolled(codexFooterBefore49), "Context 49%", "Context "+strconv.Itoa(tc.base)+"%", 1)
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON(tc.state, 2)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
				{Argv: compactReadArgv("worker"), Stdout: screen},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			}
			if tc.stale {
				rules = append([]fakecli.Rule{
					{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
					{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 3)},
					{Argv: compactReadArgv("worker"), Call: 2, Stdout: screen},
					{Argv: compactReadArgv("worker"), Call: 3, Stdout: "› Ask Codex\nNo context footer\n"},
				}, rules...)
			}
			f := newCompactFixture(t, "codex", rules)
			compactLowContextEnv(t, f.env)
			t.Chdir(f.root)
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "500")
			value := compactJSON(t, out)
			if code != tc.code || value["status"] != tc.status {
				t.Fatalf("state=%s base=%d exit=%d want=%d stdout=%q stderr=%q", tc.state, tc.base, code, tc.code, out, stderr)
			}
			calls := f.calls(t)
			if tc.base < 10 {
				assertCompactNoInput(t, calls)
			} else if countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}) != 1 {
				t.Fatalf("unexpected input: %#v", calls)
			}
			if tc.status == "timeout" && countArgv(calls, compactReadArgv("worker")) < 3 {
				t.Fatalf("wait did not continue through final read: %#v", calls)
			}
			t.Logf("fixture_root=%s state=%s base=%d exit=%d result=%s", f.root, tc.state, tc.base, code, out)
		})
	}
}

func TestDispatchCompactCodexWindowlessSendsBriefOnce(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	screen := codexWindowlessScreen(t)
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0",
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: strings.Replace(codexScrolled(codexFooterBefore49), "Context 49%", "Context 38%", 1)},
		fakecli.Rule{Argv: compactReadArgv("worker"), Stdout: screen},
		fakecli.Rule{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		fakecli.Rule{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
	)
	f.env["SOHO_COMPACT_TIMEOUT"] = "5000" // real time: the success path ends at the proof, so a -race run under load needs room
	compactLowContextEnv(t, f.env)
	if runtime.GOOS != "windows" {
		if _, err := fakecli.Install(t, f.bin, "ps", []fakecli.Rule{{
			Argv:   []string{"-o", "lstart=,state=,comm=", "-p", strconv.Itoa(os.Getpid())},
			Stdout: "Sun Oct 25 20:00:00 2026 S compact-windowless-fixture\n",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 || dispatchLastLineStatus(t, out) != "submitted" || !strings.Contains(stderr, "dispatch: compacted 'worker' in ") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if countArgvPrefix(calls, []string{"agent", "prompt", "worker"}) != 1 {
		t.Fatalf("brief not sent exactly once: %#v", calls)
	}
	t.Logf("exit=%d brief_prompts=1 stdout=%s stderr=%s", code, out, stderr)
}
