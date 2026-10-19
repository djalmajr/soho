package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// A pi target writes every prompt it takes into its local session file as a
// "type":"message" line whose role is "user". These tests prove the dispatch
// arrival proof that reads only the count of those lines holding the composed
// prompt path (the same window as --amend and the normal dispatch): a rise
// within the check window is arrival proof when the screen cannot show the
// prompt; without the session file (no agent_session, a kind that is not a
// local path, a missing file) the screen-based path stands.

const (
	piTranscriptSessionName = "2026-10-02T06-18-06-537Z_01a0fb43-4b49-70d7-8f62-5e7a10390e69.jsonl"
	piTranscriptStamp       = "20260929T000000"
)

type piDispatchFixture struct {
	*dispatchArrivalFixture
	composed string
	report   string
	session  string
}

// piAgentGet is an agent get response carrying the agent_session the way
// `herdr agent get` reports it for a pi agent.
func piAgentGet(mode string, seq int, sessionKind, sessionValue string) string {
	body := `{"result":{"agent":{"agent":"pi","agent_status":"` + mode + `","state_change_seq":` + strconv.Itoa(seq) + `,"cwd":"/tmp/pi-work"`
	if sessionValue != "" {
		body += `,"agent_session":{"source":"herdr:pi","agent":"pi","kind":"` + sessionKind + `","value":` + strconv.Quote(sessionValue) + `}`
	}
	return body + `}}}`
}

// newPiDispatchFixture builds the arrival fixture with the roster kind pi,
// the check window at 2 s and the poll at 100 ms. The Call 2 `agent get`
// rule is the PiSessionPath resolution; when sessionValue is set it carries
// the agent_session and the session file exists at the path.
func newPiDispatchFixture(t *testing.T, mode string, seq int, sessionKind, sessionValue, settle string) *piDispatchFixture {
	t.Helper()
	extra := []fakecli.Rule{}
	if sessionValue != "" {
		extra = append(extra, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: piAgentGet(mode, seq, sessionKind, sessionValue)})
	}
	base := newDispatchArrivalFixture(t, mode, seq, seq, "old work output without a prompt marker\n", settle, extra...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\tw0test:p0a\tpi\timplementer\talibaba\t0\t\tnow\tqwen3\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(base.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	base.env["SOHO_PROMPT_CHECK_SECONDS"] = "2"
	base.env["SOHO_WAIT_POLL_MS"] = "100"
	f := &piDispatchFixture{
		dispatchArrivalFixture: base,
		composed:               filepath.Join(base.state, "ws", "briefs", "worker-"+piTranscriptStamp+".md"),
		report:                 filepath.Join(base.state, "ws", "reports", "worker-"+piTranscriptStamp+".md"),
	}
	if sessionKind == "path" && sessionValue != "" {
		f.session = sessionValue
	}
	return f
}

// seedSession writes the session file's pre-send content (it must exist and
// be readable before dispatch resolves the session path).
func (f *piDispatchFixture) seedSession(t *testing.T, lines ...string) {
	t.Helper()
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(f.session), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.session, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// piTranscriptLine writes a session line the way pi writes it: the whole
// line is JSON, so the text inside its string is escaped the way
// JSON.stringify writes it (a Windows path separator doubled, a quote
// escaped). The product counts the path in that escaped form
// (transcriptPathMarker, internal/cli/dispatch.go); a line written with the
// raw text never carries the marker on Windows.
func piTranscriptLine(role, text string) string {
	body, err := json.Marshal(text)
	if err != nil {
		// The text is the plain dispatch sentence; Marshal cannot fail.
		panic(err)
	}
	return `{"type":"message","id":"9a234373","parentId":"7dfce086","message":{"role":"` + role + `","content":[{"type":"text","text":` + string(body) + `}]}}`
}

// appendSessionLine appends the line the way a pi session records the prompt
// it receives: the goroutine polls the fake herdr's call log for the `agent
// prompt` call and appends once it appears. The product logs that call while
// submitting the prompt — after the pre-send count and before the check
// window opens — so the line deterministically lands inside the 2 s check
// window. If the deadline passes without the prompt call, the goroutine
// gives up silently: the subtest's own code/status assertions fail on the
// missing rise.
func appendSessionLine(t *testing.T, f *piDispatchFixture, line string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
					file, err := os.OpenFile(f.session, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						t.Errorf("session append open: %v", err)
						return
					}
					defer file.Close()
					if _, err := file.WriteString(line + "\n"); err != nil {
						t.Errorf("session append: %v", err)
					}
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

// withEarlierPiReport makes --amend valid for the fixture's worker (the pi
// twin of withEarlierReport, which the claude fixture owns).
func withEarlierPiReport(t *testing.T, f *piDispatchFixture) {
	t.Helper()
	previous := filepath.Join(f.state, "ws", "reports", "previous.md")
	if err := os.WriteFile(previous, []byte("old report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte(previous+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchPiTranscriptArrival(t *testing.T) {
	previousPrompt := "Read the file /var/tmp/old/worker-19990101T000000.md in full and execute it."
	sessionPath := func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "agent", "sessions", "--tmp-pi-work--", piTranscriptSessionName)
	}

	t.Run("amend to a working pi: the transcript rise proves arrival, exit 0, no Enter, no resend", func(t *testing.T) {
		pinDispatchClock(t)
		f := newPiDispatchFixture(t, "working", 5, "path", sessionPath(t), "0")
		f.seedSession(t, piTranscriptLine("user", previousPrompt))
		withEarlierPiReport(t, f)
		appendSessionLine(t, f, piTranscriptLine("user", claudeDispatchText(f.composed, f.report, true)))
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" || !strings.Contains(errText, "prompt queued") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("the transcript rise was not accepted as arrival: %s", errText)
		}
		requireNoEnter(t, f.bin)
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 1 {
			t.Fatalf("prompt attempts=%d, want 1 (no resend)", got)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); !os.IsNotExist(err) {
			t.Fatalf("a not-received marker was written: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.queued")); err != nil {
			t.Fatalf("queued marker missing: %v", err)
		}
		sidecar := mustRead(t, filepath.Join(f.state, "ws", "briefs", "worker-"+piTranscriptStamp+".dispatch.json"))
		if !strings.Contains(sidecar, `"arrival":"queued"`) {
			t.Fatalf("sidecar did not record the queued arrival: %s", sidecar)
		}
	})
	t.Run("a normal dispatch to an idle pi: the transcript rise proves arrival", func(t *testing.T) {
		pinDispatchClock(t)
		f := newPiDispatchFixture(t, "idle", 1, "path", sessionPath(t), "0")
		f.seedSession(t, piTranscriptLine("user", previousPrompt))
		appendSessionLine(t, f, piTranscriptLine("user", claudeDispatchText(f.composed, f.report, false)))
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 1 {
			t.Fatalf("prompt attempts=%d, want 1 (no resend)", got)
		}
	})
	t.Run("an assistant line holding the path is not proof", func(t *testing.T) {
		pinDispatchClock(t)
		f := newPiDispatchFixture(t, "working", 5, "path", sessionPath(t), "0")
		f.seedSession(t, piTranscriptLine("user", previousPrompt))
		withEarlierPiReport(t, f)
		appendSessionLine(t, f, piTranscriptLine("assistant", claudeDispatchText(f.composed, f.report, true)))
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a user line holding the path already in the session before the send is not a rise", func(t *testing.T) {
		pinDispatchClock(t)
		f := newPiDispatchFixture(t, "working", 5, "path", sessionPath(t), "0")
		f.seedSession(t, piTranscriptLine("user", claudeDispatchText(f.composed, f.report, true)))
		withEarlierPiReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a pi target without any agent_session keeps today's path", func(t *testing.T) {
		pinDispatchClock(t)
		f := newPiDispatchFixture(t, "working", 5, "", "", "0")
		withEarlierPiReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a pi target whose session kind is not path keeps today's path", func(t *testing.T) {
		pinDispatchClock(t)
		f := newPiDispatchFixture(t, "working", 5, "id", "3f2b8c1a-9d4e-4c7a-b1f0-5e6d7c8b9a0f", "0")
		withEarlierPiReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a pi target with a session path but a missing file keeps today's path", func(t *testing.T) {
		pinDispatchClock(t)
		// The session file is never created: the path resolves, the file is
		// missing, and the screen-based path stands.
		f := newPiDispatchFixture(t, "working", 5, "path", sessionPath(t), "0")
		withEarlierPiReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
}

// TestPiTranscriptLineCarriesTheEscapedMarker: the composed path lands in
// the session inside a JSON string; the marker must be the path escaped the
// way JSON writes it (a Windows separator doubled, a quote escaped), and a
// session line written the way JSON.stringify writes it must carry it.
func TestPiTranscriptLineCarriesTheEscapedMarker(t *testing.T) {
	windowsComposed := `C:\Users\dj4lm\AppData\Local\Temp\hs\001\state\ws\briefs\worker-20260929T000000.md`
	quotedComposed := `C:\Users\x\re"port.md`
	unixComposed := "/tmp/hs/001/state/ws/briefs/worker-20260929T000000.md"
	report := "/tmp/hs/001/state/ws/reports/worker-20260929T000000.md"
	cases := []struct {
		name, composed string
		amend          bool
	}{
		{"amend text to a windows composed path", windowsComposed, true},
		{"normal text to a windows composed path", windowsComposed, false},
		{"text to a path that holds a backslash and a quote", quotedComposed, true},
		{"amend text to a unix composed path", unixComposed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := piTranscriptLine("user", claudeDispatchText(tc.composed, report, tc.amend))
			marker := transcriptPathMarker(tc.composed)
			if !strings.Contains(line, marker) {
				t.Fatalf("the fixture line does not carry the marker the product counts:\nline:   %s\nmarker: %s", line, marker)
			}
		})
	}
}
