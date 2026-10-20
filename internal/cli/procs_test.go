package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// procsFixture is the command fixture with a fake herdr (worker and other
// idle), a two-agent roster, a hermetic TMPDIR and the s85 pressure
// measurements faked, so the gc output stays hermetic.
type procsFixture struct {
	env        platform.Env
	cwd        string
	state      string
	fakeDir    string
	configFile string
	sleepExe   string
	shExe      string
}

// The fixture processes are the test binary itself, re-executed in a
// fixture role: no shell, no external sleep, no interpreter. The
// executables are named sleep and sh, so the process table prints the
// same command base names the old fixtures printed. The role rides in
// test-private environment (argv carries only the test flag), and the
// fakecli config marker is stripped from the re-execution (a re-executed
// test binary that inherits it would dispatch as the fake instead of the
// role).
const (
	procsHelperRoleEnv = "HERDR_PROCS_HELPER_ROLE"
	procsHelperArg1Env = "HERDR_PROCS_HELPER_ARG1"
	procsHelperArg2Env = "HERDR_PROCS_HELPER_ARG2"
)

// TestProcFixtureHelper is the re-executed helper entry: when the role env
// is set the fixture role runs and the process exits; in the main test run
// the env is unset and the function returns without running anything.
func TestProcFixtureHelper(t *testing.T) {
	role := os.Getenv(procsHelperRoleEnv)
	if role == "" {
		return
	}
	os.Exit(runProcsHelperRole(role, os.Getenv(procsHelperArg1Env), os.Getenv(procsHelperArg2Env)))
}

// runProcsHelperRole runs one fixture role inside the re-executed test
// binary. The roles keep the default signal handling, so a real SIGTERM
// kills them and SIGKILL stops them.
func runProcsHelperRole(role, childExe, kidFile string) (code int) {
	// stripFakeEnv removes the fakecli config marker: the re-executed test
	// binary must not dispatch as the fake CLI.
	stripFakeEnv := func() []string {
		items := os.Environ()
		out := items[:0]
		for _, item := range items {
			if strings.HasPrefix(item, "SOHO_FAKECLI_CONFIG=") {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	switch role {
	case "sleeper":
		// A long-lived process that dies from the default SIGTERM: the
		// owned stand-in for the old `sleep 300`.
		blockForever()
	case "tree":
		// A parent that owns exactly one sleeper child and waits on it:
		// the owned stand-in for `sh -c "sleep 300 & wait"`. The parent
		// dies from the default SIGTERM and it exits when its child stops
		// (Wait returns), as the shell tree did, and the child reparents.
		cmd := exec.Command(childExe, "-test.run=^TestProcFixtureHelper$")
		items := stripFakeEnv()
		items = append(items, procsHelperRoleEnv+"=sleeper")
		cmd.Env = items
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "fixture tree: start child: %v\n", err)
			return 1
		}
		if err := os.WriteFile(kidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
			_ = cmd.Process.Kill()
			fmt.Fprintf(os.Stderr, "fixture tree: kid file: %v\n", err)
			return 1
		}
		_ = cmd.Wait()
	default:
		fmt.Fprintf(os.Stderr, "fixture: unknown role %q\n", role)
		return 127
	}
	return 0
}

// blockForever blocks the process for the rest of its life inside a real
// blocking syscall (a read on a pipe whose write end this process keeps
// open, so the read never returns and never reaches EOF): a Go process
// whose only goroutine is parked in select{} is a self-declared deadlock,
// and a timer sleep does not hold a syscall thread either. The default
// SIGTERM and SIGKILL still stop the process while it is blocked.
func blockForever() {
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: pipe: %v\n", err)
		os.Exit(1)
	}
	_ = w // held open for the life of the process: the read never returns.
	buf := make([]byte, 1)
	_, _ = r.Read(buf)
	os.Exit(1) // unreachable: the read blocks until a signal stops the process
}

// helperExe places an executable under dir/name so ps prints name as the
// command base name, exactly like the old sleep/sh fixtures did: a hard
// link of the test binary when supported (Unix), a byte copy otherwise,
// and on Windows a hard link of the package's one prepared independent
// image (fakecli's byte copy of the test binary in its own temp dir) —
// the running parent image is never linked. When a link is unsupported
// it falls back to an independent byte copy (slower first exec, same
// behavior). A loaded parent image can keep a hard-linked fixture in use
// on Windows Server CI; the prepared image leaves fixture cleanup
// independent of the parent's lifetime, and the slow first exec of a
// fresh full copy is paid once per package instead of once per fixture.
func helperExe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The prepared image's hard link: Install appends .exe to the
		// bare name and may already have written name.json by the time a
		// failed link returns (dir/name.exe itself does not exist then);
		// both are harmless — spawnHelper and the tree role strip
		// SOHO_FAKECLI_CONFIG from the re-execution, so the
		// fixture never dispatches as the fake.
		exe, err := fakecli.Install(t, dir, name, nil)
		if err != nil {
			t.Logf("falling back to an independent byte copy: %v", err)
			exePath, readErr := os.Executable()
			if readErr != nil {
				t.Fatalf("locating the test binary: %v", readErr)
			}
			data, readErr := os.ReadFile(exePath)
			if readErr != nil {
				t.Fatalf("reading the test binary: %v", readErr)
			}
			target := filepath.Join(dir, name+".exe")
			if writeErr := os.WriteFile(target, data, 0o700); writeErr != nil {
				t.Fatalf("writing the fixture copy: %v", writeErr)
			}
			return target
		}
		return exe
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	target := filepath.Join(dir, name)
	if err := os.Link(exe, target); err != nil {
		data, readErr := os.ReadFile(exe)
		if readErr != nil {
			t.Fatalf("reading the test binary: %v", readErr)
		}
		if writeErr := os.WriteFile(target, data, 0o700); writeErr != nil {
			t.Fatalf("writing the fixture copy: %v", writeErr)
		}
	}
	return target
}

// spawnHelper starts the test binary in a fixture role under the test's
// env and owns the process for the whole test (killed and reaped in the
// cleanup, also when an assertion failed), so the test never leaves a
// process behind and never touches one it did not start.
func spawnHelper(t *testing.T, env platform.Env, exe string, role string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(exe, "-test.run=^TestProcFixtureHelper$")
	items := env.List()
	out := items[:0]
	for _, item := range items {
		// The re-executed test binary must not dispatch as the fake CLI.
		if strings.HasPrefix(item, "SOHO_FAKECLI_CONFIG=") {
			continue
		}
		out = append(out, item)
	}
	items = out
	items = append(items, procsHelperRoleEnv+"="+role)
	for i, a := range args {
		items = append(items, fmt.Sprintf("HERDR_PROCS_HELPER_ARG%d=%s", i+1, a))
	}
	cmd.Env = items
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the %s fixture: %v", role, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func newProcsFixture(t *testing.T) *procsFixture {
	t.Helper()
	env, cwd := commandFixture(t)
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "p-worker"
	env["SOHO_REGRID"] = "off"
	env["TMPDIR"] = t.TempDir()
	state := filepath.Join(env.Get("SOHO_DIR"), "ws")
	if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	roster := "# header\n" + releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work") + "\n" +
		releaseRow("other", "p-other", "1", "reviewer", false, "/tmp/work") + "\n"
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"idle"}}}`},
		{Argv: []string{"agent", "get", "other"}, Stdout: `{"result":{"agent":{"name":"other","agent_status":"idle"}}}`},
		{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "report-metadata", "p-other", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"old"}}}`},
		{Argv: []string{"tab", "rename", "t1", "herd"}},
	}
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	// withFakeCLI narrows PATH to the fake dir; the registry helpers read
	// ps from PATH, so a ps-only directory comes back (the fake herdr
	// still wins: its dir is first). Nothing else is resolvable: no shell,
	// no interpreter, no sleep.
	env["PATH"] = fakeDir + string(os.PathListSeparator) + psOnlyDir(t)
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) {
		return int64(60) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) {
		return int64(30) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
	return &procsFixture{env: env, cwd: cwd, state: state, fakeDir: fakeDir, configFile: filepath.Join(fakeDir, "herdr.json"), sleepExe: helperExe(t, fakeDir, "sleep"), shExe: helperExe(t, fakeDir, "sh")}
}

// psOnlyDir returns a directory holding only a link to the system ps: the
// tail of the restricted PATH the fixture env carries (no shell,
// interpreter or sleep resolvable; ps is the only native command the
// production reads use).
func psOnlyDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	} // Native kernel APIs do not need ps.
	path, err := exec.LookPath("ps")
	if err != nil {
		t.Fatalf("locate the system ps: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(path, filepath.Join(dir, "ps")); err != nil {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading the system ps: %v", readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(dir, "ps"), data, 0o755); writeErr != nil {
			t.Fatalf("copying the system ps: %v", writeErr)
		}
	}
	return dir
}

func (f *procsFixture) run(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run(argv, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

func (f *procsFixture) procsRows(t *testing.T) []core.ProcRow {
	t.Helper()
	rows, err := core.ReadProcs(f.state)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *procsFixture) writeProcs(t *testing.T, rows ...core.ProcRow) {
	t.Helper()
	if err := core.WriteProcs(f.state, rows); err != nil {
		t.Fatal(err)
	}
}

func (f *procsFixture) registryPath() string { return filepath.Join(f.state, "procs.tsv") }

// sleeper starts a real long-lived process the test itself owns (killed
// in the cleanup, also when an assertion failed) and returns its pid. The
// process is the test binary re-executed as a sleeper, so it dies from a
// real SIGTERM and the process table prints sleep as the base name.
func (f *procsFixture) sleeper(t *testing.T) int {
	t.Helper()
	cmd := spawnHelper(t, f.env, f.sleepExe, "sleeper")
	return cmd.Process.Pid
}

// sleeperGone is a sleeper the test killed and reaped: the pid is gone.
func (f *procsFixture) sleeperGone(t *testing.T) int {
	t.Helper()
	cmd := spawnHelper(t, f.env, f.sleepExe, "sleeper")
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // reap: the entry leaves the table
	return pid
}

// treeProc starts a re-executed parent (named sh) that owns exactly one
// sleeper child (named sleep), and returns the parent pid and the
// descendant pid. The parent is the owned stand-in for
// `sh -c "sleep 300 & wait"`: it exits when its child stops. The child is
// owned through its exact pid (the parent may die before the test ends):
// the cleanup kills it and confirms the absence.
func (f *procsFixture) treeProc(t *testing.T) (parent, child int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX pid/ppid snapshot contract; native Windows tree tests are in internal/platform")
	}
	kidFile := filepath.Join(t.TempDir(), "kid")
	cmd := spawnHelper(t, f.env, f.shExe, "tree", f.sleepExe, kidFile)
	parent = cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, name, ok := platform.ProcInfo(parent, f.env); !ok {
			t.Fatalf("the sh %d is already gone (name %q)", parent, name)
		}
		kids, err := procDescendantsForTest(parent, f.env)
		if err == nil && len(kids) == 1 {
			child = kids[0]
		}
		if child != 0 {
			ownedStarted, ownedName, verified := platform.ProcInfo(child, f.env)
			if !verified {
				t.Fatal("fixture child identity is unreadable")
			}
			t.Cleanup(func() {
				currentStarted, currentName, live := platform.ProcInfo(child, f.env)
				if !live || currentStarted != ownedStarted || currentName != ownedName {
					return
				}
				// The child is owned (spawned by the fixture parent): the
				// cleanup kills it and confirms the absence. It may already
				// be stopped (the release, or the parent reaping it).
				p, err := os.FindProcess(child)
				if err != nil || p.Kill() != nil {
					return
				}
				until := time.Now().Add(5 * time.Second)
				for procAlive(t, f.env, child) {
					if time.Now().After(until) {
						t.Error("the fixture child survived the cleanup kill")
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
			return parent, child
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sh %d never forked its child (last: %v, %v)", parent, kids, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// procDescendantsForTest reads the pid/ppid snapshot the same way the
// stop helper does, so the test asserts on the same data.
func procDescendantsForTest(parent int, env platform.Env) ([]int, error) {
	r := platform.RunCli("ps", []string{"-A", "-o", "pid=,ppid="}, platform.RunOptions{Env: env, TimeoutMs: 5000})
	if r.NotFound || r.TimedOut || r.Error != "" {
		return nil, os.ErrNotExist
	}
	out := []int{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		child, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || ppid != parent {
			continue
		}
		out = append(out, child)
	}
	return out, nil
}

func procAlive(t *testing.T, env platform.Env, pid int) bool {
	t.Helper()
	_, _, ok := platform.ProcInfo(pid, env)
	return ok
}

func TestProcsAddRegistersNameAndStartTime(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	code, out, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("procs add: code=%d out=%q err=%q", code, out, errOut)
	}
	rows := f.procsRows(t)
	if len(rows) != 1 {
		t.Fatalf("registry rows = %v; want the one line", rows)
	}
	row := rows[0]
	started, name, ok := platform.ProcInfo(pid, f.env)
	if !ok {
		t.Fatal("the sleeper is not readable")
	}
	if row.Pid != pid || row.Name != name || row.Started != started {
		t.Fatalf("row = %+v; want pid=%d name=%q started=%q", row, pid, name, started)
	}
	if row.Owner != "worker" || row.Pane != "p-worker" {
		t.Fatalf("owner/pane = %q/%q; the pane's roster agent owns the line", row.Owner, row.Pane)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(row.Created) {
		t.Fatalf("created = %q; want ISO-8601 UTC", row.Created)
	}
	// The same pid with the same start time replaces the line, no
	// duplicate.
	if code, _, _ := f.run(t, "procs", "add", strconv.Itoa(pid)); code != 0 {
		t.Fatalf("second add: code=%d", code)
	}
	if rows := f.procsRows(t); len(rows) != 1 {
		t.Fatalf("registry rows after the second add = %v; want still one line", rows)
	}
}

func TestProcsAddRefusals(t *testing.T) {
	f := newProcsFixture(t)
	cases := []struct {
		name  string
		arg   string
		cause string
	}{
		{"pid 1", "1", "pid 1 is the system init process"},
		{"this process", "self", "that is this soho process"},
		{"the invoking process", "parent", "that is the process that invoked soho"},
		{"a dead pid", "dead", "no process with that pid is running"},
		{"not a number", "abc", "not a positive integer pid"},
		{"a negative pid", "-5", "not a positive integer pid"},
		{"zero", "0", "not a positive integer pid"},
	}
	dead := f.sleeperGone(t)
	self := strconv.Itoa(os.Getpid())
	parent := strconv.Itoa(os.Getppid())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arg := tc.arg
			switch arg {
			case "self":
				arg = self
			case "parent":
				arg = parent
			case "dead":
				arg = strconv.Itoa(dead)
			}
			code, _, errOut := f.run(t, "procs", "add", arg)
			if code != 2 {
				t.Fatalf("code=%d; want 2", code)
			}
			if !strings.Contains(errOut, "procs add: refusing "+arg+": "+tc.cause) {
				t.Fatalf("stderr = %q; want the refusal naming %q", errOut, tc.cause)
			}
			if _, err := os.Stat(f.registryPath()); !os.IsNotExist(err) {
				t.Fatalf("the registry exists after a refusal: %v", err)
			}
		})
	}
}

func TestProcsAddAgentOutsideTheRoster(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	code, _, errOut := f.run(t, "procs", "add", strconv.Itoa(pid), "--agent", "ghost")
	if code != 3 {
		t.Fatalf("code=%d; want 3", code)
	}
	if !strings.Contains(errOut, "procs add: agent 'ghost' is not in the roster") {
		t.Fatalf("stderr = %q", errOut)
	}
	if rows := f.procsRows(t); len(rows) != 0 {
		t.Fatalf("registry rows = %v; nothing is registered on a refusal", rows)
	}
	// The roster's own pane comes from the roster line.
	code, _, _ = f.run(t, "procs", "add", strconv.Itoa(pid), "--agent", "other")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	rows := f.procsRows(t)
	if len(rows) != 1 || rows[0].Owner != "other" || rows[0].Pane != "p-other" {
		t.Fatalf("rows = %v; want the line owned by 'other' on pane p-other", rows)
	}
}

func TestProcsListStates(t *testing.T) {
	f := newProcsFixture(t)
	running := f.sleeper(t)
	if code, _, _ := f.run(t, "procs", "add", strconv.Itoa(running)); code != 0 {
		t.Fatalf("add: code=%d", code)
	}
	gone := f.sleeperGone(t)
	reused := f.sleeper(t)
	rows := f.procsRows(t)
	f.writeProcs(t, append(rows,
		core.ProcRow{Pid: gone, Started: "Sun Oct  3 00:00:00 2026", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: reused, Started: "adulterated start time", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
	)...)
	code, out, errOut := f.run(t, "procs")
	if code != 0 || errOut != "" {
		t.Fatalf("procs: code=%d err=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q; want three lines", out)
	}
	if !strings.HasPrefix(lines[0], strconv.Itoa(running)+"  "+filepath.Base(f.sleepExe)+"  worker  0m  running") {
		t.Fatalf("line 1 = %q; want the running state", lines[0])
	}
	if !strings.HasPrefix(lines[1], strconv.Itoa(gone)+"  sleep  worker  0m  gone") {
		t.Fatalf("line 2 = %q; want the gone state", lines[1])
	}
	if !strings.HasPrefix(lines[2], strconv.Itoa(reused)+"  sleep  worker  0m  reused") {
		t.Fatalf("line 3 = %q; want the reused state (the start time is not the registered one)", lines[2])
	}
}

// A ps that fails every identity query must never read as gone: the
// state column shows the unknown read, and nothing is dropped.
func TestProcsListUnknownState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("failed ps read injection is Unix-specific; Windows handle failure controls are in internal/platform")
	}
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	f.writeProcs(t, core.ProcRow{Pid: pid, Started: "Sat Oct  3 00:00:00 2026", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
	// The fake ps fails every query: the fixture's PATH puts the fake dir
	// first, so the registry helpers resolve it before the system ps.
	if _, err := fakecli.Install(t, f.fakeDir, "ps", []fakecli.Rule{{AnyArgs: true, Code: 9}}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, "procs")
	if code != 0 || errOut != "" {
		t.Fatalf("procs: code=%d err=%q", code, errOut)
	}
	if !strings.HasPrefix(out, strconv.Itoa(pid)+"  sleep  worker  0m  unknown") {
		t.Fatalf("line = %q; want the unknown state (a failed read is not gone)", out)
	}
	if rows := f.procsRows(t); len(rows) != 1 {
		t.Fatalf("registry rows = %v; nothing is dropped on an unknown read", rows)
	}
}

func TestReleaseStopsTheAgentTreeAndKeepsTheOthers(t *testing.T) {
	f := newProcsFixture(t)
	parent, child := f.treeProc(t)
	other := f.sleeper(t)
	_, parentName, _ := platform.ProcInfo(parent, f.env)
	otherStarted, otherName, _ := platform.ProcInfo(other, f.env)
	parentStarted, _, _ := platform.ProcInfo(parent, f.env)
	f.writeProcs(t,
		core.ProcRow{Pid: parent, Started: parentStarted, Name: parentName, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: other, Started: otherStarted, Name: otherName, Owner: "other", Pane: "p-other", Created: core.FrictionISO(platform.Now())},
	)
	code, out, _ := f.run(t, "release", "worker")
	if code != 0 {
		t.Fatalf("release: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "stopped process "+strconv.Itoa(parent)+" ("+parentName+")") {
		t.Fatalf("output = %q; want the stopped line for the agent's process", out)
	}
	if !strings.Contains(out, "released worker") {
		t.Fatalf("output = %q", out)
	}
	if procAlive(t, f.env, parent) {
		t.Fatal("the agent's process survived the release")
	}
	if procAlive(t, f.env, child) {
		t.Fatal("the descendant of the agent's process survived the release (the tree must be stopped)")
	}
	if !procAlive(t, f.env, other) {
		t.Fatal("the other agent's process was stopped; the release only touches the released agent's lines")
	}
	rows := f.procsRows(t)
	if len(rows) != 1 || rows[0].Pid != other {
		t.Fatalf("registry rows = %v; want only the other agent's line", rows)
	}
}

func TestReleaseKeepProcsKeepsTheProcesses(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	started, name, _ := platform.ProcInfo(pid, f.env)
	f.writeProcs(t, core.ProcRow{Pid: pid, Started: started, Name: name, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
	code, out, _ := f.run(t, "release", "worker", "--keep-procs")
	if code != 0 {
		t.Fatalf("release: code=%d out=%q", code, out)
	}
	if strings.Contains(out, "stopped process") {
		t.Fatalf("output = %q; --keep-procs must not stop anything", out)
	}
	if !procAlive(t, f.env, pid) {
		t.Fatal("the process was stopped despite --keep-procs")
	}
	if rows := f.procsRows(t); len(rows) != 1 {
		t.Fatalf("registry rows = %v; the lines stay for gc", rows)
	}
}

func TestReleaseNeverSignalsAReusedPid(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	f.writeProcs(t, core.ProcRow{Pid: pid, Started: "adulterated start time", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
	code, out, _ := f.run(t, "release", "worker")
	if code != 0 {
		t.Fatalf("release: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "process "+strconv.Itoa(pid)+" was reused; not stopped") {
		t.Fatalf("output = %q; want the reused note", out)
	}
	if !procAlive(t, f.env, pid) {
		t.Fatal("the reused pid was signalled; a reused pid is never touched")
	}
	if rows := f.procsRows(t); len(rows) != 0 {
		t.Fatalf("registry rows = %v; the stale line leaves the registry", rows)
	}
}

func TestGcListAndStopOrphanProcesses(t *testing.T) {
	f := newProcsFixture(t)
	owned := f.sleeper(t)
	orphan := f.sleeper(t)
	reused := f.sleeper(t)
	gone := f.sleeperGone(t)
	ownedStarted, ownedName, _ := platform.ProcInfo(owned, f.env)
	orphanStarted, orphanName, _ := platform.ProcInfo(orphan, f.env)
	reusedName := "sleep"
	f.writeProcs(t,
		core.ProcRow{Pid: owned, Started: ownedStarted, Name: ownedName, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: orphan, Started: orphanStarted, Name: orphanName, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: reused, Started: "adulterated start time", Name: reusedName, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: gone, Started: "Sun Oct  3 00:00:00 2026", Name: "sleep", Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now())},
	)
	t.Run("without --yes the gc only lists the running orphans", func(t *testing.T) {
		code, out, errOut := f.run(t, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("gc: code=%d err=%q out=%q", code, errOut, out)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\norphan processes:\n" +
			strconv.Itoa(orphan) + "  " + orphanName + "  ghost  0m\n" +
			"run 'soho gc --yes' to stop them\n"
		if out != want {
			t.Fatalf("output = %q; want %q", out, want)
		}
		for _, pid := range []int{owned, orphan, reused} {
			if !procAlive(t, f.env, pid) {
				t.Fatalf("the dry run stopped process %d; it must only list", pid)
			}
		}
		if rows := f.procsRows(t); len(rows) != 4 {
			t.Fatalf("registry rows = %v; the dry run changes nothing", rows)
		}
	})
	t.Run("--yes stops the orphans, drops the stale lines and keeps the owned", func(t *testing.T) {
		code, out, errOut := f.run(t, "gc", "--yes")
		if code != 0 || errOut != "" {
			t.Fatalf("gc --yes: code=%d err=%q out=%q", code, errOut, out)
		}
		if !strings.Contains(out, "stopped process "+strconv.Itoa(orphan)+" ("+orphanName+")") {
			t.Fatalf("output = %q; want the orphan stopped", out)
		}
		if !strings.Contains(out, "process "+strconv.Itoa(reused)+" was reused; not stopped") {
			t.Fatalf("output = %q; want the reused note", out)
		}
		if !strings.Contains(out, "process "+strconv.Itoa(gone)+" already gone") {
			t.Fatalf("output = %q; want the gone note", out)
		}
		// No copy was removed: the closing line counts the process lines
		// (the orphan stopped, the reused and the gone dropped).
		if strings.Contains(out, "freed:") || !strings.HasSuffix(out, "process lines handled: 3\n") {
			t.Fatalf("output = %q; want only the process lines count as the closing line", out)
		}
		if procAlive(t, f.env, orphan) {
			t.Fatal("the orphan process survived gc --yes")
		}
		if !procAlive(t, f.env, owned) {
			t.Fatal("the owned process was stopped; a live owner's lines are never touched")
		}
		if !procAlive(t, f.env, reused) {
			t.Fatal("the reused pid was signalled; a reused pid is never touched")
		}
		rows := f.procsRows(t)
		if len(rows) != 1 || rows[0].Pid != owned {
			t.Fatalf("registry rows = %v; want only the owned line", rows)
		}
	})
}

// TestProcsLockHeld proves the held-lock contract of the procs registry:
// the lock is acquired synchronously before the command runs (exclusive
// create, the same pid time content takeRegistryLock writes) and is held
// for the whole subtest — only the t.Cleanup removes it, never a timer —
// so the command meets it no matter how long its preflight takes.
func TestProcsLockHeld(t *testing.T) {
	// holdLock acquires the procs registry lock synchronously: the
	// exclusive create is the acquisition assertion (an existing lock
	// fails it), and only this cleanup removes the lock (the fixture state
	// dir is a TempDir; nothing else is removed).
	holdLock := func(t *testing.T, f *procsFixture) {
		t.Helper()
		lock := filepath.Join(f.state, "procs.tsv.lock")
		file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("acquiring the lock: %v", err)
		}
		if _, err := fmt.Fprintf(file, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = file.Close()
			t.Fatalf("writing the lock: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("closing the lock: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(lock) })
	}
	t.Run("release warns and keeps the lines", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		started, name, _ := platform.ProcInfo(pid, f.env)
		f.writeProcs(t, core.ProcRow{Pid: pid, Started: started, Name: name, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		holdLock(t, f)
		code, out, errOut := f.run(t, "release", "worker")
		if code != 0 {
			t.Fatalf("release must never fail for the lock: code=%d out=%q", code, out)
		}
		if !strings.Contains(errOut, "the processes of 'worker' were not stopped") || !strings.Contains(errOut, "procs: registry is locked by another soho") {
			t.Fatalf("stderr = %q; want the lock warning", errOut)
		}
		if !procAlive(t, f.env, pid) {
			t.Fatal("the process was stopped while the lock was held")
		}
		if rows := f.procsRows(t); len(rows) != 1 {
			t.Fatalf("registry rows = %v; the lines stay", rows)
		}
		if !strings.Contains(out, "released worker") {
			t.Fatalf("output = %q; the release completes", out)
		}
	})
	t.Run("gc --yes exits 4 without stopping anything", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		started, name, _ := platform.ProcInfo(pid, f.env)
		f.writeProcs(t, core.ProcRow{Pid: pid, Started: started, Name: name, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now())})
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		holdLock(t, f)
		code, out, errOut := f.run(t, "gc", "--yes")
		if code != 4 {
			t.Fatalf("gc --yes: code=%d out=%q; want 4", code, out)
		}
		if !strings.Contains(errOut, "procs: registry is locked by another soho") {
			t.Fatalf("stderr = %q; want the lock error", errOut)
		}
		if strings.Contains(out, "stopped process") {
			t.Fatalf("output = %q; nothing is stopped behind a held lock", out)
		}
		if !procAlive(t, f.env, pid) {
			t.Fatal("the process was stopped behind a held lock")
		}
		if rows := f.procsRows(t); len(rows) != 1 {
			t.Fatalf("registry rows = %v; the lines stay", rows)
		}
	})
	// The preflight (the agent identity read through herdr) is slower than
	// the 1500 ms the old fixture timer removed the lock at: the lock must
	// still be held when the release reaches the registry. The fixture's
	// fake herdr is re-installed with the same rules newProcsFixture uses,
	// only the agent get worker rule delayed 2000 ms — the preflight the
	// release reads before the registry lock is attempted.
	t.Run("a slow preflight still meets the held lock", func(t *testing.T) {
		f := newProcsFixture(t)
		bin := "herdr"
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		// Install refuses only an existing executable (herdr / herdr.exe)
		// and rewrites the config; the metadata uses the bare name
		// (herdr.json, herdr.calls.jsonl). The old call log is removed so
		// the call-log check sees only calls made after the re-install.
		for _, file := range []string{bin, "herdr.json", "herdr.calls.jsonl"} {
			if err := os.Remove(filepath.Join(f.fakeDir, file)); err != nil && !os.IsNotExist(err) {
				t.Fatalf("removing the installed fake %s: %v", file, err)
			}
		}
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"idle"}}}`, Delay: 2000},
			{Argv: []string{"agent", "get", "other"}, Stdout: `{"result":{"agent":{"name":"other","agent_status":"idle"}}}`},
			{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "report-metadata", "p-other", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"old"}}}`},
			{Argv: []string{"tab", "rename", "t1", "herd"}},
		}
		if _, err := fakecli.Install(t, f.fakeDir, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		pid := f.sleeper(t)
		started, name, _ := platform.ProcInfo(pid, f.env)
		f.writeProcs(t, core.ProcRow{Pid: pid, Started: started, Name: name, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		holdLock(t, f)
		code, out, errOut := f.run(t, "release", "worker")
		if code != 0 {
			t.Fatalf("release must never fail for the lock: code=%d out=%q", code, out)
		}
		if !strings.Contains(errOut, "the processes of 'worker' were not stopped") || !strings.Contains(errOut, "procs: registry is locked by another soho") {
			t.Fatalf("stderr = %q; want the lock warning", errOut)
		}
		if !procAlive(t, f.env, pid) {
			t.Fatal("the process was stopped while the lock was held")
		}
		if rows := f.procsRows(t); len(rows) != 1 {
			t.Fatalf("registry rows = %v; the lines stay", rows)
		}
		if !strings.Contains(out, "released worker") {
			t.Fatalf("output = %q; the release completes", out)
		}
		// The delayed rule must really be the one that ran: the fake's call
		// log lists the agent get worker preflight.
		calls, err := fakecli.ReadCalls(filepath.Join(f.fakeDir, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatalf("reading the fake herdr call log: %v", err)
		}
		delayed := false
		for _, call := range calls {
			if strings.Join(call.Argv, " ") == "agent get worker" {
				delayed = true
			}
		}
		if !delayed {
			t.Fatalf("the delayed agent get worker rule was not hit: %v", calls)
		}
	})
}

func TestProcsNowrite(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	started, name, _ := platform.ProcInfo(pid, f.env)
	f.writeProcs(t, core.ProcRow{Pid: pid, Started: started, Name: name, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
	env := f.env.Clone()
	env["SOHO_NOWRITE"] = "1"
	t.Run("the exact procs runs read-only", func(t *testing.T) {
		code, out, errOut := f.runWithEnv(t, env, "procs")
		if code != 0 || errOut != "" {
			t.Fatalf("procs under NOWRITE: code=%d err=%q", code, errOut)
		}
		if !strings.HasPrefix(out, strconv.Itoa(pid)+"  "+name+"  worker  0m  running") {
			t.Fatalf("output = %q", out)
		}
	})
	t.Run("procs add is refused under NOWRITE", func(t *testing.T) {
		code, _, errOut := f.runWithEnv(t, env, "procs", "add", strconv.Itoa(pid))
		if code != 2 {
			t.Fatalf("code=%d; want 2", code)
		}
		if !strings.Contains(errOut, "read-only") {
			t.Fatalf("stderr = %q", errOut)
		}
	})
}

func (f *procsFixture) runWithEnv(t *testing.T, env platform.Env, argv ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run(argv, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

// A process of a live owner is never a gc candidate: with no copy and no
// orphan process, the dry run and --yes both say nothing to remove (rdocs17).
func TestGcWithOnlyOwnedProcessesHasNothingToRemove(t *testing.T) {
	f := newProcsFixture(t)
	owned := f.sleeper(t)
	started, name, _ := platform.ProcInfo(owned, f.env)
	f.writeProcs(t, core.ProcRow{Pid: owned, Started: started, Name: name, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())})
	for _, args := range [][]string{{"gc"}, {"gc", "--yes"}} {
		code, out, errOut := f.run(t, args...)
		if code != 0 || errOut != "" || !strings.HasSuffix(out, "\nnothing to remove\n") {
			t.Fatalf("%v: code=%d err=%q out=%q; want nothing to remove", args, code, errOut, out)
		}
		if !procAlive(t, f.env, owned) {
			t.Fatalf("%v stopped the owned process", args)
		}
	}
}

// An empty process registry says so (G3).
func TestProcsEmptyRegistrySaysSo(t *testing.T) {
	f := newProcsFixture(t)
	code, out, errOut := f.run(t, "procs")
	if code != 0 || out != "no registered processes\n" || errOut != "" {
		t.Fatalf("procs: code=%d out=%q err=%q; want no registered processes", code, out, errOut)
	}
}

// TestProcsHelperImageIsALinkOfThePreparedImage pins the fixture
// identity: sleep and sh keep the base names the process table prints,
// and the one sleeper this test starts reads as that base name, is
// killed, and is reaped with the reap proved by the process state (not
// by an ignored Wait error), so at the Windows remove below no process
// this test started is running from that link. On Windows the fixtures
// are additionally hard links of the package's one prepared image
// (never of the running parent image), and the link is removable.
func TestProcsHelperImageIsALinkOfThePreparedImage(t *testing.T) {
	f := newProcsFixture(t)
	sleepBase := "sleep"
	shBase := "sh"
	if runtime.GOOS == "windows" {
		sleepBase = "sleep.exe"
		shBase = "sh.exe"
	}
	if got := filepath.Base(f.sleepExe); got != sleepBase {
		t.Fatalf("base of sleepExe = %q; want %q", got, sleepBase)
	}
	if got := filepath.Base(f.shExe); got != shBase {
		t.Fatalf("base of shExe = %q; want %q", got, shBase)
	}
	// The one owned sleeper: spawnHelper is used directly so the test
	// holds the *exec.Cmd for the Kill and the reap; its cleanup
	// tolerates the already-reaped process.
	cmd := spawnHelper(t, f.env, f.sleepExe, "sleeper")
	if !procAlive(t, f.env, cmd.Process.Pid) {
		t.Fatal("the sleeper is not alive")
	}
	started, name, ok := platform.ProcInfo(cmd.Process.Pid, f.env)
	if !ok {
		t.Fatal("the sleeper is not readable")
	}
	// The same name the procs table line asserts (TestProcsListStates).
	if name != filepath.Base(f.sleepExe) {
		t.Fatalf("ProcInfo name = %q (started %q); want the table base name %q", name, started, filepath.Base(f.sleepExe))
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the sleeper: %v", err)
	}
	// The reap is proven by the wait status (normal exit on Windows, a
	// signal on Unix) plus the pid's absence, not by the Wait error.
	waitErr := cmd.Wait()
	if cmd.ProcessState == nil {
		t.Fatalf("the sleeper was not reaped (wait err %v)", waitErr)
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !(ws.Exited() || ws.Signaled()) {
		t.Fatalf("the sleeper was not reaped (wait err %v, process state %v)", waitErr, cmd.ProcessState.String())
	}
	if procAlive(t, f.env, cmd.Process.Pid) {
		t.Fatalf("the sleeper is still alive after Kill and Wait (wait err %v)", waitErr)
	}
	if runtime.GOOS == "windows" {
		// Both fixtures are links of the one prepared image, and
		// neither is the running parent image. The single sleeper was
		// killed and reaped above, so at the remove no process this
		// test started is running from that link.
		sleepInfo, err := os.Stat(f.sleepExe)
		if err != nil {
			t.Fatalf("stat sleep: %v", err)
		}
		shInfo, err := os.Stat(f.shExe)
		if err != nil {
			t.Fatalf("stat sh: %v", err)
		}
		if !os.SameFile(sleepInfo, shInfo) {
			t.Fatal("sleep and sh are not links of the one prepared image")
		}
		parentExe, err := os.Executable()
		if err != nil {
			t.Fatalf("locating the test binary: %v", err)
		}
		parentInfo, err := os.Stat(parentExe)
		if err != nil {
			t.Fatalf("stat the test binary: %v", err)
		}
		if os.SameFile(sleepInfo, parentInfo) {
			t.Fatal("the fixture links the running parent image")
		}
		// The link is removable: no process this test started is
		// running from it (the sleeper was reaped above), and this
		// process (the running parent image) is still up.
		if err := os.Remove(f.sleepExe); err != nil {
			t.Fatalf("removing the fixture link: %v", err)
		}
		if _, err := os.Lstat(f.sleepExe); !os.IsNotExist(err) {
			t.Fatalf("lstat after the remove: %v; want not-exist", err)
		}
	}
}

// refuseProcsRegistry swaps the process registry write for the permission
// error of a sandbox that cannot write the state dir (DJA-323), or for err;
// the returned function puts the real write back.
func refuseProcsRegistry(t *testing.T, err error) func() {
	t.Helper()
	old := upsertProc
	upsertProc = func(sd string, _ core.ProcRow) error {
		if err != nil {
			return err
		}
		return &fs.PathError{Op: "open", Path: filepath.Join(sd, "procs.tsv.lock"), Err: fs.ErrPermission}
	}
	restore := func() { upsertProc = old }
	t.Cleanup(restore)
	return restore
}

func TestProcsAddQueuesWhenTheStateDirRefusesWrites(t *testing.T) {
	queued := func(t *testing.T, f *procsFixture, pid int) string {
		t.Helper()
		restore := refuseProcsRegistry(t, nil)
		code, out, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
		restore()
		queue := core.PendingProcsFile(f.env, "ws")
		wantLine := "procs add: the state dir " + f.state + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		if code != 0 || out != "" || errOut != wantLine {
			t.Fatalf("code=%d out=%q err=%q; want 0 and the queue line %q", code, out, errOut, wantLine)
		}
		if _, err := os.Stat(f.registryPath()); !os.IsNotExist(err) {
			t.Fatalf("the refused registry was written: %v", err)
		}
		return queue
	}

	t.Run("the full line goes to the queue and procs lists it after", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		queue := queued(t, f, pid)
		started, name, ok := platform.ProcInfo(pid, f.env)
		if !ok {
			t.Fatal("the sleeper is not readable")
		}
		raw, err := os.ReadFile(queue)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(lines) != 2 || lines[0] != "state_dir\tpid\tstarted\tname\towner\tpane\tcreated" {
			t.Fatalf("queue=%q; want the header and one line", raw)
		}
		fields := strings.Split(lines[1], "\t")
		if len(fields) != 7 || fields[0] != f.state || fields[1] != strconv.Itoa(pid) || fields[2] != started || fields[3] != name || fields[4] != "worker" || fields[5] != "p-worker" {
			t.Fatalf("queue line=%q; want the state dir in field 1 and the six registry fields", lines[1])
		}
		code, out, errOut := f.run(t, "procs")
		if code != 0 || errOut != "" || !strings.HasPrefix(out, strconv.Itoa(pid)+"  "+name+"  worker  ") {
			t.Fatalf("procs: code=%d out=%q err=%q; want the queued process", code, out, errOut)
		}
		if rows := f.procsRows(t); len(rows) != 1 || rows[0].Pid != pid || rows[0].Started != started {
			t.Fatalf("registry rows=%v; want the queued line", rows)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue is still there: %v", err)
		}
	})

	t.Run("release stops the queued process of its owner", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		queue := queued(t, f, pid)
		code, out, _ := f.run(t, "release", "worker")
		if code != 0 || !strings.Contains(out, "stopped process "+strconv.Itoa(pid)) {
			t.Fatalf("release: code=%d out=%q; want the queued process stopped", code, out)
		}
		if procAlive(t, f.env, pid) {
			t.Fatal("the queued process survived the release")
		}
		if rows := f.procsRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%v; want none", rows)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue is still there: %v", err)
		}
	})

	t.Run("an error that is not a permission keeps today's exit 4 and queues nothing", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		refuseProcsRegistry(t, errors.New("disk full"))
		code, _, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
		if code != 4 || !strings.Contains(errOut, "procs add: could not register pid "+strconv.Itoa(pid)+" (disk full)") || strings.Contains(errOut, "queued") {
			t.Fatalf("code=%d err=%q; want today's exit 4 and message", code, errOut)
		}
		if _, err := os.Stat(core.PendingProcsFile(f.env, "ws")); !os.IsNotExist(err) {
			t.Fatalf("a non-permission error wrote the queue: %v", err)
		}
	})

	t.Run("a held lock keeps today's exit 4 and queues nothing", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 200 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		lock := filepath.Join(f.state, "procs.tsv.lock")
		if err := os.WriteFile(lock, []byte("1 now\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(lock) })
		code, _, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
		if code != 4 || !strings.Contains(errOut, "procs: registry is locked by another soho") || strings.Contains(errOut, "queued") {
			t.Fatalf("code=%d err=%q; want today's lock exit 4", code, errOut)
		}
		if _, err := os.Stat(core.PendingProcsFile(f.env, "ws")); !os.IsNotExist(err) {
			t.Fatalf("a held lock wrote the queue: %v", err)
		}
	})

	t.Run("a malformed line leaves the queue with a warning and an ended process enters", func(t *testing.T) {
		f := newProcsFixture(t)
		gone := f.sleeperGone(t)
		queue := core.PendingProcsFile(f.env, "ws")
		if err := os.MkdirAll(filepath.Dir(queue), 0o700); err != nil {
			t.Fatal(err)
		}
		content := "state_dir\tpid\tstarted\tname\towner\tpane\tcreated\n" +
			f.state + "\tnot-a-pid\tx\tsleep\tworker\tp-worker\t2026-01-01T00:00:00Z\n" +
			f.state + "\t" + strconv.Itoa(gone) + "\tMon Jan  1 00:00:00 2026\tsleep\tworker\tp-worker\t2026-01-01T00:00:00Z\n"
		if err := os.WriteFile(queue, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, "procs")
		if code != 0 || !strings.Contains(errOut, "procs: dropped a malformed line from "+queue+": ") || !strings.Contains(out, strconv.Itoa(gone)+"  sleep  worker  ") {
			t.Fatalf("procs: code=%d out=%q err=%q; want the warning and the ended process listed", code, out, errOut)
		}
		if rows := f.procsRows(t); len(rows) != 1 || rows[0].Pid != gone {
			t.Fatalf("registry rows=%v; want the ended process only", rows)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue is still there: %v", err)
		}
	})
}

func TestSandboxedProcsStayQuietOnAPermissionIncorporation(t *testing.T) {
	t.Run("a second procs add prints only the queue line, and procs lists without a warning", func(t *testing.T) {
		f := newProcsFixture(t)
		refuseProcsRegistry(t, nil)
		refuseIncorporation(t, nil)
		queue := core.PendingProcsFile(f.env, "ws")
		wantLine := "procs add: the state dir " + f.state + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		for _, name := range []string{"first", "second"} {
			pid := f.sleeper(t)
			code, out, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
			if code != 0 || out != "" || errOut != wantLine {
				t.Fatalf("%s add: code=%d out=%q err=%q; want 0 and only %q", name, code, out, errOut, wantLine)
			}
		}
		raw, err := os.ReadFile(queue)
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"); len(lines) != 3 {
			t.Fatalf("queue lines=%q; want the header and both processes", lines)
		}
		code, out, errOut := f.run(t, "procs")
		if code != 0 || !strings.HasPrefix(out, "no registered processes\n") || errOut != "" {
			t.Fatalf("procs: code=%d out=%q err=%q; want the registry as it is, no warning", code, out, errOut)
		}
	})

	t.Run("an error that is not a permission keeps the warning", func(t *testing.T) {
		f := newProcsFixture(t)
		refuseIncorporation(t, errors.New("disk full"))
		queue := core.PendingProcsFile(f.env, "ws")
		code, _, errOut := f.run(t, "procs")
		want := "procs: the queued process registrations in " + queue + " were not incorporated (disk full); the queue stays and the registry is used as it is\n"
		if code != 0 || errOut != want {
			t.Fatalf("procs: code=%d err=%q; want %q", code, errOut, want)
		}
	})
}

func TestProcsAddWithARefusedQueueDirectory(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	dir := core.PendingDir(f.env, "ws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	queueOwnedByAnother(t)
	refuseProcsRegistry(t, nil)
	code, _, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
	want := "; the queue failed too: queue: refusing " + dir + ": it belongs to another user)"
	if code != 4 || !strings.Contains(errOut, "procs add: could not register pid "+strconv.Itoa(pid)+" (") || !strings.Contains(errOut, want) {
		t.Fatalf("code=%d err=%q; want exit 4 with the queue refusal", code, errOut)
	}
	code, _, errOut = f.run(t, "procs")
	if code != 0 || !strings.Contains(errOut, "procs: the queued process registrations in "+core.PendingProcsFile(f.env, "ws")+" were not incorporated (queue: refusing "+dir+": it belongs to another user)") {
		t.Fatalf("procs: code=%d err=%q; want the incorporation warning", code, errOut)
	}
}
