package spawn

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestStartAgentClearsShellLine(t *testing.T) {
	for _, tc := range []struct {
		name, goos        string
		created           bool
		attempts, clearRC int
	}{
		{name: "reused-pane", goos: "darwin", attempts: 1},
		{name: "fresh-pane", goos: "darwin", created: true, attempts: 1},
		{name: "reused-retries", goos: "darwin", attempts: 3},
		{name: "fresh-retries", goos: "darwin", created: true, attempts: 3},
		{name: "clear-fails", goos: "darwin", attempts: 1, clearRC: 7},
		{name: "windows-reused", goos: "win32", attempts: 3},
		{name: "windows-fresh", goos: "win32", created: true, attempts: 3},
		{name: "linux-reused", goos: "linux", attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setStartupPlatform(t, tc.goos)
			oldDelay := busyRetryDelay
			busyRetryDelay = 0
			t.Cleanup(func() { busyRetryDelay = oldDelay })
			clear := []string{"pane", "send-text", "w0test:p0a", "\x15"}
			clearError := ""
			if tc.clearRC != 0 {
				clearError = "precaution failed\n"
			}
			rules := []fakecli.Rule{{Argv: clear, Code: tc.clearRC, Stderr: clearError}}
			for attempt := 1; attempt < tc.attempts; attempt++ {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Call: attempt, Code: 1, Stderr: "agent_pane_busy\n"})
			}
			rules = append(rules, fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`})
			f := newSpawnFixture(t, rules)
			oldErr := platform.Stderr
			var stderr strings.Builder
			platform.Stderr = &stderr
			t.Cleanup(func() { platform.Stderr = oldErr })
			o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1000"}
			args := []string{"-m", "model", "--flag", "value with spaces"}
			if blocked := startAgent(o, "codex", f.env, tc.created, args); blocked {
				t.Fatal("agent start blocked despite successful final attempt")
			}
			if stderr.Len() != 0 {
				t.Fatalf("start emitted an extra warning: %q", stderr.String())
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var got, want [][]string
			for _, call := range calls {
				got = append(got, call.Argv)
			}
			start := append([]string{"agent", "start", "worker", "--kind", "codex", "--pane", "w0test:p0a", "--timeout", "1000", "--"}, args...)
			for attempt := 0; attempt < tc.attempts; attempt++ {
				if tc.goos != "win32" && (!tc.created || attempt > 0) {
					want = append(want, clear)
				}
				want = append(want, start)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("herdr argv order = %#v, want %#v", got, want)
			}
		})
	}
}

func TestSpawnClearsShellLine(t *testing.T) {
	for _, given := range []bool{true, false} {
		name := "fresh-pane"
		if given {
			name = "given-pane"
		}
		t.Run(name, func(t *testing.T) {
			setStartupPlatform(t, "darwin")
			rules := append([]fakecli.Rule{{Argv: []string{"pane", "send-text", "w0test:p0a", "\x15"}}}, freshSpawnRules()...)
			f := newSpawnFixture(t, rules)
			configureSpawnFixture(t, &f)
			args := []string{"worker", "--name", "worker", "--fresh"}
			if given {
				args = append(args, "--pane", "w0test:p0a")
			}
			code, stdout, stderr, calls := runCmdSpawn(t, f, args)
			if code != 0 || !strings.Contains(stdout, `"status": "ready"`) {
				t.Fatalf("spawn code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			start, clears, creates := -1, 0, 0
			for i, call := range calls {
				if len(call.Argv) < 2 {
					continue
				}
				if call.Argv[0] == "agent" && call.Argv[1] == "start" {
					start = i
					break
				}
				if call.Argv[0] == "pane" && call.Argv[1] == "send-text" {
					clears++
				}
				if (call.Argv[0] == "pane" && call.Argv[1] == "split") || (call.Argv[0] == "tab" && call.Argv[1] == "create") {
					creates++
				}
			}
			if start < 0 {
				t.Fatalf("no agent start: %#v", calls)
			}
			if given {
				want := []string{"pane", "send-text", "w0test:p0a", "\x15"}
				if clears != 1 || creates != 0 || start == 0 || !reflect.DeepEqual(calls[start-1].Argv, want) {
					t.Fatalf("given pane: clears=%d creates=%d calls=%#v", clears, creates, calls)
				}
			} else if clears != 0 || creates != 1 {
				t.Fatalf("fresh pane: clears=%d creates=%d calls=%#v", clears, creates, calls)
			}
		})
	}
}

func TestUpdateRelaunchClearsShellLine(t *testing.T) {
	for _, created := range []bool{false, true} {
		name := "given-pane"
		if created {
			name = "fresh-pane"
		}
		t.Run(name, func(t *testing.T) {
			setStartupPlatform(t, "darwin")
			clear := []string{"pane", "send-text", "w0test:p0a", "\x15"}
			args := []string{"-m", "model", "--flag", "value with spaces"}
			start := []string{"agent", "start", "worker", "--kind", "codex", "--pane", "w0test:p0a", "--timeout", "1000", "--", "-m", "model", "--flag", "value with spaces"}
			get := []string{"agent", "get", "worker"}
			read := []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}
			f := newSpawnFixture(t, []fakecli.Rule{
				{Argv: clear},
				{Argv: start, Stdout: `{"result":{"started":true}}`},
				{Argv: get, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
				{Argv: get, Call: 2, Code: 1, Stderr: `{"error":{"code":"agent_not_found"}}`},
				{Argv: get, Call: 3, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
				{Argv: read, Call: 1, Stdout: "Updating Codex via npm install\nPlease restart Codex\n"},
				{Argv: read, Call: 2, Stdout: "ready\n"},
			})
			f.env["TMPDIR"] = f.root
			f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
			f.env["PATH"] = f.bin
			f.env["SOHO_WAIT_POLL_MS"] = "1"
			oldErr := platform.Stderr
			var stderr strings.Builder
			platform.Stderr = &stderr
			t.Cleanup(func() { platform.Stderr = oldErr })
			o := spawnOptions{name: "worker", pane: "w0test:p0a", timeout: "1000"}
			if startAgent(o, "codex", f.env, created, args) {
				t.Fatal("initial start blocked")
			}
			state, screen, blocked := checkStartWindow(o, "codex", f.env, created, args, f.ctx)
			if state != "working" || screen != "ready\n" || blocked || !strings.Contains(stderr.String(), "updated itself at start and exited; started it again") {
				t.Fatalf("created=%v state=%q screen=%q blocked=%v stderr=%q", created, state, screen, blocked, stderr.String())
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var got [][]string
			for _, call := range calls {
				got = append(got, call.Argv)
			}
			// Include state/screen probes so the assertion proves the update →
			// gone → relaunch path, as well as every full start/clear argv.
			want := [][]string{start, get, read, get, start, get, read}
			if !created {
				want = [][]string{clear, start, get, read, get, clear, start, get, read}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("created=%v update relaunch argv=%#v, want %#v", created, got, want)
			}
		})
	}
}
