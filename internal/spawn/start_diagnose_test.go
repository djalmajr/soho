package spawn

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-587: a failed spawn of a spawn-created pane whose screen carries a
// shell "command not found" line names the two causes the spawn's own
// typing can produce — the command's start consumed by an interactive
// prompt when the shell opens, or the executable missing from the
// shell's PATH — on its own stderr line, before the unchanged friction
// message, exit 4 and pane close.

const startTimeoutLine = `{"error":{"code":"timeout","message":"agent did not register within 1s"}}`

// cursorWord is cursor-agent's executable name minus its first character
// (the key an interactive prompt reads before the spawn's typing reaches
// the shell), derived so it can never drift from cursor-agent.
var cursorWord = "cursor-agent"[1:] // "ursor-agent"

func consumedLine(word, exe string) string {
	return "soho: spawn: the pane's shell consumed the start of the agent command ('" + word + "' instead of '" + exe + "'): an interactive prompt when the shell opens (for example oh-my-zsh's update question) read a key; answer or turn off that prompt on this machine, then spawn again"
}

// startAgentExitWithStderr is startAgentExit (created=true) with the
// stderr captured, so the test asserts the diagnosis line next to the
// recovered friction message.
func startAgentExitWithStderr(t *testing.T, o spawnOptions, kind string, env platform.Env) (code int, msg, stderr string) {
	t.Helper()
	old := platform.Stderr
	var buf strings.Builder
	platform.Stderr = &buf
	t.Cleanup(func() { platform.Stderr = old })
	code, msg = startAgentExit(t, o, kind, env)
	return code, msg, buf.String()
}

func TestStartAgentShellNotFoundDiagnosis(t *testing.T) {
	const closedPrefix = "agent start failed for worker ("

	for _, tc := range []struct {
		kind     string
		name     string
		screen   string
		wantDiag string // the diagnosis line; "" means the diagnosis prints nothing
	}{
		{"codex", "the zsh form names a suffix of the executable: the consumed line", "[oh-my-zsh] You can update manually by running `omz update`\nzsh: command not found: odex\n", consumedLine("odex", "codex")},
		{"codex", "the bash form names a suffix of the executable: the consumed line", "bash: odex: command not found\n", consumedLine("odex", "codex")},
		{"codex", "the zsh form names the executable itself: the cannot-find line", "zsh: command not found: codex\n", "soho: spawn: the pane's shell cannot find 'codex' (command not found): it is not on that shell's PATH"},
		{"codex", "a screen without such a line: the diagnosis prints nothing", paneBootstrapScreen, ""},
		{"codex", "a not-found word that is not a suffix: the diagnosis prints nothing", "zsh: command not found: foo\n", ""},
		{"cursor", "kind cursor and a suffix of cursor-agent: the consumed line names cursor-agent", "zsh: command not found: " + cursorWord + "\n", consumedLine(cursorWord, "cursor-agent")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, []fakecli.Rule{
				startTimeoutRule(),
				{Argv: paneReadArgv, Stdout: tc.screen},
				{Argv: []string{"pane", "close", "w0test:p0a"}},
			})
			o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1"}
			code, msg, stderr := startAgentExitWithStderr(t, o, tc.kind, f.env)
			// The exit and the close outcome are unchanged: 4, and the
			// closed-pane friction message with the screen tail.
			if code != 4 {
				t.Fatalf("exit=%d; want 4 (unchanged)", code)
			}
			if want := fmt.Sprintf("%s%s) in pane w0test:p0a; closed the pane this spawn opened (last screen lines: %s)", closedPrefix, tc.kind, lastLines(tc.screen, 5)); msg != want {
				t.Fatalf("msg=%q; want %q (the friction message is unchanged)", msg, want)
			}
			wantStderr := startTimeoutLine + "\n"
			if tc.wantDiag != "" {
				wantStderr += tc.wantDiag + "\n"
			}
			if stderr != wantStderr {
				t.Fatalf("stderr=%q; want exactly %q", stderr, wantStderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			read, closed := herdrCallIndex(calls, paneReadArgv, false), herdrCallIndex(calls, []string{"pane", "close", "w0test:p0a"}, false)
			if read < 0 || closed < 0 || read > closed {
				t.Fatalf("the pane read is not before the close: read=%d close=%d calls=%#v", read, closed, calls)
			}
		})
	}
}
