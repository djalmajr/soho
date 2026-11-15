package job

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

// managed_rebind_test.go: BindLaunch taking over a confirmed launch
// (rebindLaunch) only from a supervisor proven gone, for the job's current
// epoch, with a record bound with the private launch context.

// goneProcess returns the pid and start identity of a process that has
// exited.
func goneProcess(t *testing.T) (int, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	started, _, live := platform.ProcStartIdentity(pid, platform.Env{})
	if live != platform.ProcRunning {
		started = "darwin:1.1"
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, _, live := platform.ProcStartIdentity(pid, platform.Env{}); live != platform.ProcGone {
		t.Fatalf("pid %d still reads as %v after it exited", pid, live)
	}
	return pid, started
}

// liveProcess starts a process that runs until the test ends and returns
// its pid and start identity.
func liveProcess(t *testing.T) (int, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(testutil.CleanEnv(t), managedHelperVar+"=block")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	started, _, live := platform.ProcStartIdentity(cmd.Process.Pid, platform.Env{})
	if live != platform.ProcRunning || started == "" {
		t.Fatalf("live helper reads as %v", live)
	}
	return cmd.Process.Pid, started
}

// editState rewrites the job's state.json with update applied, status
// included (Store.Record keeps the status).
func editState(t *testing.T, dir string, update func(*State)) {
	t.Helper()
	st, err := readState(dir)
	if err != nil {
		t.Fatal(err)
	}
	update(&st)
	if err := saveState(dir, st); err != nil {
		t.Fatal(err)
	}
}

// confirmedFixture is newManagedFixture with the pending launch turned into
// a confirmed one bound to pid/started with the fixture's selection.
func confirmedFixture(t *testing.T, pid int, started string, edit func(*launchRecord)) (managedFixture, *Store, core.UserSelection) {
	t.Helper()
	f := newManagedFixture(t)
	editState(t, f.dir, func(s *State) { s.Status = StatusRunning })
	rec := recordState(t, f.dir)
	rec.State = launchConfirmed
	rec.PID, rec.Started, rec.Exe = pid, started, "/prior/soho"
	rec.ConfigBase = f.base
	if edit != nil {
		edit(&rec)
	}
	if err := writeLaunch(f.dir, rec); err != nil {
		t.Fatal(err)
	}
	return f, Open(f.root), core.UserSelection{Base: f.base}
}

func TestBindLaunchRebind(t *testing.T) {
	requireProcExec(t)
	env := launchEnv(launchTestWorkspace, launchTestPane)
	selfStarted, selfExe, ok := selfIdentity(env)
	if !ok {
		t.Fatal("selfIdentity unreadable")
	}
	t.Run("a gone supervisor is taken over", func(t *testing.T) {
		pid, started := goneProcess(t)
		f, store, sel := confirmedFixture(t, pid, started, nil)
		before := recordState(t, f.dir)
		bound, reason := BindLaunch(store, "job-1", env, sel)
		if !bound || reason != "" {
			t.Fatalf("bound=%v reason=%q", bound, reason)
		}
		rec := recordState(t, f.dir)
		if rec.PID != os.Getpid() || rec.Started != selfStarted || rec.Exe != selfExe {
			t.Fatalf("record %+v", rec)
		}
		before.PID, before.Started, before.Exe = rec.PID, rec.Started, rec.Exe
		if rec != before {
			t.Fatalf("rebind changed more than the binding: %+v vs %+v", rec, before)
		}
	})
	t.Run("a reused pid (another start identity) is taken over", func(t *testing.T) {
		pid, _ := liveProcess(t)
		_, store, sel := confirmedFixture(t, pid, "darwin:1.1", nil)
		if bound, reason := BindLaunch(store, "job-1", env, sel); !bound || reason != "" {
			t.Fatalf("bound=%v reason=%q", bound, reason)
		}
	})
	t.Run("finishing is still the launch's epoch", func(t *testing.T) {
		pid, started := goneProcess(t)
		f, store, sel := confirmedFixture(t, pid, started, nil)
		editState(t, f.dir, func(s *State) { s.Status = StatusFinishing })
		if bound, reason := BindLaunch(store, "job-1", env, sel); !bound || reason != "" {
			t.Fatalf("bound=%v reason=%q", bound, reason)
		}
	})
	refusals := []struct {
		name   string
		live   bool
		edit   func(*launchRecord)
		state  func(*State)
		env    platform.Env
		sel    func(core.UserSelection) core.UserSelection
		mutate func(t *testing.T, dir string)
		reason string
	}{
		{name: "a live supervisor keeps the record", live: true, reason: "the supervisor that confirmed the launch may still be running"},
		{name: "a failed job", state: func(s *State) { s.Status = StatusFailed }, reason: "the job is not running"},
		{name: "a job still preparing", state: func(s *State) { s.Status = StatusPreparing }, reason: "the job is not running"},
		{name: "a done job", state: func(s *State) { s.Status = StatusDone }, reason: "the job is not running"},
		{name: "another epoch (root pane)", state: func(s *State) { s.RootPane = "p-other" }, reason: "the launch is not the job's current launch"},
		{name: "another epoch (workspace)", state: func(s *State) { s.WorkspaceID = "w-other" }, reason: "the launch is not the job's current launch"},
		{name: "a record confirmed without the private context", edit: func(r *launchRecord) { r.ConfigBase = "" }, sel: func(core.UserSelection) core.UserSelection { return core.UserSelection{} }, reason: "the launch was not bound with the private launch context"},
		{name: "a record without a start identity", edit: func(r *launchRecord) { r.Started = "" }, reason: "the launch was not bound with the private launch context"},
		{name: "a record without an executable", edit: func(r *launchRecord) { r.Exe = "" }, reason: "the launch was not bound with the private launch context"},
		{name: "a malformed nonce", edit: func(r *launchRecord) { r.Nonce = "XYZ" }, reason: "the launch is not confirmed"},
		{name: "a revoked record", edit: func(r *launchRecord) { r.State = launchRevoked }, reason: "the launch is not confirmed"},
		{name: "another selection", sel: func(s core.UserSelection) core.UserSelection { s.Base += "-other"; return s }, reason: "the launch carries another configuration selection"},
		{name: "another home", sel: func(s core.UserSelection) core.UserSelection { s.Home = "/home-other"; return s }, reason: "the launch carries another configuration selection"},
		{name: "another pane", env: launchEnv(launchTestWorkspace, "p-other"), reason: "the launch belongs to another workspace or pane"},
		{name: "another store", edit: func(r *launchRecord) { r.Store += "-other" }, reason: "the launch belongs to another job or store"},
		{name: "a symlinked record", mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, launchFileName)
			if err := os.Rename(path, path+".real"); err != nil {
				t.Fatal(err)
			}
			requireSymlink(t, path+".real", path)
		}, reason: "the launch record is unreadable"},
		{name: "no record", mutate: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, launchFileName)); err != nil {
				t.Fatal(err)
			}
		}, reason: "the job has no launch record"},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			var pid int
			var started string
			if c.live {
				pid, started = liveProcess(t)
			} else {
				pid, started = goneProcess(t)
			}
			f, store, sel := confirmedFixture(t, pid, started, c.edit)
			if c.state != nil {
				editState(t, f.dir, c.state)
			}
			if c.sel != nil {
				sel = c.sel(sel)
			}
			if c.mutate != nil {
				c.mutate(t, f.dir)
			}
			callEnv := env
			if c.env != nil {
				callEnv = c.env
			}
			var before []byte
			if c.mutate == nil {
				before = launchFile(t, f.dir)
			}
			bound, reason := BindLaunch(store, "job-1", callEnv, sel)
			if bound || reason != c.reason {
				t.Fatalf("bound=%v reason=%q, want %q", bound, reason, c.reason)
			}
			if before != nil && string(launchFile(t, f.dir)) != string(before) {
				t.Fatal("a refused rebind changed the record")
			}
		})
	}
}

// TestBindLaunchRebindRefusesUnknownAndBusy: a recorded supervisor whose
// liveness reads as unknown, and a launch whose lock another holder keeps,
// are never adopted, and the record stays byte for byte.
func TestBindLaunchRebindRefusesUnknownAndBusy(t *testing.T) {
	requireProcExec(t)
	env := launchEnv(launchTestWorkspace, launchTestPane)
	t.Run("unknown liveness keeps the record", func(t *testing.T) {
		// A pid outside the platform range reads as unknown
		// (platform.ProcStartIdentity), never as gone.
		pid := math.MaxInt32 + 1
		if _, _, live := platform.ProcStartIdentity(pid, platform.Env{}); live != platform.ProcUnknown {
			t.Fatalf("pid %d reads %v, want unknown", pid, live)
		}
		f, store, sel := confirmedFixture(t, pid, "darwin:1.1", nil)
		before := launchFile(t, f.dir)
		bound, reason := BindLaunch(store, "job-1", env, sel)
		if bound || reason != "the liveness of the supervisor that confirmed the launch is unknown" {
			t.Fatalf("bound=%v reason=%q", bound, reason)
		}
		if string(launchFile(t, f.dir)) != string(before) {
			t.Fatal("an unknown liveness changed the record")
		}
	})
	holdLaunchLock := func(t *testing.T, dir string) {
		t.Helper()
		lock, err := openLaunchLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := tryFlock(lock); err != nil {
			t.Fatalf("hold launch.lock: %v", err)
		}
		t.Cleanup(func() {
			_ = funlock(lock)
			_ = lock.Close()
		})
	}
	t.Run("a held launch lock is not taken over through BindLaunch", func(t *testing.T) {
		pid, started := goneProcess(t)
		f, store, sel := confirmedFixture(t, pid, started, nil)
		store.Clock = &launchTestClock{now: time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)}
		before := launchFile(t, f.dir)
		holdLaunchLock(t, f.dir)
		bound, reason := BindLaunch(store, "job-1", env, sel)
		if bound || reason != "the launch lock stayed busy" {
			t.Fatalf("bound=%v reason=%q", bound, reason)
		}
		if string(launchFile(t, f.dir)) != string(before) {
			t.Fatal("a busy lock changed the record")
		}
	})
	t.Run("a held launch lock stops the rebind itself", func(t *testing.T) {
		pid, started := goneProcess(t)
		f, store, sel := confirmedFixture(t, pid, started, nil)
		store.Clock = &launchTestClock{now: time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)}
		selfStarted, selfExe, ok := selfIdentity(env)
		if !ok {
			t.Fatal("selfIdentity unreadable")
		}
		before := launchFile(t, f.dir)
		holdLaunchLock(t, f.dir)
		bound, reason := rebindLaunch(store, "job-1", env, launchBinding{Started: selfStarted, Exe: selfExe, Sel: sel})
		if bound || reason != "the launch lock could not be taken" {
			t.Fatalf("bound=%v reason=%q", bound, reason)
		}
		if string(launchFile(t, f.dir)) != string(before) {
			t.Fatal("a busy lock changed the record")
		}
	})
}
