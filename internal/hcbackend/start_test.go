package hcbackend

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	// startCaller is the fake's caller terminal (HC_TERMINAL_ID=t1): it
	// carries the soho.session label the run's --label must pick up.
	startCaller = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	// startRunResult is the fake's hc run answer: the full terminal record,
	// the address the readiness poll runs on.
	startRunResult = `{"address":"t7","name":"w7","state":"live"}`
	// startReadyIdle is one readiness get: reported and idle.
	startReadyIdle = `{"address":"t7","state":"live","reported":true,"status":{"state":"idle"}}`
)

// startEnv builds the Start fixture: the caller read, the run answer and
// the given readiness rules for `hc get t7 --json`, in that order.
func startEnv(t *testing.T, readyRules []fakecli.Rule) (platform.Env, func() []fakecli.Call) {
	t.Helper()
	rules := []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: startCaller}}
	rules = append(rules, readyRules...)
	return fakeHc(t, rules)
}

// runArgv is the exact hc run argv Start must launch for the fixture's
// worker (name w7, session sX), with the kind and the request's args after
// the "--".
func runArgv(kind string, args ...string) []string {
	argv := []string{"run", "--bg", "--json", "--name", "w7", "--label", "soho.session=sX", "--no-notify", "--outlive-parent", "--", kind}
	return append(argv, args...)
}

// TestStartArgvPerKind records the launch for each of the five kinds and
// asserts the full argv, element by element: --json once, in the options
// region, before the "--"; the executable is the kind's name; the request's
// args go out byte for byte.
func TestStartArgvPerKind(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "pi", "grok", "agy"} {
		t.Run(kind, func(t *testing.T) {
			env, calls := startEnv(t, []fakecli.Rule{
				{Argv: runArgv(kind, "--model", "m"), Stdout: startRunResult},
				{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
			})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: kind, Timeout: "3000", Args: []string{"--model", "m"}}, transport.Call{})
			if rep.Err != nil {
				t.Fatalf("err: %v", rep.Err)
			}
			if !rep.Valid || rep.Value != "t7" {
				t.Fatalf("rep: %+v want Valid with Value t7", rep)
			}
			wantArgv(t, calls(), [][]string{
				{"get", "t1", "--json"},
				runArgv(kind, "--model", "m"),
				{"get", "t7", "--json"},
			})
		})
	}
}

// TestStartArgvTokensByteForByte: the claude full-mode settings blob and the
// codex trust override go out as single argv elements, byte for byte, with
// no quoting added, and --json appears exactly once, before the "--".
func TestStartArgvTokensByteForByte(t *testing.T) {
	t.Run("claude settings blob", func(t *testing.T) {
		args := []string{"--model", "m", "--permission-mode", "bypassPermissions", "--settings", `{"enableAllProjectMcpServers":true}`}
		env, calls := startEnv(t, []fakecli.Rule{
			{Argv: runArgv("claude", args...), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000", Args: args}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, runArgv("claude", args...), {"get", "t7", "--json"}})
		assertJSONOnceBeforeSeparator(t, calls()[1].Argv)
	})

	t.Run("codex trust token", func(t *testing.T) {
		args := []string{"-m", "m", "-s", "workspace-write", "-a", "on-request", "--no-daemon", `-c`, `projects={'/tmp/w7'={trust_level="trusted"}}`}
		env, calls := startEnv(t, []fakecli.Rule{
			{Argv: runArgv("codex", args...), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "codex", Timeout: "3000", Args: args}, transport.Call{})
		if rep.Err != nil || !rep.Valid {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, runArgv("codex", args...), {"get", "t7", "--json"}})
		assertJSONOnceBeforeSeparator(t, calls()[1].Argv)
	})
}

// assertJSONOnceBeforeSeparator asserts --json appears exactly once in the
// argv, and that one occurrence is before the first "--".
func assertJSONOnceBeforeSeparator(t *testing.T, argv []string) {
	t.Helper()
	sep, count := -1, 0
	for i, a := range argv {
		if a == "--" && sep < 0 {
			sep = i
		}
		if a == "--json" {
			count++
			if sep >= 0 {
				t.Fatalf("--json at %d is after the -- at %d: %#v", i, sep, argv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("--json appears %d times, want once: %#v", count, argv)
	}
}

// TestStartCwd: a non-empty Cwd goes out as --cwd between --name and
// --label; an empty one leaves no --cwd at all.
func TestStartCwd(t *testing.T) {
	env, calls := startEnv(t, []fakecli.Rule{
		{Argv: []string{"run", "--bg", "--json", "--name", "w7", "--cwd", "/tmp/w7", "--label", "soho.session=sX", "--no-notify", "--outlive-parent", "--", "claude"}, Stdout: startRunResult},
		{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
	})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Cwd: "/tmp/w7", Timeout: "3000"}, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep: %+v", rep)
	}
	wantArgv(t, calls(), [][]string{
		{"get", "t1", "--json"},
		{"run", "--bg", "--json", "--name", "w7", "--cwd", "/tmp/w7", "--label", "soho.session=sX", "--no-notify", "--outlive-parent", "--", "claude"},
		{"get", "t7", "--json"},
	})
}

// TestStartRefusalsBeforeLaunch: an unsupported kind and every invalid
// name refuse before any hc launch (the fake records zero).
func TestStartRefusalsBeforeLaunch(t *testing.T) {
	t.Run("unsupported kind", func(t *testing.T) {
		env, calls := startEnv(t, nil)
		b := &backend{env: env, client: hc.New(env)}
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "opencode", Timeout: "3000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnsupported {
			t.Fatalf("rep: %+v want FailureUnsupported", rep)
		}
		want := "kind opencode does not report status under transport=hc; use transport=herdr"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if got := len(calls()); got != 0 {
			t.Fatalf("launches=%d want 0: %#v", got, calls())
		}
	})

	names := map[string]string{
		"":                      `worker name '' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		strings.Repeat("a", 33): `worker name '` + strings.Repeat("a", 33) + `' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		"Upper":                 `worker name 'Upper' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		"with.dot":              `worker name 'with.dot' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
		"has space":             `worker name 'has space' is not a valid hc terminal name (1-32 of a-z 0-9 - _)`,
	}
	for name, want := range names {
		t.Run("invalid name "+name, func(t *testing.T) {
			env, calls := startEnv(t, nil)
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Start(context.Background(), transport.Start{Name: name, Kind: "claude", Timeout: "3000"}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
				t.Fatalf("rep: %+v want FailureCommand", rep)
			}
			if rep.Err.Message != want {
				t.Fatalf("message %q want %q", rep.Err.Message, want)
			}
			if got := len(calls()); got != 0 {
				t.Fatalf("launches=%d want 0: %#v", got, calls())
			}
		})
	}
}

// TestStartNameBoundary32: a 32-character name is valid and starts.
func TestStartNameBoundary32(t *testing.T) {
	name := strings.Repeat("a", 32)
	argv := []string{"run", "--bg", "--json", "--name", name, "--label", "soho.session=sX", "--no-notify", "--outlive-parent", "--", "claude"}
	env, calls := startEnv(t, []fakecli.Rule{
		{Argv: argv, Stdout: `{"address":"t7","state":"live"}`},
		{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
	})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Start(context.Background(), transport.Start{Name: name, Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
		t.Fatalf("rep: %+v", rep)
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, argv, {"get", "t7", "--json"}})
}

// TestStartNameTaken: a run that fails with hc's name_taken error is a
// FailureConflict named after the worker, and the readiness poll never runs.
func TestStartNameTaken(t *testing.T) {
	env, calls := startEnv(t, []fakecli.Rule{
		{Argv: runArgv("claude"), Code: 1, Stderr: `{"error":{"kind":"name_taken","message":"terminal name w7 is already in use"}}`},
	})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureConflict {
		t.Fatalf("rep: %+v want FailureConflict", rep)
	}
	if rep.Err.Message != "name_taken: w7" {
		t.Fatalf("message %q want %q", rep.Err.Message, "name_taken: w7")
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, runArgv("claude")})
}

// TestStartRunFailure: a run failure with any other hc error kind is a
// FailureCommand carrying the hc error's own text.
func TestStartRunFailure(t *testing.T) {
	env, calls := startEnv(t, []fakecli.Rule{
		{Argv: runArgv("claude"), Code: 1, Stderr: `{"error":{"kind":"cwd_missing","message":"/nope is not a directory"}}`},
	})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
		t.Fatalf("rep: %+v want FailureCommand", rep)
	}
	want := "hc exited 1: cwd_missing: /nope is not a directory"
	if rep.Err.Message != want {
		t.Fatalf("message %q want %q", rep.Err.Message, want)
	}
	wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, runArgv("claude")})
}

// TestStartSessionUnresolvable: outside an hc terminal the session cannot
// be labelled, so Start refuses before any launch.
func TestStartSessionUnresolvable(t *testing.T) {
	env, calls := startEnv(t, nil)
	delete(env, "HC_TERMINAL_ID")
	b := &backend{env: env, client: hc.New(env)}
	rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
		t.Fatalf("rep: %+v want FailureUnavailable", rep)
	}
	if rep.Err.Message != "not inside an hc terminal: HC_TERMINAL_ID is not set" {
		t.Fatalf("message %q", rep.Err.Message)
	}
	if got := len(calls()); got != 0 {
		t.Fatalf("launches=%d want 0", got)
	}
}

// TestStartReadiness: the readiness poll, each case with a scripted fake
// get sequence on the new terminal's address.
func TestStartReadiness(t *testing.T) {
	newBackend := func(env platform.Env) *backend { return &backend{env: env, client: hc.New(env)} }
	startWorker := func(t *testing.T, readyRules []fakecli.Rule, timeout string) transport.Reply[string] {
		t.Helper()
		env, _ := startEnv(t, readyRules)
		return newBackend(env).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: timeout}, transport.Call{})
	}

	t.Run("null, working, then idle: ready, the address goes out", func(t *testing.T) {
		env, calls := startEnv(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":false,"status":null}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 3, Stdout: startReadyIdle},
		})
		rep := newBackend(env).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "10000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			runArgv("claude"),
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
		})
	})

	t.Run("done: ready", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"done"}}`},
		}, "3000")
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
	})

	t.Run("idle without reported: keeps polling until reported", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"idle"}}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: startReadyIdle},
		}, "3000")
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
	})

	t.Run("blocked with msg", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission: write to file"}}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 is blocked during startup: permission: write to file"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
	})

	t.Run("blocked without msg: ends at startup", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked"}}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 is blocked during startup"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
	})

	t.Run("finished with exit code and reason", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"finished","exit_code":1,"reason":"exited"}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 exited during startup (exit 1, exited)"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
	})

	t.Run("finished with exit code only", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: `{"address":"t7","state":"finished","exit_code":130}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 exited during startup (exit 130)"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
	})

	t.Run("get exit 3: the exited failure without the parts hc does not report", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal t7"}}`},
		}, "3000")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 exited during startup"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
	})

	t.Run("never ready: the timeout failure, bounded, no error to report", func(t *testing.T) {
		start := time.Now()
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		}, "300")
		elapsed := time.Since(start)
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 still not ready after 0.3s"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
		if elapsed < 300*time.Millisecond {
			t.Fatalf("elapsed %v: the poll gave up before the 300 ms deadline", elapsed)
		}
		if elapsed >= 300*time.Millisecond+2*time.Second {
			t.Fatalf("elapsed %v: past the deadline plus 2 s", elapsed)
		}
	})

	t.Run("never ready and the gets keep failing: the last error goes in the timeout message", func(t *testing.T) {
		rep := startWorker(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
		}, "300")
		if rep.Err == nil || rep.Err.Kind != transport.FailureCommand {
			t.Fatalf("rep: %+v want FailureCommand", rep)
		}
		want := "agent w7 still not ready after 0.3s: hc exited 1: core_unreachable: connection refused"
		if rep.Err.Message != want {
			t.Fatalf("message %q want %q", rep.Err.Message, want)
		}
	})

	t.Run("a cancelled context stops the poll", func(t *testing.T) {
		env, _ := startEnv(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		})
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(50*time.Millisecond, cancel)
		defer timer.Stop()
		rep := newBackend(env).Start(ctx, transport.Start{Name: "w7", Kind: "claude", Timeout: "10000"}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureCancelled {
			t.Fatalf("rep: %+v want FailureCancelled", rep)
		}
		if rep.Err.Message != "context canceled" {
			t.Fatalf("message %q", rep.Err.Message)
		}
	})

	t.Run("a transient get failure, then idle: ready", func(t *testing.T) {
		env, calls := startEnv(t, []fakecli.Rule{
			{Argv: runArgv("claude"), Stdout: startRunResult},
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Code: 1, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`},
			{Argv: []string{"get", "t7", "--json"}, Call: 2, Stdout: startReadyIdle},
		})
		rep := newBackend(env).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "10000"}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v want ready with Value t7", rep)
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			runArgv("claude"),
			{"get", "t7", "--json"},
			{"get", "t7", "--json"},
		})
	})
}

// TestStartTimeoutParsing: the deadline is milliseconds as a decimal
// string, as herdr takes it; empty, zero or invalid mean 60000.
func TestStartTimeoutParsing(t *testing.T) {
	if d := startTimeout(""); d != 60*time.Second {
		t.Fatalf("empty: %v want 60s", d)
	}
	if d := startTimeout("0"); d != 60*time.Second {
		t.Fatalf("zero: %v want 60s", d)
	}
	if d := startTimeout("abc"); d != 60*time.Second {
		t.Fatalf("invalid: %v want 60s", d)
	}
	if d := startTimeout("-5"); d != 60*time.Second {
		t.Fatalf("negative: %v want 60s", d)
	}
	if d := startTimeout("60000"); d != 60*time.Second {
		t.Fatalf("60000: %v want 60s", d)
	}
	if d := startTimeout("300"); d != 300*time.Millisecond {
		t.Fatalf("300: %v want 300ms", d)
	}
}

// TestStartDeadlineSeconds pins the timeout message's seconds rendering.
func TestStartDeadlineSeconds(t *testing.T) {
	if s := startDeadlineSeconds(60 * time.Second); s != "60" {
		t.Fatalf("%q want 60", s)
	}
	if s := startDeadlineSeconds(300 * time.Millisecond); s != "0.3" {
		t.Fatalf("%q want 0.3", s)
	}
	if s := startDeadlineSeconds(250 * time.Millisecond); s != "0.25" {
		t.Fatalf("%q want 0.25", s)
	}
}
