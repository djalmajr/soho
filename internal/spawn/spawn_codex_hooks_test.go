package spawn

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/spawn/codexhooks"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// codexHooksMenuScreen is codex 0.163.0's opening hooks review menu, as a
// fresh spawn showed it with the spawn already ready (DJA-434).
const codexHooksMenuScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"› 1. Review hooks\n" +
	"  2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

// codexHooksMenuOnTrustScreen is the same menu after one down: the › is on
// option 2.
const codexHooksMenuOnTrustScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"  1. Review hooks\n" +
	"› 2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

// codexHooksTableScreen is the hooks table the dispatch's Enter opened on
// option 1, as two workers showed it.
const codexHooksTableScreen = "Hooks\n" +
	"Lifecycle hooks from config and enabled plugins.\n" +
	"⚠ 3 hooks need review before they can run.\n" +
	"SessionStart  4 installed  2 active  2 review\n" +
	"SessionEnd    1 installed  0 active  1 review\n" +
	"t trust all · enter review · esc close\n"

// codexPromptScreen is codex's normal prompt after the approval.
const codexPromptScreen = "› Ask Codex to do anything\n" +
	"\n" +
	"  Context 0% used\n"

// codexBriefQuoteScreen is a codex prompt whose transcript quotes the
// menu's phrases without the menu itself (no "enter confirm" footer, no
// numbered option line).
const codexBriefQuoteScreen = "• The brief says: Hooks need review appears; pick Trust all and continue.\n" +
	"  Hooks need review\n" +
	"  then press t trust all on the table if hooks need review before they can run.\n" +
	"› Ask Codex to do anything\n"

func newCodexHooksFixture(t *testing.T, reads []string, fallback string) spawnFixture {
	t.Helper()
	var rules []fakecli.Rule
	for i, screen := range reads {
		rules = append(rules, fakecli.Rule{Argv: grokScreenRead("worker"), Call: i + 1, Stdout: screen})
	}
	rules = append(rules, fakecli.Rule{Argv: grokScreenRead("worker"), Stdout: fallback})
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true})
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: codex\napprovals: ask\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	return f
}

// agentSendKeys lists, in order, the keys of the `agent send-keys worker
// <key>` calls.
func agentSendKeys(calls []fakecli.Call) []string {
	keys := []string{}
	for _, call := range calls {
		if len(call.Argv) == 4 && call.Argv[0] == "agent" && call.Argv[1] == "send-keys" && call.Argv[2] == "worker" {
			keys = append(keys, call.Argv[3])
		}
	}
	return keys
}

func assertSpawnReady(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != 0 || !strings.Contains(stdout, `"status": "ready"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func assertSpawnBlocked(t *testing.T, code int, stdout, stderr, warning string) {
	t.Helper()
	if code != 7 || !strings.Contains(stdout, `"status": "blocked_at_startup"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Hooks need review") && !strings.Contains(stdout, "hooks need review before they can run") {
		t.Fatalf("the hooks screen was not printed: %q", stdout)
	}
	if !strings.Contains(stderr, warning) {
		t.Fatalf("stderr lacks %q: %q", warning, stderr)
	}
	if strings.Contains(stderr, "trusted") {
		t.Fatalf("a blocked spawn claims the hooks trusted: %q", stderr)
	}
}

const trustedLine = "spawn: trusted 3 codex hook(s) for 'worker' (hooks_review=trust); they were new or changed in the user's codex hooks"

func TestSpawnCodexHooksMenuTrustSendsDownThenEnter(t *testing.T) {
	// The menu with the › on option 1: one down moves it to "Trust all and
	// continue", the re-read sees it there, then enter; the prompt follows.
	f := newCodexHooksFixture(t, []string{codexHooksMenuScreen, codexHooksMenuOnTrustScreen}, codexPromptScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if got, want := agentSendKeys(calls), []string{"down", "enter"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
	if n := strings.Count(stderr, "trusted"); n != 1 || !strings.Contains(stderr, "soho: "+trustedLine+"\n") {
		t.Fatalf("stderr = %q, want the one trusted line", stderr)
	}
	if got := sendKeysCalls(calls); len(got) != 0 {
		t.Fatalf("pane text or keys typed: %#v", got)
	}
}

func TestSpawnCodexHooksMenuAlreadyOnTrustSendsEnterOnly(t *testing.T) {
	// The › already on option 2: enter with no down.
	f := newCodexHooksFixture(t, []string{codexHooksMenuOnTrustScreen}, codexPromptScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if got, want := agentSendKeys(calls), []string{"enter"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
	if !strings.Contains(stderr, trustedLine) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSpawnCodexHooksMenuNeverOnTrustSendsNoEnter(t *testing.T) {
	// The › never reaches "Trust all and continue": 3 downs and no enter,
	// then the blocked path — an enter is never sent unseen.
	f := newCodexHooksFixture(t, nil, codexHooksMenuScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnBlocked(t, code, stdout, stderr, "the approval did not take")
	if got, want := agentSendKeys(calls), []string{"down", "down", "down"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
}

func TestSpawnCodexHooksTableTrustSendsT(t *testing.T) {
	// The table leaves after t: no esc.
	f := newCodexHooksFixture(t, []string{codexHooksTableScreen}, codexPromptScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if got, want := agentSendKeys(calls), []string{"t"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
	if !strings.Contains(stderr, trustedLine) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSpawnCodexHooksTableStillVisibleSendsEsc(t *testing.T) {
	// The table still visible after t (the start-window read plus the 4
	// re-reads): esc, then the prompt.
	reads := []string{codexHooksTableScreen, codexHooksTableScreen, codexHooksTableScreen, codexHooksTableScreen, codexHooksTableScreen}
	f := newCodexHooksFixture(t, reads, codexPromptScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if got, want := agentSendKeys(calls), []string{"t", "esc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
}

func TestSpawnCodexHooksMenuStaysAfterEnterBlocks(t *testing.T) {
	// The menu is still there after the enter: blocked_at_startup, exit 7,
	// the line saying the approval did not take.
	f := newCodexHooksFixture(t, nil, codexHooksMenuOnTrustScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnBlocked(t, code, stdout, stderr, "the approval did not take (hooks_review=trust)")
	if got, want := agentSendKeys(calls), []string{"enter"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
}

func TestSpawnCodexHooksBlockTypesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		file  string
		env   string
		label string
	}{
		{name: "block in the file", file: "block"},
		{name: "invalid in the file counts as block", file: "yes"},
		{name: "SOHO_HOOKS_REVIEW=block over a trust file", file: "trust", env: "block"},
		{name: "invalid SOHO_HOOKS_REVIEW over a trust file counts as block", file: "trust", env: "Trust"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexHooksFixture(t, nil, codexHooksMenuScreen)
			f.ctx.Entries["hooks_review"] = core.ConfigEntry{Value: tc.file, Source: "user"}
			if tc.env != "" {
				f.env["SOHO_HOOKS_REVIEW"] = tc.env
			}
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
			assertSpawnBlocked(t, code, stdout, stderr, "hooks_review=block; nothing was typed")
			if got := agentSendKeys(calls); len(got) != 0 {
				t.Fatalf("block typed keys: %q", got)
			}
		})
	}
}

func TestSpawnCodexWithoutHooksScreenUnchanged(t *testing.T) {
	// A codex without the hooks screen: one visible read (the start
	// window's), no key, ready — the call pattern of before.
	f := newCodexHooksFixture(t, nil, codexPromptScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if n := countCallArgv(calls, grokScreenRead("worker")...); n != 1 {
		t.Fatalf("visible reads = %d, want 1: %#v", n, calls)
	}
	if got := agentSendKeys(calls); len(got) != 0 {
		t.Fatalf("send-keys = %q", got)
	}
	if strings.Contains(stderr, "hooks") {
		t.Fatalf("stderr names hooks: %q", stderr)
	}
	// The same spawn under hooks_review=block makes the same herdr calls:
	// without the screen the key costs no call in either mode.
	g := newCodexHooksFixture(t, nil, codexPromptScreen)
	g.ctx.Entries["hooks_review"] = core.ConfigEntry{Value: "block", Source: "user"}
	_, _, _, blockCalls := runCmdSpawn(t, g, []string{"worker"})
	argvs := func(calls []fakecli.Call) []string {
		var out []string
		for _, call := range calls {
			out = append(out, strings.Join(call.Argv, " "))
		}
		return out
	}
	trustArgv, blockArgv := argvs(calls), argvs(blockCalls)
	for i := range trustArgv {
		trustArgv[i] = strings.ReplaceAll(trustArgv[i], f.root, "<root>")
	}
	for i := range blockArgv {
		blockArgv[i] = strings.ReplaceAll(blockArgv[i], g.root, "<root>")
	}
	if !reflect.DeepEqual(trustArgv, blockArgv) {
		t.Fatalf("calls differ between trust and block:\n%q\n%q", trustArgv, blockArgv)
	}
}

func TestSpawnCodexBriefQuotingHooksPhrasesDoesNotMatch(t *testing.T) {
	// A screen that quotes the phrases without the rest of the screen is
	// not the hooks screen: nothing is typed and the spawn is ready.
	f := newCodexHooksFixture(t, nil, codexBriefQuoteScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if got := agentSendKeys(calls); len(got) != 0 {
		t.Fatalf("send-keys = %q", got)
	}
}

func TestSpawnGrokOnCodexHooksScreenIsNotApproved(t *testing.T) {
	// Codex only: the same screen on a grok spawn is left alone.
	rules := []fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: codexHooksMenuScreen}}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	_, _, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if got := agentSendKeys(calls); len(got) != 0 || strings.Contains(stderr, "trusted") {
		t.Fatalf("grok got the codex approval: keys=%q stderr=%q", got, stderr)
	}
}

func TestCodexHooksRecognitionIsStrict(t *testing.T) {
	drop := func(screen, line string) string { return strings.Replace(screen, line, "", 1) }
	for _, tc := range []struct {
		name   string
		screen string
		menu   bool
		table  bool
	}{
		{"the menu", codexHooksMenuScreen, true, false},
		{"the menu on option 2", codexHooksMenuOnTrustScreen, true, false},
		{"the table", codexHooksTableScreen, false, true},
		{"the menu without the footer", drop(codexHooksMenuScreen, "  enter confirm · esc skip\n"), false, false},
		{"the menu without the trust option", drop(codexHooksMenuScreen, "  2. Trust all and continue\n"), false, false},
		{"the menu without the title", drop(codexHooksMenuScreen, "  Hooks need review\n"), false, false},
		{"the table without the footer", drop(codexHooksTableScreen, "t trust all · enter review · esc close\n"), false, false},
		{"the table without the notice", drop(codexHooksTableScreen, "⚠ 3 hooks need review before they can run.\n"), false, false},
		{"a brief quoting the phrases", codexBriefQuoteScreen, false, false},
		{"the prompt", codexPromptScreen, false, false},
	} {
		if got := codexhooks.IsMenu(tc.screen); got != tc.menu {
			t.Errorf("%s: codexhooks.IsMenu = %v, want %v", tc.name, got, tc.menu)
		}
		if got := codexhooks.IsTable(tc.screen); got != tc.table {
			t.Errorf("%s: codexhooks.IsTable = %v, want %v", tc.name, got, tc.table)
		}
	}
	if got := codexhooks.Count(codexHooksMenuScreen); got != "3" {
		t.Errorf("menu count = %q", got)
	}
	if got := codexhooks.Count(codexHooksTableScreen); got != "3" {
		t.Errorf("table count = %q", got)
	}
	if got := codexhooks.Count(strings.Replace(codexHooksMenuScreen, "  3 hooks are new or changed.\n", "", 1)); got != "the" {
		t.Errorf("count without a number = %q, want the", got)
	}
}

func TestCoreHooksReviewMode(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	env := map[string]string{}
	if got := core.HooksReviewMode(ctx, env); got != "trust" {
		t.Fatalf("default = %q, want trust", got)
	}
	for value, want := range map[string]string{"trust": "trust", "block": "block", "off": "block", "TRUST": "block"} {
		ctx.Entries["hooks_review"] = core.ConfigEntry{Value: value}
		if got := core.HooksReviewMode(ctx, env); got != want {
			t.Errorf("file %q = %q, want %q", value, got, want)
		}
	}
	if !core.ConfigKeyOk("hooks_review") || !core.ConfigValueOk("hooks_review", "block", nil, "") || core.ConfigValueOk("hooks_review", "yes", nil, "") {
		t.Fatalf("config set does not accept hooks_review=trust|block only")
	}
}

// codexComposer is codex's real composer, below any transcript.
const codexComposer = "› Ask Codex to do anything\n" +
	"GPT-6.1-Sol low · Context 0% used · weekly 65% left · main\n" +
	"? for shortcuts\n"

func TestSpawnCodexWholeHooksScreenQuotedInTranscriptDoesNotMatch(t *testing.T) {
	// The whole menu (› on option 2) or the whole table quoted in the
	// transcript, with the composer below: not the active dialog, so no key
	// and the spawn is ready.
	for name, screen := range map[string]string{
		"menu":  codexHooksMenuOnTrustScreen + codexComposer,
		"table": codexHooksTableScreen + codexComposer,
	} {
		t.Run(name, func(t *testing.T) {
			f := newCodexHooksFixture(t, nil, screen)
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
			assertSpawnReady(t, code, stdout, stderr)
			if got := agentSendKeys(calls); len(got) != 0 {
				t.Fatalf("send-keys = %q", got)
			}
			if strings.Contains(stderr, "hooks") {
				t.Fatalf("stderr names hooks: %q", stderr)
			}
		})
	}
}

func TestSpawnCodexHooksRedrawLosesMenuSendsNoEnter(t *testing.T) {
	// After the down every re-read shows only the selected line: the menu is
	// gone, so no enter — the blocked path takes it.
	f := newCodexHooksFixture(t, []string{codexHooksMenuScreen}, "› 2. Trust all and continue\n")
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 7 || !strings.Contains(stdout, `"status": "blocked_at_startup"`) || !strings.Contains(stderr, "the approval did not take") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got, want := agentSendKeys(calls), []string{"down"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
}

func TestSpawnCodexHooksMenuWithTrailingBlankLinesMatches(t *testing.T) {
	// Blank lines after the footer still leave it the last non-empty line.
	f := newCodexHooksFixture(t, []string{codexHooksMenuOnTrustScreen + "\n\n  \n"}, codexPromptScreen)
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	assertSpawnReady(t, code, stdout, stderr)
	if got, want := agentSendKeys(calls), []string{"enter"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
}
