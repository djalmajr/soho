package cli

// DJA-434: codex's hooks review screen on the visible read the dispatch
// already makes before typing. hooks_review=trust approves the hooks with
// spawn's code and the prompt goes out; hooks_review=block types nothing
// and ends blocked (exit 7) with the task state restored.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const dispatchHooksMenuScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"› 1. Review hooks\n" +
	"  2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

const dispatchHooksMenuOnTrustScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"  1. Review hooks\n" +
	"› 2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

const dispatchCodexPromptScreen = "› Ask Codex to do anything\n\n  Context 0% used\n"

// dispatchVisibleRead is the argv of the dispatch's visible read.
var dispatchVisibleRead = []string{"agent", "read", "worker", "--source", "visible"}

// dispatchHooksCalls lists, in order, the agent send-keys keys and the
// prompts the dispatch sent to the worker.
func dispatchHooksCalls(calls []fakecli.Call) []string {
	out := []string{}
	for _, call := range calls {
		if len(call.Argv) >= 4 && call.Argv[0] == "agent" && call.Argv[2] == "worker" {
			switch call.Argv[1] {
			case "send-keys":
				out = append(out, "send-keys "+call.Argv[3])
			case "prompt":
				out = append(out, "prompt")
			}
		}
	}
	return out
}

func newDispatchHooksFixture(t *testing.T, mode string) *dispatchArrivalFixture {
	t.Helper()
	// The first visible read before typing (the check's baseline, with
	// prompt_settle_seconds=0) shows the menu; after the down the › is on
	// "Trust all and continue"; after the enter, the prompt.
	extra := []fakecli.Rule{
		{Argv: dispatchVisibleRead, Call: 1, Stdout: dispatchHooksMenuScreen},
		{Argv: dispatchVisibleRead, Call: 2, Stdout: dispatchHooksMenuOnTrustScreen},
		{Argv: dispatchVisibleRead, Stdout: dispatchCodexPromptScreen},
		{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true},
	}
	extra = append(extra, workingArrivalRules()...)
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", extra...)
	if mode != "" {
		f.env["SOHO_HOOKS_REVIEW"] = mode
	}
	return f
}

func TestDispatchCodexHooksTrustApprovesThenSends(t *testing.T) {
	f := newDispatchHooksFixture(t, "")
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	if got, want := dispatchHooksCalls(dispatchCalls(t, f)), []string{"send-keys down", "send-keys enter", "prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if !strings.Contains(errText, "soho: dispatch: trusted 3 codex hook(s) for 'worker' (hooks_review=trust); they were new or changed in the user's codex hooks\n") {
		t.Fatalf("stderr lacks the trusted line: %q", errText)
	}
}

func TestDispatchCodexHooksBlockTypesNothing(t *testing.T) {
	for _, mode := range []string{"block", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			f := newDispatchHooksFixture(t, mode)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 7 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
			}
			for _, want := range []string{`"wait_status":"blocked"`, `"cause":"codex-hooks-review"`, `"report_exists":false`} {
				if !strings.Contains(out, want) {
					t.Fatalf("dispatch JSON lacks %s: %s", want, out)
				}
			}
			if !strings.Contains(errText, "dispatch: 'worker' is on codex's hooks review screen (its user codex hooks are new or changed) and hooks_review=block; nothing was typed") {
				t.Fatalf("stderr = %q", errText)
			}
			if got := dispatchHooksCalls(dispatchCalls(t, f)); len(got) != 0 {
				t.Fatalf("block typed into the hooks screen: %q", got)
			}
			dispatchAssertNoTaskArtifactsButBriefs(t, f)
		})
	}
}

func TestDispatchCodexWithoutHooksScreenUnchanged(t *testing.T) {
	// A codex prompt on the baseline read: no key before the prompt, and
	// the visible reads are the ones the dispatch made before.
	extra := []fakecli.Rule{{Argv: dispatchVisibleRead, Stdout: dispatchCodexPromptScreen}}
	extra = append(extra, workingArrivalRules()...)
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	if got, want := dispatchHooksCalls(dispatchCalls(t, f)), []string{"prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if strings.Contains(errText, "hooks") {
		t.Fatalf("stderr names hooks: %q", errText)
	}
}

// dispatchAssertNoTaskArtifactsButBriefs checks the restored task state of
// a refusal after the send was claimed: no task-report pointer and no
// last-report (the composed brief stays, as on the startup recheck).
func dispatchAssertNoTaskArtifactsButBriefs(t *testing.T, f *dispatchArrivalFixture) {
	t.Helper()
	for _, name := range []string{"task-report-worker.json", "last-report-worker"} {
		if _, err := os.Stat(filepath.Join(f.state, "ws", name)); err == nil {
			t.Fatalf("the refusal left %s", name)
		}
	}
}

func TestDispatchCodexWholeHooksScreenQuotedInTranscriptDoesNotMatch(t *testing.T) {
	// The whole menu (› on option 2) or table quoted in the transcript with
	// codex's real composer below: no key, the prompt goes out.
	composer := "› Ask Codex to do anything\n" +
		"GPT-6.1-Sol low · Context 0% used · weekly 65% left · main\n" +
		"? for shortcuts\n"
	table := "Hooks\n" +
		"Lifecycle hooks from config and enabled plugins.\n" +
		"⚠ 3 hooks need review before they can run.\n" +
		"SessionStart  4 installed  2 active  2 review\n" +
		"SessionEnd    1 installed  0 active  1 review\n" +
		"t trust all · enter review · esc close\n"
	for name, screen := range map[string]string{"menu": dispatchHooksMenuOnTrustScreen + composer, "table": table + composer} {
		t.Run(name, func(t *testing.T) {
			extra := []fakecli.Rule{
				{Argv: dispatchVisibleRead, Stdout: screen},
				{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true},
			}
			extra = append(extra, workingArrivalRules()...)
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", extra...)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
			}
			if got, want := dispatchHooksCalls(dispatchCalls(t, f)), []string{"prompt"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("calls = %q, want %q", got, want)
			}
			if strings.Contains(errText, "hooks") {
				t.Fatalf("stderr names hooks: %q", errText)
			}
		})
	}
}
