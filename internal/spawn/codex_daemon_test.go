package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func stubCodexHelp(t *testing.T, supported bool) *int {
	t.Helper()
	old := codexHelp
	oldSupported := CodexSupportsNoDaemon(platform.Env{"PATH": t.TempDir()}, t.TempDir())
	calls := new(int)
	codexNoDaemonOnce = sync.Once{}
	codexHelp = func(platform.Env, string) platform.RunResult {
		*calls++
		code := 0
		help := "old codex"
		if supported {
			help = "Usage: codex --no-daemon"
		}
		return platform.RunResult{Status: &code, Stdout: help}
	}
	t.Cleanup(func() {
		codexHelp = old
		codexNoDaemonOnce = sync.Once{}
		codexNoDaemonOnce.Do(func() { codexNoDaemonSupported = oldSupported })
	})
	return calls
}

func TestConfigNativeArgsCodexNoDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, kind, args, want string
		supported              bool
	}{
		{"empty", "codex", "", "--no-daemon", true},
		{"configured", "codex", "--verbose", "--verbose --no-daemon", true},
		{"already", "codex", "--no-daemon", "--no-daemon", true},
		{"remote", "codex", "--remote endpoint", "--remote endpoint", true},
		{"remote-equals", "codex", "--remote=endpoint", "--remote=endpoint", true},
		{"unsupported", "codex", "--verbose", "--verbose", false},
		// A configured "--" starts the prompt: the option goes before it.
		{"configured-prompt", "codex", "-- --remote", "--no-daemon -- --remote", true},
		{"configured-prompt-words", "codex", "--verbose -- --no-daemon", "--verbose --no-daemon -- --no-daemon", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCodexHelp(t, tc.supported)
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"args_" + tc.kind: {Value: tc.args}}}
			want := tc.want
			if got := ConfigNativeArgs(tc.kind, "", "worker", ctx, platform.Env{}, t.TempDir(), nil); got != want {
				t.Fatalf("args=%q want=%q", got, want)
			}
		})
	}
	for _, kind := range []string{"claude", "grok", "pi", "cursor", "gemini", "agy", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			calls := stubCodexHelp(t, true)
			if got := ConfigNativeArgs(kind, "", "worker", &core.Config{}, platform.Env{}, t.TempDir(), nil); got != "" || *calls != 0 {
				t.Fatalf("args=%q help calls=%d", got, *calls)
			}
		})
	}
}

func TestCodexNoDaemonSupportOnce(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(strings.ToUpper(strings.TrimSpace(map[bool]string{true: "supported", false: "unsupported"}[supported])), func(t *testing.T) {
			calls := stubCodexHelp(t, supported)
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if got := CodexSupportsNoDaemon(platform.Env{}, ""); got != supported {
						t.Errorf("support=%v", got)
					}
				}()
			}
			wg.Wait()
			if *calls != 1 {
				t.Fatalf("help calls=%d", *calls)
			}
		})
	}
}

func TestCodexNoDaemonFailedHelp(t *testing.T) {
	one, zero := 1, 0
	for _, tc := range []struct {
		name   string
		result platform.RunResult
	}{
		{"missing", platform.RunResult{NotFound: true}},
		{"nonzero", platform.RunResult{Status: &one, Stdout: "--no-daemon"}},
		{"timed-out", platform.RunResult{TimedOut: true, Stdout: "--no-daemon"}},
		// A help that handles the deadline's SIGTERM exits 0 with ETIMEDOUT.
		{"deadline-exit-0", platform.RunResult{Status: &zero, Error: "ETIMEDOUT", Stdout: "--no-daemon"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCodexHelp(t, true)
			codexHelp = func(platform.Env, string) platform.RunResult { return tc.result }
			if CodexSupportsNoDaemon(platform.Env{}, "") {
				t.Fatal("failed help enabled flag")
			}
		})
	}
}

// codexSpawnLaunch spawns a codex worker with the configured args.codex and
// tail after "--", and returns the argv the agent start received after its
// own "--".
func codexSpawnLaunch(t *testing.T, configured string, tail []string) []string {
	t.Helper()
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	if configured != "" {
		f.ctx.Entries["args_codex"] = core.ConfigEntry{Value: configured}
	}
	if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: codex\napprovals: ask\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_TRUST_CWD"] = "off"
	argv := []string{"worker", "--name", "worker", "--pane", "w0test:p0a", "--fresh"}
	if len(tail) > 0 {
		argv = append(append(argv, "--"), tail...)
	}
	code, out, stderr, calls := runCmdSpawn(t, f, argv)
	if code != 0 {
		t.Fatalf("spawn exit=%d out=%s stderr=%s", code, out, stderr)
	}
	for _, c := range calls {
		if len(c.Argv) > 2 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			for i, a := range c.Argv {
				if a == "--" {
					return c.Argv[i+1:]
				}
			}
		}
	}
	t.Fatal("agent start missing")
	return nil
}

func TestSpawnCodexTrailingNativeArgsDecideNoDaemon(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		tail       []string
		want       []string
	}{
		{"none", "", nil, []string{"--no-daemon"}},
		{"remote", "", []string{"--remote", "unix:///tmp/dja583.sock"}, []string{"--remote", "unix:///tmp/dja583.sock"}},
		{"remote-equals", "", []string{"--remote=unix:///tmp/dja583.sock"}, []string{"--remote=unix:///tmp/dja583.sock"}},
		{"explicit", "", []string{"--no-daemon"}, []string{"--no-daemon"}},
		// After "--" the words are the prompt: "--remote" there is text.
		{"literal-prompt", "", []string{"--", "--remote"}, []string{"--no-daemon", "--", "--remote"}},
		// A trailing --remote drops the option, not the configured prompt.
		{"configured-prompt", "-- --no-daemon", []string{"--remote", "x"}, []string{"--", "--no-daemon", "--remote", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCodexHelp(t, true)
			argv := codexSpawnLaunch(t, tc.configured, tc.tail)
			if len(argv) < len(tc.want) || !slices.Equal(argv[len(argv)-len(tc.want):], tc.want) {
				t.Fatalf("launch argv %q does not end with %q", argv, tc.want)
			}
			if tc.name != "none" && tc.name != "literal-prompt" && slices.Contains(argv[:len(argv)-len(tc.want)], "--no-daemon") {
				t.Fatalf("--no-daemon left before the trailing args: %q", argv)
			}
		})
	}
}

func TestSpawnCodexUpdateCheckLaunch(t *testing.T) {
	stubCodexHelp(t, false)
	argv := codexSpawnLaunch(t, "", nil)
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && argv[i+1] == "check_for_update_on_startup=false" {
			return
		}
	}
	t.Fatalf("missing update override in launch argv: %q", argv)
}

func TestSpawnCodexUpdateOverrideSources(t *testing.T) {
	for _, tc := range []struct {
		name, configured string
		tail             []string
		want             string
	}{
		{"tail", "", []string{"-c", "check_for_update_on_startup=false"}, "check_for_update_on_startup=false"},
		{"config and tail", "--verbose", []string{"--foo"}, "check_for_update_on_startup=false"},
		{"configured true", "-c check_for_update_on_startup=true", []string{"--foo"}, "check_for_update_on_startup=true"},
		{"tail long", "--verbose", []string{"--config", "check_for_update_on_startup=true"}, "check_for_update_on_startup=true"},
		{"prompt", "", []string{"--", "-c", "check_for_update_on_startup=true"}, "check_for_update_on_startup=false"},
		{"positional prompt", "hello -c check_for_update_on_startup=true", nil, "check_for_update_on_startup=false"},
		{"configured prompt stops tail", "-- hello", []string{"-c", "check_for_update_on_startup=true"}, "check_for_update_on_startup=false"},
		{"option value", "-m model -c check_for_update_on_startup=true", nil, "check_for_update_on_startup=true"},
		{"multiple images", "--image first.png second.png -c check_for_update_on_startup=true", nil, "check_for_update_on_startup=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCodexHelp(t, false)
			argv := codexSpawnLaunch(t, tc.configured, tc.tail)
			end := optionsEnd(argv)
			if tc.name == "positional prompt" {
				end = slices.Index(argv, "hello")
			}
			count := 0
			for i := 0; i+1 < end; i++ {
				if (argv[i] == "-c" || argv[i] == "--config") && strings.HasPrefix(argv[i+1], "check_for_update_on_startup=") {
					count++
					if argv[i+1] != tc.want {
						t.Fatalf("argv=%q want %s", argv, tc.want)
					}
				}
			}
			if count != 1 {
				t.Fatalf("argv=%q has %d settings", argv, count)
			}
		})
	}
}
