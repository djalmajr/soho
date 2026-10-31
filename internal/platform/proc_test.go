//go:build !windows

package platform

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// procTestEnv is the test process's own environment with the PATH
// restricted to the system ps and nothing else: the production code
// resolves ps from that env, while no shell, no Python/Node/Bun and no
// sleep binary can resolve through it. A fixture that still needs an
// interpreter or an external binary fails the test instead of skipping.
func procTestEnv(t *testing.T) Env {
	t.Helper()
	env := Env{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	psPath, err := exec.LookPath("ps")
	if err != nil {
		t.Skip("ps is not available (the production process code depends on it)")
	}
	bin := t.TempDir()
	if err := os.Symlink(psPath, filepath.Join(bin, "ps")); err != nil {
		// A copy stands in where symlinks are unsupported.
		data, readErr := os.ReadFile(psPath)
		if readErr != nil {
			t.Fatalf("reading ps: %v", readErr)
		}
		if err := os.WriteFile(filepath.Join(bin, "ps"), data, 0o700); err != nil {
			t.Fatalf("staging ps: %v", err)
		}
	}
	env["PATH"] = bin
	return env
}

// procGone waits (with a ceiling) until the pid is not running anymore
// (a zombie is not running).
func procGone(t *testing.T, env Env, pid int, ceiling time.Duration) {
	t.Helper()
	deadline := time.Now().Add(ceiling)
	for time.Now().Before(deadline) {
		if !procAliveStates([]int{pid}, env)[pid] {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d is still running after %s", pid, ceiling)
}

// treeFixture starts the owned two-level tree (a parent that owns exactly
// one sleeper child and waits on it — the native stand-in for the old
// `sh -c "sleep 300 & wait"`) and polls until the parent has exactly one
// descendant.
func treeFixture(t *testing.T, env Env) (parent, child int) {
	t.Helper()
	cmd := fixtureSpawn(t, env, "tree")
	parent = cmd.Process.Pid
	var kids []int
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		kids, err = procDescendants(parent, env)
		if err == nil && len(kids) == 1 {
			return parent, kids[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendants of %d never became one (last: %v, %v); want the one owned child", parent, kids, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProcInfoCases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process info is unix; the windows runtime round exercises the kernel32 path")
	}
	env := procTestEnv(t)
	// Give the native fixture a short, exact name. Linux's comm field
	// truncates a long test-binary filename; that must not weaken the
	// command-name assertion or make it depend on the build output name.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "hsproc")
	if err := os.WriteFile(fixture, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := fixtureSpawnExecutable(t, env, fixture, "sleeper")
	pid := cmd.Process.Pid
	wantName := "hsproc"
	t.Run("a running process gives its start time and command base name", func(t *testing.T) {
		started, name, ok := ProcInfo(pid, env)
		if !ok || started == "" || name != wantName {
			t.Fatalf("ProcInfo(%d) = (%q, %q, %v); want (non-empty lstart, %s, true)", pid, started, name, ok, wantName)
		}
		again, againName, okAgain := ProcInfo(pid, env)
		if !okAgain || again != started || againName != name {
			t.Fatalf("second read = (%q, %q, %v); the start time must be stable for the life of the process", again, againName, okAgain)
		}
	})
	t.Run("a dead pid gives no snapshot", func(t *testing.T) {
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait() // reap: the entry leaves the table; the kill error is expected
		if started, name, ok := ProcInfo(pid, env); ok || started != "" || name != "" {
			t.Fatalf("ProcInfo(dead %d) = (%q, %q, %v); want the zero result", pid, started, name, ok)
		}
	})
	t.Run("a zombie (killed, not reaped) is not running", func(t *testing.T) {
		zombie := fixtureSpawn(t, env, "sleeper")
		zpid := zombie.Process.Pid
		if err := zombie.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		// The entry stays a zombie until it is reaped: the state must read
		// as not running while it exists.
		procGone(t, env, zpid, 5*time.Second)
	})
}

// psWarm runs the fixture's staged ps (the real ps on the fixture's own
// PATH) until a round trip is quick, so the stop-tree test below measures
// the stop, not ps's cold start under host load: the readers' 2 s deadline
// is a production contract, so the binary must start fast instead (DJA-637,
// after fakePsWarm in proc_flags_unix_test.go).
func psWarm(t *testing.T, env Env) {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for {
		began := time.Now()
		RunCli("ps", []string{"-o", "pid=,state=", "-p", "1"}, RunOptions{Env: env, TimeoutMs: 2000})
		if time.Since(began) < 400*time.Millisecond {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("ps took over 400 ms per round trip for 30 s; the host load is too high to exercise the TERM round")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStopProcessTreeCases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix TERM/KILL round; the windows round exercises taskkill /T")
	}
	env := procTestEnv(t)
	t.Run("stops the pid and the descendants of it", func(t *testing.T) {
		// The owned tree is the parent plus exactly one sleeper
		// descendant: the native stand-in for the old sh + sleep tree.
		parent, child := treeFixture(t, env)
		// Warm the staged ps (its pages resident, a round trip measured
		// under the current load): a cold ps can outlive the readers' 2 s
		// deadline under host load, and a TimedOut ps reads every pid
		// alive, which pushes the stop into the KILL round this test must
		// not need (DJA-637, after fakePsWarm).
		psWarm(t, env)
		started := time.Now()
		readStarted, _, ok := ProcInfo(parent, env)
		if !ok {
			t.Fatalf("the parent %d is not readable", parent)
		}
		if err := StopProcessTree(parent, readStarted, env); err != nil {
			t.Fatalf("StopProcessTree(%d): %v", parent, err)
		}
		procGone(t, env, parent, 5*time.Second)
		procGone(t, env, child, 5*time.Second)
		if elapsed := time.Since(started); elapsed > 4*time.Second {
			t.Fatalf("the TERM round took %s; a sleeping process must not need the KILL round", elapsed)
		}
	})
	t.Run("a dead pid is a no-op", func(t *testing.T) {
		cmd := fixtureSpawn(t, env, "sleeper")
		pid := cmd.Process.Pid
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait() // reap: the entry leaves the table; the kill error is expected
		if err := StopProcessTree(pid, "", env); err != nil {
			t.Fatalf("StopProcessTree(dead %d): %v", pid, err)
		}
	})
	t.Run("a stop never reaches a pid the tree does not contain", func(t *testing.T) {
		other := fixtureSpawn(t, env, "sleeper")
		parent, _ := treeFixture(t, env)
		readStarted, _, ok := ProcInfo(parent, env)
		if !ok {
			t.Fatal("the parent is not readable")
		}
		if err := StopProcessTree(parent, readStarted, env); err != nil {
			t.Fatal(err)
		}
		if !procAliveStates([]int{other.Process.Pid}, env)[other.Process.Pid] {
			t.Fatalf("the stop signalled a pid outside the tree (%d)", other.Process.Pid)
		}
	})
}

// TestStopRejectsAChildReparentedAfterTheSnapshot is the review probe as
// a durable test with the real process table: the hook TERMs the parent
// between the snapshot and the identity read, so the child reparents
// while the snapshot still lists it under the parent. The child must not
// be signalled and must stay alive.
func TestStopRejectsAChildReparentedAfterTheSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix TERM/KILL round; the windows round exercises the same contract")
	}
	env := procTestEnv(t)
	parent, child := treeFixture(t, env)
	readStarted, _, ok := ProcInfo(parent, env)
	if !ok {
		t.Fatalf("the parent %d is not readable", parent)
	}
	procStopAfterSnapshot = func(root int, e Env) {
		// Reparent the child before the identity pass: TERM the parent,
		// then wait for the reparenting to settle against the real
		// process table (no fixed sleep).
		if p, err := os.FindProcess(root); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		settle := time.Now().Add(5 * time.Second)
		for time.Now().Before(settle) {
			id, live := procReadIdentityReal(child, e)
			if live == ProcRunning && id.ppidKnown && id.ppid != root {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Cleanup(func() { procStopAfterSnapshot = nil })
	if err := StopProcessTree(parent, readStarted, env); err != nil {
		t.Fatalf("StopProcessTree(%d): %v; nothing verified is a clean no-op", parent, err)
	}
	if !procAliveStates([]int{child}, env)[child] {
		t.Fatal("the child was signalled after it reparented; only verified pids are signalled")
	}
	// The child outlived the stop: it is owned by this test, stop it by
	// pid.
	if p, err := os.FindProcess(child); err == nil {
		_ = p.Kill()
	}
}

// TestStopSkipsAKillWhenTheStartTimeChanged: a process that ignores TERM
// reaches the KILL round; the injected identity read then reports a
// different start time for it, and the KILL must not go. The fixture
// prints its handshake only after SIGTERM/SIG_IGN is installed, and the
// test waits for it (with a 10 s deadline) before it reads the identity
// and stops: the stop never races the fixture's start-up, and a fixture
// that dies before it signalled fails the test instead of fooling the
// assertions.
func TestStopSkipsAKillWhenTheStartTimeChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix TERM/KILL round; the windows round exercises the same contract")
	}
	env := procTestEnv(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^"+procFixtureHelperTest+"$")
	items := env.List()
	items = append(items, procFixtureRoleEnv+"=ignoreterm")
	cmd.Env = items
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("opening the fixture's stdout: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr != nil {
			ready <- fmt.Errorf("handshake read: %v (line %q)", readErr, line)
			return
		}
		if strings.TrimSpace(line) != "ready" {
			ready <- fmt.Errorf("unexpected handshake line %q", line)
			return
		}
		ready <- nil
	}()
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the ignoreterm fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // SIGKILL: the process ignores TERM
		_ = cmd.Wait()
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("the fixture never signalled its SIG_IGN readiness: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the fixture did not print the handshake within 10 s of start; SIG_IGN may not be installed")
	}
	pid := cmd.Process.Pid
	readStarted, _, ok := ProcInfo(pid, env)
	if !ok {
		t.Fatalf("the fixture %d is not readable", pid)
	}
	real := procReadIdentity
	reads := map[int]int{}
	procReadIdentity = func(pid int, env Env) (procIdentity, ProcLiveness) {
		id, live := real(pid, env)
		if live == ProcRunning {
			reads[pid]++
			// The collection read and the pre-TERM re-read pass through;
			// from the pre-KILL re-read on the start time is adulterated.
			if reads[pid] >= 3 {
				id.started = "adulterated start time"
			}
		}
		return id, live
	}
	t.Cleanup(func() { procReadIdentity = real })
	if err := StopProcessTree(pid, readStarted, env); err == nil {
		t.Fatal("StopProcessTree succeeded although the process survived; the skipped KILL is a failure to report")
	}
	if !procAliveStates([]int{pid}, env)[pid] {
		t.Fatal("the process was KILLed despite the changed start time")
	}
}

// TestStopTermsTheDeepestDescendantFirst: the TERM round goes from the
// deepest descendant to the root, the root last. The parent ignores TERM
// (and stays alive after its child dies) so both TERM sends are
// observable; the stand-in parent would exit the moment its child stops
// and the second send would be skipped on a zombie.
func TestStopTermsTheDeepestDescendantFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix TERM/KILL round; the windows round exercises the same contract")
	}
	env := procTestEnv(t)
	cmd := fixtureSpawn(t, env, "ignoreterm-child")
	var child int
	deadline := time.Now().Add(5 * time.Second)
	for {
		kids, err := procDescendants(cmd.Process.Pid, env)
		if err == nil && len(kids) == 1 {
			child = kids[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendants of %d never became one (last: %v, %v)", cmd.Process.Pid, kids, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	readStarted, _, ok := ProcInfo(cmd.Process.Pid, env)
	if !ok {
		t.Fatalf("the fixture %d is not readable", cmd.Process.Pid)
	}
	real := procSendTerm
	var order []int
	procSendTerm = func(pid int, env Env) error {
		order = append(order, pid)
		return real(pid, env)
	}
	t.Cleanup(func() { procSendTerm = real })
	if err := StopProcessTree(cmd.Process.Pid, readStarted, env); err != nil {
		t.Fatalf("StopProcessTree(%d): %v", cmd.Process.Pid, err)
	}
	if len(order) != 2 || order[0] != child || order[1] != cmd.Process.Pid {
		t.Fatalf("TERM order = %v; want the child before the parent", order)
	}
}

// TestReview92RejectReparentedSnapshot (review probe, now durable, with
// the real process table and the full lstart/ppid/state reads): the owned
// root is TERMed between the pid/ppid snapshot and the identity pass, so
// the child reparents while the snapshot still lists it under the root.
// The root keeps its registered start time in the identity pass (the real
// entry is dead and the stop must not signal it again), the child's
// current parent diverges from the snapshot's parent and it is dropped,
// and it stays alive. Removing the `id.ppid != parent` comparison makes
// this test fail (the child gets the TERM).
func TestReview92RejectReparentedSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix TERM/KILL round; the windows round exercises the same contract")
	}
	env := procTestEnv(t)
	dir := t.TempDir()
	kidFile := filepath.Join(dir, "kid")
	beat := filepath.Join(dir, "beat")
	root := fixtureSpawn(t, env, "forkroot", kidFile, beat)
	rootPid := root.Process.Pid
	var kid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(kidFile)
		kid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if kid > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if kid <= 0 {
		t.Fatal("child not started")
	}
	t.Cleanup(func() { p, _ := os.FindProcess(kid); _ = p.Kill() })
	if !waitBeatPPID(t, beat, rootPid, 5*time.Second) {
		t.Fatalf("initial parent invalid: the heartbeat never reported %d", rootPid)
	}
	t.Logf("owned parent=%d child=%d initial-ppid=%d", rootPid, kid, rootPid)
	readStarted, _, ok := ProcInfo(rootPid, env)
	if !ok {
		t.Fatalf("the root %d is not readable", rootPid)
	}
	// The snapshot (the real pid/ppid table) lists the child under the
	// root. The hook TERMs the root before the identity pass and lets the
	// reparenting settle; the identity pass keeps the root verified
	// against its registered start time (as the review fixture's ps did),
	// while the child's identity read is the real one: its current parent
	// is no longer the root, and only the `id.ppid != parent` comparison
	// can drop it.
	procStopAfterSnapshot = func(r int, e Env) {
		if p, err := os.FindProcess(r); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		settle := time.Now().Add(5 * time.Second)
		for time.Now().Before(settle) {
			id, live := procReadIdentityReal(kid, e)
			if live == ProcRunning && id.ppidKnown && id.ppid != r {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Cleanup(func() { procStopAfterSnapshot = nil })
	realIdentity := procReadIdentity
	t.Cleanup(func() { procReadIdentity = realIdentity })
	procReadIdentity = func(pid int, e Env) (procIdentity, ProcLiveness) {
		if pid == rootPid {
			return procIdentity{started: readStarted, ppid: os.Getpid(), ppidKnown: true}, ProcRunning
		}
		return procReadIdentityReal(pid, e)
	}
	err := StopProcessTree(rootPid, readStarted, env)
	t.Logf("StopProcessTree error=%v", err)
	if err != nil {
		t.Fatalf("StopProcessTree: %v; the divergent candidate is dropped without a signal", err)
	}
	// The last heartbeat line can predate the reparenting (the beat has a
	// 50 ms granularity): poll for the new parent, with a ceiling.
	deadline = time.Now().Add(time.Second)
	ppid := rootPid
	for time.Now().Before(deadline) {
		ppid = beatPPID(beat)
		if ppid != rootPid {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ppid == rootPid {
		t.Fatal("child did not reparent: fixture invalid")
	}
	t.Logf("after snapshot: child=%d ppid=%d former-parent=%d", kid, ppid, rootPid)
	before, _ := os.ReadFile(beat)
	time.Sleep(200 * time.Millisecond)
	after, _ := os.ReadFile(beat)
	if string(before) == string(after) {
		t.Fatal("stop signalled the child after it changed parent: heartbeat stopped")
	}
}

// TestReview92StopsOwnedChild (review probe control, now durable, with
// the real process table and the full lstart/ppid/state reads): the
// parent remains in the tree — the child's current parent is still the
// snapshot's parent, it stays verified and the stop reaches it.
//
// Verify ownership before stopping: a terminated heartbeat writer can leave
// an empty file. After stopping, check both process states and the heartbeat.
func TestReview92StopsOwnedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix TERM/KILL round; the windows round exercises the same contract")
	}
	env := procTestEnv(t)
	dir := t.TempDir()
	kidFile := filepath.Join(dir, "kid")
	beat := filepath.Join(dir, "beat")
	root := fixtureSpawn(t, env, "forkroot", kidFile, beat)
	rootPid := root.Process.Pid
	var kid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(kidFile)
		kid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if kid > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if kid <= 0 {
		t.Fatal("child not started")
	}
	t.Cleanup(func() { p, _ := os.FindProcess(kid); _ = p.Kill() })
	if !waitBeatPPID(t, beat, rootPid, 5*time.Second) {
		t.Fatalf("initial parent invalid: the heartbeat never reported %d", rootPid)
	}
	t.Logf("owned parent=%d child=%d initial-ppid=%d", rootPid, kid, rootPid)
	readStarted, _, ok := ProcInfo(rootPid, env)
	if !ok {
		t.Fatalf("the root %d is not readable", rootPid)
	}
	kidID, kidLive := procReadIdentityReal(kid, env)
	if kidLive != ProcRunning || !kidID.ppidKnown || kidID.ppid != rootPid {
		t.Fatalf("pre-stop: child %d is not owned by the root (live=%v ppidKnown=%v ppid=%d, want running under %d)", kid, kidLive, kidID.ppidKnown, kidID.ppid, rootPid)
	}
	t.Logf("pre-stop ownership: child=%d ppid=%d under root=%d", kid, kidID.ppid, rootPid)
	err := StopProcessTree(rootPid, readStarted, env)
	t.Logf("StopProcessTree error=%v", err)
	if err != nil {
		t.Fatalf("StopProcessTree: %v; the owned tree is verified and stopped", err)
	}
	// Mutation captured: omitting verified descendants leaves the child alive.
	procGone(t, env, rootPid, 5*time.Second)
	procGone(t, env, kid, 5*time.Second)
	before, _ := os.ReadFile(beat)
	time.Sleep(200 * time.Millisecond)
	after, _ := os.ReadFile(beat)
	if string(before) != string(after) {
		t.Fatal("owned tree child survived stop: heartbeat still advances")
	}
}
