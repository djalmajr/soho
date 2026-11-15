package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func preflightPanic(fn func()) (value any) {
	defer func() { value = recover() }()
	fn()
	return
}

func TestPreflightFreshSpawnErrorMetadata(t *testing.T) {
	for _, admission := range []bool{true, false} {
		for _, entry := range []bool{true, false} {
			t.Run(fmt.Sprintf("admission=%t/entry=%t", admission, entry), func(t *testing.T) {
				f := configuredFixture(t, []fakecli.Rule{{Argv: []string{"status", "server", "--json"}, Stdout: `{}`}})
				args := []string{"worker", "--model", "provider/model", "--fresh", "--force"}
				message, code := "spawn: configured launch preflight refused", 4
				if entry {
					args = append(args, "--effort", "invalid")
					message, code = "invalid effort 'invalid' (low|medium|high|xhigh|max)", 2
				}
				value := preflightPanic(func() {
					if admission {
						PreflightFreshSpawn(args, f.ctx, f.env, f.cwd)
					} else {
						CmdSpawn(args, f.ctx, f.env, f.cwd)
					}
				})
				exit, ok := value.(*platform.ExitError)
				if !ok || exit == nil || exit.Code != code || exit.Msg != message || exit.Friction != !admission {
					t.Fatalf("metadata=%#v want code=%d message=%q friction=%t", value, code, message, !admission)
				}
			})
		}
	}
}

func preflightCallerFixture(t *testing.T) spawnFixture {
	t.Helper()
	f := configuredFixture(t, nil)
	t.Cleanup(herdr.ResetCallerCheck)
	f.env["HERDR_ENV"], f.env["HERDR_PANE_ID"], f.env["SOHO_CALLER_CHECK"] = "1", "w1:p9", "refuse"
	t.Cleanup(platform.OverrideAncestorParents(map[int]int{os.Getpid(): 1}))
	return f
}

func TestPreflightFreshSpawnRealCallerRefusal(t *testing.T) {
	f := preflightCallerFixture(t)
	shell := os.Getpid() + 10000
	calls := 0
	t.Cleanup(herdr.OverrideCallerProcessInfoRunner(func(exe string, args []string, opts platform.RunOptions) platform.RunResult {
		calls++
		if exe != "herdr" || !reflect.DeepEqual(args, []string{"pane", "process-info", "--pane", "w1:p9"}) || opts.Env.Get("HERDR_PANE_ID") != "w1:p9" {
			t.Fatalf("unexpected caller request: exe=%q args=%q", exe, args)
		}
		status := 0
		return platform.RunResult{Status: &status, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)}
	}))
	value := preflightPanic(func() { PreflightFreshSpawn([]string{"worker", "--fresh", "--force"}, f.ctx, f.env, f.cwd) })
	exit, ok := value.(*platform.ExitError)
	want := fmt.Sprintf("spawn: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. Nothing was changed. Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn", shell)
	// The real CallerGate records its audit synchronously and throws Die,
	// whose Friction flag is false (not a deferred logging request).
	if !ok || exit == nil || exit.Code != 2 || exit.Msg != want || exit.Friction || calls != 1 {
		t.Fatalf("caller metadata=%#v process-info reads=%d", value, calls)
	}
	log, err := os.ReadFile(filepath.Join(core.StateDirPath(f.ctx, f.env, f.cwd), "friction.log"))
	if err != nil || strings.Count(string(log), "\n") != 1 || !strings.Contains(string(log), want) {
		t.Fatalf("caller audit=%q err=%v", log, err)
	}
	nativeCalls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if (err != nil && !os.IsNotExist(err)) || len(nativeCalls) != 0 {
		t.Fatalf("caller refusal native effects=%+v err=%v", nativeCalls, err)
	}
}

func TestPreflightFreshSpawnCallerPanicIdentity(t *testing.T) {
	for _, value := range []any{&platform.ExitError{Code: 7, Msg: "synthetic caller failure", Friction: true}, (*platform.ExitError)(nil), &struct{ label string }{"unknown caller panic"}} {
		t.Run(fmt.Sprintf("%T/%v", value, value == nil), func(t *testing.T) {
			f := preflightCallerFixture(t)
			t.Cleanup(herdr.OverrideCallerProcessInfoRunner(func(string, []string, platform.RunOptions) platform.RunResult { panic(value) }))
			got := preflightPanic(func() { PreflightFreshSpawn([]string{"worker", "--fresh"}, f.ctx, f.env, f.cwd) })
			if got != value {
				t.Fatalf("panic identity changed: got=%#v want=%#v", got, value)
			}
			if exit, ok := value.(*platform.ExitError); ok && exit != nil && (!exit.Friction || exit.Code != 7 || exit.Msg != "synthetic caller failure") {
				t.Fatalf("shared caller error mutated: %#v", exit)
			}
		})
	}
}

func TestPreflightFreshSpawnNoEffects(t *testing.T) {
	for _, tc := range []struct {
		name, mode, selector, inline, native, model, message string
		code, probes                                         int
	}{
		{"tui-valid", "", "v2", `{}`, "", "provider/model", "", 0, 1},
		{"tui-no-capability", "", "v2", `{}`, "", "provider/model", "spawn: configured launch preflight refused", 4, 1},
		{"hc-valid-ignores-tui-selector", "hc", "invalid", `{}`, "", "provider/model", "", 0, 0},
		{"hc-malformed-profile", "hc", "v2", `null`, "", "provider/model", "launch profile: inline configuration is not a single non-null JSON object", 2, 0},
		{"hc-invalid-native", "hc", "v2", `{}`, "--model private", "provider/model", "native launch profile is invalid", 2, 0},
		{"legacy-valid", "", "legacy", `null`, "--legacy-option", "legacy-model", "", 0, 0},
		{"legacy-resume", "", "legacy", `{}`, "--resume", "legacy-model", "", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capability := spawnCapability
			if tc.name == "tui-no-capability" {
				capability = `{}`
			}
			f := configuredFixture(t, []fakecli.Rule{{Argv: []string{"status", "server", "--json"}, Stdout: capability}})
			f.env["SOHO_TRANSPORT"], f.env["SOHO_OPENCODE_TUI_PROFILE"], f.env["OPENCODE_CONFIG_CONTENT"] = tc.mode, tc.selector, tc.inline
			// No fixture state is allocated at the new explicit root.
			f.env["SOHO_DIR"] = filepath.Join(f.root, "absent-state")
			f.env["SOHO_ARGS_OPENCODE"] = tc.native
			kind := "opencode"
			if tc.name == "legacy-resume" {
				kind = "claude"
				f.env["SOHO_ARGS_CLAUDE"] = tc.native
			}
			args := []string{"worker", "--fresh", "--kind", kind, "--name", "worker", "--model", tc.model, "--timeout", "1", "--force"}
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr strings.Builder
			platform.Stdout, platform.Stderr = &out, &stderr
			defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
			code, message := configuredExit(func() { PreflightFreshSpawn(args, f.ctx, f.env, f.cwd) })
			if code != tc.code || (tc.message != "" && message != tc.message) || out.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("exit=%d message=%q out=%q stderr=%q", code, message, out.String(), stderr.String())
			}
			if tc.name == "legacy-resume" && !strings.Contains(message, "would make claude resume") {
				t.Errorf("resume refusal=%q", message)
			}
			if _, err := os.Stat(f.env.Get("SOHO_DIR")); !os.IsNotExist(err) {
				t.Errorf("preflight created StateDir: %v", err)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(calls) != tc.probes {
				t.Errorf("calls=%+v", calls)
			}
			for _, c := range calls {
				if !reflect.DeepEqual(c.Argv, []string{"status", "server", "--json"}) || c.Stdin != "" {
					t.Errorf("preflight effect: %+v", c)
				}
			}
		})
	}
}
