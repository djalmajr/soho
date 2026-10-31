package hcbackend

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// TestSession: the session is the caller terminal's soho.session label, or
// "hc-<address>" when the label is absent; outside an hc terminal it is an
// error.
func TestSession(t *testing.T) {
	t.Run("a label present returns the label", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s1"}}`}})
		got, err := Session(env)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "s1" {
			t.Fatalf("session %q want s1", got)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}})
	})

	t.Run("a label absent returns hc-<address>", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live"}`}})
		got, err := Session(env)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "hc-t1" {
			t.Fatalf("session %q want hc-t1", got)
		}
	})

	t.Run("no HC_TERMINAL_ID returns an error without a launch", func(t *testing.T) {
		env, calls := fakeHc(t, nil)
		delete(env, "HC_TERMINAL_ID")
		_, err := Session(env)
		if !errors.Is(err, ErrNotInHcTerminal) {
			t.Fatalf("err: %v want ErrNotInHcTerminal", err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("calls %v want none", got)
		}
	})

	t.Run("a failed get is an error, not the not-inside sentinel", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"core down"}}`}})
		_, err := Session(env)
		if err == nil {
			t.Fatal("no err")
		}
		if errors.Is(err, ErrNotInHcTerminal) {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a get without a label and an address is an error", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: `{"state":"live"}`}})
		_, err := Session(env)
		if err == nil {
			t.Fatal("no err")
		}
	})
}

// TestSessionOncePerServices: the session (value or error) is computed at
// most once per Services, through the instance's runner.
func TestSessionOncePerServices(t *testing.T) {
	t.Run("a second read never re-issues the session get", func(t *testing.T) {
		var gets []string
		env := platform.Env{"HC_TERMINAL_ID": "t9"}
		b := &backend{env: env, client: hc.New(env)}
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			if reflect.DeepEqual(args, []string{"get", "t9", "--json"}) {
				gets = append(gets, "get")
			}
			zero := 0
			return platform.RunResult{Status: &zero, Stdout: `{"address":"t9","state":"live","labels":{"soho.session":"sX"},"terminals":[]}`}
		}
		_ = b.Caller()
		_ = b.Caller()
		_ = b.List(context.Background(), transport.Scope{}, transport.Call{})
		if len(gets) != 1 {
			t.Fatalf("session gets %d want 1", len(gets))
		}
	})

	t.Run("a failed session read is memoized too", func(t *testing.T) {
		var gets int
		env := platform.Env{"HC_TERMINAL_ID": "t9"}
		b := &backend{env: env, client: hc.New(env)}
		b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
			gets++
			exit := 1
			return platform.RunResult{Status: &exit, Stderr: `{"error":{"kind":"internal","message":"core down"}}`}
		}
		first, err1 := b.session()
		second, err2 := b.session()
		if err1 == nil || err2 == nil {
			t.Fatalf("errs: %v %v", err1, err2)
		}
		if first != second {
			t.Fatalf("values %q %q", first, second)
		}
		if gets != 1 {
			t.Fatalf("session reads %d want 1", gets)
		}
	})
}
