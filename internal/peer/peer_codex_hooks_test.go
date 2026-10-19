package peer_test

// DJA-434: codex's hooks review screen on the send's pre-send visible read.
// hooks_review=trust approves the hooks with spawn's code before the
// message goes out; hooks_review=block (or an invalid value) is the dialog
// refusal: exit 17, nothing sent.

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const peerHooksMenuScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"› 1. Review hooks\n" +
	"  2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

const peerHooksMenuOnTrustScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"  1. Review hooks\n" +
	"› 2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

func peerHooksFixture(t *testing.T, mode string) *fixture {
	t.Helper()
	visible := []string{"agent", "read", "w0test:p0a", "--source", "visible"}
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("codex", "idle", "1")},
		{Argv: visible, Call: 1, Stdout: peerHooksMenuScreen},
		{Argv: visible, Call: 2, Stdout: peerHooksMenuOnTrustScreen},
		{Argv: visible, ArgvPrefix: true, Stdout: "› Ask Codex to do anything\n"},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: ""},
		{Argv: []string{"agent", "send-keys", "w0test:p0a"}, ArgvPrefix: true},
		{Argv: []string{"agent", "prompt", "w0test:p0a"}, ArgvPrefix: true},
	})
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	if mode != "" {
		f.env["SOHO_HOOKS_REVIEW"] = mode
	}
	return f
}

// peerHooksInputs lists, in order, the keys and prompts sent to the target.
func peerHooksInputs(t *testing.T, f *fixture) []string {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, call := range calls {
		if len(call.Argv) > 3 && call.Argv[1] == "send-keys" {
			out = append(out, "send-keys "+call.Argv[3])
		}
		if len(call.Argv) > 1 && call.Argv[1] == "prompt" {
			out = append(out, "prompt")
		}
	}
	return out
}

func TestSendCodexHooksTrustApprovesBeforeTheMessage(t *testing.T) {
	f := peerHooksFixture(t, "")
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
	got := peerHooksInputs(t, f)
	if len(got) < 3 || !reflect.DeepEqual(got[:3], []string{"send-keys down", "send-keys enter", "prompt"}) {
		t.Fatalf("inputs = %q (code=%d out=%q stderr=%q)", got, code, out, stderr)
	}
	if !strings.Contains(stderr, "soho: send: trusted 3 codex hook(s) for 'local/w0test:p0a' (hooks_review=trust); they were new or changed in the user's codex hooks\n") {
		t.Fatalf("stderr lacks the trusted line: %q", stderr)
	}
}

func TestSendCodexHooksBlockRefusesAsDialog(t *testing.T) {
	for _, mode := range []string{"block", "bogus"} {
		t.Run(mode, func(t *testing.T) {
			f := peerHooksFixture(t, mode)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
			if code != 17 || out != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			for _, want := range []string{"hooks_review=block; nothing was typed", "soho: send: local/w0test:p0a is showing a dialog; nothing was sent\n"} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr lacks %q: %q", want, stderr)
				}
			}
			if got := peerHooksInputs(t, f); len(got) != 0 {
				t.Fatalf("block typed: %q", got)
			}
		})
	}
}
