package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// A Claude Code target writes every prompt it takes into its local session
// transcript as a "type":"user" line. These tests prove the dispatch arrival
// proof that reads only the count of those lines holding the composed prompt
// path (the same window as --amend and the normal dispatch): a rise within
// the check window is arrival proof when the screen cannot show the prompt;
// without the transcript (or for a target whose session is not a local id)
// the screen-based path stands.

const (
	claudeTranscriptSession   = "a1b2c3d4-0000-1111-2222-334455667788"
	claudeTranscriptRemoteRef = "b7e6f5a4-8888-4444-9999-000011112222"
	claudeTranscriptCwd       = "/tmp/work"
	claudeTranscriptStamp     = "20260929T000000"
)

type claudeDispatchFixture struct {
	*dispatchArrivalFixture
	composed   string
	report     string
	transcript string
}

// pinDispatchClock pins the clock at 2026-09-29 00:00:00 UTC (so the
// composed prompt is worker-20260929T000000.md) but still advances with the
// real clock, so the check window's deadline passes in real time. The clock
// starts to advance at its first reading, not when the test pins it: the
// stamp has a resolution of one second, and a runner that takes a second to
// build the fixture (it installs and opens fake processes) would otherwise
// move the stamp before dispatch composes the prompt, so the transcript
// line the test appends would carry another path than the one dispatch
// counts. Nothing before the stamp in dispatch calls a fake process.
func pinDispatchClock(t *testing.T) {
	t.Helper()
	base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	var once sync.Once
	var began time.Time
	oldNow := platform.Now
	platform.Now = func() time.Time {
		once.Do(func() { began = time.Now() })
		return base.Add(time.Since(began))
	}
	t.Cleanup(func() { platform.Now = oldNow })
}

func claudeAgentGet(mode string, seq int, sessionKind, sessionValue string) string {
	body := `{"result":{"agent":{"agent_status":"` + mode + `","state_change_seq":` + strconv.Itoa(seq)
	if sessionValue != "" {
		body += `,"cwd":"` + claudeTranscriptCwd + `","agent_session":{"source":"herdr","agent":"claude","kind":"` + sessionKind + `","value":"` + sessionValue + `"}}`
	}
	return body + `}}`
}

// newClaudeDispatchFixture builds the arrival fixture with the roster kind
// claude, the check window at 2 s and the poll at 100 ms. The Call 2
// `agent get` rule is the ClaudeTranscriptPath resolution; when sessionValue
// is set it carries the agent_session and cwd, the transcript file exists
// under a CLAUDE_CONFIG_DIR layout, and the fixture returns its path.
func newClaudeDispatchFixture(t *testing.T, mode string, seq int, sessionKind, sessionValue, settle string) *claudeDispatchFixture {
	t.Helper()
	extra := []fakecli.Rule{}
	if sessionValue != "" {
		extra = append(extra, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: claudeAgentGet(mode, seq, sessionKind, sessionValue)})
	}
	base := newDispatchArrivalFixture(t, mode, seq, seq, "old work output without a prompt marker\n", settle, extra...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\tw0test:p0a\tclaude\timplementer\tanthropic\t0\t\tnow\tclaude-sonnet\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(base.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	base.env["CLAUDE_CONFIG_DIR"] = t.TempDir()
	base.env["SOHO_PROMPT_CHECK_SECONDS"] = "2"
	base.env["SOHO_WAIT_POLL_MS"] = "100"
	f := &claudeDispatchFixture{
		dispatchArrivalFixture: base,
		composed:               filepath.Join(base.state, "ws", "briefs", "worker-"+claudeTranscriptStamp+".md"),
		report:                 filepath.Join(base.state, "ws", "reports", "worker-"+claudeTranscriptStamp+".md"),
	}
	if sessionValue != "" {
		f.transcript = filepath.Join(base.env["CLAUDE_CONFIG_DIR"], "projects", herdr.ClaudeProjectDir(claudeTranscriptCwd), sessionValue+".jsonl")
		if err := os.MkdirAll(filepath.Dir(f.transcript), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.transcript, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// seedTranscript writes the transcript's pre-send content (it must exist and
// be readable before dispatch resolves the transcript path).
func (f *claudeDispatchFixture) seedTranscript(t *testing.T, lines ...string) {
	t.Helper()
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(f.transcript, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeTranscriptLine writes a transcript line the way a Claude Code session
// transcript does: the whole line is JSON, so the text inside its string is
// escaped the way JSON writes it (a Windows path separator doubled, a quote
// escaped). The product counts the path in that escaped form
// (transcriptPathMarker, internal/cli/dispatch.go); a line written with
// the raw text never carries the marker on Windows, which is how the rc.11
// gowin51 round failed.
func claudeTranscriptLine(role, text string) string {
	body, err := json.Marshal(text)
	if err != nil {
		// The text is the plain dispatch sentence; Marshal cannot fail.
		panic(err)
	}
	return `{"type":"` + role + `","message":{"role":"` + role + `","content":[{"type":"text","text":` + string(body) + `}]}}`
}

// claudeDispatchText is the prompt text dispatch sends (internal/cli/
// dispatch.go), for a normal dispatch or an amendment.
func claudeDispatchText(composed, report string, amend bool) string {
	if amend {
		return fmt.Sprintf("Read the file %s in full and execute it. It amends the brief you are working on. When finished, write your report to %s and reply with exactly that path and nothing else.", composed, report)
	}
	return fmt.Sprintf("Read the file %s in full and execute it. It contains your role, your brief, and your report contract. When finished, write your report to %s and reply with exactly that path and nothing else.", composed, report)
}

// appendTranscriptLine appends the line the way a Claude Code session
// records the prompt it receives: the goroutine polls the fake herdr's call
// log (the same idiom dispatch_r11_test.go uses for the report write) for
// the `agent prompt` call and appends once it appears. The product logs that
// call while submitting the prompt — after the pre-send count and before the
// check window opens (internal/cli/dispatch.go) — so the line deterministically
// lands inside the 2 s check window. The old one-shot 400 ms timer raced the
// pre-send count: on a loaded host the dispatch setup (several fake herdr
// calls) took longer than 400 ms, the line landed before the count, the
// count never rose and the rise-proof path never ran. If the deadline passes
// without the prompt call, the goroutine gives up silently: the subtest's
// own code/status assertions fail on the missing rise.
func appendTranscriptLine(t *testing.T, f *claudeDispatchFixture, line string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
					file, err := os.OpenFile(f.transcript, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						t.Errorf("transcript append open: %v", err)
						return
					}
					defer file.Close()
					if _, err := file.WriteString(line + "\n"); err != nil {
						t.Errorf("transcript append: %v", err)
					}
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

// requireNoEnter asserts that dispatch sent no key to the target.
func requireNoEnter(t *testing.T, bin string) {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
			t.Fatalf("a key was sent to the target: %#v", call.Argv)
		}
	}
}

// withEarlierReport makes --amend valid for the fixture's worker.
func withEarlierReport(t *testing.T, f *claudeDispatchFixture) {
	t.Helper()
	previous := filepath.Join(f.state, "ws", "reports", "previous.md")
	if err := os.WriteFile(previous, []byte("old report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte(previous+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchClaudeTranscriptArrival(t *testing.T) {
	previousPrompt := "Read the file /var/tmp/old/worker-19990101T000000.md in full and execute it."

	t.Run("amend to a working claude: the transcript rise proves arrival, exit 0, no Enter, no resend", func(t *testing.T) {
		pinDispatchClock(t)
		f := newClaudeDispatchFixture(t, "working", 5, "id", claudeTranscriptSession, "0")
		f.seedTranscript(t, claudeTranscriptLine("user", previousPrompt))
		withEarlierReport(t, f)
		appendTranscriptLine(t, f, claudeTranscriptLine("user", claudeDispatchText(f.composed, f.report, true)))
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
		sidecar := mustRead(t, filepath.Join(f.state, "ws", "briefs", "worker-"+claudeTranscriptStamp+".dispatch.json"))
		if !strings.Contains(sidecar, `"arrival":"queued"`) {
			t.Fatalf("sidecar did not record the queued arrival: %s", sidecar)
		}
	})
	t.Run("the same amend without the transcript line: today's exit 15, no Enter", func(t *testing.T) {
		pinDispatchClock(t)
		f := newClaudeDispatchFixture(t, "working", 5, "id", claudeTranscriptSession, "0")
		f.seedTranscript(t, claudeTranscriptLine("user", previousPrompt))
		withEarlierReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); err != nil {
			t.Fatalf("not-received marker missing: %v", err)
		}
	})
	t.Run("an assistant line holding the path is not proof", func(t *testing.T) {
		pinDispatchClock(t)
		f := newClaudeDispatchFixture(t, "working", 5, "id", claudeTranscriptSession, "0")
		f.seedTranscript(t, claudeTranscriptLine("user", previousPrompt))
		withEarlierReport(t, f)
		appendTranscriptLine(t, f, claudeTranscriptLine("assistant", claudeDispatchText(f.composed, f.report, true)))
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a normal dispatch to an idle claude: the transcript rise proves arrival", func(t *testing.T) {
		pinDispatchClock(t)
		f := newClaudeDispatchFixture(t, "idle", 1, "id", claudeTranscriptSession, "0")
		f.seedTranscript(t, claudeTranscriptLine("user", previousPrompt))
		appendTranscriptLine(t, f, claudeTranscriptLine("user", claudeDispatchText(f.composed, f.report, false)))
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
	t.Run("a remote target (a session that is not a local id) keeps today's path", func(t *testing.T) {
		pinDispatchClock(t)
		// The agent_session is a reference to a session that does not run
		// locally: the local transcript must not resolve and must not prove
		// the target, even though a local file with that name gains a line
		// holding the path inside the window.
		f := newClaudeDispatchFixture(t, "working", 5, "ref", claudeTranscriptRemoteRef, "0")
		f.seedTranscript(t, claudeTranscriptLine("user", previousPrompt))
		withEarlierReport(t, f)
		appendTranscriptLine(t, f, claudeTranscriptLine("user", claudeDispatchText(f.composed, f.report, true)))
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a target without any agent_session keeps today's path", func(t *testing.T) {
		pinDispatchClock(t)
		f := newClaudeDispatchFixture(t, "working", 5, "", "", "0")
		withEarlierReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
	t.Run("a user line holding the path already in the transcript before the send is not a rise", func(t *testing.T) {
		pinDispatchClock(t)
		f := newClaudeDispatchFixture(t, "working", 5, "id", claudeTranscriptSession, "0")
		f.seedTranscript(t, claudeTranscriptLine("user", claudeDispatchText(f.composed, f.report, true)))
		withEarlierReport(t, f)
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		requireNoEnter(t, f.bin)
	})
}

// TestClaudeTranscriptLineCarriesTheWindowsMarker: the rc.11 Windows round
// (gowin51) failed TestDispatchClaudeTranscriptArrival because the fixture
// line wrote the composed path as raw text while the product counts the path
// the way JSON writes it (transcriptPathMarker): a Windows separator
// doubled, a quote escaped. The line the fixture appends must carry the
// marker the product counts, for a Windows-shaped composed path as well as a
// Unix one.
func TestClaudeTranscriptLineCarriesTheWindowsMarker(t *testing.T) {
	windowsComposed := `C:\Users\dj4lm\AppData\Local\Temp\hs\001\state\ws\briefs\worker-20260929T000000.md`
	unixComposed := "/tmp/hs/001/state/ws/briefs/worker-20260929T000000.md"
	report := "/tmp/hs/001/state/ws/reports/worker-20260929T000000.md"
	cases := []struct{ name, line, marker string }{
		{"amend text to a windows composed path", claudeTranscriptLine("user", claudeDispatchText(windowsComposed, report, true)), transcriptPathMarker(windowsComposed)},
		{"normal text to a windows composed path", claudeTranscriptLine("user", claudeDispatchText(windowsComposed, report, false)), transcriptPathMarker(windowsComposed)},
		{"amend text to a unix composed path", claudeTranscriptLine("user", claudeDispatchText(unixComposed, report, true)), transcriptPathMarker(unixComposed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.line, tc.marker) {
				t.Fatalf("the fixture line does not carry the marker the product counts:\nline:   %s\nmarker: %s", tc.line, tc.marker)
			}
		})
	}
}

// TestClaudeTranscriptPathMarker: the composed path lands in the transcript
// inside a JSON string; the marker must be the path escaped the way JSON
// writes it (a Windows separator doubled, a quote escaped).
func TestClaudeTranscriptPathMarker(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/var/tmp/herdr-soho/w14/reports/build-2-20261001T164144.brief.md", "/var/tmp/herdr-soho/w14/reports/build-2-20261001T164144.brief.md"},
		{`C:\herdr-soho\w14\reports\build-2-20261001T164144.brief.md`, `C:\\herdr-soho\\w14\\reports\\build-2-20261001T164144.brief.md`},
		{`C:\Users\x\re"port.md`, `C:\\Users\\x\\re\"port.md`},
		{"plain.md", "plain.md"},
	}
	for _, tc := range cases {
		if got := transcriptPathMarker(tc.in); got != tc.want {
			t.Fatalf("transcriptPathMarker(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
