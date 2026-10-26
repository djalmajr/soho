package cli

// F11: the dispatch's compact-phase marker end to end — the claim before the
// first command, the status lines, the pre-send check for every dispatch,
// the releases on success/timeout/panic, the SIGKILL leftover, and the
// cleanup-failure boundary (a phase that succeeded but whose marker could
// not be removed aborts with 4 before the brief is sent).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

const (
	compactPhaseIdleJSON = `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`
	compactPhaseBusyJSON = `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`
)

// compactPhaseDispatchFixture is a hermetic dispatch: a fake herdr (plus the
// system ps, for the marker's by-pid reads), its own state dir with a
// rostered codex worker (pane p1), roles and brief, and a pinned clock
// (advancing by clockStep on every read) so the compact phase's 300 s cap is
// bounded in test time.
type compactPhaseDispatchFixture struct {
	root     string
	stateDir string
	bin      string
	brief    string
	env      platform.Env
}

func newCompactPhaseDispatchFixture(t *testing.T, root string, clockStep time.Duration) *compactPhaseDispatchFixture {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	state, roles, bin := filepath.Join(root, "state"), filepath.Join(root, "roles"), filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(state, "ws", "wait"), filepath.Join(state, "ws", "briefs"), filepath.Join(state, "ws", "reports"), roles, bin, filepath.Join(root, "home"), filepath.Join(root, "skill")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "skill", "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tp1\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 3, 15, 26, 48, 0, time.UTC)
	origNow := platform.Now
	platform.Now = func() time.Time {
		value := clock
		clock = clock.Add(clockStep)
		return value
	}
	t.Cleanup(func() { platform.Now = origNow })
	f := &compactPhaseDispatchFixture{
		root:     root,
		stateDir: filepath.Join(state, "ws"),
		bin:      bin,
		brief:    brief,
		env: isolatedUserEnv(platform.Env{
			"PATH":                         bin + string(os.PathListSeparator) + "/usr/bin:/bin",
			"SOHO_FAKECLI_CONFIG":          bin,
			"HERDR_ENV":                    "1",
			"SOHO_DIR":                     state,
			"HERDR_WORKSPACE_ID":           "ws",
			"SOHO_ROLES":                   roles,
			"SOHO_SKILL_DIR":               filepath.Join(root, "skill"),
			"SOHO_PROMPT_CHECK_SECONDS":    "0",
			"SOHO_PROMPT_SETTLE_SECONDS":   "0",
			"SOHO_BRIEF_LINT":              "off",
			"SOHO_WAIT_POLL_MS":            "50",
			"FAKECLI_WAIT_FILE_TIMEOUT_MS": "15000",
			"TMPDIR":                       root,
			"HOME":                         filepath.Join(root, "home"),
			"HERDR_SOCKET_PATH":            filepath.Join(root, "missing.sock"),
		}),
	}
	// A race-instrumented fake child otherwise pauses for one second on
	// exit; the fixture sets only GORACE and leaves the rest of the env as built.
	f.env["GORACE"] = fakecli.RaceOptions(f.env.List())
	return f
}

func (f *compactPhaseDispatchFixture) install(t *testing.T, rules []fakecli.Rule) {
	t.Helper()
	if _, err := fakecli.Install(t, f.bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
}

func (f *compactPhaseDispatchFixture) calls(t *testing.T) []fakecli.Call {
	t.Helper()
	logPath := filepath.Join(f.bin, "herdr.calls.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return nil // herdr was never invoked
	}
	calls, err := fakecli.ReadCalls(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func (f *compactPhaseDispatchFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"dispatch"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

// runAsync starts the dispatch in a goroutine (the fixture's run swaps the
// platform writers, which the main goroutine must not touch while it runs)
// and delivers the result when the dispatch ends.
func (f *compactPhaseDispatchFixture) runAsync(t *testing.T, args ...string) <-chan struct {
	code int
	out  string
	err  string
} {
	t.Helper()
	result := make(chan struct {
		code int
		out  string
		err  string
	}, 1)
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		// Async callers gate the fake herdr on this fixture file.
		// Release it even when Fatal prevented the test from doing so.
		if err := os.WriteFile(filepath.Join(f.root, "release"), []byte("go\n"), 0o600); err != nil {
			t.Errorf("release async compact dispatch during cleanup: %v", err)
		}
		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			t.Error("async compact dispatch did not finish within 60s during cleanup; platform writers may still be in use")
		}
	})
	go func() {
		defer close(done)
		code, out, stderr := f.run(t, args...)
		result <- struct {
			code int
			out  string
			err  string
		}{code, out, stderr}
	}()
	return result
}

func TestCompactPhaseRunAsyncCleanup(t *testing.T) {
	for _, consume := range []bool{false, true} {
		name := "unread-result"
		if consume {
			name = "consumed-result"
		}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var result <-chan struct {
			code int
			out  string
			err  string
		}
		t.Run(name, func(t *testing.T) {
			f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
			release := filepath.Join(f.root, "release")
			f.install(t, []fakecli.Rule{
				{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
				{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, WaitFile: release, Stdout: `{"result":{"sent":true}}`},
				{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
			})
			result = f.runAsync(t, "worker", f.brief, "--no-wait")
			deadline := time.Now().Add(60 * time.Second)
			for countArgvPrefix(f.calls(t), []string{"agent", "prompt"}) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("async dispatch never reached the fixture's release gate")
				}
				time.Sleep(50 * time.Millisecond)
			}
			if consume {
				if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if res := <-result; res.code != 0 {
					t.Fatalf("code=%d out=%q err=%q", res.code, res.out, res.err)
				}
			}
		})
		if platform.Stdout != oldOut || platform.Stderr != oldErr {
			t.Fatal("async cleanup did not restore the platform writers before the subtest ended")
		}
		if !consume {
			select {
			case res := <-result:
				if res.code != 0 {
					t.Fatalf("code=%d out=%q err=%q", res.code, res.out, res.err)
				}
			default:
				t.Fatal("async cleanup returned before publishing the unread result")
			}
		}
	}
}

// compactPhaseWaitForMarker polls until the marker stands (the phase claimed
// it) or the deadline passes.
func compactPhaseWaitForMarker(t *testing.T, marker string) *core.CompactPending {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		rec, err := core.CompactPendingRead(marker)
		if err == nil {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("the marker %s never appeared: %v", marker, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func compactPhaseMarkerAbsent(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the marker %s still stands after the phase: %v", marker, err)
	}
}

func compactPhaseNoTaskState(t *testing.T, f *compactPhaseDispatchFixture) {
	t.Helper()
	if _, err := os.Stat(taskreport.TaskReportPointerPath(f.stateDir, "worker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed or refused dispatch wrote the pointer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, "last-report-worker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed or refused dispatch wrote last-report: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(f.stateDir, "briefs"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("a failed or refused dispatch wrote composed files: %v err=%v", entries, err)
	}
}

// TestDispatchCompactSuccessShowsThePhaseAndSendsAfterIt proves the lifecycle:
// the marker stands while the phase runs (read through the fake send-text's
// wait, which blocks the dispatch inside the phase), the progress line
// precedes the phase, and after a success the marker is gone, the brief is
// sent once, and the task state exists.
func TestDispatchCompactSuccessShowsThePhaseAndSendsAfterIt(t *testing.T) {
	f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
	release := filepath.Join(f.root, "release")
	f.install(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactPhaseIdleJSON},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, WaitFile: release, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: "> /compact\nContext compacted\n"},
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	})
	marker := core.CompactPendingPath(f.stateDir, "worker")
	result := f.runAsync(t, "worker", f.brief, "--compact", "--no-wait")
	// The dispatch blocks inside the compaction phase (the fake send-text
	// waits for the release file): the marker stands with the contract's
	// fields, read through the real file.
	rec := compactPhaseWaitForMarker(t, marker)
	if rec.Version != 1 || rec.Token == "" || rec.Pane != "p1" || rec.Brief != f.brief || rec.PID != os.Getpid() || rec.Started == "" {
		t.Fatalf("marker during the phase = %+v; want version 1, a token, pane p1, the brief's absolute path, this process's pid and a start", rec)
	}
	if _, err := time.Parse(time.RFC3339, rec.CreatedAt); err != nil {
		t.Fatalf("created_at %q is not RFC3339: %v", rec.CreatedAt, err)
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := <-result
	if res.code != 0 {
		t.Fatalf("code=%d out=%q err=%q", res.code, res.out, res.err)
	}
	compactPhaseMarkerAbsent(t, marker)
	wantLine := "soho: dispatch: compacting 'worker' before sending the brief; status shows this phase\n"
	if !strings.Contains(res.err, wantLine) {
		t.Fatalf("stderr=%q; want the progress line before the phase", res.err)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"pane", "send-text"}); n != 1 {
		t.Fatalf("send-text calls = %d; want 1: %#v", n, calls)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the brief was sent %d times after the phase; want 1", n)
	}
	if _, err := os.Stat(taskreport.TaskReportPointerPath(f.stateDir, "worker")); err != nil {
		t.Fatalf("the task pointer is missing after the phase: %v", err)
	}
}

// TestDispatchCompactTimeoutRemovesTheMarkerAndSendsNothing: a timeout in the
// phase removes the marker, keeps the phase's own exit 9 and stdout JSON, and
// sends nothing (no prompt, no task state).
func TestDispatchCompactTimeoutRemovesTheMarkerAndSendsNothing(t *testing.T) {
	f := newCompactPhaseDispatchFixture(t, "", 400*time.Second) // the 300 s cap runs out within one poll
	f.install(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactPhaseIdleJSON},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "> /compact\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	marker := core.CompactPendingPath(f.stateDir, "worker")
	code, out, errText := f.run(t, "worker", f.brief, "--compact", "--no-wait")
	if code != 9 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errText)
	}
	if !strings.Contains(out, `"status":"timeout"`) {
		t.Fatalf("the phase's timeout JSON is missing from stdout: %q", out)
	}
	if !strings.Contains(errText, "soho: dispatch: the compact step of 'worker' did not finish (exit 9); the brief was not sent") {
		t.Fatalf("stderr=%q; want the did-not-finish note", errText)
	}
	compactPhaseMarkerAbsent(t, marker)
	if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
		t.Fatalf("a timed-out phase sent the brief %d times; want 0", n)
	}
	compactPhaseNoTaskState(t, f)
}

// TestDispatchCompactBusyWorkerReleasesTheMarkerAndSendsNothing: the
// DieFriction panic path (a busy worker) also removes the marker and sends
// nothing.
func TestDispatchCompactBusyWorkerReleasesTheMarkerAndSendsNothing(t *testing.T) {
	f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
	f.install(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactPhaseBusyJSON},
	})
	marker := core.CompactPendingPath(f.stateDir, "worker")
	code, out, errText := f.run(t, "worker", f.brief, "--compact", "--no-wait")
	if code != 10 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errText)
	}
	if out != "" {
		t.Fatalf("a panicked phase printed stdout: %q", out)
	}
	if !strings.Contains(errText, "soho: dispatch: the compact step of 'worker' did not finish (exit 10); the brief was not sent") {
		t.Fatalf("stderr=%q; want the did-not-finish note", errText)
	}
	compactPhaseMarkerAbsent(t, marker)
	if n := countArgvPrefix(f.calls(t), []string{"pane", "send-text"}); n != 0 {
		t.Fatalf("a busy worker got %d send-text call(s); want 0", n)
	}
	compactPhaseNoTaskState(t, f)
}

// compactPhaseReplaceMarker writes the replacement over the marker while the
// dispatch is blocked inside the phase (the fake send-keys waits for the
// release file), so the cleanup sees exactly the wanted state.
func compactPhaseReplaceMarker(t *testing.T, f *compactPhaseDispatchFixture, release, content string) {
	t.Helper()
	marker := core.CompactPendingPath(f.stateDir, "worker")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the marker never stood before the replacement: %v", err)
	}
	if err := os.WriteFile(marker, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchCompactSuccessWithUnremovableMarkerAbortsBeforeSend is the
// cleanup-failure boundary: the phase succeeded, but the marker carries a
// different token (or is unreadable) when the cleanup runs — the marker is
// never removed, the brief is never sent, and the dispatch aborts with 4 and
// a diagnosis.
func TestDispatchCompactSuccessWithUnremovableMarkerAbortsBeforeSend(t *testing.T) {
	selfStarted := func(t *testing.T) string {
		t.Helper()
		started, live := platform.ReadProc(os.Getpid(), platform.Env{"PATH": "/usr/bin:/bin"})
		if live != platform.ProcRunning || started == "" {
			t.Fatalf("own identity read = %q/%v; want running with a start", started, live)
		}
		return started
	}
	cases := []struct {
		name        string
		replacement string
		wantInFile  string
		wantDiag    string
	}{
		{
			name:        "a later token after success aborts with 4 and keeps the marker",
			replacement: "", // built per fixture: a valid record with another token
			wantInFile:  "later-token",
			wantDiag:    "another token",
		},
		{
			name:        "a corrupted marker after success aborts with 4 and keeps the file",
			replacement: "corrupted\n",
			wantInFile:  "corrupted",
			wantDiag:    "unreadable",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
			release := filepath.Join(f.root, "release")
			f.install(t, []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactPhaseIdleJSON},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, WaitFile: release, Stdout: `{"result":{}}`},
				{Argv: compactReadArgv("worker"), Call: 2, Stdout: "> /compact\nContext compacted\n"},
				{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
			})
			marker := core.CompactPendingPath(f.stateDir, "worker")
			result := f.runAsync(t, "worker", f.brief, "--compact", "--no-wait")
			claimed := compactPhaseWaitForMarker(t, marker)
			if c.replacement == "" {
				c.replacement = jsonjs.Stringify(jsonjs.O(
					"version", 1,
					"token", "later-token",
					"pane", "p1",
					"brief", f.brief,
					"pid", claimed.PID,
					"started", selfStarted(t),
					"created_at", time.Now().UTC().Format(time.RFC3339),
				)) + "\n"
			}
			compactPhaseReplaceMarker(t, f, release, c.replacement)
			res := <-result
			if res.code != 4 {
				t.Fatalf("code=%d out=%q err=%q; want the 4 abort before the brief", res.code, res.out, res.err)
			}
			if res.out != "" {
				t.Fatalf("the aborted dispatch printed a result JSON: %q", res.out)
			}
			if !strings.Contains(res.err, "soho: warning: dispatch: the compact-phase marker of 'worker' was not removed:") {
				t.Fatalf("stderr=%q; want the recorded cleanup failure", res.err)
			}
			if !strings.Contains(res.err, "soho: dispatch: the compact phase of 'worker' finished, but its marker could not be removed (") || !strings.Contains(res.err, "the brief was not sent") {
				t.Fatalf("stderr=%q; want the 4 diagnosis naming that the brief was not sent", res.err)
			}
			if !strings.Contains(res.err, c.wantDiag) {
				t.Fatalf("stderr=%q; want the %q cause in the diagnosis", res.err, c.wantDiag)
			}
			after, err := os.ReadFile(marker)
			if err != nil || !strings.Contains(string(after), c.wantInFile) {
				t.Fatalf("the marker was removed or changed (must keep %q): %q err=%v", c.wantInFile, after, err)
			}
			if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
				t.Fatalf("the brief was sent %d times; want 0", n)
			}
			compactPhaseNoTaskState(t, f)
			log, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
			if err != nil || !strings.Contains(string(log), "error(exit 4)") {
				t.Fatalf("the abort did not record its friction line: %q err=%v", log, err)
			}
		})
	}
}

func compactPhaseTestMarker(t *testing.T, token, pane, brief string, pid int, started string) string {
	t.Helper()
	return jsonjs.Stringify(jsonjs.O(
		"version", 1,
		"token", token,
		"pane", pane,
		"brief", brief,
		"pid", pid,
		"started", started,
		"created_at", time.Now().UTC().Format(time.RFC3339),
	)) + "\n"
}

func (f *compactPhaseDispatchFixture) writeMarker(t *testing.T, content string) string {
	t.Helper()
	path := core.CompactPendingPath(f.stateDir, "worker")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func compactPhaseDeadPID(t *testing.T) int {
	t.Helper()
	cmd := compactPhaseLivenessChild(t, "600")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

const compactPhaseLivenessChildEnv = "COMPACT_PHASE_LIVENESS_CHILD"

// TestCompactPhaseLivenessChild is the re-executed liveness helper: it
// sleeps the duration named by COMPACT_PHASE_LIVENESS_CHILD (milliseconds)
// and exits, so the parent gets a dead pid. Re-executing the test binary
// keeps the helper portable (no /bin/sleep).
func TestCompactPhaseLivenessChild(t *testing.T) {
	ms := os.Getenv(compactPhaseLivenessChildEnv)
	if ms == "" {
		t.Skip("runs only as the liveness helper's child")
	}
	d, err := time.ParseDuration(ms + "ms")
	if err != nil {
		t.Fatalf("%s = %q: %v", compactPhaseLivenessChildEnv, ms, err)
	}
	time.Sleep(d)
}

// compactPhaseLivenessChild re-executes the test binary as the liveness
// helper. The child keeps the parent's environment (PATH and SYSTEMROOT
// included) minus the HERDR_* context; nothing is printed.
func compactPhaseLivenessChild(t *testing.T, sleepMS string) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestCompactPhaseLivenessChild$", "-test.timeout=60s")
	childEnv := []string{}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "HERDR_") {
			continue
		}
		childEnv = append(childEnv, entry)
	}
	childEnv = append(childEnv, compactPhaseLivenessChildEnv+"="+sleepMS)
	cmd.Env = childEnv
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

var compactPhasePlainDispatchRules = func() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	}
}()

// TestDispatchWithoutCompactRefusesAnActivePhase: a dispatch without
// --compact checks the marker before any state change or send; a live
// owner for this pane refuses with 10, keeps the marker, sends nothing and
// writes no state.
func TestDispatchWithoutCompactRefusesAnActivePhase(t *testing.T) {
	f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
	started, live := platform.ReadProc(os.Getpid(), f.env)
	if live != platform.ProcRunning || started == "" {
		t.Fatalf("fixture own identity read = %q/%v; want running with a start", started, live)
	}
	marker := f.writeMarker(t, compactPhaseTestMarker(t, "live-token", "p1", f.brief, os.Getpid(), started))
	f.install(t, compactPhasePlainDispatchRules)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 10 {
		t.Fatalf("code=%d out=%q err=%q; want the 10 refusal before the send", code, out, errText)
	}
	if out != "" {
		t.Fatalf("the refusal printed stdout: %q", out)
	}
	want := fmt.Sprintf("soho: dispatch: the compact phase of 'worker' (pane p1) is in progress (owner pid %d is alive); the brief was not sent; wait for it to end (soho status worker)", os.Getpid())
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q; want %q", errText, want)
	}
	after, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(after), "live-token") {
		t.Fatalf("the live phase's marker was replaced or removed: %q err=%v", after, err)
	}
	if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
		t.Fatalf("the refused dispatch sent the brief %d times; want 0", n)
	}
	compactPhaseNoTaskState(t, f)
}

// TestDispatchWithoutCompactCleansDeadAndForeignRecords: a record whose owner
// is proven dead (a different start included) or whose pane no longer matches
// is removed and the dispatch proceeds; a corrupted marker refuses with 4
// without being removed.
func TestDispatchWithoutCompactCleansDeadAndForeignRecords(t *testing.T) {
	t.Run("a dead owner's record is removed and the send proceeds", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
		marker := f.writeMarker(t, compactPhaseTestMarker(t, "dead-token", "p1", f.brief, compactPhaseDeadPID(t), "Sat Oct  3 00:00:00 2026"))
		f.install(t, compactPhasePlainDispatchRules)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("code=%d out=%q err=%q; want the send to proceed", code, out, errText)
		}
		compactPhaseMarkerAbsent(t, marker)
		if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 1 {
			t.Fatalf("the brief was sent %d times; want 1", n)
		}
	})
	t.Run("a reused pid with a different start is cleaned the same way", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
		marker := f.writeMarker(t, compactPhaseTestMarker(t, "reused-token", "p1", f.brief, os.Getpid(), "Thu Dec 25 23:59:59 1999"))
		f.install(t, compactPhasePlainDispatchRules)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("code=%d out=%q err=%q; want the send to proceed", code, out, errText)
		}
		compactPhaseMarkerAbsent(t, marker)
	})
	t.Run("a foreign pane's record is removed even with a live owner", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
		started, live := platform.ReadProc(os.Getpid(), f.env)
		if live != platform.ProcRunning {
			t.Fatalf("own identity read = %v; want running", live)
		}
		marker := f.writeMarker(t, compactPhaseTestMarker(t, "foreign-token", "p-other", f.brief, os.Getpid(), started))
		f.install(t, compactPhasePlainDispatchRules)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("code=%d out=%q err=%q; want the send to proceed", code, out, errText)
		}
		compactPhaseMarkerAbsent(t, marker)
	})
	t.Run("a corrupted marker refuses with 4 and is kept", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
		marker := f.writeMarker(t, "corrupted\n")
		f.install(t, compactPhasePlainDispatchRules)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 4 || out != "" {
			t.Fatalf("code=%d out=%q err=%q; want the 4 refusal", code, out, errText)
		}
		want := "soho: dispatch: the compact-phase marker " + marker + " of 'worker' is unreadable; the brief was not sent"
		if !strings.Contains(errText, want) {
			t.Fatalf("stderr=%q; want %q", errText, want)
		}
		after, err := os.ReadFile(marker)
		if err != nil || string(after) != "corrupted\n" {
			t.Fatalf("the corrupted marker was changed: %q err=%v", after, err)
		}
		if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
			t.Fatalf("the refused dispatch sent the brief %d times; want 0", n)
		}
		compactPhaseNoTaskState(t, f)
	})
	t.Run("an out-of-range pid marker refuses with 4 and is kept", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
		// 2^32+1 exceeds the native pid bound on every platform (unix
		// pid_t, windows DWORD): it would truncate in the native call, so
		// the record is corrupt — refused before any owner consultation.
		content := fmt.Sprintf(`{"version":1,"token":"out-of-range","pane":"p1","brief":%q,"pid":4294967297,"started":"Sat Oct  3 00:00:00 2026","created_at":%q}
`, f.brief, time.Now().UTC().Format(time.RFC3339))
		marker := f.writeMarker(t, content)
		f.install(t, compactPhasePlainDispatchRules)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 4 || out != "" {
			t.Fatalf("code=%d out=%q err=%q; want the 4 refusal", code, out, errText)
		}
		want := "soho: dispatch: the compact-phase marker " + marker + " of 'worker' is unreadable; the brief was not sent"
		if !strings.Contains(errText, want) {
			t.Fatalf("stderr=%q; want %q", errText, want)
		}
		after, err := os.ReadFile(marker)
		if err != nil || string(after) != content {
			t.Fatalf("the out-of-range marker was changed: %q err=%v", after, err)
		}
		if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
			t.Fatalf("the refused dispatch sent the brief %d times; want 0", n)
		}
		compactPhaseNoTaskState(t, f)
	})
}

// TestDispatchWithoutCompactUnremovableStaleRecordRefuses: the dispatch
// check's own cleanup is fail-closed — when a stale record (dead owner or
// foreign pane) cannot be removed, the dispatch refuses with 4 before the
// send and keeps the file, which a send would leave falsely claiming
// "brief was not sent". The removal failure is forced with a read-only
// wait/ dir (the roster lock lives in state/ itself and stays writable);
// the technique is unix-only.
func TestDispatchWithoutCompactUnremovableStaleRecordRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("forcing the removal failure uses the unix wait/ directory permission; the windows API does not honor it")
	}
	deadPID := compactPhaseDeadPID(t)
	started, live := platform.ReadProc(os.Getpid(), platform.Env{"PATH": "/usr/bin:/bin"})
	if live != platform.ProcRunning {
		t.Fatalf("own identity read = %v; want running", live)
	}
	cases := []struct {
		name   string
		marker string
		wantIn string
	}{
		{
			name:   "a dead owner's record that cannot be removed refuses with 4",
			marker: compactPhaseTestMarker(t, "dead-token", "p1", "/abs/brief.md", deadPID, "Sat Oct  3 00:00:00 2026"),
			wantIn: "dead owner",
		},
		{
			name:   "a foreign pane's record that cannot be removed refuses with 4",
			marker: compactPhaseTestMarker(t, "foreign-token", "p-other", "/abs/brief.md", os.Getpid(), started),
			wantIn: "foreign pane p-other",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCompactPhaseDispatchFixture(t, "", 500*time.Millisecond)
			marker := f.writeMarker(t, c.marker)
			before, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			waitDir := filepath.Join(f.stateDir, "wait")
			if err := os.Chmod(waitDir, 0o500); err != nil { // r-x: the marker cannot be unlinked
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(waitDir, 0o700) })
			f.install(t, compactPhasePlainDispatchRules)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 4 || out != "" {
				t.Fatalf("code=%d out=%q err=%q; want the 4 refusal before the send", code, out, errText)
			}
			if !strings.Contains(errText, "could not be removed:") || !strings.Contains(errText, c.wantIn) || !strings.Contains(errText, "the brief was not sent") {
				t.Fatalf("stderr=%q; want the %q diagnosis and the unsent brief", errText, c.wantIn)
			}
			after, err := os.ReadFile(marker)
			if err != nil || string(after) != string(before) {
				t.Fatalf("the marker was changed instead of kept: %q err=%v", after, err)
			}
			if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
				t.Fatalf("the refused dispatch sent the brief %d times; want 0", n)
			}
			compactPhaseNoTaskState(t, f)
		})
	}
}

const f11CompactPhaseChildEnv = "F11_COMPACT_PHASE_CHILD"

// TestF11CompactPhaseChild is the child half of the SIGKILL test: it runs the
// real dispatch --compact in this re-executed process, and the parent kills
// the process while the compaction phase blocks in the fake send-text.
func TestF11CompactPhaseChild(t *testing.T) {
	root := os.Getenv(f11CompactPhaseChildEnv)
	if root == "" {
		t.Skip("runs only as the parent's SIGKILL child")
	}
	f := newCompactPhaseDispatchFixture(t, root, 500*time.Millisecond)
	f.install(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactPhaseIdleJSON},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "old output line\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, WaitFile: filepath.Join(root, "never")},
	})
	// Unreachable while the parent is alive: the kill lands mid-phase, before
	// the fake's own wait timeout.
	_ = Run([]string{"dispatch", "worker", f.brief, "--compact"}, f.env)
}

// TestDispatchCompactSigKILLKeepsTheInterruptedMarker runs a real dispatch in
// a child process and kills it (a hard kill skips the cleanup) while the
// phase blocks: the marker stands with the child's dead pid, no task state
// was written, and status diagnoses the interruption with exit 9.
func TestDispatchCompactSigKILLKeepsTheInterruptedMarker(t *testing.T) {
	if os.Getenv(f11CompactPhaseChildEnv) != "" {
		t.Skip("child mode")
	}
	root := t.TempDir()
	// Registered after t.TempDir, so it runs first: on Windows a fake herdr
	// the killed child started can hold its exe for a moment after the kill,
	// and the TempDir cleanup would fail with "Access is denied" (DJA-553).
	t.Cleanup(func() { compactRemoveWithRetry(filepath.Join(root, "bin"), 10*time.Second) })
	stateDir := filepath.Join(root, "state", "ws")
	marker := core.CompactPendingPath(stateDir, "worker")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv := []string{}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "HERDR_") {
			continue
		}
		childEnv = append(childEnv, entry)
	}
	childEnv = append(childEnv, f11CompactPhaseChildEnv+"="+root)
	cmd := exec.Command(bin, "-test.run=^TestF11CompactPhaseChild$", "-test.timeout=90s")
	cmd.Env = childEnv
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rec := compactPhaseWaitForMarker(t, marker)
	if rec.PID != cmd.Process.Pid {
		t.Fatalf("marker pid = %d; want the child's pid %d", rec.PID, cmd.Process.Pid)
	}
	if rec.Pane != "p1" || rec.Brief != filepath.Join(root, "brief.md") || rec.Token == "" || rec.Started == "" {
		t.Fatalf("marker = %+v; want pane p1, the child's brief path, a token and a start", rec)
	}
	// The hard kill: the child's cleanup never runs. On Windows the kill
	// takes the whole tree, so no fake herdr the child started outlives it.
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the child survived the kill")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the marker was removed by the killed dispatch: %v", err)
	}
	compactPhaseNoTaskStateFor(t, stateDir, root)
	if data, err := os.ReadFile(filepath.Join(root, "brief.md")); err != nil || len(data) == 0 {
		t.Fatalf("the pending brief was touched: %q err=%v", data, err)
	}
	env := platform.Env{
		"PATH":               "/usr/bin:/bin",
		"HERDR_ENV":          "1",
		"SOHO_DIR":           filepath.Join(root, "state"),
		"HERDR_WORKSPACE_ID": "ws",
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := waitpkg.CmdStatus([]string{"worker"}, ctx, env, root)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if code != 9 {
		t.Fatalf("status code = %d out=%q err=%q; want the interrupted exit 9", code, out, errOut)
	}
	want := fmt.Sprintf("worker\tcompact-interrupted\t\tbrief was not sent: %s\t-\t-\n", filepath.Join(root, "brief.md"))
	if out.String() != want {
		t.Fatalf("status line:\n got:  %q\nwant: %q", out.String(), want)
	}
}

func compactPhaseNoTaskStateFor(t *testing.T, stateDir, root string) {
	t.Helper()
	f := &compactPhaseDispatchFixture{stateDir: stateDir, root: root}
	compactPhaseNoTaskState(t, f)
}

// compactPhaseStatusFixture is the F11 status fixture: a rostered worker
// (pane p1), a wait dir, an optional stale complete report (pinned mtimes so
// the control's task_s is deterministic), and a fake herdr for the fall-through.
type compactPhaseStatusFixture struct {
	root        string
	stateDir    string
	ctx         *core.Config
	env         platform.Env
	cwd         string
	staleReport string
	briefPath   string
}

func newCompactPhaseStatusFixture(t *testing.T, withStaleReport bool) *compactPhaseStatusFixture {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	skill := filepath.Join(root, "skill")
	cwd := filepath.Join(root, "cwd")
	for _, dir := range []string{cwd, filepath.Join(state, "ws", "wait"), filepath.Join(state, "ws", "reports"), bin, skill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp1\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	briefPath := filepath.Join(root, "brief.md")
	f := &compactPhaseStatusFixture{
		root:      root,
		stateDir:  filepath.Join(state, "ws"),
		briefPath: briefPath,
		ctx:       &core.Config{Entries: map[string]core.ConfigEntry{}},
		env: isolatedUserEnv(platform.Env{
			"PATH":                bin + string(os.PathListSeparator) + "/usr/bin:/bin",
			"HERDR_ENV":           "1",
			"SOHO_DIR":            state,
			"HERDR_WORKSPACE_ID":  "ws",
			"HERDR_SOCKET_PATH":   filepath.Join(root, "none.sock"),
			"SOHO_FAKECLI_CONFIG": bin,
			"SOHO_SKILL_DIR":      skill,
		}),
		cwd: cwd,
	}
	if !withStaleReport {
		return f
	}
	f.staleReport = filepath.Join(state, "ws", "reports", "worker-20261003T100000.md")
	if err := os.WriteFile(f.staleReport, []byte("# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws", "last-report-worker"), []byte(f.staleReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportMod, lastMod := time.Date(2026, 1, 2, 10, 0, 30, 0, time.UTC), time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(f.staleReport, reportMod, reportMod); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(state, "ws", "last-report-worker"), lastMod, lastMod); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "busy on the task\n"},
		{Argv: []string{"agent", "get", "ghost"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"agent target ghost not found"},"id":"cli:agent:get"}`},
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *compactPhaseStatusFixture) writeMarker(t *testing.T, agent, content string) string {
	t.Helper()
	path := core.CompactPendingPath(f.stateDir, agent)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *compactPhaseStatusFixture) runStatus(t *testing.T, env platform.Env, agents []string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := waitpkg.CmdStatus(agents, f.ctx, env, f.cwd)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

// TestStatusCompactPhaseLines proves the status side: the marker outranks a
// stale complete report, the three states carry their exits (0, 9, 4), a
// corrupted marker is a visible 4 that is never removed, and a roster-absent
// or switched-pane agent keeps the old behavior.
func TestStatusCompactPhaseLines(t *testing.T) {
	selfStarted := func(t *testing.T) string {
		t.Helper()
		started, live := platform.ReadProc(os.Getpid(), platform.Env{"PATH": "/usr/bin:/bin"})
		if live != platform.ProcRunning || started == "" {
			t.Fatalf("own identity read = %q/%v; want running with a start", started, live)
		}
		return started
	}
	markerJSON := func(t *testing.T, token, pane string, pid int, started string) string {
		t.Helper()
		return compactPhaseTestMarker(t, token, pane, "/abs/brief.md", pid, started)
	}
	dead := compactPhaseDeadPID(t)
	t.Run("compacting outranks a stale complete report", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, true)
		marker := f.writeMarker(t, "worker", markerJSON(t, "tok", "p1", os.Getpid(), selfStarted(t)))
		code, out, errText := f.runStatus(t, f.env, []string{"worker"})
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		want := fmt.Sprintf("worker\tcompacting\t\tbrief was not sent: /abs/brief.md\t-\t-\n")
		if out != want {
			t.Fatalf("status line:\n got:  %q\nwant: %q (the stale done report must not show)", out, want)
		}
		// The control without the marker keeps the old done line.
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
		code, out, errText = f.runStatus(t, f.env, []string{"worker"})
		if code != 0 {
			t.Fatalf("control code=%d out=%q err=%q", code, out, errText)
		}
		wantDone := fmt.Sprintf("worker\tdone\t%s\t30\t-\n", f.staleReport)
		if out != wantDone {
			t.Fatalf("control line:\n got:  %q\nwant: %q", out, wantDone)
		}
	})
	t.Run("compact-interrupted on a dead owner exits 9", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, true)
		f.writeMarker(t, "worker", markerJSON(t, "tok", "p1", dead, selfStarted(t)))
		code, out, errText := f.runStatus(t, f.env, []string{"worker"})
		if code != 9 {
			t.Fatalf("code=%d out=%q err=%q; want the interrupted exit 9", code, out, errText)
		}
		want := fmt.Sprintf("worker\tcompact-interrupted\t\tbrief was not sent: /abs/brief.md\t-\t-\n")
		if out != want {
			t.Fatalf("status line:\n got:  %q\nwant: %q", out, want)
		}
	})
	t.Run("compact-interrupted on a different start exits 9", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, true)
		f.writeMarker(t, "worker", markerJSON(t, "tok", "p1", os.Getpid(), "Thu Dec 25 23:59:59 1999"))
		code, out, errText := f.runStatus(t, f.env, []string{"worker"})
		if code != 9 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		if !strings.Contains(out, "compact-interrupted") {
			t.Fatalf("status line: %q", out)
		}
	})
	t.Run("compact-unknown when the liveness read fails exits 4", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("an unverifiable owner needs a failing by-pid read for an alive pid; the windows read is direct kernel32 and never fails that way")
		}
		f := newCompactPhaseStatusFixture(t, true)
		f.writeMarker(t, "worker", markerJSON(t, "tok", "p1", os.Getpid(), selfStarted(t)))
		// A PATH without ps: the by-pid read fails for the alive pid, which
		// reads as unknown, never as gone. The rest of the env is kept, so
		// the workspace stays resolvable without a herdr call.
		env := f.env.Clone()
		env["PATH"] = f.env["PATH"][:strings.IndexByte(f.env["PATH"], os.PathListSeparator)]
		code, out, errText := f.runStatus(t, env, []string{"worker"})
		if code != 4 {
			t.Fatalf("code=%d out=%q err=%q; want the unknown exit 4", code, out, errText)
		}
		want := fmt.Sprintf("worker\tcompact-unknown\t\tbrief was not sent: /abs/brief.md\t-\t-\n")
		if out != want {
			t.Fatalf("status line:\n got:  %q\nwant: %q", out, want)
		}
	})
	t.Run("a corrupted marker is a visible error, exit 4, never removed", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, true)
		marker := f.writeMarker(t, "worker", "not json\n")
		code, out, errText := f.runStatus(t, f.env, []string{"worker"})
		if code != 4 {
			t.Fatalf("code=%d out=%q err=%q; want the unreadable exit 4", code, out, errText)
		}
		want := fmt.Sprintf("worker\tcompact-unknown\t\tcompact-pending marker unreadable: %s; brief was not sent\t-\t-\n", marker)
		if out != want {
			t.Fatalf("status line:\n got:  %q\nwant: %q", out, want)
		}
		if !strings.Contains(errText, "soho: warning: status: the compact-pending marker "+marker+" of 'worker' is unreadable; it was left in place") {
			t.Fatalf("stderr=%q; want the visible warning", errText)
		}
		after, err := os.ReadFile(marker)
		if err != nil || string(after) != "not json\n" {
			t.Fatalf("the corrupted marker was changed: %q err=%v", after, err)
		}
	})
	t.Run("an out-of-range pid is a visible error, exit 4, owner never consulted", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, true)
		// 2^32+1 exceeds the native pid bound (unix pid_t / windows DWORD):
		// the record is corrupt, so the read fails before any owner read.
		content := fmt.Sprintf(`{"version":1,"token":"out-of-range","pane":"p1","brief":%q,"pid":4294967297,"started":"Sat Oct  3 00:00:00 2026","created_at":%q}\n`, f.briefPath, time.Now().UTC().Format(time.RFC3339))
		marker := f.writeMarker(t, "worker", content)
		code, out, errText := f.runStatus(t, f.env, []string{"worker"})
		if code != 4 {
			t.Fatalf("code=%d out=%q err=%q; want the unreadable exit 4", code, out, errText)
		}
		want := fmt.Sprintf("worker\tcompact-unknown\t\tcompact-pending marker unreadable: %s; brief was not sent\t-\t-\n", marker)
		if out != want {
			t.Fatalf("status line:\n got:  %q\nwant: %q", out, want)
		}
		if !strings.Contains(errText, "soho: warning: status: the compact-pending marker "+marker+" of 'worker' is unreadable; it was left in place") {
			t.Fatalf("stderr=%q; want the visible warning", errText)
		}
		after, err := os.ReadFile(marker)
		if err != nil || string(after) != content {
			t.Fatalf("the out-of-range marker was changed: %q err=%v", after, err)
		}
		logPath := filepath.Join(f.root, "bin", "herdr.calls.jsonl")
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("herdr was invoked for a corrupt record (log %s present); the owner must never be consulted", logPath)
		}
	})
	t.Run("a switched pane keeps the old behavior", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, true)
		f.writeMarker(t, "worker", markerJSON(t, "tok", "p-other", os.Getpid(), selfStarted(t)))
		code, out, errText := f.runStatus(t, f.env, []string{"worker"})
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		wantDone := fmt.Sprintf("worker\tdone\t%s\t30\t-\n", f.staleReport)
		if out != wantDone {
			t.Fatalf("status line:\n got:  %q\nwant: %q (the switched-pane marker must be ignored)", out, wantDone)
		}
	})
	t.Run("an agent outside the roster ignores its marker", func(t *testing.T) {
		f := newCompactPhaseStatusFixture(t, false)
		marker := f.writeMarker(t, "ghost", markerJSON(t, "tok", "p1", os.Getpid(), selfStarted(t)))
		code, out, errText := f.runStatus(t, f.env, []string{"ghost"})
		if code != 0 || out != "ghost\tunknown-agent\t\t-\t-\n" {
			t.Fatalf("code=%d out=%q err=%q; want the old unknown-agent line", code, out, errText)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("the marker of a roster-absent agent was touched: %v", err)
		}
	})
}

// TestStatusCompactPhaseUnderNowriteWritesNothing: status is a NOWRITE read
// (since the CLI now admits `status [agents...]` read-only): with the
// marker standing it proves the read without mutation — the phase line is
// printed with its exit (the zero-rc fix: the interrupted 9 surfaces,
// not 0), and the marker is byte-identical afterwards. The marker read
// path itself is write-free (core.TestCompactPhaseReadPathWritesNothingUnderNowrite).
func TestStatusCompactPhaseUnderNowriteWritesNothing(t *testing.T) {
	dead := compactPhaseDeadPID(t)
	cases := []struct {
		name    string
		pid     int
		started string
		code    int
		line    string
	}{
		{
			name: "compacting is read with exit 0 and an unchanged marker",
			pid:  os.Getpid(),
			code: 0,
			line: "worker\tcompacting\t\t",
		},
		{
			name: "the interrupted 9 surfaces through the full CLI, not 0",
			pid:  dead,
			code: 9,
			line: "worker\tcompact-interrupted\t\t",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCompactPhaseStatusFixture(t, true)
			started := c.started
			if c.started == "" {
				var live platform.ProcLiveness
				started, live = platform.ReadProc(os.Getpid(), platform.Env{"PATH": "/usr/bin:/bin"})
				if live != platform.ProcRunning || started == "" {
					t.Fatalf("own identity read = %q/%v; want running with a start", started, live)
				}
			}
			marker := f.writeMarker(t, "worker", compactPhaseTestMarker(t, "tok", "p1", f.briefPath, c.pid, started))
			before, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for k, v := range f.env {
				env[k] = v
			}
			env["SOHO_NOWRITE"] = "1"
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, errOut bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &errOut
			code := Run([]string{"status", "worker"}, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != c.code {
				t.Fatalf("code=%d out=%q err=%q; want %d", code, out.String(), errOut.String(), c.code)
			}
			if !strings.HasPrefix(out.String(), c.line) {
				t.Fatalf("status line:\n got:  %q\nwant prefix: %q (the stale done report must not show)", out.String(), c.line)
			}
			after, err := os.ReadFile(marker)
			if err != nil || string(after) != string(before) {
				t.Fatalf("the marker changed under NOWRITE: %q err=%v", after, err)
			}
		})
	}
}

// compactRemoveWithRetry removes dir, retrying while a process that is still
// exiting holds one of its files (Windows sharing violations), for at most
// limit. A path that cannot be removed in time is left for t.TempDir to report.
func compactRemoveWithRetry(dir string, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for {
		if err := os.RemoveAll(dir); err == nil || time.Now().After(deadline) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
