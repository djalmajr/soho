package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA659 F2: consumer controls pinning the CURRENT per-transport
// list-failure behavior of EnforceWorkerCap that the shared capacity
// result must preserve. The herdr list terminates through its existing
// failure contract: an unsuccessful command passes the raw herdr output
// through to the platform streams and exits with herdr's own code, and a
// successful command without an agent list dies 4 through the friction
// diagnostic. The hc list failure currently drops the error and counts
// zero, so the cap passes below a positive cap — an existing limitation
// this slice intentionally does not fix, pinned here so a later refactor
// that starts honoring the error cannot change the policy silently. The
// unlimited cap returns before any native list or caller lookup. All
// cases reuse the existing tm9 live-worker fixture and the real
// EnforceWorkerCap entry; no alternate capacity model is created.

// dja659CapRows are three roster rows that a successful list naming all
// three panes counts as live, so a positive cap of 3 is reached exactly.
var dja659CapRows = []string{
	"w1\tp1\tgrok\timplementer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\timplementer\tbuild",
	"w2\tp2\tgrok\treviewer\txai\t1\t/repo\tnow\tgrok-4.7\tfull\treviewer\treview",
	"w3\tp3\tgrok\ttasker\txai\t1\t/repo\tnow\tgrok-4.7\tfull\ttasker\tbuild",
}

const dja659CapList = `{"result":{"agents":[{"name":"w1","pane_id":"p1"},{"name":"w2","pane_id":"p2"},{"name":"w3","pane_id":"p3"}]}}`

func TestDJA659CapacityPolicyControls(t *testing.T) {
	t.Run("an unlimited cap returns before any native list or caller lookup", func(t *testing.T) {
		// The roster and the list below reach the positive cap (the paired
		// subtest dies 8 on the same fixture), so a zero count here would
		// be the only way the no-call assertion is meaningful. At
		// max_workers=0 the cap must return before the list is attempted,
		// so the fake never sees a call: no call log, or an empty one.
		env, cwd, _ := tm9LiveWorkerFixture(t, dja659CapRows, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: dja659CapList}})
		env["SOHO_MAX_WORKERS"] = "0"
		ctx := LoadConfig(env, cwd)
		func() {
			defer func() {
				if value := recover(); value != nil {
					t.Fatalf("the unlimited cap terminated: %v", value)
				}
			}()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
		calls, err := fakecli.ReadCalls(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.calls.jsonl"))
		if os.IsNotExist(err) {
			// No call log means no native call: the contract holds.
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 0 {
			t.Fatalf("native calls at the unlimited cap: %#v", calls)
		}
	})

	t.Run("the same fixture at a positive cap dies 8, so the no-call case is not vacuous", func(t *testing.T) {
		// Pair control: the identical roster and list reach the cap when
		// max_workers is positive — without a caller pane to exclude, all
		// three live rows count and the established refusal goes out.
		env, cwd, _ := tm9LiveWorkerFixture(t, dja659CapRows, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: dja659CapList}})
		env["SOHO_MAX_WORKERS"] = "3"
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 8 || !strings.Contains(exit.Msg, "max_workers=3 reached (3 live: w1 w2 w3)") {
			t.Fatalf("cap result=%#v, want the established refusal for the three live rows", recovered)
		}
	})

	t.Run("a failed herdr list terminates through the existing failure contract", func(t *testing.T) {
		// An unsuccessful herdr command: the raw herdr stderr passes
		// through to the platform's stderr and herdr's own exit code (1)
		// is used through a plain die — the cap path must not swallow the
		// failure into a zero count, which would be a new failure policy.
		env, cwd, _ := tm9LiveWorkerFixture(t, dja659CapRows, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stderr: "agent list: socket closed", Code: 1}})
		env["SOHO_MAX_WORKERS"] = "3"
		ctx := LoadConfig(env, cwd)
		var captured strings.Builder
		oldErr := platform.Stderr
		platform.Stderr = &captured
		defer func() { platform.Stderr = oldErr }()
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 1 || exit.Msg != "" || exit.Friction {
			t.Fatalf("failure contract=%#v, want the plain die with herdr's own exit code and no friction marking", recovered)
		}
		if !strings.Contains(captured.String(), "agent list: socket closed") {
			t.Fatalf("the raw herdr stderr did not pass through: %q", captured.String())
		}
	})

	t.Run("a herdr list without an agent list dies 4 through the friction diagnostic", func(t *testing.T) {
		// A successful herdr command that answers without the agent list:
		// the established missing-list friction diagnostic goes out, exit
		// 4 — the other branch of the same existing failure contract.
		env, cwd, _ := tm9LiveWorkerFixture(t, dja659CapRows, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{}}`}})
		env["SOHO_MAX_WORKERS"] = "3"
		ctx := LoadConfig(env, cwd)
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 4 || exit.Msg != "herdr agent list returned no agent list" || !exit.Friction {
			t.Fatalf("failure contract=%#v, want the missing-list friction diagnostic", recovered)
		}
	})

	t.Run("a failed hc list currently counts zero and returns below a positive cap", func(t *testing.T) {
		// The existing hc limitation, pinned honestly and intentionally
		// not fixed here: the cap path consumes only the list's value,
		// and a failed hc list is an error without a value, so the three
		// live rows count zero and the positive cap passes. The caller
		// session resolves (the label names the workspace the roster was
		// written under), so the state dir resolves and the real `hc ls`
		// for that session is attempted and fails; the call log proves the
		// failure was real and not a bypassed fixture.
		env, cwd, state := tm9LiveWorkerFixture(t, dja659CapRows, nil)
		env["SOHO_TRANSPORT"] = "hc"
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_MAX_WORKERS"] = "3"
		bin := env["SOHO_FAKECLI_CONFIG"]
		if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","labels":{"soho.session":"ws"}}`},
			{Argv: []string{"ls", "--label", "soho.session=ws", "--json"}, Stderr: "session ws not found", Code: 1},
		}); err != nil {
			t.Fatal(err)
		}
		ctx := LoadConfig(env, cwd)
		func() {
			defer func() {
				if value := recover(); value != nil {
					t.Fatalf("the failed hc list must not terminate today (existing limitation, not fixed here): %v", value)
				}
			}()
			EnforceWorkerCap(&ctx, env, cwd)
		}()
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
		if err != nil {
			t.Fatalf("no hc call log: the list was never attempted: %v", err)
		}
		lsCalls := 0
		for _, c := range calls {
			if dja659SameCapArgv(c.Argv, []string{"ls", "--label", "soho.session=ws", "--json"}) {
				lsCalls++
			}
		}
		if lsCalls != 1 {
			t.Fatalf("the failing hc ls was not attempted exactly once: %#v", calls)
		}
		roster, err := os.ReadFile(filepath.Join(state, "agents.tsv"))
		if err != nil || !strings.Contains(string(roster), "w1\tp1\t") {
			t.Fatalf("the roster changed below the cap: %q err=%v", roster, err)
		}
	})
}

func dja659SameCapArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
