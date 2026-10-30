package cli

import (
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestIsInteractiveReadyKeepsTheFailOpenOneAttemptContract: isInteractiveReady
// runs exactly one agent get with the adapter's timeout and fails open — a
// failed call, an empty or unparsable body reads as ready; only an explicit
// interactive_ready false in a parsed body reads as not ready.
func TestIsInteractiveReadyKeepsTheFailOpenOneAttemptContract(t *testing.T) {
	run := func(t *testing.T, rule fakecli.Rule) (bool, []fakecli.Call) {
		t.Helper()
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{rule}); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"PATH": bin, "TMPDIR": t.TempDir(), "SOHO_FAKECLI_CONFIG": bin}
		ready := isInteractiveReady("worker", herdr.New(env))
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return ready, calls
	}
	argv := []string{"agent", "get", "worker"}
	t.Run("a failed get reads as ready, one attempt", func(t *testing.T) {
		ready, calls := run(t, fakecli.Rule{Argv: argv, Code: 1, Stderr: "boom"})
		if !ready {
			t.Fatalf("a failed agent get must read as ready (fail-open)")
		}
		if len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("an empty body reads as ready, one attempt", func(t *testing.T) {
		ready, calls := run(t, fakecli.Rule{Argv: argv, Stdout: ""})
		if !ready {
			t.Fatalf("an empty body must read as ready (fail-open)")
		}
		if len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("an unparsable body reads as ready, one attempt", func(t *testing.T) {
		ready, calls := run(t, fakecli.Rule{Argv: argv, Stdout: "not json"})
		if !ready {
			t.Fatalf("an unparsable body must read as ready (fail-open)")
		}
		if len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("only an explicit interactive_ready false reads as not ready", func(t *testing.T) {
		ready, calls := run(t, fakecli.Rule{Argv: argv, Stdout: `{"result":{"agent":{"interactive_ready":false}}}`})
		if ready {
			t.Fatalf("an explicit interactive_ready false must read as not ready")
		}
		if len(calls) != 1 {
			t.Fatalf("one attempt expected, got %#v", calls)
		}
	})
	t.Run("a parsed body without the field reads as ready", func(t *testing.T) {
		ready, _ := run(t, fakecli.Rule{Argv: argv, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`})
		if !ready {
			t.Fatalf("a parsed body without interactive_ready must read as ready (fail-open)")
		}
	})
}
