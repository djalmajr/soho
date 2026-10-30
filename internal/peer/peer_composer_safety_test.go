package peer_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The peer composer safety of the send's retry Enter: idInInputBox may
// license the key only for a recognized actual composer (the shared provider
// recognition for the kind) holding this exact message's header, and no key
// goes on a dialog UI (provider.DialogUI), an unrelated id, an unknown kind
// or layout, older history, a longer-id prefix collision, or a quoted
// payload. The send-flow consumers below drive the real `send` command
// through the fake herdr and count the send-keys and prompt calls: exactly
// one Enter at most, no resend of the text. The unit consumers at the end
// pin the same refusals on idInInputBox and the conservative
// messageStillInScreen directly.

// safetyID is the seeded peer id and the exact prompt the fake herdr must
// receive (stalledID from the stalled send tests).
func safetyID(t *testing.T) (id, prompt string) { return stalledID(t) }

// countEntersSafety counts `agent send-keys ... enter` calls on any machine.
func countEntersSafety(t *testing.T, f *fixture) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	enters := 0
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
			for _, a := range call.Argv {
				if a == "enter" {
					enters++
				}
			}
		}
	}
	return enters
}

// stalledSafetyRules is the stalled-send skeleton (agent_prompt_stalled) for
// a kind whose pre-send screen is pre and whose post-prompt visible reads
// (the stalled read and the proof window's polls) return post: the state
// stays stable (no sequence move), the recent read holds no receipt marker,
// and no transcript is armed (no agent_session).
func stalledSafetyRules(kind, pre, post string, prompt string) []fakecli.Rule {
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind(kind, "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind(kind, "idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind(kind, "idle", "1")},
	}
	if kind == "claude" {
		// The claude transcript-path get: no agent_session, so the
		// transcript proof is not armed and the screen rules decide.
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "idle", "1")})
	}
	rules = append(rules,
		fakecli.Rule{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		// The stalled read and the proof window's polls: the same screen,
		// catch-all so a second poll on a slow machine still matches.
		fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: post},
		fakecli.Rule{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind(kind, "idle", "1")},
		fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "old history without the marker\n"},
	)
	return rules
}

// assertNoKeysStalled15 runs the send and pins the refused-Enter outcome:
// exit 15 with the proof-window message (stalled), exactly one prompt (no
// resend), and zero Enter keys.
func assertNoKeysStalled15(t *testing.T, f *fixture) {
	t.Helper()
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 15 || out != "" {
		t.Fatalf("the refused Enter must keep the stalled 15: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(stderr, "did not confirm taking the message") || !strings.Contains(stderr, "no proof within the") {
		t.Fatalf("the 15 says the proof window ran without keys: %q", stderr)
	}
	if enters := countEntersSafety(t, f); enters != 0 {
		t.Fatalf("no key goes on a refused shape: %d enters", enters)
	}
	if prompts := countPromptCalls(t, f); prompts != 1 {
		t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
	}
}

func TestComposerSafetyNoKeysOnRefusedShapes(t *testing.T) {
	// Every subtest reseeds the id through safetyID before it builds its
	// rules, so the prompt body matches the rules'.
	pre := "old history\n"
	t.Run("unrelated: another message's header in the box, this id nowhere, gets no Enter", func(t *testing.T) {
		_, prompt := safetyID(t)
		otherID := "99999999"
		otherPrompt := peer.PeerHeader("local/-", "-", "-", "-", otherID) + "\n\n> other\n" + peer.PeerEndLine(otherID)
		// The recognized pi box holds another message's header; this
		// message's id is not in the box.
		post := piSendScreen("old turn output", otherPrompt)
		f := newFixture(t, stalledSafetyRules("pi", piSendScreen("old turn output", ""), post, prompt))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assertNoKeysStalled15(t, f)
	})
	t.Run("unknown layout: the full typed prompt without the kind's composer shape gets no Enter", func(t *testing.T) {
		_, prompt := safetyID(t)
		// No '─' separator lines at all: the pi layout is not recognized,
		// and the prompt in the last lines is not permission to press
		// Enter — the last-fifteen-lines fallback is gone.
		post := prompt + "\n"
		f := newFixture(t, stalledSafetyRules("pi", pre, post, prompt))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assertNoKeysStalled15(t, f)
	})
	t.Run("unknown kind: a grok screen with the typed prompt gets no Enter", func(t *testing.T) {
		_, prompt := safetyID(t)
		// An unknown kind has no composer shape to recognize: the typed
		// prompt on screen is not permission to press Enter.
		post := "work output\n" + prompt + "\n"
		f := newFixture(t, stalledSafetyRules("grok", pre, post, prompt))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assertNoKeysStalled15(t, f)
	})
	t.Run("dialog: the codex question UI in the composer screen gets no Enter (shared classifier)", func(t *testing.T) {
		id, prompt := safetyID(t)
		_ = prompt
		// The composer line holds this message's header and the screen
		// carries the codex question UI. The current startup/blocked
		// protections do not catch this at status idle; the shared
		// classifier (provider.DialogUI) does: no Enter.
		post := "› " + peer.PeerOpenTag(id) + "\n" +
			"Enter to submit answer:\n"
		f := newFixture(t, stalledSafetyRules("codex", pre, post, prompt))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assertNoKeysStalled15(t, f)
	})
	t.Run("dialog: the claude approval UI under the recognized box gets no Enter (shared classifier)", func(t *testing.T) {
		id, prompt := safetyID(t)
		_ = prompt
		sep := strings.Repeat("─", 66)
		// The recognized claude box holds this message's header, and the
		// screen carries the claude approval UI. Only the shared
		// classifier catches this shape (no startup trust pattern, status
		// not blocked): no Enter.
		post := sep + "\n❯ " + peer.PeerOpenTag(id) + "\n" + sep + "\nDo you want to continue with the refactor?\n"
		f := newFixture(t, stalledSafetyRules("claude", pre, post, prompt))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assertNoKeysStalled15(t, f)
	})
	t.Run("history: a delivered codex turn with the empty composer sends with no key", func(t *testing.T) {
		id, prompt := safetyID(t)
		// The delivered message sits in the history above a real empty
		// composer: no Enter goes, and the receipt (history plus the
		// recent marker) proves the arrival: sent, exit 0.
		post := "› " + peer.PeerOpenTag(id) + "\n" +
			"  > hello\n" +
			"  " + peer.PeerEndLine(id) + "\n" +
			"• Working (3s • esc to interrupt)\n" +
			"› Ask Codex to do anything\n" +
			"  GPT-6.1-Sol high · ~/repo\n" +
			"  ← for agents · ? for shortcuts   ⚠ 2 warnings · f2 to view\n"
		rules := stalledSafetyRules("codex", pre, post, prompt)
		// The recent read carries this message's receipt marker, so the
		// stalled check proves the arrival over the window.
		for i := range rules {
			if rules[i].Argv[1] == "read" && rules[i].Argv[4] == "recent-unwrapped" {
				rules[i].Stdout = prompt + "\n"
			}
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "500", "50"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("the delivered turn with the empty composer must be sent without keys: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEntersSafety(t, f); enters != 0 {
			t.Fatalf("no Enter goes to a delivered turn in the history: %d enters", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("longer id: a box holding a longer id with this one as a prefix gets no Enter", func(t *testing.T) {
		id, prompt := safetyID(t)
		_ = prompt
		// The box holds the header of the longer id 01020304a — a bare
		// "#01020304" contains-check would press Enter here; the hex
		// boundary says it is a longer id, not this message.
		longer := id + "a"
		post := piSendScreen("old turn output", peer.PeerHeader("local/-", "-", "-", "-", longer)+"\n\n> other\n"+peer.PeerEndLine(longer))
		f := newFixture(t, stalledSafetyRules("pi", piSendScreen("old turn output", ""), post, prompt))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		assertNoKeysStalled15(t, f)
	})
}

func TestComposerSafetyLegitimateRetry(t *testing.T) {
	// The recognized composer holding this exact message's header licenses
	// exactly one Enter, and the proof window then decides: proven is sent,
	// no resend of the text.
	t.Run("pi: the full prompt in the recognized box gets one Enter and the proof passes", func(t *testing.T) {
		_, prompt := safetyID(t)
		history := "old turn output"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
			// The stalled read: the message still sits in the box, between
			// the two last '─' lines.
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: piSendScreen(history, prompt)},
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
			// After the Enter the state goes working: the sequence change
			// proves the turn.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("pi", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: piSendScreen(history+"\n"+prompt, "")},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEntersSafety(t, f); enters != 1 {
			t.Fatalf("the recognized box holding the header needs exactly one Enter: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("claude: the full prompt in the recognized box gets one Enter and the proof passes", func(t *testing.T) {
		_, prompt := safetyID(t)
		sep := strings.Repeat("─", 66)
		foot := "~/code/proj (main)\n"
		box := func() string { return sep + "\n❯ " + prompt + "\n" + sep + "\n" + foot }
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: sep + "\n❯\n" + sep + "\n" + foot},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("claude", "idle", "1")},
			// The claude transcript-path get: no agent_session, so the
			// transcript proof is not armed and the screen rules decide.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("claude", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
			// The stalled read: the message still sits in the box.
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: box()},
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
			// After the Enter the state goes working: the sequence change
			// proves the turn.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSONKind("claude", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: "✻ Working…\n" + prompt + "\n" + sep + "\n❯\n" + sep + "\n" + foot},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEntersSafety(t, f); enters != 1 {
			t.Fatalf("the recognized box holding the header needs exactly one Enter: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("codex: the full prompt in the recognized composer gets one Enter and the proof passes", func(t *testing.T) {
		_, prompt := safetyID(t)
		status := "  GPT-6.1-Sol high · ~/repo\n  ← for agents · ? for shortcuts   ⚠ 2 warnings · f2 to view\n"
		// The stalled read screen keeps the prompt's opening tag inside the
		// composer's 8 non-empty line window: the 5-line part B header plus
		// the body and the closing line fit without the status footer, so
		// the composer is recognized and the Enter goes.
		composer := "› " + prompt + "\n"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("codex", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "history\n› Ask Codex to do anything\n" + status},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("codex", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("codex", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
			// The stalled read: the message still sits in the composer.
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: composer},
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
			// After the Enter the state goes working: the sequence change
			// proves the turn.
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSONKind("codex", "working", "2")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 3, Stdout: "history\n" + prompt + "\n• Working (3s)\n› Ask Codex to do anything\n" + status},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countEntersSafety(t, f); enters != 1 {
			t.Fatalf("the recognized composer holding the header needs exactly one Enter: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
}

func TestComposerSafetyHonorsSendKeyFailure(t *testing.T) {
	// The send-key transport reports whether the Enter actually landed
	// (herdr send-keys exited 0); a failed send is never claimed as pressed,
	// and no second Enter or resend follows.
	t.Run("stalled: the Enter does not send, the window fails, the 15 says so", func(t *testing.T) {
		_, prompt := safetyID(t)
		history := "old turn output"
		box := piSendScreen(history, prompt)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 2, Stdout: box},
			// The Enter is attempted and fails: herdr send-keys exits 1.
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 1},
			// The proof window: the state stays idle with the same sequence
			// and the message still sits in the box, so the window fails.
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "old history without the marker\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: box},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || out != "" {
			t.Fatalf("the unproved stalled send keeps the 15: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, "the Enter did not send") {
			t.Fatalf("the 15 must say the Enter did not send: %q", stderr)
		}
		if strings.Contains(stderr, "after one Enter") {
			t.Fatalf("a failed Enter must not be claimed as pressed: %q", stderr)
		}
		// One attempt (which failed), no second Enter, no resend.
		if enters := countEntersSafety(t, f); enters != 1 {
			t.Fatalf("exactly one Enter attempt, never a second: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the stalled path never resends the text: %d prompts", prompts)
		}
	})
	t.Run("main path: the Enter does not send and the window fails, the lost 15 says so", func(t *testing.T) {
		_, prompt := safetyID(t)
		history := "old turn output"
		box := piSendScreen(history, prompt)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(history, "")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSONKind("pi", "idle", "1")},
			// The prompt succeeds (no stalled error), but the proof window
			// cannot prove the arrival: the recent read holds no receipt
			// marker and the state never moves.
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
			// The window's reads and the pre-Enter reads: the message still
			// sits in the box.
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("pi", "idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "old history without the marker\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: box},
			// The Enter is attempted and fails: herdr send-keys exits 1.
			{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 1},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 15 || out != "" {
			t.Fatalf("the unproved send with the failed Enter keeps the lost 15: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, "the Enter did not send") {
			t.Fatalf("the lost 15 must say the Enter did not send: %q", stderr)
		}
		if enters := countEntersSafety(t, f); enters != 1 {
			t.Fatalf("exactly one Enter attempt, never a second: %d", enters)
		}
		if prompts := countPromptCalls(t, f); prompts != 1 {
			t.Fatalf("the path never resends the text: %d prompts", prompts)
		}
	})
}

func TestComposerSafetyUnitShapes(t *testing.T) {
	// Direct unit consumers on the linkname'd helpers: the refused shapes
	// and the conservative screen proof, without the send flow.
	const id = "01020304"
	endLine := peer.PeerEndLine(id)
	header := peer.PeerHeader("local/-", "-", "-", "-", id)
	piBox := func(box string) string {
		sep := strings.Repeat("─", 66)
		return "history\n" + sep + "\n" + box + "\n" + sep + "\nfooter\n"
	}
	t.Run("the recognized box holding the header licenses the Enter", func(t *testing.T) {
		if !idInInputBox("pi", piBox(header), id) {
			t.Fatalf("the pi box holding the header must license the Enter:\n%s", piBox(header))
		}
	})
	t.Run("a quoted header in the box is payload, not the typed header", func(t *testing.T) {
		if idInInputBox("pi", piBox("> "+header), id) {
			t.Fatalf("a quoted header in the box must not license the Enter:\n%s", piBox("> "+header))
		}
	})
	t.Run("a longer id with this one as a prefix does not license the Enter", func(t *testing.T) {
		longer := peer.PeerHeader("local/-", "-", "-", "-", id+"a") + "\n\n> other\n" + peer.PeerEndLine(id+"a")
		if idInInputBox("pi", piBox(longer), id) {
			t.Fatalf("a longer id's header must not license the Enter:\n%s", piBox(longer))
		}
	})
	t.Run("an unrelated id's header does not license the Enter", func(t *testing.T) {
		other := peer.PeerHeader("local/-", "-", "-", "-", "99999999") + "\n\n> other\n" + peer.PeerEndLine("99999999")
		if idInInputBox("pi", piBox(other), id) {
			t.Fatalf("another message's header must not license the Enter:\n%s", piBox(other))
		}
	})
	t.Run("an unrecognized layout and an unknown kind never license the Enter", func(t *testing.T) {
		plain := header + "\n"
		if idInInputBox("pi", plain, id) {
			t.Fatalf("an unrecognized pi layout must not license the Enter:\n%s", plain)
		}
		if idInInputBox("grok", plain, id) {
			t.Fatalf("an unknown kind must not license the Enter:\n%s", plain)
		}
	})
	t.Run("a known dialog UI never licenses the Enter", func(t *testing.T) {
		sep := strings.Repeat("─", 66)
		claude := sep + "\n❯ " + header + "\n" + sep + "\nDo you want to continue with the refactor?\n"
		if idInInputBox("claude", claude, id) {
			t.Fatalf("the claude approval UI must not license the Enter:\n%s", claude)
		}
		codex := "› " + header + "\nEnter to submit answer:\n"
		if idInInputBox("codex", codex, id) {
			t.Fatalf("the codex question UI must not license the Enter:\n%s", codex)
		}
	})
	t.Run("a delivered codex turn is history, not the box", func(t *testing.T) {
		screen := "› " + header + "\n  > hello\n  " + endLine + "\n• Working (3s • esc to interrupt)\n› Ask Codex to do anything\n"
		if idInInputBox("codex", screen, id) {
			t.Fatalf("a delivered codex turn must not read as the input box:\n%s", screen)
		}
		if messageStillInScreen("codex", screen, endLine, id) {
			t.Fatalf("a delivered codex turn must not count as still on screen:\n%s", screen)
		}
	})
	t.Run("messageStillInScreen stays conservative on the unknown layout", func(t *testing.T) {
		// Without the recognized composer the whole visible screen still
		// counts as held: conservative, never converted into a positive
		// receipt.
		if !messageStillInScreen("pi", header+"\n", endLine, id) {
			t.Fatal("without the recognized box the whole visible screen still counts")
		}
	})
}
