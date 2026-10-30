package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const transcriptTestUUID = "3f2b8c1a-9d4e-4c7a-b1f0-5e6d7c8b9a0f"

// claudeAgentGetJSON is the `herdr agent get` response the compact transcript
// tests use: the worker's cwd and its claude session id.
func claudeAgentGetJSON(state, cwd string) string {
	return `{"result":{"agent":{"agent_status":"` + state + `","state_change_seq":1,"cwd":"` + cwd + `","agent_session":{"agent":"claude","kind":"id","value":"` + transcriptTestUUID + `"}}}}`
}

// newCompactTranscriptFixture builds a compact fixture of the given kind
// whose agent get reports the given cwd and, when transcriptContent is
// non-empty, the transcript file under its own CLAUDE_CONFIG_DIR. The
// transcript path follows Claude Code's encoding of the cwd (herdr.ClaudeProjectDir).
func newCompactTranscriptFixture(t *testing.T, kind, cwd, transcriptContent string, rules []fakecli.Rule) (*compactFixture, string) {
	t.Helper()
	f := newCompactFixture(t, kind, rules)
	configRoot := filepath.Join(f.root, "claude")
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	transcript := filepath.Join(configRoot, "projects", herdr.ClaudeProjectDir(cwd), transcriptTestUUID+".jsonl")
	if transcriptContent != "" {
		if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(transcript, []byte(transcriptContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f, transcript
}

// waitForCall polls the fake herdr call log until a call with the exact argv
// is recorded (or the deadline runs out).
func waitForCall(t *testing.T, f *compactFixture, argv []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countArgv(f.calls(t), argv) >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %v call in the log before the deadline", argv)
}

// waitForCallCount polls the fake herdr call log until the number of calls
// with the exact argv reaches n (or the deadline runs out).
func waitForCallCount(t *testing.T, f *compactFixture, argv []string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countArgv(f.calls(t), argv) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only %d of the %d %v calls were logged before the deadline", countArgv(f.calls(t), argv), n, argv)
}

// appendTranscript appends one line to the transcript, the way Claude Code
// appends its compact_boundary line when the compaction finishes.
func appendTranscript(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactClaudeTranscriptBoundaryProvesCompaction covers the real claude
// screen: after the /compact it keeps showing the older conversation, with no
// echo of the command and no Compacted line; the session transcript gains a
// compact_boundary line instead. The count past the pre-send baseline proves
// the compaction (the screen never does), then the idle wait and the compacted
// JSON run as today.
func TestCompactClaudeTranscriptBoundaryProvesCompaction(t *testing.T) {
	cwd := "/tmp/dot_work_1"
	oldScreen := "older conversation line one\nolder conversation line two\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: claudeAgentGetJSON("idle", cwd)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		// The real screen never gains a proof line below the sent command.
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: oldScreen},
	}
	f, transcript := newCompactTranscriptFixture(t, "claude", cwd,
		`{"type":"user","content":"SENTINEL-OLD-CONVERSATION"}
{"type":"assistant","content":"SENTINEL-OLDER-REPLY"}
`, rules)
	var code int
	var out, errText string
	done := make(chan struct{})
	go func() {
		code, out, errText = f.run(t, "compact", "worker", "--timeout", "30000")
		close(done)
	}()
	// The baseline count ran before the send: once the /compact reached the
	// pane, append the boundary line the way the session transcript records
	// the compaction.
	waitForCall(t, f, []string{"pane", "send-keys", "p1", "Enter"})
	appendTranscript(t, transcript, `{"type":"system","subtype":"compact_boundary","summary":"SENTINEL-BOUNDARY-SUMMARY"}
`)
	<-done
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	// No transcript content reaches stdout: only the result JSON does.
	for _, sentinel := range []string{"SENTINEL-OLD-CONVERSATION", "SENTINEL-OLDER-REPLY", "SENTINEL-BOUNDARY-SUMMARY"} {
		if strings.Contains(out, sentinel) {
			t.Fatalf("transcript content on stdout: %s", out)
		}
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	data, err := os.ReadFile(transcript)
	if err != nil || !strings.Contains(string(data), "SENTINEL-BOUNDARY-SUMMARY") {
		t.Fatalf("the boundary line was not recorded in the transcript: %v", err)
	}
}

// TestCompactClaudeTranscriptCountStableTimesOut verifies the negative side:
// the session was compacted before (the baseline already holds one boundary)
// and no new boundary appears after the send, so the wait runs out as a
// timeout, exactly as today.
func TestCompactClaudeTranscriptCountStableTimesOut(t *testing.T) {
	cwd := "/tmp/dot_work_1"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: claudeAgentGetJSON("idle", cwd)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "older conversation line\n"},
	}
	f, transcript := newCompactTranscriptFixture(t, "claude", cwd,
		`{"type":"system","subtype":"compact_boundary","summary":"earlier compaction"}
{"type":"user","content":"after the earlier compaction"}
`, rules)
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 9 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
	// The transcript is unchanged: no new boundary, nothing else read out.
	data, err := os.ReadFile(transcript)
	if err != nil || strings.Count(string(data), `"subtype":"compact_boundary"`) != 1 {
		t.Fatalf("transcript changed: %v %q", err, string(data))
	}
}

// TestCompactClaudeWithoutAgentSessionUsesScreenPath verifies that an agent
// get without agent_session leaves today's screen path as the evidence: the
// proof below the sent /compact still ends the wait, with no new warning.
func TestCompactClaudeWithoutAgentSessionUsesScreenPath(t *testing.T) {
	f := newCompactFixture(t, "claude", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.claudeStale},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeFresh},
	})
	f.env["CLAUDE_CONFIG_DIR"] = filepath.Join(f.root, "claude")
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	// The screen proof is what ended the wait: the pre-send read plus the
	// first poll that found it.
	if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
		t.Fatalf("recent reads=%d want 2: %#v", n, calls)
	}
}

// TestCompactCodexIgnoresTranscriptBoundary is the "claude only" side of the
// transcript rule: a codex worker whose session transcript gains a
// compact_boundary line is not compacted by that line (for codex only the
// screen proof is the evidence), so the wait runs out as a timeout.
func TestCompactCodexIgnoresTranscriptBoundary(t *testing.T) {
	cwd := "/tmp/dot_work_1"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: claudeAgentGetJSON("idle", cwd)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		// The screen never gains codex's proof (Context compacted).
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "older conversation line\n"},
	}
	f, transcript := newCompactTranscriptFixture(t, "codex", cwd,
		`{"type":"user","content":"old line"}
`, rules)
	var code int
	var out, errText string
	done := make(chan struct{})
	go func() {
		code, out, errText = f.run(t, "compact", "worker", "--timeout", "500")
		close(done)
	}()
	waitForCall(t, f, []string{"pane", "send-keys", "p1", "Enter"})
	appendTranscript(t, transcript, `{"type":"system","subtype":"compact_boundary"}
`)
	<-done
	if code != 9 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "codex" || value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
}

// TestCompactClaudeTranscriptFileMissingUsesScreenPath verifies that an agent
// session that exists but whose transcript file is missing leaves today's
// screen path as the evidence, with no new warning.
func TestCompactClaudeTranscriptFileMissingUsesScreenPath(t *testing.T) {
	cwd := "/tmp/dot_work_1"
	f, _ := newCompactTranscriptFixture(t, "claude", cwd, "", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: claudeAgentGetJSON("idle", cwd)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.claudeStale},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeFresh},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
		t.Fatalf("recent reads=%d want 2: %#v", n, calls)
	}
}

// TestCompactClaudeTranscriptLatchSurvivesLostTranscript covers the transcript
// side of the proofObserved latch: the pre-send baseline count holds no
// compact_boundary, the session transcript gains one while the wait runs
// (the first positive evidence of this attempt, from the transcript, the
// screen never doing), the first idle wait runs out the deadline with the
// worker still working, and the transcript file is lost before the last
// read — a transient loss that a re-evaluation of the last read would read
// as no evidence. The latch keeps the attempt: the capped idle wait after
// the deadline still runs, and the worker back at idle ends it as compacted
// while a worker never idle ends it as a timeout; /compact is never resent.
// The boundary append and the file removal are released by the fake CLI's
// own WaitFile blocks on the call log, with no blind sleep.
func TestCompactClaudeTranscriptLatchSurvivesLostTranscript(t *testing.T) {
	// The fake herdr subprocess inherits the test environment (CleanEnv only
	// strips HERDR_* and TMPDIR), so the WaitFile blocks die with a failed
	// call, not a hang, if the choreography deadlocks.
	t.Setenv("FAKECLI_WAIT_FILE_TIMEOUT_MS", "30000")
	cwd := "/tmp/dot_work_1"
	const noProof = "older conversation line\n"
	const boundary = `{"type":"system","subtype":"compact_boundary","summary":"SENTINEL-BOUNDARY"}
`
	runCase := func(t *testing.T, wantCode int, idleAfterDeadline bool) {
		// The step outlives the capped idle wait (ten 2 s polls, compact.go),
		// so that wait ends on the first clock tick, without spending a real
		// poll pause; the generous real budget (DJA-556) cannot expire
		// mid-choreography, a slow fake Herdr included (DJA-557).
		fakeFastClock(t, 30*time.Second)
		state := "working"
		if idleAfterDeadline {
			state = "idle"
		}
		markers := t.TempDir()
		releaseRead := filepath.Join(markers, "release-read")
		releaseDelete := filepath.Join(markers, "release-delete")
		rules := []fakecli.Rule{
			// 1: the pre-send state check. 2: the pre-send transcript lookup
			// (the baseline count, before the send; the loop's transcript
			// check reuses this path and only reads the file).
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: claudeAgentGetJSON("idle", cwd)},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: claudeAgentGetJSON("idle", cwd)},
			// 3: the first idle wait, the worker still working. It blocks
			// until the test has removed the transcript file (the transient
			// loss), so the removal is before any re-evaluation.
			{Argv: []string{"agent", "get", "worker"}, Call: 3, WaitFile: releaseDelete, Stdout: claudeAgentGetJSON("working", cwd)},
			// 4 on: the capped idle wait after the deadline (the worker back
			// at idle when it returns, working when it never does) and the
			// timeout's transcript-state lookup.
			{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: claudeAgentGetJSON(state, cwd)},
			// 1: the pre-send read, no proof. 2: the first poll, the same
			// screen (the screen never gains a proof line); it blocks until
			// the test has appended the boundary line. 3: the last read after
			// the deadline, the same screen.
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: noProof},
			{Argv: compactReadArgv("worker"), Call: 2, WaitFile: releaseRead, Stdout: noProof},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: noProof},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		}
		f, transcript := newCompactTranscriptFixture(t, "claude", cwd, `{"type":"user","content":"before the compaction"}
`, rules)
		var code int
		var out, errText string
		done := make(chan struct{})
		go func() {
			code, out, errText = f.run(t, "compact", "worker", "--timeout", "5000")
			close(done)
		}()
		// The baseline count ran before the send: once the Enter reached the
		// pane, append the boundary (the first positive evidence) and release
		// the first poll.
		waitForCall(t, f, []string{"pane", "send-keys", "p1", "Enter"})
		appendTranscript(t, transcript, boundary)
		if err := os.WriteFile(releaseRead, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		// The first idle wait is now polling (the third agent get): remove
		// the transcript file (the transient loss) and release that call.
		waitForCallCount(t, f, []string{"agent", "get", "worker"}, 3)
		if err := os.Remove(transcript); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(releaseDelete, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		<-done
		if code != wantCode || errText != "" {
			t.Fatalf("code=%d out=%s stderr=%s want %d with no stderr", code, out, errText, wantCode)
		}
		value := compactJSON(t, out)
		if value["agent"] != "worker" || value["kind"] != "claude" {
			t.Fatalf("json=%v", value)
		}
		switch wantCode {
		case 0:
			if value["status"] != "compacted" {
				t.Fatalf("json=%v want compacted (the transcript latch survives the lost file; the capped idle wait still ran)", value)
			}
		case 9:
			if value["status"] != "timeout" || value["transcript"] != "missing" {
				t.Fatalf("json=%v want timeout with transcript missing (the latched wait ran to the capped deadline and the transcript was lost)", value)
			}
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
			t.Fatalf("send-text calls=%d want 1 (the latch never resends /compact): %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
			t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
		}
		if data, err := os.ReadFile(transcript); err == nil {
			t.Fatalf("the transcript file was supposed to be lost: %q", data)
		}
	}
	t.Run("the worker back at idle ends the latched attempt as compacted", func(t *testing.T) {
		runCase(t, 0, true)
	})
	t.Run("the worker never idle ends the latched attempt as a timeout without a resend", func(t *testing.T) {
		runCase(t, 9, false)
	})
}
