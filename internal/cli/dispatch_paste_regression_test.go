package cli

// Delayed paste, kind-aware input regions: the dispatch's prompt text can
// land in the target's composer after the first arrival check, just before
// the automatic text resend. The resend then must re-read the actual input
// region right before the retry and act only on a recognized composer
// (pi, claude, codex): this exact composed prompt path in the recognized
// composer gets one bounded Enter and never a duplicated text; the path in
// the history above an empty or unrelated composer, or on a screen whose
// input region is not recognized, keeps the delivery uncertain (no Enter
// and no resend); a known trust/approval/question UI or a blocked target
// takes neither. The transport is the fake herdr installed by the shared
// busy fixture: a re-executed Go test binary, no scripts.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// pasteGetRules scripts the fake herdr's `agent get worker` for the
// idle-target arrival flow: the gets before the retry (the pre-send state,
// the first arrival window, the stale-auth probe and the recheck's fresh
// state) keep the given pre state, and from call 7 on the agent has taken
// the prompt (takenSeq != 0: working, that seq) or never does (takenSeq
// 0 keeps the pre state).
func pasteGetRules(pre string, seq, takenSeq int) []fakecli.Rule {
	stay := busyStateJSON(pre, seq)
	after := stay
	if takenSeq != 0 {
		after = busyStateJSON("working", takenSeq)
	}
	get := []string{"agent", "get", "worker"}
	rules := make([]fakecli.Rule, 0, 13)
	for call := 1; call <= 6; call++ {
		rules = append(rules, fakecli.Rule{Argv: get, Call: call, Stdout: stay})
	}
	for call := 7; call <= 12; call++ {
		rules = append(rules, fakecli.Rule{Argv: get, Call: call, Stdout: after})
	}
	rules = append(rules, fakecli.Rule{Argv: get, Stdout: after})
	return rules
}

// pasteRoster points the fixture's worker row at kind (the fixture's own
// row is codex): the region rules are kind-aware, so a claude or pi target
// needs its row to say so.
func pasteRoster(t *testing.T, f *busyDispatchFixture, kind string) {
	t.Helper()
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\t\t" + kind + "\timplementer\t" + kinds.KindFamily(kind) + "\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.stateDir, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
}

// arrivalRosterKind re-points the arrival fixture's worker row at kind
// (the fixture's own row is codex): the region rules are kind-aware, so a
// pi or claude target needs its row to say so.
func arrivalRosterKind(t *testing.T, f *dispatchArrivalFixture, kind string) {
	t.Helper()
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\tw0test:p0a\t" + kind + "\timplementer\t" + kinds.KindFamily(kind) + "\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pasteInstall installs the paste fixture rules: the reads before the
// retry (the pre-send H0 read and the post-window read) see a clean
// screen, and every later read — including the recheck's fresh read — sees
// screen (the text that landed in the composer in between).
func pasteInstall(t *testing.T, f *busyDispatchFixture, getRules []fakecli.Rule, screen string, extra ...fakecli.Rule) {
	t.Helper()
	visible := []string{"agent", "read", "worker", "--source", "visible"}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
	}
	rules = append(rules, getRules...)
	rules = append(rules,
		fakecli.Rule{Argv: visible, Call: 1, ArgvPrefix: true, Stdout: "clean screen\n"},
		fakecli.Rule{Argv: visible, Call: 2, ArgvPrefix: true, Stdout: "clean screen\n"},
		fakecli.Rule{Argv: visible, ArgvPrefix: true, Stdout: screen},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "clean screen\n"},
	)
	rules = append(rules, extra...)
	f.install(t, rules)
}

// pasteCodexComposerScreen is the visible screen of a codex target whose
// composer already holds the prompt for the given path: the '›' composer
// line (inside the last 8 non-empty lines) to the end of the screen is the
// recognized composer region.
func pasteCodexComposerScreen(path string) string {
	return "old output\n• Working (12s · esc to interrupt)\n\n› Read the file " + path + " in full and execute it.\n\n  GPT-6.1-Sol medium · Context 20% use…\n"
}

// pasteCodexWrappedComposerScreen is the same screen with the exact path
// wrapped over two composer lines (the split mid-name, so a line-by-line
// check misses it).
func pasteCodexWrappedComposerScreen(path string) string {
	return "old output\n• Working (12s · esc to interrupt)\n\n› Read the file " + path[:len(path)-13] + "\n" + path[len(path)-13:] + " in full and execute it.\n\n  GPT-6.1-Sol medium · Context 20% use…\n"
}

// pasteCodexHistoryScreen is the visible screen of a codex target whose
// history shows this prompt as taken (above the composer) while the
// composer is empty: the path is visible, but it is not in the composer.
func pasteCodexHistoryScreen(path string) string {
	return "old output\nRead the file " + path + " in full and execute it.\n• Working (35m 51s · esc to interrupt)\n\n› Ask Codex to do anything\n\n  GPT-6.1-Sol medium · Context 20% use…\n  ← for agents · ? for shortcuts          ⚠ 3 warnings · f2 to view\n"
}

// pasteCodexDraftScreen is the visible screen of a codex target whose
// composer holds an unrelated draft while the history shows this prompt.
func pasteCodexDraftScreen(path string) string {
	return "old output\nRead the file " + path + " in full and execute it.\n\n› some unrelated draft the user is typing\n\n  GPT-6.1-Sol medium · Context 20% use…\n"
}

// pasteClaudeScreen renders the given claude box lines between the two
// '─' border lines, with the given history above and footer below.
func pasteClaudeScreen(history, box, footer string) string {
	sep := "──────────────────────────────────────────────────────────────────────────"
	return history + sep + "\n" + box + "\n" + sep + "\n" + footer
}

// pastePiScreen renders the given pi box lines between the two '─' border
// lines, with the given history above and footer below.
func pastePiScreen(history, box, footer string) string {
	sep := "──────────────────────────────────────────────────────────────────"
	return history + sep + "\n" + box + "\n" + sep + "\n" + footer
}

// pasteSendKeys counts the send-keys calls and reports the index of the
// last one, so the test can bound the keys and check their order.
func pasteSendKeys(calls []fakecli.Call) (count, lastIndex int) {
	for i, call := range calls {
		if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "send-keys" {
			count++
			lastIndex = i
		}
	}
	return count, lastIndex
}

func pastePromptsAfter(calls []fakecli.Call, afterIndex int) int {
	count := 0
	for i, call := range calls {
		if i < afterIndex || len(call.Argv) < 2 || call.Argv[0] != "agent" || call.Argv[1] != "prompt" {
			continue
		}
		count++
	}
	return count
}

// TestDispatchDelayedComposerTextGetsBoundedEnterWithoutResend is the
// reproduction: the text lands in the codex composer after the first
// arrival check, just before the automatic text resend. The dispatch must
// re-read the recognized composer region, see the exact composed path
// there, send exactly one bounded Enter and never retype the text.
func TestDispatchDelayedComposerTextGetsBoundedEnterWithoutResend(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteInstall(t, f, pasteGetRules("idle", 1, 2), pasteCodexComposerScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(out, `"enter_sent":true`) || strings.Contains(out, `"resent":true`) {
		t.Fatalf("the Enter without a resend is not in the result: %s", out)
	}
	if !strings.Contains(errText, "reached its input after the arrival check; sent Enter without resending") {
		t.Fatalf("the delayed-paste warning is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1 (the text was never duplicated); calls=%#v", n, calls)
	}
	enters, enterAt := pasteSendKeys(calls)
	if enters != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one bounded Enter; calls=%#v", enters, calls)
	}
	if len(calls[enterAt].Argv) != 4 || strings.Join(calls[enterAt].Argv, " ") != "agent send-keys worker enter" {
		t.Fatalf("the Enter is not a plain enter key: %#v", calls[enterAt].Argv)
	}
	if pastePromptsAfter(calls, enterAt) != 0 {
		t.Fatalf("a prompt followed the Enter; the text was duplicated: %#v", calls)
	}
	log, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
	if err != nil || !strings.Contains(string(log), "reached its input after the arrival check") {
		t.Fatalf("friction.log missed the delayed-paste line: %s (err %v)", log, err)
	}
}

// TestDispatchDelayedComposerTextWrappedInTheComposer: the exact composed
// path is wrapped over two codex composer lines. The recheck must rejoin
// the region lines and send only the bounded Enter.
func TestDispatchDelayedComposerTextWrappedInTheComposer(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteInstall(t, f, pasteGetRules("idle", 1, 2), pasteCodexWrappedComposerScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "reached its input after the arrival check; sent Enter without resending") {
		t.Fatalf("the wrapped composer was not recognized: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one bounded Enter; calls=%#v", n, calls)
	}
}

func TestReviewR4PiFirstKeyGuard(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteRoster(t, f, "pi")
	path := f.composedPath()
	cut := strings.LastIndex(path, "-") + 1
	screen := pastePiScreen("old output\n", "› Read "+path[:cut]+"\n  "+path[cut:]+" in full.", "footer\n")
	pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), screen,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	calls := f.calls(t)
	keys, _ := pasteSendKeys(calls)
	prompts := countArgvPrefix(calls, []string{"agent", "prompt"})
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" || keys != 0 || prompts != 1 {
		t.Fatalf("literal Pi text must not authorize Enter: screen=%q code=%d enters=%d prompts=%d out=%s stderr=%s calls=%#v", screen, code, keys, prompts, out, errText, calls)
	}
}

func TestDispatchDelayedComposerIndentedWrappedPointer(t *testing.T) {
	f := newBusyDispatchFixture(t)
	path := f.composedPath()
	const width = 80
	remaining := "Read the file " + path + " in full and execute it."
	var lines []string
	for prefix := "› "; remaining != ""; prefix = "  " {
		n := width - 2
		if len(remaining) < n {
			n = len(remaining)
		}
		lines = append(lines, prefix+remaining[:n])
		remaining = remaining[n:]
	}
	warnings := "⚠ 2 warnings · f2 to view"
	screen := "• Earlier output …\n  └ command result\n\n  New activity · ↓ Back to bottom · esc\n\n" + strings.Join(lines, "\n") +
		"\n\n  GPT-6.1-Sol high · Context 20% · ← for agents\n" + strings.Repeat(" ", width-len([]rune(warnings))) + warnings + "\n"
	pasteInstall(t, f, pasteGetRules("idle", 1, 2), screen,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" || !strings.Contains(out, `"enter_sent":true`) || strings.Contains(out, `"resent":true`) {
		t.Fatalf("code=%d out=%s stderr=%s screen=%q", code, out, errText, screen)
	}
	if !strings.Contains(errText, "reached its input after the arrival check; sent Enter without resending") {
		t.Fatalf("post-deadline Enter branch not taken: %s", errText)
	}
	calls := f.calls(t)
	count, at := pasteSendKeys(calls)
	if count != 1 || strings.Join(calls[at].Argv, " ") != "agent send-keys worker enter" || countArgvPrefix(calls, []string{"agent", "prompt"}) != 1 || pastePromptsAfter(calls, at) != 0 {
		t.Fatalf("want initial prompt then exactly one plain Enter, no resend: %#v", calls)
	}
}

func TestDispatchDelayedComposerSoftWrapSpaces(t *testing.T) {
	for _, negative := range []bool{false, true} {
		t.Run(fmt.Sprint(negative), func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			path := f.composedPath()
			screen := "› Read " + path + " \n  in full and execute it."
			if negative {
				at := strings.LastIndex(path, "-") + 1
				screen = "› Read " + path[:at] + " \n  " + path[at:] + " in full and execute it."
			}
			screen += "\n\n" + strings.Repeat("-", len(path)+50) + "\n" + strings.Repeat("-", len(path)+50)
			pasteInstall(t, f, pasteGetRules("idle", 1, 2), screen,
				fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
				fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
			)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			wantCode, wantEnters, wantStatus := 0, 1, "submitted"
			if negative {
				wantCode, wantEnters, wantStatus = 15, 0, "not-received"
			}
			calls := f.calls(t)
			enters, at := pasteSendKeys(calls)
			if code != wantCode || dispatchOutputStatus(t, out) != wantStatus || enters != wantEnters || countArgvPrefix(calls, []string{"agent", "prompt"}) != 1 {
				t.Fatalf("code=%d out=%s stderr=%s calls=%#v screen=%q", code, out, errText, calls, screen)
			}
			if !negative && (strings.Join(calls[at].Argv, " ") != "agent send-keys worker enter" || pastePromptsAfter(calls, at) != 0 || !strings.Contains(errText, "reached its input after the arrival check; sent Enter without resending")) {
				t.Fatalf("missing Enter-only delayed branch: calls=%#v stderr=%s", calls, errText)
			}
		})
	}
}

// TestDispatchCurrentPathInCodexHistoryAboveEmptyComposer: this exact path
// is visible in the history above the empty codex composer. The region is
// recognized but does not hold the path, and the visible path keeps the
// delivery uncertain: no Enter and no resend.
func TestDispatchCurrentPathInCodexHistoryAboveEmptyComposer(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexHistoryScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the history echo must keep the delivery uncertain: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("a history echo got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("a history echo got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchCurrentPathInClaudeHistoryAboveEmptyComposer: the real
// rc.14 claude shape — the empty box (a bare ❯) between the two '─'
// borders — with this exact path in the history above the box. No Enter
// and no resend.
func TestDispatchCurrentPathInClaudeHistoryAboveEmptyComposer(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteRoster(t, f, "claude")
	path := f.composedPath()
	screen := pasteClaudeScreen("  > old line\nRead the file "+path+" in full and execute it.\n✻ Fluttering… (9s · ↓ 135 tokens)\n", "❯", "  [Opus 5.5] ██████░░░░ 67% [main*]\n  ⏵⏵ bypass permissions on\n")
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), screen,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the claude history echo must keep the delivery uncertain: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the claude box got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the claude box got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchCurrentPathAboveUnrelatedDraft: the codex composer holds an
// unrelated draft and the history shows this exact path. The region is
// recognized, does not hold the path, and the visible path keeps the
// delivery uncertain: no Enter and no resend.
func TestDispatchCurrentPathAboveUnrelatedDraft(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexDraftScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the unrelated draft must keep the delivery uncertain: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the unrelated draft got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the unrelated draft got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchUnknownInputRegionWithVisiblePath: the screen shows this
// exact path but carries no recognized composer (no '›' line, no box
// borders). An unknown region is not positive composer proof: no Enter and
// no resend.
func TestDispatchUnknownInputRegionWithVisiblePath(t *testing.T) {
	f := newBusyDispatchFixture(t)
	screen := "Read the file " + f.composedPath() + " in full and execute it.\nsome idle chrome\n"
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), screen,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the unknown region must keep the delivery uncertain: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the unknown region got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the unknown region got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchIdleQuestionUIGetsNoEnterAndNoResend: the agent state is
// idle (not blocked) and the screen carries the codex question UI with
// this exact path visible. The known question UI takes no Enter from here
// (the Enter would answer it) and no repeated prompt.
func TestDispatchIdleQuestionUIGetsNoEnterAndNoResend(t *testing.T) {
	f := newBusyDispatchFixture(t)
	screen := "• Working\nQuestion: which file should the agent read?\n  a) alpha\n  b) beta\nenter to submit answer · esc to cancel\nRead the file " + f.composedPath() + " in full and execute it.\n"
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), screen,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after its question dialog") {
		t.Fatalf("the idle question UI must end not-received: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the question UI got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the question UI got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchIdleTrustDialogGetsNoInput: the cursor workspace trust
// dialog lands before the recheck (state idle, Windows simulated through
// the shared platform seam): the preflight refusal ends the dispatch
// blocked before any prompt or key.
func TestDispatchIdleTrustDialogGetsNoInput(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteRoster(t, f, "cursor")
	setDispatchPlatform(t, "win32")
	visible := []string{"agent", "read", "worker", "--source", "visible"}
	f.install(t, append(append([]fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
	}, pasteGetRules("idle", 1, 0)...),
		fakecli.Rule{Argv: visible, Call: 1, ArgvPrefix: true, Stdout: "clean screen\n"},
		fakecli.Rule{Argv: visible, ArgvPrefix: true, Stdout: cursorTrustScreen(t)},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "clean screen\n"},
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
	))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 || dispatchOutputStatus(t, out) != "blocked" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(out, `"cause":"cursor-startup-workspace-trust"`) {
		t.Fatalf("the trust refusal cause is missing: %s", out)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 0 {
		t.Fatalf("the trust dialog got %d prompt(s); want none; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the trust dialog got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchFailedSendKeysIsUnverifiedInput: the exact path is in the
// recognized codex composer, but the send-key call fails. The failed Enter
// is failed and unverified input — not enter_sent — and the dispatch ends
// not-received without claiming the key landed.
func TestDispatchFailedSendKeysIsUnverifiedInput(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexComposerScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"send_failed","message":"the fake send-keys failed"}}`},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if strings.Contains(out, `"enter_sent":true`) {
		t.Fatalf("the failed send-key must not claim enter_sent: %s", out)
	}
	if !strings.Contains(errText, "after a failed Enter on the text left in its input box") {
		t.Fatalf("the failed send-key cause is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one (the failed) Enter; calls=%#v", n, calls)
	}
}

// TestDispatchDelayedComposerTextWithoutArrivalEndsNotReceived: the
// bounded Enter is the only key the retry sends; when the agent never
// takes the prompt the dispatch ends not-received after the Enter (the
// wait later retries the bounded Enter within its own limit), and the text
// is never retyped.
func TestDispatchDelayedComposerTextWithoutArrivalEndsNotReceived(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexComposerScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an Enter on the text left in its input box") {
		t.Fatalf("the not-received cause is wrong: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one bounded Enter; calls=%#v", n, calls)
	}
	marker, err := os.ReadFile(filepath.Join(f.stateDir, "wait", "worker.not-received"))
	if err != nil {
		t.Fatalf("not-received marker missing: %v", err)
	}
	parts := strings.Fields(strings.TrimSpace(string(marker)))
	if len(parts) != 2 || parts[1] != "1" {
		t.Fatalf("not-received marker lost the seq: %q", string(marker))
	}
}

// TestDispatchHistoryEchoIsNotTheComposerPrompt (input versus history):
// the screen holds a different dispatch's prompt echo — the previous
// stamp, one second old — and no recognized composer. It is not this
// prompt (exact dispatch identity), and unknown input permits neither
// an Enter nor a text resend.
func TestDispatchHistoryEchoIsNotTheComposerPrompt(t *testing.T) {
	f := newBusyDispatchFixture(t)
	previous := filepath.Join(f.stateDir, "briefs", "worker-20261003T152647.md")
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), "Read the file "+previous+" in full and execute it.\n",
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if strings.Contains(errText, "sending it once more") || !strings.Contains(errText, "unconfirmed input region") {
		t.Fatalf("unknown history shape must remain uncertain: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want only the initial submission; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("a history echo got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchForeignPromptInInputKeepsTheResend (exact dispatch
// identity): the screen holds a prompt carrying the "Read the file "
// marker but a different path, and no recognized composer. A marker-only
// check would send the Enter; the exact composed path is absent, but an
// unknown input region cannot authorize a text resend either.
func TestDispatchForeignPromptInInputKeepsTheResend(t *testing.T) {
	f := newBusyDispatchFixture(t)
	foreign := "Read the file /other/dispatch/other-20261001T000000.md in full and execute it.\n"
	pasteInstall(t, f, pasteGetRules("idle", 1, 0), foreign,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if strings.Contains(errText, "sending it once more") || !strings.Contains(errText, "unconfirmed input region") {
		t.Fatalf("unknown foreign draft must remain uncertain: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want only the initial submission; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("a foreign prompt got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchBlockedDialogGetsNoEnterAndNoResend: the target is blocked
// on a dialog whose question quotes this dispatch's path. Neither the
// bounded Enter (it would answer the dialog) nor the resent text (it would
// be a repeated prompt on the dialog) may be sent: the dispatch ends
// not-received and leaves the pane to be read.
func TestDispatchBlockedDialogGetsNoEnterAndNoResend(t *testing.T) {
	f := newBusyDispatchFixture(t)
	dialog := "Question: do you want to read " + f.composedPath() + " ?\n  1. Yes\n  2. No\n"
	pasteInstall(t, f, pasteGetRules("blocked", 1, 0), dialog,
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after its block on a dialog") {
		t.Fatalf("the blocked-dialog cause is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the dialog got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the dialog got %d key(s); want none; calls=%#v", n, calls)
	}
}

// pasteFirstInstall installs the first-Enter fixture rules: the pre-send
// H0 read sees a clean screen and every later visible read — including the
// post-window read the first-Enter gate acts on — sees screen.
func pasteFirstInstall(t *testing.T, f *busyDispatchFixture, getRules []fakecli.Rule, screen string, extra ...fakecli.Rule) {
	t.Helper()
	visible := []string{"agent", "read", "worker", "--source", "visible"}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
	}
	rules = append(rules, getRules...)
	rules = append(rules,
		fakecli.Rule{Argv: visible, Call: 1, ArgvPrefix: true, Stdout: "clean screen\n"},
		fakecli.Rule{Argv: visible, ArgvPrefix: true, Stdout: screen},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "clean screen\n"},
	)
	rules = append(rules, extra...)
	f.install(t, rules)
}

// pasteCodexLongerPathScreen is the codex composer holding a longer path
// that contains the composed path (the ".md.rejected" negative control):
// the screen shows the path text, but it is not the exact composed path.
func pasteCodexLongerPathScreen(path string) string {
	return "old output\n• Working (12s · esc to interrupt)\n\n› Read the file " + path + ".rejected in full and execute it.\n\n  GPT-6.1-Sol medium · Context 20% use…\n"
}

// pasteCodexQuestionScreen is the codex question UI above a composer that
// holds the exact path (the path-collision case): the dialog must win.
func pasteCodexQuestionScreen(path string) string {
	return "old output\nQuestion: which file should I edit?\nenter to submit answer · esc to cancel\n› Read the file " + path + " in full and execute it.\n"
}

// TestDispatchFirstEnterOnComposerSendsEnter is the legitimate first
// Enter: the post-window read shows a recognized codex composer holding the
// exact composed prompt path (the prompt typed and not sent). The dispatch
// sends exactly one Enter, never retypes the text, and the arrival settles
// it as submitted.
func TestDispatchFirstEnterOnComposerSendsEnter(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteFirstInstall(t, f, pasteGetRules("idle", 1, 2), pasteCodexComposerScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(out, `"enter_sent":true`) || strings.Contains(out, `"resent":true`) {
		t.Fatalf("the first Enter without a resend is not in the result: %s", out)
	}
	if !strings.Contains(errText, "sat in the input box; sent Enter") {
		t.Fatalf("the first-Enter warning is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1 (the text was never duplicated); calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one Enter; calls=%#v", n, calls)
	}
}

// TestDispatchFirstEnterOnEmptyComposerGetsNoEnter: the history shows this
// prompt taken above an empty recognized composer. A marker-only check
// would send the Enter; the recognized composer does not hold the exact
// path, so the first Enter is refused and the delivery stays uncertain
// (no Enter, no resend).
func TestDispatchFirstEnterOnEmptyComposerGetsNoEnter(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexHistoryScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the uncertain cause is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the empty composer got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the empty composer got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchFirstEnterOnUnrelatedDraftGetsNoEnter: the recognized
// composer holds an unrelated draft while the history shows this prompt.
// The Enter would submit the draft with this prompt's Enter — refused, and
// the delivery stays uncertain (no Enter, no resend).
func TestDispatchFirstEnterOnUnrelatedDraftGetsNoEnter(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexDraftScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the uncertain cause is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("the draft got %d prompt(s); want the initial one only; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the draft got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchFirstEnterOnLongerPathGetsNoSendAction is the longer-path
// negative control: the composer holds the composed path as a prefix of a
// longer path ("...md.rejected"). The longer path is not positive proof of
// the exact path — no Enter — and the screen shows the path text — no
// resend: the delivery stays uncertain.
func TestDispatchFirstEnterOnLongerPathGetsNoSendAction(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexLongerPathScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an unconfirmed input region") {
		t.Fatalf("the longer path must keep the delivery uncertain: %s", errText)
	}
	if strings.Contains(errText, "sat in the input box") {
		t.Fatalf("the longer path must not authorize the Enter: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1 (the longer path is not provably absent); calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 0 {
		t.Fatalf("the longer path got %d key(s); want none; calls=%#v", n, calls)
	}
}

// TestDispatchFirstEnterFailedSendKeys: the composer holds the exact path
// and the gate passes, but the send-key call itself fails. The input is
// failed and unverified: no enter_sent claim, not-received after the
// failed Enter, no resend.
func TestDispatchFirstEnterFailedSendKeys(t *testing.T) {
	f := newBusyDispatchFixture(t)
	pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexComposerScreen(f.composedPath()),
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"send_failed","message":"the fake send-keys failed"}}`},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if strings.Contains(out, `"enter_sent":true`) {
		t.Fatalf("the failed send-key must not claim enter_sent: %s", out)
	}
	if !strings.Contains(errText, "after a failed Enter on the text left in its input box") {
		t.Fatalf("the failed send-key cause is missing: %s", errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("prompt attempts=%d, want 1; calls=%#v", n, calls)
	}
	if n, _ := pasteSendKeys(calls); n != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one (the failed) Enter; calls=%#v", n, calls)
	}
}

// TestDispatchFirstEnterDialogUIGetsNoEnter: the known dialog UIs win over
// the composer path — the screen carries both a recognized composer that
// holds the exact path and a known question/approval UI (the path
// collision). The Enter would be the dialog's input: refused, not-received
// after the dialog, no key.
func TestDispatchFirstEnterDialogUIGetsNoEnter(t *testing.T) {
	t.Run("the codex question above the composer with the path", func(t *testing.T) {
		f := newBusyDispatchFixture(t)
		pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), pasteCodexQuestionScreen(f.composedPath()),
			fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
			fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
		)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if !strings.Contains(errText, "after its question dialog") {
			t.Fatalf("the dialog cause is missing: %s", errText)
		}
		calls := f.calls(t)
		if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
			t.Fatalf("the question dialog got %d prompt(s); want the initial one only; calls=%#v", n, calls)
		}
		if n, _ := pasteSendKeys(calls); n != 0 {
			t.Fatalf("the question dialog got %d key(s); want none; calls=%#v", n, calls)
		}
	})
	t.Run("the claude approval below the box with the path", func(t *testing.T) {
		f := newBusyDispatchFixture(t)
		pasteRoster(t, f, "claude")
		screen := pasteClaudeScreen("old output\n",
			"❯ Read the file "+f.composedPath()+" in full and execute it.",
			"Do you want to make this edit?\n  1. Yes\n  2. No\n")
		pasteFirstInstall(t, f, pasteGetRules("idle", 1, 0), screen,
			fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
			fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
		)
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if !strings.Contains(errText, "after its approval dialog") {
			t.Fatalf("the dialog cause is missing: %s", errText)
		}
		calls := f.calls(t)
		if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
			t.Fatalf("the approval dialog got %d prompt(s); want the initial one only; calls=%#v", n, calls)
		}
		if n, _ := pasteSendKeys(calls); n != 0 {
			t.Fatalf("the approval dialog got %d key(s); want none; calls=%#v", n, calls)
		}
	})
}
