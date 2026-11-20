package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// grokTrustDialogScreen is grok's first-run trust dialog, as reported on a
// real Windows machine: herdr still reports the agent ready while it is on
// this screen.
const grokTrustDialogScreen = "Do you trust the contents of this directory?\n" +
	"  1. Yes, proceed (y)\n" +
	"  2. No, quit (n)\n"

func TestSpawnGrokTrustDialogBlocksAtStartup(t *testing.T) {
	// A fresh grok showing the trust dialog must not take the typed
	// /context-window — its "n" answers "No, quit" and the agent exits. No
	// pane send-text, the JSON is blocked_at_startup, the screen is printed
	// and the exit is 7, like the other startup dialogs.
	rules := []fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: grokTrustDialogScreen}}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("the blocked JSON carries context_window: %q", stdout)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("spawn typed into the trust dialog: %#v", got)
	}
	roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil || !strings.Contains(string(roster), "worker") {
		t.Fatalf("the blocked worker is not registered: roster=%q err=%v", roster, err)
	}
}

func TestSpawnGrokTrustDialogWithoutWindowBlocksAtStartup(t *testing.T) {
	// The dialog blocks the start with or without context_window.grok set:
	// without it, the first dispatch would type its prompt into the dialog
	// and its "n" answers "No, quit". No configured window, no pane
	// send-text, blocked_at_startup, screen printed, exit 7.
	rules := []fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: grokTrustDialogScreen}}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("the blocked JSON carries context_window: %q", stdout)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("spawn typed into the trust dialog: %#v", got)
	}
}

func TestSpawnGrokNoDialogNoWindowUnchanged(t *testing.T) {
	// A fresh grok without the dialog and without a configured window is
	// unchanged: the extra screen read happens, but nothing is typed and the
	// JSON is the same as before.
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("context_window in the JSON without the key: %q", stdout)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("send-keys calls without the key: %#v", got)
	}
}

func TestSpawnGrokTrustOnlyProceedLineIsNotDialog(t *testing.T) {
	// "Yes, proceed" without "No, quit" (and without the question line) is
	// not the trust dialog: the spawn types and confirms the command as
	// usual.
	screen := "  Yes, proceed (y)\n  No, keep going (n)\n"
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: screen},
		{Argv: grokScreenRead("worker"), Call: 2, Stdout: screen},
		{Argv: grokScreenRead("worker"), Stdout: "Context window set to 500k\n"},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	pane := spawnPane(calls)
	if pane == "" {
		t.Fatalf("no agent start call: %#v", calls)
	}
	if n := countCallArgv(calls, "pane", "send-text", pane, "/context-window 500k"); n != 1 {
		t.Fatalf("text sent %d times: %#v", n, calls)
	}
	if n := countCallArgv(calls, "pane", "send-keys", pane, "Enter"); n != 1 {
		t.Fatalf("Enter sent %d times: %#v", n, calls)
	}
	if !spawnJSONHasField(t, stdout, `"status": "ready"`) || !spawnJSONHasField(t, stdout, `"context_window": "500k"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
}

func TestSpawnGrokTrustScreenReadBeforeTyping(t *testing.T) {
	// Without a dialog nothing changes: the dialog check reuses the visible
	// screen the start window already read, and the path that types the
	// configured /context-window re-reads the screen right before typing
	// (the dialog can land on the screen after the start window). The same
	// command is then typed and confirmed and the JSON is the same as
	// before.
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "grok-4.7 ready\n"},
		{Argv: grokScreenRead("worker"), Stdout: "Context window set to 500k\n"},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	pane := spawnPane(calls)
	if pane == "" {
		t.Fatalf("no agent start call: %#v", calls)
	}
	text := lastCallIndex(calls, "pane", "send-text", pane, "/context-window 500k")
	enter := lastCallIndex(calls, "pane", "send-keys", pane, "Enter")
	var reads []int
	want := strings.Join(grokScreenRead("worker"), "\x00")
	for i, call := range calls {
		if strings.Join(call.Argv, "\x00") == want {
			reads = append(reads, i)
		}
	}
	// reads[0] is the start-window read that feeds the first dialog check,
	// reads[1] is the pre-typing re-read that feeds the second (spent only
	// on the path that types the command), and the confirmation reads come
	// after the Enter.
	if len(reads) < 3 || !(reads[0] < reads[1] && reads[1] < text && text < enter && reads[2] > enter) {
		t.Fatalf("order reads=%v text=%d enter=%d", reads, text, enter)
	}
	if !spawnJSONHasField(t, stdout, `"status": "ready"`) || !spawnJSONHasField(t, stdout, `"context_window": "500k"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
}

func TestSpawnGrokGoneAfterEnterWarnsExited(t *testing.T) {
	// The grok exits after the Enter: every read of the confirmation window
	// fails (agent_not_found), so the spawn says it exited — the typed text
	// answered a dialog — instead of the generic "could not confirm" warning.
	// The JSON status stays ready and carries no context_window.
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "grok-4.7 ready\n"}, // start window (the screen the dialog check sees)
		// From the Enter on the agent is gone: every remaining read of the
		// confirmation window fails with agent_not_found.
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "500" // bound the reads inside the 10s window
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "ready"`) {
		t.Fatalf("the status changed: spawn JSON=%q", stdout)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("unconfirmed window leaked into the JSON: %q", stdout)
	}
	want := "spawn: grok 'worker' exited after /context-window; it may have been showing a dialog — read its pane"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr=%q, want %q", stderr, want)
	}
	if strings.Contains(stderr, "could not confirm") {
		t.Fatalf("the generic warning came back: %q", stderr)
	}
	pane := spawnPane(calls)
	if n := countCallArgv(calls, "pane", "send-text", pane, "/context-window 500k"); n != 1 {
		t.Fatalf("text sent %d times: %#v", n, calls)
	}
	if n := countCallArgv(calls, "pane", "send-keys", pane, "Enter"); n != 1 {
		t.Fatalf("Enter sent %d times: %#v", n, calls)
	}
}

func TestSpawnGrokTrustDialogPatterns(t *testing.T) {
	// Case and space variations of the dialog lines: the question line, or
	// both answer lines together, is the dialog; one answer line alone, or
	// the words apart, is not.
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"question line as reported", "Do you trust the contents of this directory?\n", true},
		{"question all lower", "do you trust the contents of this directory\n", true},
		{"question all upper", "DO YOU TRUST THE CONTENTS OF THIS DIRECTORY?", true},
		{"question mixed case", "dO YoU TrUsT tHe cOnTeNtS oF tHiS dIrEcToRy", true},
		{"question with line padding", "    Do you trust the contents of this directory?    \n", true},
		{"question with extra inner spaces", "Do  you\ttrust   the contents of this directory\n", true},
		{"question embedded in a longer line", "❯ Do you trust the contents of this directory? (workspace: /home/u/dev)\n", true},
		{"both answers as reported", "  1. Yes, proceed (y)\n  2. No, quit (n)\n", true},
		{"both answers upper case", "YES, PROCEED (Y)\nNO, QUIT (N)", true},
		{"both answers with extra spaces", "yes,  proceed (y)\nno, \tquit (n)", true},
		{"both answers padded lines", "   YES, PROCEED   \n   NO, QUIT   \n", true},
		{"only the proceed line", "Yes, proceed (y)\n", false},
		{"only the quit line", "No, quit (n)\n", false},
		{"answers without commas", "Yes proceed (y)\nNo quit (n)\n", false},
		{"normal sentence citing both answers on one line", `The documentation says "yes, proceed" or "no, quit"; this is an explanation.`, false},
		{"normal sentence citing both answers on one line, padded", `   The documentation says "yes, proceed" or "no, quit"; this is an explanation.   \n`, false},
		{"normal grok prompt", "grok-4.7 ready\n❯ /help\n", false},
		{"the answer words apart", "Type \"yes, proceed\" to continue or press n\n", false},
		{"empty screen", "", false},
		{"blank screen", "   \n\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGrokTrustDialog(tc.screen); got != tc.want {
				t.Fatalf("isGrokTrustDialog(%q)=%v, want %v", tc.screen, got, tc.want)
			}
		})
	}
}

// grokTrustCitationScreen is a normal grok screen that cites both dialog
// answers on one line, quoted from the review: a sentence is not the
// dialog, which draws one answer per line.
const grokTrustCitationScreen = `The documentation says "yes, proceed" or "no, quit"; this is an explanation.`

func TestSpawnGrokTrustR2LateDialogWithWindowBlocks(t *testing.T) {
	// The dialog is drawn after the start-window read: the first read shows
	// a loading screen, and only the re-read right before typing the
	// configured /context-window sees the trust dialog. No text is typed
	// into the dialog: blocked_at_startup, the screen is printed, exit 7,
	// no pane send-text or send-keys.
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "loading grok\n"},
		{Argv: grokScreenRead("worker"), Stdout: grokTrustDialogScreen},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if !strings.Contains(stdout, "Do you trust the contents of this directory?") {
		t.Fatalf("the dialog screen was not printed: %q", stdout)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("the blocked JSON carries context_window: %q", stdout)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("spawn typed into the trust dialog: %#v", got)
	}
}

func TestSpawnGrokTrustR2NormalCitationNotDialog(t *testing.T) {
	// A normal sentence that cites both dialog answers on one line is not
	// the trust dialog: the detector says so, and the full spawn proceeds
	// without blocking — exit 0, ready, no typed text, no block warning.
	if isGrokTrustDialog(grokTrustCitationScreen) {
		t.Fatal("the detector treats a one-line citation as the trust dialog")
	}
	rules := []fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: grokTrustCitationScreen}}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "ready"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if strings.Contains(stderr, "blocked during startup") {
		t.Fatalf("the citation blocked the start: %q", stderr)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("nothing is typed without a configured window: %#v", got)
	}
}

func TestSpawnGrokTrustR2GoneAfterOneScreen(t *testing.T) {
	// The confirmation window sees a screen without the "Context window
	// set to" line after the Enter, and then the agent is gone: every
	// later read fails with agent_not_found and the final agent state is
	// gone. The exit warning fires even though a screen was seen after the
	// Enter — the status stays ready and carries no context_window.
	rules := []fakecli.Rule{
		{Argv: grokScreenRead("worker"), Call: 1, Stdout: "grok ready\n"},
		{Argv: grokScreenRead("worker"), Call: 2, Stdout: "not confirmed yet\n"},
		{Argv: grokScreenRead("worker"), Call: 3, Stdout: "still showing the menu\n"},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
	}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_WAIT_POLL_MS"] = "500" // bound the reads inside the 10s window
	f.ctx.Entries["context_window_grok"] = core.ConfigEntry{Value: "500k", Source: "user"}
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"status": "ready"`) {
		t.Fatalf("the status changed: spawn JSON=%q", stdout)
	}
	if strings.Contains(stdout, "context_window") {
		t.Fatalf("the unconfirmed window leaked into the JSON: %q", stdout)
	}
	want := "spawn: grok 'worker' exited after /context-window; it may have been showing a dialog — read its pane"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr=%q, want %q", stderr, want)
	}
	if strings.Contains(stderr, "could not confirm") {
		t.Fatalf("the generic warning came back: %q", stderr)
	}
	pane := spawnPane(calls)
	if n := countCallArgv(calls, "pane", "send-text", pane, "/context-window 500k"); n != 1 {
		t.Fatalf("text sent %d times: %#v", n, calls)
	}
	if n := countCallArgv(calls, "pane", "send-keys", pane, "Enter"); n != 1 {
		t.Fatalf("Enter sent %d times: %#v", n, calls)
	}
}
