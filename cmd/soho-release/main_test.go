// Native actual-CLI signal/cancellation contract for the release tool.
// The real soho-release binary (built from this package's source by
// the trusted absolute go toolchain) runs a real `build` against a native
// fake slow `go` child — the running test binary re-executed via a PATH
// shim, no script engine. The child's identity is the exact original
// native start time and command name read while it is alive (a PID file is
// a locator, never an identity); the single Wait goroutine started right
// after the CLI's Start is shared by the normal path and the cleanup, so
// no double Wait and no output race. After the live child, its verified
// identity and the actual owned staging are checked, the test sends an
// owned signal to the exact CLI child handle it started and proves the
// bounded outcome: the CLI exits through the cancellation path (not the
// default signal death), the go child is proven gone, only the owned
// staging is removed, no final destination appears, and unrelated content
// is preserved. Unknown liveness is never treated as absence and never
// signalled.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

// TestMain doubles as the re-executed fake `go`: the release CLI runs
// `go build ...`, so the helper dispatches on argv[1] == "build", exactly
// like the internal/release tests dispatch their fakes.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "build" {
		os.Exit(runSlowGoHelper())
	}
	os.Exit(m.Run())
}

// runSlowGoHelper is the fake slow `go build` child: it records its own
// PID (a locator only: the identity is the native start+name read) and
// sleeps until the CLI's exec.CommandContext cancels the context and kills
// it.
func runSlowGoHelper() int {
	if pidPath := os.Getenv("SIGNAL_TEST_PIDFILE"); pidPath != "" {
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	}
	time.Sleep(30 * time.Second)
	return 0
}

// procTrustedEnv returns this test process's own environment. It is
// captured before any child gets a restricted PATH, so the native by-pid
// lookup (ps on unix, kernel32 on windows) runs with the real toolchain
// environment, never the child's reduced one.
func procTrustedEnv() platform.Env {
	return platform.Env{"PATH": testutil.HostPath(), "SystemRoot": testutil.HostSystemRoot()}
}

// signalCase shares the state of one signal case between the normal path
// and the cleanup: the exact owned CLI handle, the single Wait result
// channel, the trusted parent lookup env and the captured identities.
type signalCase struct {
	trusted       platform.Env
	root          string
	dest          string
	pidFile       string
	sentinel      string
	sentinelBytes []byte
	sibling       string

	cli      *exec.Cmd
	waitCh   chan error
	waited   bool
	lastErr  error
	cliPID   int
	cliStart string

	goPID   int
	goStart string
	goName  string

	out    *bytes.Buffer
	errOut *bytes.Buffer
}

// startSignalCase builds the fixtures, compiles the real CLI with the
// trusted absolute go toolchain (context bounded), starts it with the
// restricted child env, starts the single Wait goroutine immediately and
// registers the cleanup before any assertion.
func startSignalCase(t *testing.T) *signalCase {
	t.Helper()
	trusted := procTrustedEnv()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "sentinel")
	sentinelBytes := []byte("release-signal-sentinel\n")
	if err := os.WriteFile(sentinel, sentinelBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "sibling", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("sibling-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "dist")

	// The real CLI, built from this package's source by the trusted
	// absolute go executable (resolved from this process's own PATH, never
	// the child's) with a bounded context.
	hsr := filepath.Join(root, "soho-release")
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("resolving the trusted go toolchain: %v", err)
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelBuild()
	built := exec.CommandContext(buildCtx, goPath, "build", "-o", hsr, ".")
	var buildOut bytes.Buffer
	built.Stderr = &buildOut
	if err := built.Run(); err != nil {
		t.Fatalf("building the real CLI with %s failed: %v: %s", goPath, err, buildOut.String())
	}
	// The fake slow `go` is the running test binary, reachable as "go"
	// through a PATH shim the CLI child alone sees.
	fakebin := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(fakebin, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(fakebin, "go")); err != nil {
		t.Fatal(err)
	}

	pidFile := filepath.Join(root, "gopid")
	c := &signalCase{
		trusted: trusted, root: root, dest: dest, pidFile: pidFile,
		sentinel: sentinel, sentinelBytes: sentinelBytes, sibling: sibling,
		out: &bytes.Buffer{}, errOut: &bytes.Buffer{},
	}
	c.cli = exec.Command(hsr, "build", "--repo", repo, "--dest", dest)
	c.cli.Env = []string{
		"PATH=" + fakebin,
		"HOME=" + root,
		"SIGNAL_TEST_PIDFILE=" + pidFile,
	}
	c.cli.Stdout, c.cli.Stderr = c.out, c.errOut
	if err := c.cli.Start(); err != nil {
		t.Fatalf("CLI start: %v", err)
	}
	c.cliPID = c.cli.Process.Pid
	c.waitCh = make(chan error, 1)
	go func() { c.waitCh <- c.cli.Wait() }() // the single Wait goroutine
	t.Cleanup(c.reap)
	cliStarted, _, cliOK := platform.ProcInfo(c.cliPID, c.trusted)
	if !cliOK {
		t.Fatalf("the CLI child is not verifiably running after Start (pid %d)", c.cliPID)
	}
	c.cliStart = cliStarted
	return c
}

// cliVerified reports whether the CLI child is verifiably the process we
// started (a fresh native start read matching the original), or gone.
func (c *signalCase) cliVerified() (string, bool) {
	started, live := platform.ReadProc(c.cliPID, c.trusted)
	if live == platform.ProcRunning && platform.SameStarted(started, c.cliStart) {
		return started, true
	}
	return "", false
}

// reap is the cleanup body: it reaps the owned CLI child (kill only when a
// fresh read still verifies the original start, then drain the single Wait
// with a bound) and kills the fake go child only when a fresh read still
// verifies the original start AND name. ProcGone needs no action and
// ProcUnknown is never signalled: unknown is not an absence.
func (c *signalCase) reap() {
	if !c.waited {
		if _, verified := c.cliVerified(); verified {
			_ = c.cli.Process.Kill()
		}
		select {
		case <-c.waitCh:
		case <-time.After(10 * time.Second):
		}
	}
	if c.goPID <= 0 {
		return
	}
	started, name, ok := platform.ProcInfo(c.goPID, c.trusted)
	if ok && platform.SameStarted(started, c.goStart) && name == c.goName {
		if p, err := os.FindProcess(c.goPID); err == nil {
			_ = p.Kill()
		}
	}
}

// waitFakeGoReady polls until the fake go child is natively verified
// (locator PID file plus a live ProcInfo with the exact original start and
// name) and the actual owned staging exists.
func (c *signalCase) waitFakeGoReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(c.pidFile); err == nil {
			if n, _ := strconv.Atoi(strings.TrimSpace(string(data))); n > 0 && n != c.cliPID {
				started, name, ok := platform.ProcInfo(n, c.trusted)
				if ok {
					c.goPID, c.goStart, c.goName = n, started, name
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake go child never verified natively (locator %q, CLI pid %d)", c.pidFile, c.cliPID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("fake go child verified: pid=%d started=%q name=%q", c.goPID, c.goStart, c.goName)
	staging, _ := filepath.Glob(filepath.Join(c.root, ".soho-release-*"))
	if len(staging) != 1 {
		t.Fatalf("expected exactly one owned staging directory before the signal, got %v", staging)
	}
	started, live := platform.ReadProc(c.goPID, c.trusted)
	if live != platform.ProcRunning || !platform.SameStarted(started, c.goStart) {
		t.Fatalf("the fake go child no longer reads as the original process (liveness=%v)", live)
	}
	if b, err := os.ReadFile(c.sentinel); err != nil || !bytes.Equal(b, c.sentinelBytes) {
		t.Fatalf("the sentinel changed before the signal")
	}
}

// waitCLIExit re-verifies the owned CLI child, sends the owned signal to
// the exact child handle, waits with a bound through the single Wait
// result, and checks the bounded cancellation outcome.
func (c *signalCase) waitCLIExit(t *testing.T, sig os.Signal) {
	t.Helper()
	if _, verified := c.cliVerified(); !verified {
		t.Fatalf("the CLI child is not verifiably the original process before the signal")
	}
	if err := c.cli.Process.Signal(sig); err != nil {
		t.Fatalf("sending the owned signal to the CLI (pid %d): %v", c.cliPID, err)
	}
	start := time.Now()
	select {
	case c.lastErr = <-c.waitCh:
		c.waited = true
	case <-time.After(15 * time.Second):
		t.Fatalf("the CLI did not exit within 15s of the signal (go child pid %d)", c.goPID)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the CLI exit took %s: not bounded", elapsed)
	}
	var exitErr *exec.ExitError
	if !errors.As(c.lastErr, &exitErr) {
		t.Fatalf("the CLI did not exit as a normal process: %v (stdout=%q stderr=%q)", c.lastErr, c.out.String(), c.errOut.String())
	}
	var signaled bool
	if ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
		signaled = ws.Signaled()
	}
	if signaled {
		t.Fatalf("the CLI died by default signal handling, not the cancellation path (stdout=%q stderr=%q)", c.out.String(), c.errOut.String())
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("CLI exit code=%d, want 1 (the build error path); stdout=%q stderr=%q", exitErr.ExitCode(), c.out.String(), c.errOut.String())
	}
	if !strings.Contains(c.errOut.String(), "release: building soho_darwin_amd64") {
		t.Fatalf("stderr does not name the cancelled first build: %q", c.errOut.String())
	}
	if !strings.Contains(c.errOut.String(), "signal: killed") {
		t.Fatalf("stderr does not show the killed go child: %q", c.errOut.String())
	}
	if c.out.String() != "" {
		t.Fatalf("the cancelled CLI printed success output: %q", c.out.String())
	}
}

// verifyPostState checks the proven three-way absence of the go child, the
// owned staging removal, the absent destination and the preserved
// unrelated content.
func (c *signalCase) verifyPostState(t *testing.T) {
	t.Helper()
	started, live := platform.ReadProc(c.goPID, c.trusted)
	if live != platform.ProcGone {
		t.Fatalf("the fake go child is not proven gone (liveness=%v, started=%q)", live, started)
	}
	if rest, _ := filepath.Glob(filepath.Join(c.root, ".soho-release-*")); len(rest) != 0 {
		t.Fatalf("the owned staging survived the signal: %v", rest)
	}
	if _, err := os.Stat(c.dest); !os.IsNotExist(err) {
		t.Fatalf("the destination exists after the signal: %v", err)
	}
	if b, err := os.ReadFile(c.sentinel); err != nil || !bytes.Equal(b, c.sentinelBytes) {
		t.Fatalf("the sentinel was not preserved: %q", b)
	}
	if b, err := os.ReadFile(c.sibling); err != nil || !bytes.Equal(b, []byte("sibling-bytes\n")) {
		t.Fatalf("the unrelated sibling content was not preserved: %q", b)
	}
}

func TestReleaseCLISignalCleansOwnedStaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows console Ctrl-C is delivered per console to every attached
		// process; a targeted console proof is unavailable from this test,
		// so Windows stays compile-only here (the installed os/signal docs
		// say os.Interrupt is the only user-generatable Windows signal).
		t.Skip("Windows console Ctrl-C needs a native console proof; root owns native Windows execution")
	}
	for _, tc := range []struct {
		name string
		sig  os.Signal
	}{
		{"SIGINT", os.Interrupt},
		{"SIGTERM", syscall.SIGTERM},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) { runReleaseCLISignalCase(t, tc.sig) })
	}
}

func runReleaseCLISignalCase(t *testing.T, sig os.Signal) {
	t.Helper()
	c := startSignalCase(t)
	c.waitFakeGoReady(t)
	c.waitCLIExit(t, sig)
	c.verifyPostState(t)
}
