package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const timeoutScreensBefore = "private-before\r\n› Ask Codex\n  Sol · Context 49% used · 475K window\n"
const timeoutScreensLast = "private-last\r\n› Ask Codex\n  Sol · Context 49% used · 475K window"

func timeoutScreensRules(sentinel bool) []fakecli.Rule {
	rules := []fakecli.Rule{}
	if sentinel {
		// Expire the real phase budget during the liveness read, after both
		// the pre-send screen and a distinct successful poll were captured.
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Delay: 2500, Stdout: compactStateJSON("idle", 1)})
	}
	return append(rules,
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: timeoutScreensBefore},
		fakecli.Rule{Argv: compactReadArgv("worker"), Stdout: timeoutScreensLast},
		fakecli.Rule{Argv: []string{"pane", "send-text", "p1", "/compact"}},
		fakecli.Rule{Argv: []string{"pane", "send-keys", "p1", "Enter"}},
	)
}

func TestCompactTimeoutScreensPaths(t *testing.T) {
	for _, sentinel := range []bool{false, true} {
		name := "regular"
		if sentinel {
			name = "deadline_sentinel"
		}
		t.Run(name, func(t *testing.T) {
			limit := "1s"
			if !sentinel {
				fakeFastClock(t, time.Hour)
				limit = "30s" // Real budget remains available for the final read.
			}
			f := newCompactFixture(t, "codex", timeoutScreensRules(sentinel))
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", limit)
			value := compactJSON(t, out)
			if code != 9 || value["status"] != "timeout" || len(value) != 4 {
				t.Fatalf("code=%d unchanged timeout JSON=%s stderr=%s", code, out, stderr)
			}
			calls := f.calls(t)
			wantReads := 3 // before, poll, final read
			if sentinel {
				wantReads = 2
			}
			if got := countArgv(calls, compactReadArgv("worker")); got != wantReads {
				t.Fatalf("reads=%d want %d for %s: %#v", got, wantReads, name, calls)
			}
			elapsed := value["elapsed_ms"].(float64)
			if (sentinel && (elapsed < 900 || elapsed > 5000)) || (!sentinel && elapsed < float64(time.Hour.Milliseconds())) {
				t.Fatalf("wrong timeout path elapsed_ms=%v sentinel=%v", elapsed, sentinel)
			}
			wait := filepath.Join(f.root, "state", "ws", "wait")
			entries, err := os.ReadDir(wait)
			if err != nil || len(entries) != 1 {
				t.Fatalf("expected one committed diagnostic, no temp files: entries=%v err=%v", entries, err)
			}
			file := entries[0].Name()
			stamp := strings.TrimSuffix(strings.TrimPrefix(file, "compact-worker-"), ".screens")
			if _, err := time.Parse("20060102T150405Z", stamp); err != nil || file != "compact-worker-"+stamp+".screens" {
				t.Fatalf("bad UTC filename %q: %v", file, err)
			}
			path := filepath.Join(wait, file)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("== kind ==\ncodex\n== elapsed ms ==\n%.0f\n== footer base ==\nvalue=49\nok=true\n== before (pre-send) ==\n%s\n== last screen ==\n%s", elapsed, timeoutScreensBefore, timeoutScreensLast)
			if string(body) != want {
				t.Fatalf("diagnostic sections/order or verbatim screens differ: got=%q want=%q", body, want)
			}
			if stderr != "" {
				t.Fatalf("standalone compact timeout changed stderr: %q", stderr)
			}
			friction, err := os.ReadFile(filepath.Join(f.root, "state", "ws", "friction.log"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, output := range []string{out, stderr, string(friction)} {
				if strings.Contains(output, "private-before") || strings.Contains(output, "private-last") || strings.Contains(output, "Context 49% used") {
					t.Fatalf("raw screen escaped local artifact: %q", output)
				}
			}
			// Compare against a normal 0666 creation under this process's umask.
			control, err := os.OpenFile(filepath.Join(f.root, "mode-control"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
			if err != nil {
				t.Fatal(err)
			}
			if err := control.Close(); err != nil {
				t.Fatal(err)
			}
			controlInfo, err := os.Stat(filepath.Join(f.root, "mode-control"))
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != controlInfo.Mode().Perm() {
				t.Fatalf("mode=%o want 0666 under umask=%o", info.Mode().Perm(), controlInfo.Mode().Perm())
			}
		})
	}
}

func TestCompactTimeoutScreensNowrite(t *testing.T) {
	fakeFastClock(t, time.Hour)
	f := newCompactFixture(t, "codex", timeoutScreensRules(false))
	f.env["SOHO_NOWRITE"] = "1"
	// Public Run refuses compact before effects; invoke the shared consumer
	// directly as well to prove its own timeout-save defense under NOWRITE.
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "30s")
	if code != 2 || out != "" || strings.Contains(stderr, "screens saved to") {
		t.Fatalf("public NOWRITE guard: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, coreOut, coreErr := runTimeoutScreensCore(f)
	if code != 9 || compactJSON(t, coreOut)["status"] != "timeout" || strings.Contains(coreErr, "screens saved to") {
		t.Fatalf("consumer NOWRITE guard: code=%d out=%q stderr=%q", code, coreOut, coreErr)
	}
	if _, err := os.Stat(filepath.Join(f.root, "state", "ws", "wait")); !os.IsNotExist(err) {
		t.Fatalf("NOWRITE created diagnostic directory: %v", err)
	}
}

func runTimeoutScreensCore(f *compactFixture) (int, string, string) {
	var stdout, errout bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	platform.Stdout, platform.Stderr = &stdout, &errout
	code := compactRun("worker", "p1", "codex", "", "", "", 30000, &core.Config{}, f.env, f.root, false)
	return code, stdout.String(), errout.String()
}

func TestCompactTimeoutScreensWriteFailureSilent(t *testing.T) {
	fakeFastClock(t, time.Hour)
	f := newCompactFixture(t, "codex", timeoutScreensRules(false))
	if err := os.WriteFile(filepath.Join(f.root, "state", "ws", "wait"), []byte("block directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Run initializes wait before entering compact; exercise the shared
	// consumer directly to isolate a diagnostic-write failure after timeout.
	code, out, stderr := runTimeoutScreensCore(f)
	if code != 9 || compactJSON(t, out)["status"] != "timeout" || stderr != "" {
		t.Fatalf("write failure changed result or printed an error/path: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestCompactTimeoutScreensHelperBoundaries(t *testing.T) {
	root := t.TempDir()
	for _, agent := range []string{"..", ".", "../escape", "a/b", `a\b`, "a\nprivate", ""} {
		if path := compactSaveTimeoutScreens(root, agent, "codex", 1, "before", "last", platform.Env{}); path != "" {
			t.Fatalf("unsafe agent %q wrote %q", agent, path)
		}
	}
	if path := compactSaveTimeoutScreens("", "worker", "codex", 1, "before", "last", platform.Env{}); path != "" {
		t.Fatalf("empty state dir wrote %q", path)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("guard side effects: %v %v", entries, err)
	}
	path := compactSaveTimeoutScreens(root, "worker", "pi", 12, "before\r\n", "last\r\n", platform.Env{})
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "== kind ==\npi\n== elapsed ms ==\n12\n== before (pre-send) ==\nbefore\r\n\n== last screen ==\nlast\r\n" {
		t.Fatalf("non-codex diagnostics should omit footer reading: %q", body)
	}
}

// TestCompactTimeoutScreensKeepsTheNewest: a save keeps only the agent's
// newest compactTimeoutScreensKeep captures, never touches another agent
// whose name extends this one (build vs build-6), and leaves other files.
func TestCompactTimeoutScreensKeepsTheNewest(t *testing.T) {
	root := t.TempDir()
	waitDir := filepath.Join(root, "wait")
	if err := os.MkdirAll(waitDir, 0o777); err != nil {
		t.Fatal(err)
	}
	var old []string
	for i := 0; i < compactTimeoutScreensKeep+2; i++ {
		name := fmt.Sprintf("compact-build-2020010%dT000000Z.screens", i+1)
		old = append(old, name)
	}
	others := []string{"compact-build-6-20200101T000000Z.screens", "compact-build-notes.screens", "build.size"}
	for _, name := range append(append([]string{}, old...), others...) {
		if err := os.WriteFile(filepath.Join(waitDir, name), []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	path := compactSaveTimeoutScreens(root, "build", "codex", 1, "before", "last", platform.Env{})
	if path == "" {
		t.Fatal("save returned no path")
	}
	var kept []string
	entries, err := os.ReadDir(waitDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		kept = append(kept, entry.Name())
	}
	want := map[string]bool{filepath.Base(path): true}
	for _, name := range old[len(old)-(compactTimeoutScreensKeep-1):] {
		want[name] = true
	}
	for _, name := range others {
		want[name] = true
	}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for _, name := range kept {
		if !want[name] {
			t.Fatalf("kept %q, want only %v", name, want)
		}
	}
}

func TestCompactTimeoutScreensFailureIsNotTimeout(t *testing.T) {
	f := newCompactFixture(t, "claude", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "before\n"},
		{Argv: compactReadArgv("worker"), Stdout: "Error compacting conversation: refused\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}},
	})
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "30s")
	if code != 9 || compactJSON(t, out)["status"] != "failed" || strings.Contains(stderr, "screens saved to") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if entries, err := os.ReadDir(filepath.Join(f.root, "state", "ws", "wait")); err != nil || len(entries) != 0 {
		t.Fatalf("non-timeout saved screens: entries=%v err=%v", entries, err)
	}
}

func TestCompactTimeoutScreensReturnedPath(t *testing.T) {
	for _, sentinel := range []bool{false, true} {
		t.Run(fmt.Sprint(sentinel), func(t *testing.T) {
			limit := int64(1000)
			if !sentinel {
				fakeFastClock(t, time.Hour)
				limit = 30000
			}
			f := newCompactFixture(t, "codex", timeoutScreensRules(sentinel))
			var stdout, stderr bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var result compactStepResult
			func() {
				defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
				platform.Stdout, platform.Stderr = &stdout, &stderr
				result = compactRunWithScreens("worker", "p1", "codex", "", "", "", limit, &core.Config{}, f.env, f.root, false)
			}()
			if result.Code != 9 || result.Screens == "" || compactJSON(t, stdout.String())["status"] != "timeout" || stderr.String() != "" {
				t.Fatalf("in-process timeout result=%+v stdout=%q stderr=%q", result, stdout.String(), stderr.String())
			}
			body, err := os.ReadFile(result.Screens)
			if err != nil || !strings.Contains(string(body), timeoutScreensBefore) || !strings.HasSuffix(string(body), timeoutScreensLast) {
				t.Fatalf("returned path did not identify committed diagnostic: result=%+v err=%v", result, err)
			}
			if filepath.Dir(result.Screens) != filepath.Join(f.root, "state", "ws", "wait") || strings.Contains(stdout.String(), result.Screens) {
				t.Fatalf("path escaped state or entered JSON: result=%+v stdout=%q", result, stdout.String())
			}
		})
	}
}

func TestDispatchCompactTimeoutScreensWarningNamesPath(t *testing.T) {
	f := newCompactPhaseDispatchFixture(t, "", 30*time.Second)
	f.install(t, timeoutScreensRules(false))
	f.env["SOHO_COMPACT_TIMEOUT"] = "90s"
	code, out, stderr := f.run(t, "worker", f.brief, "--compact", "--no-wait")
	if code != 9 || compactJSON(t, out)["status"] != "timeout" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	paths, err := filepath.Glob(filepath.Join(f.stateDir, "wait", "compact-worker-*.screens"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("diagnostics=%v err=%v", paths, err)
	}
	want := "dispatch: compact of 'worker' timed out after 90s (compact_timeout=90000); the brief was not sent; screens saved to " + paths[0]
	if !strings.Contains(stderr, "soho: warning: "+want+"\n") || strings.Contains(stderr, "warning: compact: 'worker' timed out") {
		t.Fatalf("dispatch warning did not append the diagnostic path: %q", stderr)
	}
	friction, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(friction), want) {
		t.Fatalf("dispatch friction lacks path: %q", friction)
	}
	for _, output := range []string{out, stderr, string(friction)} {
		if strings.Contains(output, "private-before") || strings.Contains(output, "private-last") {
			t.Fatalf("raw screen escaped local file: %q", output)
		}
	}
	if strings.Contains(out, paths[0]) || countArgvPrefix(f.calls(t), []string{"agent", "prompt", "worker"}) != 0 {
		t.Fatalf("path entered JSON or brief was sent after timeout: out=%q calls=%#v", out, f.calls(t))
	}
}
