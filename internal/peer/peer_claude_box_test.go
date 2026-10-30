package peer_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	_ "unsafe"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The rc.14 false negative (Windows, local claude): the send to a working
// claude exited 15 "stalled" while the message had been taken and claude was
// already working. Two causes, both fixed in the peer.go send flow:
//
//  1. For claude, idInInputBox and messageStillInScreen read the last 15
//     non-empty lines as the input box, so the taken message's `end of
//     message` line and its #id — already in the history — kept counting as
//     "in the box". Like pi, claude's box is now the region between its two
//     border lines ('─' only, within the last 12 non-empty lines).
//  2. The stalled path's check (stalledTaken) never consulted the session
//     transcript proof: it now does, before the screen rules, like
//     pollWindow — for a local claude or pi, the user lines holding the #id
//     growing is delivery, unless the visible queue line still holds the id.
//
// The stalled 15 of the unproved path now says the proof window ran.

//go:linkname claudeInputRegion github.com/djalmajr/soho/internal/peer.claudeInputRegion
func claudeInputRegion(visible string) ([]string, bool)

//go:linkname idInInputBox github.com/djalmajr/soho/internal/peer.idInInputBox
func idInInputBox(kind, visible, id string) bool

//go:linkname messageStillInScreen github.com/djalmajr/soho/internal/peer.messageStillInScreen
func messageStillInScreen(kind, visible, endLine, id string) bool

// rc14ClaudeScreen is the real rc.14 screen (the end), verbatim: the taken
// message's end line in the history, the empty box (a bare ❯) between the
// two '─' border lines, and the footer below.
const rc14ClaudeScreen = `  > Não precisa responder.
  [herdr-soho:peer] #debf6313 end of message
  ⎿  1 skill available
✻ Fluttering… (9s · ↓ 135 tokens)
  ⎿  Tip: Try the new fullscreen renderer — flicker-free output, mouse
     support, auto-copy on select · /tui fullscreen
──────────────────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────────────────
  [Opus 5.5] ██████░░░░ 67% [5h:8% 2h56m] [7d:54% 3d10h] [main*]
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents · 1 feed…
                                  ✔ Update installed · Restart to update
`

const claudeBoxSep = "──────────────────────────────────────────────────────────────────────"

// TestClaudeInputBoxRealScreen pins the box functions on the real rc.14
// screen: the taken message's marker sits in the history, above the top
// border, so it is neither still on screen nor in the box — and the same
// screen with the message text typed inside the box (between the borders,
// not sent yet) is.
func TestClaudeInputBoxRealScreen(t *testing.T) {
	const id = "debf6313"
	endLine := peer.PeerEndLine(id)
	t.Run("the real screen: the marker in the history is not still on screen and not in the box", func(t *testing.T) {
		if idInInputBox("claude", rc14ClaudeScreen, id) {
			t.Fatal("the end line and the #id in the history must not count as in the box")
		}
		if messageStillInScreen("claude", rc14ClaudeScreen, endLine, id) {
			t.Fatal("the taken message in the history must not count as still on screen")
		}
	})
	t.Run("the same screen with the message typed inside the box (between the borders, not sent) is in the box and on screen", func(t *testing.T) {
		prompt := peer.PeerHeader("local/w14:p1", "orchestrator", "claude", "-", id) +
			"\n\n> Não precisa responder.\n" + peer.PeerEndLine(id)
		boxed := strings.Replace(rc14ClaudeScreen, "❯\n", "❯ "+prompt+"\n", 1)
		if boxed == rc14ClaudeScreen {
			t.Fatal("the box was not replaced")
		}
		if !idInInputBox("claude", boxed, id) {
			t.Fatal("the id between the borders must count as in the box")
		}
		if !messageStillInScreen("claude", boxed, endLine, id) {
			t.Fatal("the message between the borders must count as still on screen")
		}
	})
	t.Run("without the borders the screen proof stays conservative and no Enter goes", func(t *testing.T) {
		noBorders := "history\n" + peer.PeerEndLine(id) + "\n"
		if !messageStillInScreen("claude", noBorders, endLine, id) {
			t.Fatal("without two borders the whole visible screen still counts")
		}
		if idInInputBox("claude", "tail #"+id+"\n", id) {
			t.Fatal("a screen without the recognized box presses no Enter")
		}
	})
}

// TestClaudeInputRegion pins the claude box region rule: the borders are
// lines of '─' (whitespace around allowed), searched only in the last 12
// non-empty lines, where they must be the last two.
func TestClaudeInputRegion(t *testing.T) {
	sep := claudeBoxSep
	t.Run("the lines between the last two border lines are the box", func(t *testing.T) {
		lines, ok := claudeInputRegion("history\n" + sep + "\n❯ boxed\n" + sep + "\nfooter\n")
		if !ok || len(lines) != 1 || lines[0] != "❯ boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("a border with surrounding spaces still counts", func(t *testing.T) {
		lines, ok := claudeInputRegion(sep + "  \n❯ boxed\n\t" + sep + "\n")
		if !ok || len(lines) != 1 || lines[0] != "❯ boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("the real rc.14 screen's box holds the bare ❯", func(t *testing.T) {
		lines, ok := claudeInputRegion(rc14ClaudeScreen)
		if !ok || len(lines) != 1 || lines[0] != "❯" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("fewer than two border lines: no region", func(t *testing.T) {
		if _, ok := claudeInputRegion("history\n" + sep + "\nboxed\nfooter\n"); ok {
			t.Fatal("one border line must not form a region")
		}
		if _, ok := claudeInputRegion("history\nboxed\nfooter\n"); ok {
			t.Fatal("no border line must not form a region")
		}
	})
	t.Run("a border above the 12 non-empty lines does not pair with the in-window one", func(t *testing.T) {
		// The old separator sits 14 non-empty lines above the in-window
		// border: a whole-screen pi rule would pair them, but the claude
		// window holds only one border, so there is no region.
		screen := sep + "\n" + strings.Repeat("chrome\n", 13) + sep + "\nboxed\n"
		if _, ok := claudeInputRegion(screen); ok {
			t.Fatal("a single in-window border must not form a region")
		}
	})
	t.Run("three border lines in the window: the last two bound the box", func(t *testing.T) {
		lines, ok := claudeInputRegion(sep + "\nhr in history\n" + sep + "\n❯ boxed\n" + sep + "\nfooter\n")
		if !ok || len(lines) != 1 || lines[0] != "❯ boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("a mixed line is not a border", func(t *testing.T) {
		lines, ok := claudeInputRegion(sep + "\n❯ \n─ x\n" + sep + "\n")
		if !ok || len(lines) != 2 || lines[1] != "─ x" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
		if _, ok := claudeInputRegion(sep + "x\n" + sep + "\n"); ok {
			t.Fatal("a trailing non-'─' rune must not count as a border")
		}
	})
	t.Run("adjacent borders without a composer line form no region", func(t *testing.T) {
		// An empty region holds no ❯, so the pair is not a box: the
		// composer guard rejects it and the last 15 lines keep deciding.
		if _, ok := claudeInputRegion(sep + "\n" + sep + "\nfooter\n"); ok {
			t.Fatal("a border pair with no composer line must not form a region")
		}
	})
	t.Run("CRLF line endings", func(t *testing.T) {
		lines, ok := claudeInputRegion("history\r\n" + sep + "\r\n❯ boxed\r\n" + sep + "\r\nfooter\r\n")
		if !ok || len(lines) != 1 || lines[0] != "❯ boxed" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("a '─' pair without the composer inside is not the box (P2: history separators)", func(t *testing.T) {
		// Two '─' lines in the history (a separator, a table) and the real
		// composer typed below them, with no real borders: the pair holds no
		// ❯ and a ❯ sits below its bottom line, so it is not the box.
		screen := "history table\n" + sep + "\nrow\n" + sep + "\n❯ typed message\n"
		if _, ok := claudeInputRegion(screen); ok {
			t.Fatal("a '─' pair without the composer inside must not form a region")
		}
	})
	t.Run("a separator in the history does not displace the real borders", func(t *testing.T) {
		// One '─' separator above the real box borders: the last two border
		// lines are still the box borders and the pair holds the empty ❯ box.
		screen := "history table\n" + sep + "\nold message\n" + sep + "\n❯\n" + sep + "\nfooter\n"
		lines, ok := claudeInputRegion(screen)
		if !ok || len(lines) != 1 || lines[0] != "❯" {
			t.Fatalf("the real borders still bound the box: region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("the typed message right after the ❯ keeps the pair as the box", func(t *testing.T) {
		// The message is still typed inside the box, right after the ❯: the
		// region holds it and the pair is still the box.
		screen := "history\n" + sep + "\n❯ typed message\nmore text\n" + sep + "\nfooter\n"
		lines, ok := claudeInputRegion(screen)
		if !ok || len(lines) != 2 || lines[0] != "❯ typed message" || lines[1] != "more text" {
			t.Fatalf("the typed message stays inside the box: region=%+v ok=%v", lines, ok)
		}
	})
}

// TestSendClaudeComposerBorders is the P2 guard end to end: the '─' pair
// counts as the claude box only while the composer sits in it — the first
// non-empty line of the region starts with ❯ and no line below the bottom
// border does. Otherwise the layout is not recognized: the screen proof
// stays conservative and no Enter goes, so a message typed below a history
// '─' pair keeps the 15 with the proof window unproved.
func TestSendClaudeComposerBorders(t *testing.T) {
	_, prompt := stalledID(t)
	sep := claudeBoxSep
	pre := sep + "\n❯\n" + sep + "\nfooter\n"
	// Each run consumes the fixture's four seeded random bytes for the
	// peer id, so reseed before every run: the id and the prompt stay
	// deterministic and equal to the rules'.
	reseed := func(t *testing.T) {
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
	}
	stalledRules := func(post, recent string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("claude", "done", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("claude", "done", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("claude", "done", "1")},
			// The claude transcript-path get: no agent_session, so the
			// transcript proof is not armed and the screen rules decide.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "done", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
			// The Enter, for the subtest whose real box holds the marker.
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
			// The stalled visible read and the proof window's repeated reads:
			// the same screen and a stable state until the window laps.
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: post},
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("claude", "done", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recent},
		}
	}
	t.Run("history separators with the composer below: no recognized box, no Enter, the send keeps the 15", func(t *testing.T) {
		// The review's history-separators scenario: two '─' lines in the
		// history (a table) and the real composer with the message typed,
		// below them and with no real borders. The pair is not a box, so
		// the layout is not recognized: no Enter goes, the screen proof
		// keeps the marker held, and the proof window laps without proof:
		// the 15 says the window ran.
		post := "history table\n" + sep + "\nrow\n" + sep + "\n❯ " + prompt + "\n"
		reseed(t)
		f := newFixture(t, stalledRules(post, prompt+"\n"))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || out != "" {
			t.Fatalf("the message typed below the history separators must not prove delivery: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, "no proof within the 1s window") {
			t.Fatalf("the 15 is the unproved window branch: %q", stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("the history '─' pair is not a recognized box: no Enter goes: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("a history separator with the real borders and the empty ❯: the taken message is out of the box", func(t *testing.T) {
		// One '─' separator in the history above the real box borders; the
		// message was taken and sits in the history, and the box holds a
		// bare ❯. The guard keeps the real pair, the marker is out of the
		// box, and the window proves the arrival over the screen: sent,
		// no key.
		post := "history table\n" + sep + "\n" + prompt + "\n" + sep + "\n❯\n" + sep + "\nfooter\n"
		reseed(t)
		f := newFixture(t, stalledRules(post, prompt+"\n"))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("the taken message out of the box must be sent: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("no Enter goes to a claude whose box holds a bare ❯: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("the real borders with the message typed right after the ❯: the message is in the box", func(t *testing.T) {
		// The message is still typed inside the real box, right after the
		// ❯: the pair is the box, the marker holds in it, and the send
		// keeps the 15 with one Enter — the screen proof never runs.
		post := "history table\n" + prompt + "\n" + sep + "\n❯ " + prompt + "\n" + sep + "\nfooter\n"
		reseed(t)
		f := newFixture(t, stalledRules(post, "old history without the marker\n"))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || out != "" {
			t.Fatalf("the message typed in the box must not prove delivery: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, "the last read showed it in its input box after one Enter") {
			t.Fatalf("the 15 is the Enter branch: %q", stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 1 {
			t.Fatalf("one Enter goes to the box that holds the marker: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
}

// TestSendClaudeStalledRealScreen is the rc.14 case end to end: the prompt
// reports agent_prompt_stalled and the visible screen is the real one — the
// taken message in the history, the empty box between the borders. The
// marker is out of the box, so no Enter goes, and the stalled check proves
// the arrival over the window: sent, exit 0, no key.
func TestSendClaudeStalledRealScreen(t *testing.T) {
	id, prompt := stalledID(t)
	// The frozen rc.14 capture holds the former generation's marker; the
	// stalled check proves the arrival over the current closing line, so
	// the capture's marker line is mapped to it.
	screen := strings.Replace(rc14ClaudeScreen, "[herdr-soho:peer] #debf6313 end of message", "[/soho:peer:"+id+"]", 1)
	pre := claudeBoxSep + "\n❯\n" + claudeBoxSep + "\n  [Opus 5.5] ██████░░░░ 67% [5h:8% 2h56m] [7d:54% 3d10h] [main*]\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("claude", "done", "1")},
		// The claude transcript-path get: this agent has no agent_session,
		// so the transcript proof is not armed and the screen rules decide.
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		// The stalled read: the marker sits in the history, above the top
		// border — the box holds a bare ❯, so no Enter.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: screen},
		// The stalled check: the screen changed since the pre-send read and
		// holds the marker out of the box and the queues.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: screen},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
		// Catch-alls for a second poll on a slow machine.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: screen},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
		t.Fatalf("the real screen must prove the stalled arrival: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no Enter goes to a claude whose box does not hold the id: %d", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

// TestSendClaudeStalledTranscript: the stalled check with the claude
// transcript growing and the screen holding no sign of the message — the
// transcript proof, before the screen rules, is sent with no key.
func TestSendClaudeStalledTranscript(t *testing.T) {
	id, prompt := stalledID(t)
	const cwd = "/tmp/claude-work"
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	userLine := transcriptUserLine(t, prompt)
	working := "✻ Fluttering… (9s · ↓ 135 tokens)\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("done", "1", cwd)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: working},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "recent history without the marker\n"},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1, Delay: 100},
	}
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
	done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, userLine)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
		t.Fatalf("the growing transcript must prove the stalled arrival: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !<-done {
		t.Fatal("the transcript line was not appended after the prompt call")
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("a transcript-proven stalled send presses no key: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

// TestSendPiStalledTranscript: the same for a local pi — the stalled check
// accepts the existing pi transcript proof.
func TestSendPiStalledTranscript(t *testing.T) {
	id, prompt := stalledID(t)
	sessionsRoot := t.TempDir()
	sessionPath := piTranscriptSessionPath(sessionsRoot)
	writePiSessionFile(t, sessionPath, piSessionLine("user", "earlier conversation")+"\n")
	userLine := piSessionLine("user", prompt)
	working := "working on the task…\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: piSessionAgentJSON("done", "1", sessionPath)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: working},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "recent history without the marker\n"},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1, Delay: 100},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
	done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), sessionPath, id, userLine)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
		t.Fatalf("the growing session file must prove the stalled arrival: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !<-done {
		t.Fatal("the session line was not appended after the prompt call")
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("a transcript-proven stalled send presses no key: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

// TestSendClaudeStalledQueueNotProof: the transcript proof keeps pollWindow's
// growth-not-proof rule — while the visible queue line still holds the id,
// the grown count is queued, not taken: the window laps without proof and
// the stalled 15 says the window ran.
func TestSendClaudeStalledQueueNotProof(t *testing.T) {
	id, prompt := stalledID(t)
	const cwd = "/tmp/claude-work"
	configRoot := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
	writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
	userLine := transcriptUserLine(t, prompt)
	// The message sits in the open queue above the box borders: the box
	// itself holds a bare ❯, so the stalled Enter branch is not taken.
	queued := "✻ Fluttering… (9s · ↓ 135 tokens)\n" + prompt + "\nPress up to edit queued messages\n" +
		claudeBoxSep + "\n❯\n" + claudeBoxSep + "\nfooter\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("done", "1", cwd)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: queued},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: prompt + "\n"},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1, Delay: 100},
	}
	f := newFixture(t, rules)
	f.env["CLAUDE_CONFIG_DIR"] = configRoot
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "500", "50"
	done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, id, userLine)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 15 || out != "" {
		t.Fatalf("a grown count with the queue line is not taken: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(stderr, "no proof within the 0.5s window") {
		t.Fatalf("the 15 says the proof window ran: %q", stderr)
	}
	if !<-done {
		t.Fatal("the transcript line was not appended after the prompt call")
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("the queue guard presses no key: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

// TestSendClaudeStalledNewMessage: the unproved stalled 15 says the proof
// window ran (in seconds), keeps Herdr's cause, and saves the last visible
// screen.
func TestSendClaudeStalledNewMessage(t *testing.T) {
	id, prompt := stalledID(t)
	pre := "chrome line\n❯ \n"
	post := "chrome line after\n❯ \n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		// The box detection and the window's checks: the screen holds no
		// marker and the state never moves, so the window laps unproved.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: post},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("claude", "done", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "old history without the marker\n"},
	}
	f := newFixture(t, rules)
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "500", "50"
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	screenPath := filepath.Join(f.dir, "state", "ws-test", "wait", "send-"+id+".screen")
	want := "soho: send: local/w0test:p0a did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 0.5s window; read its pane for #01020304 or a reply before sending again; screen saved to " + screenPath + "\n"
	if code != 15 || out != "" || stderr != want {
		t.Fatalf("the unproved stalled 15 says the window ran and cites the saved screen: code=%d out=%q stderr=%q want %q", code, out, stderr, want)
	}
	if saved, err := os.ReadFile(screenPath); err != nil || string(saved) != post {
		t.Fatalf("the 15 saves the last visible screen: %v %q", err, saved)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("no Enter without the marker in the box: %d", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

// TestClaudeAlternatePromptBelowHistoryPair covers the R-S83b P2: two '─'
// separators in the history hold an old "❯" row, and the current composer
// below them uses another prompt ("›") with the typed peer message. The pair
// passes the composer guard, but the typed marker below the bottom border
// must still count as typed (claudeBoxScope).
func TestClaudeAlternatePromptBelowHistoryPair(t *testing.T) {
	const id = "01020304"
	border := strings.Repeat("─", 40)
	screen := strings.Join([]string{
		"earlier reply",
		border,
		"❯ old row",
		border,
		"› " + peer.PeerOpenTag(id),
		"  > hello",
		"  " + peer.PeerEndLine(id),
		"  status footer",
	}, "\n") + "\n"
	if !idInInputBox("claude", screen, id) {
		t.Fatalf("a typed marker under a history '─' pair must stay in the box:\n%s", screen)
	}
	if !messageStillInScreen("claude", screen, peer.PeerEndLine(id), id) {
		t.Fatalf("messageStillInScreen must keep the typed message:\n%s", screen)
	}
	if idInInputBox("claude", rc14ClaudeScreen, "debf6313") {
		t.Fatalf("the real rc.14 screen must stay out of the box")
	}
}

// TestClaudeFooterBareIDIsNotTyped covers the R-S83c P3: a bare #<id> in the
// status footer below the box (a branch name) does not make a delivered
// message read as typed; only the full peer header counts below the box.
func TestClaudeFooterBareIDIsNotTyped(t *testing.T) {
	const id = "01020304"
	border := strings.Repeat("─", 40)
	screen := strings.Join([]string{
		"❯ " + peer.PeerOpenTag(id),
		"  > hello",
		"  " + peer.PeerEndLine(id),
		"✻ Working… (3s)",
		border,
		"❯",
		border,
		"  [Opus 5.5] [topic/#" + id + "]",
	}, "\n") + "\n"
	if idInInputBox("claude", screen, id) {
		t.Fatalf("a bare #id in the footer must not count as typed:\n%s", screen)
	}
}
