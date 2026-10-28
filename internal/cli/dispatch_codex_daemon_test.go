package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchCodexNoDaemonWarning(t *testing.T) {
	for _, tc := range []struct {
		name, kind, args string
		supported, warn  bool
	}{
		{"legacy", "codex", "", true, true},
		{"isolated", "codex", "--no-daemon", true, false},
		{"remote", "codex", "--remote endpoint", true, false},
		{"remote-equals", "codex", "--remote=endpoint", true, false},
		{"pi", "pi", "", true, false},
		{"unsupported", "codex", "", false, false},
		{"literal-prompt", "codex", "-- --no-daemon", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			old := dispatchCodexSupportsNoDaemon
			dispatchCodexSupportsNoDaemon = func(platform.Env, string) bool { return tc.supported }
			t.Cleanup(func() { dispatchCodexSupportsNoDaemon = old })
			path := filepath.Join(f.state, "ws", "agents.tsv")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(data), "\n")
			row := strings.Split(lines[1], "\t")
			row[2], row[13] = tc.kind, tc.args
			lines[1] = strings.Join(row, "\t")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			want := "soho: dispatch: 'worker' was started without --no-daemon: codex runs its commands through a shared server that may carry another pane's identity, so its procs, copies and send can land in another workspace. Release it with --close and spawn it again\n"
			count := 0
			if tc.warn {
				count = 1
			}
			if code != 0 || strings.Count(stderr, want) != count || !strings.Contains(out, `"submitted"`) {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			sent := false
			for _, call := range calls {
				if len(call.Argv) >= 3 && strings.Join(call.Argv[:3], " ") == "agent prompt worker" {
					sent = true
				}
			}
			if !sent {
				t.Fatal("dispatch did not send prompt")
			}
		})
	}
}
