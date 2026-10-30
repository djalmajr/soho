package hc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

// hcEnvFromDir builds the platform env that resolves hc to the fake
// installed in dir, the herdr tests' way.
func hcEnvFromDir(t *testing.T, dir string) platform.Env {
	t.Helper()
	env := platform.Env{}
	for _, item := range fakecli.Env(os.Environ(), dir) {
		k, v, ok := strings.Cut(item, "=")
		if ok {
			env[k] = v
		}
	}
	return env
}

// newHCFake installs a fake hc in a fresh directory and returns the
// platform env that resolves hc to it, the herdr tests' way, plus the
// directory the calls log lands in.
func newHCFake(t *testing.T, rules []fakecli.Rule) (platform.Env, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "hc", rules); err != nil {
		t.Fatal(err)
	}
	return hcEnvFromDir(t, dir), dir
}

// TestErrorMapping is the exit-code and stderr contract: one row per named
// exit code (3, 7, 9, 10, 11, 124), plus a generic non-zero code, a stderr
// that is not the error JSON, and an error without hint.
func TestErrorMapping(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		exit       int
		stdout     string
		stderr     string
		wantKind   string
		wantMsg    string
		wantHint   string
		wantExit   int
		wantStdout string
	}{
		{"not_found exit 3", 3, "", `{"error":{"kind":"not_found","message":"no such agent 'x'","hint":"hc agents lists them"}}`, "not_found", "no such agent 'x'", "hc agents lists them", 3, ""},
		{"needs_target exit 7", 7, "", `{"error":{"kind":"needs_target","message":"no agent is running","hint":"start one with hc run --bg"}}`, "needs_target", "no agent is running", "start one with hc run --bg", 7, ""},
		{"blocked exit 9", 9, "", `{"error":{"kind":"blocked","message":"the command is blocked","hint":"unblock it with hc unblock"}}`, "blocked", "the command is blocked", "unblock it with hc unblock", 9, ""},
		{"status_mismatch exit 10", 10, "", `{"error":{"kind":"status_mismatch","message":"the status is not running","hint":"read hc server status"}}`, "status_mismatch", "the status is not running", "read hc server status", 10, ""},
		{"finished exit 11", 11, "", `{"error":{"kind":"finished","message":"the program finished","hint":"read its exit with hc run --bg"}}`, "finished", "the program finished", "read its exit with hc run --bg", 11, ""},
		{"run still running exit 124", 124, "", "pid 4242 still running\n", "", "pid 4242 still running", "", 124, ""},
		{"generic non-zero exit 1", 1, "", `{"error":{"kind":"blocked","message":"the disk is full"}}`, "blocked", "the disk is full", "", 1, ""},
		{"stderr is not the error JSON", 1, "", "boom: something exploded\n", "", "boom: something exploded", "", 1, ""},
		{"error without hint", 3, "", `{"error":{"kind":"not_found","message":"no such pane"}}`, "not_found", "no such pane", "", 3, ""},
		{"exit 0 returns stdout", 0, `{"ok":true}`, "", "", "", "", 0, `{"ok":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := newHCFake(t, []fakecli.Rule{{Argv: []string{"status", "--json"}, Stdout: tc.stdout, Stderr: tc.stderr, Code: tc.exit}})
			stdout, err := Run(ctx, env, "status")
			if tc.exit == 0 {
				if err != nil {
					t.Fatalf("exit 0: err = %v, want nil", err)
				}
				if stdout != tc.stdout {
					t.Fatalf("stdout = %q, want %q", stdout, tc.stdout)
				}
				return
			}
			if err == nil {
				t.Fatal("non-zero exit: err = nil, want *Error")
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %T (%v), want *Error", err, err)
			}
			if e.Kind != tc.wantKind || e.Message != tc.wantMsg || e.Hint != tc.wantHint || e.Exit != tc.wantExit {
				t.Fatalf("got kind=%q message=%q hint=%q exit=%d, want kind=%q message=%q hint=%q exit=%d", e.Kind, e.Message, e.Hint, e.Exit, tc.wantKind, tc.wantMsg, tc.wantHint, tc.wantExit)
			}
		})
	}
}

// TestRunMissingBinaryIsErrNotInstalled: an empty PATH is the missing binary
// — the sentinel, never an *Error.
func TestRunMissingBinaryIsErrNotInstalled(t *testing.T) {
	env := platform.Env{"PATH": t.TempDir()}
	_, err := Run(context.Background(), env, "status")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want errors.Is ErrNotInstalled", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Fatalf("missing binary is an *Error: %+v", e)
	}
	_, err = Version(context.Background(), env)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Version err = %v, want ErrNotInstalled", err)
	}
}

// TestRunSuccessReturnsStdout keeps the exit-0 contract that the probe
// steps build on: the raw stdout, no error.
func TestRunSuccessReturnsStdout(t *testing.T) {
	env, dir := newHCFake(t, []fakecli.Rule{{Argv: []string{"server", "status", "--json"}, Stdout: "one line\n"}})
	out, err := Run(context.Background(), env, "server", "status")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if out != "one line\n" {
		t.Fatalf("stdout = %q, want %q", out, "one line\n")
	}
	calls, err := fakecli.ReadCalls(filepath.Join(dir, "hc.calls.jsonl"))
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls = %+v, %v; want exactly one", calls, err)
	}
}
