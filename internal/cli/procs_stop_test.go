package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func procsStopRow(t *testing.T, f *procsFixture, pid int, owner string) core.ProcRow {
	t.Helper()
	started, name, live := platform.ReadProcFull(pid, f.env)
	if live != platform.ProcRunning || started == "" || name == "" {
		t.Fatalf("owned sleeper pid=%d identity=%q name=%q state=%v GOOS=%s PATH=%q", pid, started, name, live, runtime.GOOS, f.env.Get("PATH"))
	}
	return core.ProcRow{Pid: pid, Started: started, Name: name, Owner: owner, Pane: "p-" + owner, Created: core.FrictionISO(platform.Now())}
}

func assertProcsStopAlive(t *testing.T, f *procsFixture, row core.ProcRow) {
	t.Helper()
	started, name, live := platform.ReadProcFull(row.Pid, f.env)
	if live != platform.ProcRunning || !platform.SameStarted(started, row.Started) || name != row.Name {
		t.Fatalf("owned sleeper changed: row=%+v started=%q name=%q state=%v", row, started, name, live)
	}
}

func TestProcsStopOwnRunning(t *testing.T) {
	f := newProcsFixture(t)
	row := procsStopRow(t, f, f.sleeper(t), "worker")
	f.writeProcs(t, row)
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(row.Pid))
	if code != 0 || out != fmt.Sprintf("stopped process %d (%s)\n", row.Pid, row.Name) || errOut != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	if procAlive(t, f.env, row.Pid) || len(f.procsRows(t)) != 0 {
		t.Fatal("own process survived or its registry row remains")
	}
}

func TestProcsStopForeign(t *testing.T) {
	f := newProcsFixture(t)
	row := procsStopRow(t, f, f.sleeper(t), "other")
	f.writeProcs(t, row)
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(row.Pid))
	if code != 2 || out != "" || !strings.Contains(errOut, fmt.Sprintf("procs stop: refusing %d: registered process belongs to another agent\n", row.Pid)) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	assertProcsStopAlive(t, f, row)
	if rows := f.procsRows(t); len(rows) != 1 || rows[0] != row {
		t.Fatalf("foreign row changed: %+v", rows)
	}
}

func TestProcsStopUnregistered(t *testing.T) {
	f := newProcsFixture(t)
	row := procsStopRow(t, f, f.sleeper(t), "worker")
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(row.Pid))
	if code != 2 || out != "" || !strings.Contains(errOut, "pid is not registered") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	assertProcsStopAlive(t, f, row)
}

func TestProcsStopInvalidAndProtected(t *testing.T) {
	f := newProcsFixture(t)
	args := []string{"0", "-1", "-123", "1", "abc", "+5", "1e3", "", "999999999999999999999999999", strconv.Itoa(os.Getpid()), strconv.Itoa(platform.ParentPID())}
	code, out, errOut := f.run(t, append([]string{"procs", "stop"}, args...)...)
	if code != 2 || out != "" || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != len(args) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	for _, arg := range args {
		cause := "not a positive decimal integer pid"
		if arg == "1" || arg == strconv.Itoa(os.Getpid()) || arg == strconv.Itoa(platform.ParentPID()) {
			cause = "pid is system init, this soho process, or its parent"
		}
		if !strings.Contains(errOut, "procs stop: refusing "+arg+": "+cause+"\n") {
			t.Fatalf("no refusal for %q: %q", arg, errOut)
		}
	}
}

func TestProcsStopReused(t *testing.T) {
	f := newProcsFixture(t)
	live := procsStopRow(t, f, f.sleeper(t), "worker")
	row := live
	row.Started = "Mon Jan  1 00:00:00 1990"
	f.writeProcs(t, row)
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(row.Pid))
	if code != 0 || out != fmt.Sprintf("process %d was reused; not stopped\n", row.Pid) || errOut != "" || len(f.procsRows(t)) != 0 {
		t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, f.procsRows(t))
	}
	assertProcsStopAlive(t, f, live)
}

func TestProcsStopMixed(t *testing.T) {
	f := newProcsFixture(t)
	own := procsStopRow(t, f, f.sleeper(t), "worker")
	foreign := procsStopRow(t, f, f.sleeper(t), "other")
	f.writeProcs(t, own, foreign)
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(own.Pid), strconv.Itoa(foreign.Pid))
	if code != 2 || out != fmt.Sprintf("stopped process %d (%s)\n", own.Pid, own.Name) || !strings.Contains(errOut, fmt.Sprintf("procs stop: refusing %d: ", foreign.Pid)) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	if procAlive(t, f.env, own.Pid) {
		t.Fatal("own process survived")
	}
	assertProcsStopAlive(t, f, foreign)
	if rows := f.procsRows(t); len(rows) != 1 || rows[0] != foreign {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestProcsStopOwnerResolution(t *testing.T) {
	for _, mode := range []string{"missing-pane", "unknown-agent", "explicit-agent"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcsFixture(t)
			row := core.ProcRow{Pid: f.sleeperGone(t), Started: "Mon Jan  1 00:00:00 1990", Name: "sleep", Owner: "other", Pane: "p-other", Created: core.FrictionISO(platform.Now())}
			if mode == "explicit-agent" {
				row = procsStopRow(t, f, f.sleeper(t), "other")
			}
			f.writeProcs(t, row)
			f.env["HERDR_PANE_ID"] = "not-in-roster"
			args := []string{"procs", "stop", strconv.Itoa(row.Pid), "123456789"}
			if mode == "unknown-agent" {
				args = append(args, "--agent", "ghost")
			} else if mode == "explicit-agent" {
				args = []string{"procs", "stop", strconv.Itoa(row.Pid), "--agent", "other"}
			}
			code, out, errOut := f.run(t, args...)
			if mode == "explicit-agent" {
				if code != 0 || errOut != "" || out != fmt.Sprintf("stopped process %d (%s)\n", row.Pid, row.Name) || procAlive(t, f.env, row.Pid) || len(f.procsRows(t)) != 0 {
					t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
				}
			} else {
				cause := fmt.Sprintf("no calling agent in the roster (pane not-in-roster, state dir %s)", f.state)
				if mode == "unknown-agent" {
					cause = "--agent ghost does not match the calling pane's agent "
				}
				if code != 2 || out != "" || strings.Count(errOut, cause) != 2 {
					t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
				}
				if rows := f.procsRows(t); len(rows) != 1 || rows[0] != row {
					t.Fatalf("unresolved caller changed registry: %+v", rows)
				}
			}
		})
	}
}

// Port of the review's naturally exited child: no signalling is needed.
func TestProcsStopExitChild(t *testing.T) {
	if os.Getenv("SOHO_PROCS_STOP_EXIT_CHILD") == "1" {
		os.Exit(0)
	}
}

func procsStopExitedChild(t *testing.T, f *procsFixture) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcsStopExitChild$")
	for _, item := range f.env.List() {
		if !strings.HasPrefix(item, "SOHO_FAKECLI_CONFIG=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "SOHO_PROCS_STOP_EXIT_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, _, live := platform.ReadProcFull(pid, f.env); live != platform.ProcGone {
		t.Fatalf("naturally exited child pid=%d state=%v GOOS=%s", pid, live, runtime.GOOS)
	}
	return pid
}

func procsStopOldRow(pid int, owner string) core.ProcRow {
	return core.ProcRow{Pid: pid, Started: "Mon Jan  1 00:00:00 1990", Name: "owned-exited-child", Owner: owner, Pane: "p-" + owner, Created: core.FrictionISO(platform.Now())}
}

func TestProcsStopRound2PaneOwner(t *testing.T) {
	for _, mode := range []string{"impersonation", "same-agent", "unmapped", "empty-pane", "unknown-agent"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcsFixture(t)
			owner, selected := "worker", "worker"
			if mode == "impersonation" {
				owner, selected = "other", "other"
			} else if mode == "unknown-agent" {
				selected = "ghost"
			} else if mode == "unmapped" {
				f.env["HERDR_PANE_ID"] = "not-in-roster"
			} else if mode == "empty-pane" {
				f.env["HERDR_PANE_ID"] = ""
			}
			row := procsStopOldRow(procsStopExitedChild(t, f), owner)
			f.writeProcs(t, row)
			args := []string{"procs", "stop", "--agent", selected, strconv.Itoa(row.Pid)}
			refused := mode == "impersonation" || mode == "unknown-agent"
			if refused {
				args = append(args, "0")
			}
			code, out, errOut := f.run(t, args...)
			rows := f.procsRows(t)
			t.Logf("mode=%s code=%d out=%q stderr=%q rows=%+v", mode, code, out, errOut, rows)
			if refused {
				cause := "--agent " + selected + " does not match the calling pane's agent worker"
				want := fmt.Sprintf("procs stop: refusing %d: %s\nprocs stop: refusing 0: %s\n", row.Pid, cause, cause)
				if code != 2 || out != "" || errOut != want || len(rows) != 1 || rows[0] != row {
					t.Fatal("pane agent override was not refused for every PID or mutated the registry")
				}
			} else if code != 0 || out != fmt.Sprintf("process %d already gone\n", row.Pid) || errOut != "" || len(rows) != 0 {
				t.Fatal("legitimate owner selection failed")
			}
		})
	}
}

func TestProcsStopRound2AllRows(t *testing.T) {
	for _, mode := range []string{"two-owned", "foreign-first", "foreign-last", "duplicate-arguments"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcsFixture(t)
			pid := procsStopExitedChild(t, f)
			old := procsStopOldRow(pid, "worker")
			current := old
			current.Started = "Sun Oct 25 12:00:00 2026"
			if mode == "foreign-first" {
				old.Owner, old.Pane = "other", "p-other"
			} else if mode == "foreign-last" {
				current.Owner, current.Pane = "other", "p-other"
			}
			for _, row := range []core.ProcRow{old, current} {
				if err := core.UpsertProc(f.state, row); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"procs", "stop", strconv.Itoa(pid)}
			lines := 2
			if mode == "duplicate-arguments" {
				args = append(args, strconv.Itoa(pid))
				lines = 4
			} else if strings.HasPrefix(mode, "foreign-") {
				lines = 1
			}
			code, out, errOut := f.run(t, args...)
			rows := f.procsRows(t)
			t.Logf("mode=%s code=%d out=%q stderr=%q rows=%+v", mode, code, out, errOut, rows)
			if code != 0 || out != strings.Repeat(fmt.Sprintf("process %d already gone\n", pid), lines) || errOut != "" {
				t.Fatal("all owned rows were not processed")
			}
			if mode == "foreign-first" {
				if len(rows) != 1 || rows[0] != old {
					t.Fatal("foreign first row changed")
				}
			} else if mode == "foreign-last" {
				if len(rows) != 1 || rows[0] != current {
					t.Fatal("foreign last row changed")
				}
			} else if len(rows) != 0 {
				t.Fatal("owned row was left behind")
			}
		})
	}
}

func TestProcsStopRound2LivePaneOwner(t *testing.T) {
	for _, mode := range []string{"impersonation", "same-agent", "unmapped"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcsFixture(t)
			owner := "worker"
			if mode == "impersonation" {
				owner = "other"
			} else if mode == "unmapped" {
				f.env["HERDR_PANE_ID"] = "not-in-roster"
			}
			row := procsStopRow(t, f, f.sleeper(t), owner)
			f.writeProcs(t, row)
			code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(row.Pid), "--agent", owner)
			rows := f.procsRows(t)
			if mode == "impersonation" {
				want := fmt.Sprintf("procs stop: refusing %d: --agent other does not match the calling pane's agent worker\n", row.Pid)
				if code != 2 || out != "" || errOut != want || len(rows) != 1 || rows[0] != row {
					t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, rows)
				}
				assertProcsStopAlive(t, f, row)
			} else if code != 0 || out != fmt.Sprintf("stopped process %d (%s)\n", row.Pid, row.Name) || errOut != "" || len(rows) != 0 || procAlive(t, f.env, row.Pid) {
				t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, rows)
			}
		})
	}
}

func TestProcsStopRound2LiveAllRows(t *testing.T) {
	for _, mode := range []string{"stale-current", "foreign-stale-own"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcsFixture(t)
			current := procsStopRow(t, f, f.sleeper(t), "worker")
			old := current
			old.Started = "Mon Jan  1 00:00:00 1990"
			rows := []core.ProcRow{old, current}
			want := fmt.Sprintf("process %d was reused; not stopped\nstopped process %d (%s)\n", current.Pid, current.Pid, current.Name)
			if mode == "foreign-stale-own" {
				current.Owner, current.Pane = "other", "p-other"
				rows = []core.ProcRow{current, old}
				want = fmt.Sprintf("process %d was reused; not stopped\n", current.Pid)
			}
			f.writeProcs(t, rows...)
			code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(current.Pid))
			remaining := f.procsRows(t)
			if code != 0 || out != want || errOut != "" {
				t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, remaining)
			}
			if mode == "foreign-stale-own" {
				if len(remaining) != 1 || remaining[0] != current {
					t.Fatalf("foreign row changed: %+v", remaining)
				}
				assertProcsStopAlive(t, f, current)
			} else if len(remaining) != 0 || procAlive(t, f.env, current.Pid) {
				t.Fatalf("current process or row survived: %+v", remaining)
			}
		})
	}
}

// Replays the review's reconstructed current identity. The stopped child
// is already reaped; a refused tree snapshot guarantees no signal path.
func TestProcsStopRound2ReviewStaleCurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix ps identity replay; native live coverage is LiveAllRows")
	}
	f := newProcsFixture(t)
	pid := procsStopExitedChild(t, f)
	old := procsStopOldRow(pid, "worker")
	current := old
	current.Started = "Sun Oct 25 12:00:00 2026"
	for _, row := range []core.ProcRow{old, current} {
		if err := core.UpsertProc(f.state, row); err != nil {
			t.Fatal(err)
		}
	}
	identityArgs := []string{"-o", "lstart=,state=,comm=", "-p", strconv.Itoa(pid)}
	if _, err := fakecli.Install(t, f.fakeDir, "ps", []fakecli.Rule{
		{Argv: identityArgs, Stdout: current.Started + " S owned-exited-child\n"},
		{AnyArgs: true, Code: 9},
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(pid))
	rows := f.procsRows(t)
	t.Logf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, rows)
	if code != 4 || out != fmt.Sprintf("process %d was reused; not stopped\n", pid) || !strings.Contains(errOut, "ps -A failed (exit 9)") || len(rows) != 1 || rows[0] != current {
		t.Fatal("current registration was ignored instead of reporting its stop failure")
	}
}

func TestProcsStopUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"--agent"}, {"--agent", "worker"}, {"42", "--agent", ""}, {"42", "--agent", "worker", "--agent", "other"}, {"42", "--unknown"}} {
		f := newProcsFixture(t)
		code, out, errOut := f.run(t, append([]string{"procs", "stop"}, args...)...)
		if code != 2 || out != "" || !strings.Contains(errOut, "procs stop exits: 0 stopped/already gone; 2 usage/refusal; 4 stop failed") {
			t.Fatalf("args=%q code=%d out=%q stderr=%q", args, code, out, errOut)
		}
	}
}

func TestProcsStopGoneOwnerGuard(t *testing.T) {
	for _, owner := range []string{"worker", "other"} {
		t.Run(owner, func(t *testing.T) {
			f := newProcsFixture(t)
			pid := f.sleeperGone(t)
			row := core.ProcRow{Pid: pid, Started: "Mon Jan  1 00:00:00 1990", Name: "sleep", Owner: owner, Pane: "p-" + owner, Created: core.FrictionISO(platform.Now())}
			f.writeProcs(t, row)
			code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(pid))
			if owner == "worker" {
				if code != 0 || out != fmt.Sprintf("process %d already gone\n", pid) || errOut != "" || len(f.procsRows(t)) != 0 {
					t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, f.procsRows(t))
				}
			} else if code != 2 || out != "" || !strings.Contains(errOut, "registered process belongs to another agent") || len(f.procsRows(t)) != 1 {
				t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, f.procsRows(t))
			}
		})
	}
}

func TestProcsStopUnknownWinsOverRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("failed ps injection is Unix-specific; Windows kernel controls live in internal/platform")
	}
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	row := core.ProcRow{Pid: pid, Started: "Mon Jan  1 00:00:00 1990", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())}
	f.writeProcs(t, row)
	if _, err := fakecli.Install(t, f.fakeDir, "ps", []fakecli.Rule{{AnyArgs: true, Code: 9}}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"0", strconv.Itoa(pid)}, {strconv.Itoa(pid), "0"}} {
		code, out, errOut := f.run(t, append([]string{"procs", "stop"}, args...)...)
		if code != 4 || out != "" || !strings.Contains(errOut, fmt.Sprintf("process %d is unreadable; not stopped", pid)) || !strings.Contains(errOut, "procs stop: refusing 0:") || len(f.procsRows(t)) != 1 {
			t.Fatalf("args=%q code=%d out=%q stderr=%q rows=%+v", args, code, out, errOut, f.procsRows(t))
		}
	}
}

func TestProcsStopIncorporatesPending(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeperGone(t)
	row := core.ProcRow{Pid: pid, Started: "Mon Jan  1 00:00:00 1990", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())}
	queue, err := core.QueueProc(f.env, "ws", f.state, row)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, "procs", "stop", strconv.Itoa(pid))
	if code != 0 || out != fmt.Sprintf("process %d already gone\n", pid) || errOut != "" || len(f.procsRows(t)) != 0 {
		t.Fatalf("code=%d out=%q stderr=%q rows=%+v", code, out, errOut, f.procsRows(t))
	}
	if _, err := os.Stat(queue); !os.IsNotExist(err) {
		t.Fatalf("queue remains: %v", err)
	}
}
