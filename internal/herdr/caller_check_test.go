package herdr

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// callerCheckFixture installs a fake herdr in one directory and returns an
// env that points PATH at it. The ancestry comes from the parents table
// through platform.OverrideAncestorParents (the same test point on every
// system, no ps and no process snapshot); an unscripted pid is a failed
// read, like a pid that is not running.
func callerCheckFixture(t *testing.T, herdrRules []fakecli.Rule, parents map[int]int) platform.Env {
	t.Helper()
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "herdr", herdrRules); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.OverrideAncestorParents(parents))
	env := platform.Env{}
	for _, item := range fakecli.Env(nil, dir) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["TMPDIR"] = t.TempDir()
	// The host's session must not leak into the check: the claim and the
	// delegation variable are always set or deleted explicitly.
	delete(env, "HERDR_PANE_ID")
	delete(env, "SOHO_DELEGATED_PANE")
	return env
}

// selfPID is this test process's own pid, where every scripted tree starts.
func selfPID() int { return os.Getpid() }

// processInfoRule is the fake rule for one pane's process-info read.
func processInfoRule(pane string, shellPID int) fakecli.Rule {
	return fakecli.Rule{
		Argv:   []string{"pane", "process-info", "--pane", pane},
		Stdout: `{"result":{"process_info":{"shell_pid":` + strconv.Itoa(shellPID) + `}}}`,
	}
}

func TestCheckCallerPane(t *testing.T) {
	const pane = "w1:p9"
	t.Run("a tree that reaches the shell_pid is verified", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(pane, 4000)},
			map[int]int{selfPID(): 4000, 4000: 1})
		env["HERDR_PANE_ID"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerVerified || got.Pane != pane || got.ShellPID != 4000 {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("a tree read to the root without the shell_pid is a mismatch", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(pane, 4000)},
			map[int]int{selfPID(): 3000, 3000: 1})
		env["HERDR_PANE_ID"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.Pane != pane || got.ShellPID != 4000 {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("a failed process-info is unknown", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{{Argv: []string{"pane", "process-info", "--pane", pane}, Stderr: `{"error":{"code":"pane_not_found","message":"gone"}}`, Code: 1}},
			map[int]int{selfPID(): 1})
		env["HERDR_PANE_ID"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("a process-info without shell_pid is unknown", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: `{"result":{"process_info":{}}}`}},
			nil)
		env["HERDR_PANE_ID"] = pane
		if got := CheckCallerPane(env); got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		ResetCallerCheck()
		env = callerCheckFixture(t,
			[]fakecli.Rule{{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: `{"result":{"process_info":{"shell_pid":0}}}`}},
			nil)
		env["HERDR_PANE_ID"] = pane
		if got := CheckCallerPane(env); got.State != CallerUnknown {
			t.Fatalf("shell_pid 0: CheckCallerPane=%#v", got)
		}
	})
	t.Run("a failed ancestry read midway is unknown", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(pane, 4000)},
			map[int]int{selfPID(): 314159}) // 314159 unscripted: the hop fails
		env["HERDR_PANE_ID"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("a cycle is unknown", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(pane, 4000)},
			map[int]int{selfPID(): 3000, 3000: 3001, 3001: 3000})
		env["HERDR_PANE_ID"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("more than 64 hops is unknown, not a mismatch", func(t *testing.T) {
		ResetCallerCheck()
		// 66 hops: self -> 9001 -> ... -> 9066 -> 1, no shell_pid (4000).
		table := map[int]int{}
		prev := selfPID()
		for i := 1; i <= 65; i++ {
			next := 9000 + i
			if i == 65 {
				table[prev] = 1
			} else {
				table[prev] = next
			}
			prev = next
		}
		env := callerCheckFixture(t, []fakecli.Rule{processInfoRule(pane, 4000)}, table)
		env["HERDR_PANE_ID"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("no HERDR_PANE_ID is unknown", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t, nil, nil)
		got := CheckCallerPane(env)
		if got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("a delegated pane equal to HERDR_PANE_ID is verified with no herdr call", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t, nil, nil) // no rules at all
		env["HERDR_PANE_ID"] = pane
		env["SOHO_DELEGATED_PANE"] = pane
		got := CheckCallerPane(env)
		if got.State != CallerVerified || got.Pane != pane {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		calls, err := fakecli.ReadCallsForConfig(env["SOHO_FAKECLI_CONFIG"] + "/herdr.json")
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(calls) != 0 {
			t.Fatalf("the delegated check called herdr: %#v", calls)
		}
	})
	t.Run("a different SOHO_DELEGATED_PANE follows the normal rule", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(pane, 4000)},
			map[int]int{selfPID(): 3000, 3000: 1})
		env["HERDR_PANE_ID"] = pane
		env["SOHO_DELEGATED_PANE"] = "w1:pother"
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
	t.Run("the result is memoized per process: one process-info call", func(t *testing.T) {
		ResetCallerCheck()
		// A fresh pane (w1:p10): the memo holds no entry for its key, so
		// the first call computes (one process-info read) and the second
		// serves the memo.
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule("w1:p10", 4000)},
			map[int]int{selfPID(): 3000, 3000: 1})
		env["HERDR_PANE_ID"] = "w1:p10"
		first := CheckCallerPane(env)
		second := CheckCallerPane(env)
		if first != second || first.State != CallerMismatch || first.Pane != "w1:p10" {
			t.Fatalf("first=%#v second=%#v", first, second)
		}
		calls, err := fakecli.ReadCallsForConfig(env["SOHO_FAKECLI_CONFIG"] + "/herdr.json")
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		n := 0
		for _, call := range calls {
			if len(call.Argv) >= 3 && call.Argv[0] == "pane" && call.Argv[1] == "process-info" && call.Argv[2] == "--pane" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("process-info calls=%d want 1", n)
		}
	})
}
