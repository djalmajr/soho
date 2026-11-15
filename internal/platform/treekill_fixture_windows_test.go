//go:build windows

package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	testutil "github.com/djalmajr/soho/internal/testutil/testenv"
)

// The Windows tree-kill fixtures drive the real wrapper.cmd -> cmd.exe chain
// against a small helper process that exercises every RunCli stream mode.
// The helper is a test-only Go binary compiled by each top-level test that
// needs it and reused by every fixture of that test, so fixture timing
// never depends on writing and starting fresh executable copies.
//
// The helper provides complete/parent/parent-exits/grandchild modes, stdin
// echo, stream output, a <pidfile>.<mode>.started marker written before any
// output, and a two-line pid file. Readiness for the timed assertions is
// proven by observing that start marker before the readiness run completes
// (treeKillChainReady), so no timed assertion races a cold start.
const treeKillHelperSource = `package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	mode := os.Getenv("SOHO_TREEKILL_FAKE_MODE")
	pidFile := os.Getenv("SOHO_TREEKILL_PID_FILE")
	if pidFile != "" {
		_ = os.WriteFile(pidFile+"."+mode+".started", []byte(fmt.Sprint(os.Getpid())), 0o600)
	}
	switch mode {
	case "complete":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tree helper: read stdin: %v (mode=%s pidFile=%s)\n", err, mode, pidFile)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "tree-output", string(input))
		fmt.Fprintln(os.Stderr, "tree-error")
	case "parent", "parent-exits":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tree helper: read stdin: %v (mode=%s pidFile=%s)\n", err, mode, pidFile)
			os.Exit(1)
		}
		child := exec.Command(os.Args[0])
		childEnv := make([]string, 0, len(os.Environ())+1)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "SOHO_TREEKILL_FAKE_MODE=") {
				childEnv = append(childEnv, entry)
			}
		}
		child.Env = append(childEnv, "SOHO_TREEKILL_FAKE_MODE=grandchild")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "tree helper: start descendant: %v (mode=%s pidFile=%s)\n", err, mode, pidFile)
			os.Exit(1)
		}
		pids := fmt.Sprintf("%d\n%d\n", os.Getpid(), child.Process.Pid)
		if err := os.WriteFile(pidFile, []byte(pids), 0o600); err != nil {
			_ = child.Process.Kill()
			fmt.Fprintf(os.Stderr, "tree helper: record pids: %v (mode=%s pidFile=%s)\n", err, mode, pidFile)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "tree-output", string(input))
		fmt.Fprintln(os.Stderr, "tree-error")
		if mode == "parent" {
			time.Sleep(time.Hour) // not select{}: with no other goroutine the runtime aborts it as a deadlock
		}
	case "grandchild":
		time.Sleep(time.Hour) // not select{}: with no other goroutine the runtime aborts it as a deadlock
	}
}
`

// treeKillHelperBuildBound bounds the one small `go build` of the fixture
// helper on a cold build cache.
const treeKillHelperBuildBound = 5 * time.Minute

// treeKillHelperBuildWaitDelay bounds how long the build's wait may linger
// after the bound cancels the build: build children can hold the output
// pipes after the go tool itself is killed.
const treeKillHelperBuildWaitDelay = 30 * time.Second

// withoutGOFLAGS returns env without any GOFLAGS entry, comparing the key
// part of each entry case-insensitively: Windows environment keys are
// case-insensitive, so go build would honor GoFlags or goflags as well.
func withoutGOFLAGS(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, hasEq := strings.Cut(entry, "=")
		if hasEq && strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func TestWithoutGOFLAGS(t *testing.T) {
	got := withoutGOFLAGS([]string{
		`PATH=C:\go\bin`,
		"GOFLAGS=-race",
		"GoFlags=-tags=extra",
		"goflags=-buildvcs=true",
		"CC=gcc",
		"GOROOT",
	})
	want := []string{`PATH=C:\go\bin`, "CC=gcc", "GOROOT"}
	if len(got) != len(want) {
		t.Fatalf("withoutGOFLAGS kept %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("withoutGOFLAGS[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// treeKillHelperExe compiles the fixture helper into the calling test's
// t.TempDir(), which the testing package removes after that test and its
// subtests finish; each top-level test that needs the helper builds it
// once, and the go build cache keeps repeated builds cheap. No fixture is
// timed while its helper is still compiling. The build runs with GOFLAGS
// removed case-insensitively, is bounded, and a failure fails the test
// with the build output.
func treeKillHelperExe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "treekillhelper.go")
	if err := os.WriteFile(src, []byte(treeKillHelperSource), 0o600); err != nil {
		t.Fatalf("write fixture helper source: %v", err)
	}
	exe := filepath.Join(dir, "treekillhelper.exe")
	ctx, stop := context.WithTimeout(context.Background(), treeKillHelperBuildBound)
	defer stop()
	build := exec.CommandContext(ctx, "go", "build", "-o", exe, src)
	build.WaitDelay = treeKillHelperBuildWaitDelay
	build.Env = withoutGOFLAGS(testutil.CleanEnv(t))
	started := time.Now()
	out, err := build.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("compile fixture helper timed out after %s: %s", treeKillHelperBuildBound, out)
		}
		t.Fatalf("compile fixture helper: %v: %s", err, out)
	}
	t.Logf("fixture helper compiled into %s in %s", dir, time.Since(started))
	return exe
}

// escapeBatchArg makes an absolute path safe to embed in a quoted command
// line of a .cmd file: cmd.exe expands %variables even inside quotes, so
// every % is doubled. A path containing a quote cannot be embedded safely
// and is refused instead of producing a broken wrapper.
func escapeBatchArg(path string) (string, error) {
	if strings.Contains(path, `"`) {
		return "", os.ErrInvalid
	}
	return strings.ReplaceAll(path, "%", "%%"), nil
}

func TestTreeKillFixtureEscapeBatchArg(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: `C:\tools\helper.exe`, want: `C:\tools\helper.exe`},
		{in: `C:\tmp%TEMP%\helper.exe`, want: `C:\tmp%%TEMP%%\helper.exe`},
		{in: `C:\a\%b%\c%d%.exe`, want: `C:\a\%%b%%\c%%d%%.exe`},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := escapeBatchArg(tc.in)
			if err != nil {
				t.Fatalf("escapeBatchArg(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("escapeBatchArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got, err := escapeBatchArg(`C:\bad"name\helper.exe`); err == nil || got != "" {
		t.Fatalf("a path containing a quote was not refused: got %q err %v", got, err)
	}
}

// treeKillFixtureWrapper writes the fixture wrapper.cmd into dir, invoking
// the compiled fixture helper through the real wrapper.cmd -> cmd.exe chain.
// No fixture writes or starts a fresh copy of a large executable, so the
// 1 s product timeouts are not racing a per-fixture executable write and
// cold start.
func treeKillFixtureWrapper(t *testing.T, dir string, helper string) string {
	t.Helper()
	arg, err := escapeBatchArg(helper)
	if err != nil {
		t.Fatalf("write fixture wrapper: %v", err)
	}
	wrapper := filepath.Join(dir, "wrapper.cmd")
	content := "@echo off\r\n@\"" + arg + "\"\r\n"
	if err := os.WriteFile(wrapper, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture wrapper: %v", err)
	}
	return wrapper
}

// treeKillChainReady proves the fixture chain reaches the helper before any
// timed assertion runs. It starts the exact wrapper chain once, in the
// parent mode that blocks before exit, and waits a bounded time for the
// helper's start marker (<pidfile>.parent.started), which the helper writes
// before any output, so the marker is observed before the run completes and
// proves the chain reached the helper. No wall time of the run is required
// to be below any value and nothing is retried: if the marker is missing
// when the bound passes, the first failure is preserved with a short
// diagnostic (which marker, elapsed). The readiness tree is then stopped by
// its own recorded pids with the same cleanup the fixtures use. The product
// TimeoutMs values asserted on (1000/5000/15000 ms) and every result
// assertion are unchanged.
func treeKillChainReady(t *testing.T, env Env, dir string) {
	t.Helper()
	const readyBoundMs = 15000 // the bound the existing completion cases already use
	readyPidFile := filepath.Join(dir, "ready-pids.txt")
	marker := readyPidFile + ".parent.started"
	readyEnv := env.Clone()
	readyEnv["SOHO_TREEKILL_FAKE_MODE"] = "parent"
	readyEnv["SOHO_TREEKILL_PID_FILE"] = readyPidFile
	t.Cleanup(func() { cleanupRecordedProcesses(readyPidFile) })
	started := time.Now()
	done := make(chan RunResult, 1)
	go func() {
		done <- RunCli("wrapper", nil, RunOptions{Env: readyEnv, Platform: "win32", Cwd: dir, TimeoutMs: readyBoundMs})
	}()
	deadline := time.Now().Add(time.Duration(readyBoundMs) * time.Millisecond)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if ready {
		// The helper records both pids right after the marker; give it a
		// bounded grace so the cleanup below stops both ends of the tree.
		pidDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(pidDeadline) {
			if _, err := os.Stat(readyPidFile); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	cleanupRecordedProcesses(readyPidFile)
	got := <-done
	elapsed := time.Since(started)
	if !ready {
		t.Fatalf("readiness marker %s not observed within %d ms (elapsed %s): status=%s timedOut=%v error=%s (mode=parent pidFile=%s PATHEXT=%s pathFirst=%v)",
			marker, readyBoundMs, elapsed, treeKillStatus(got), got.TimedOut, got.Error, readyPidFile, readyEnv.Get("PATHEXT"), strings.HasPrefix(readyEnv.Get("PATH"), dir+";"))
	}
	t.Logf("tree-kill fixture ready: start marker %s observed before completion (elapsed %s, bound %d ms)", filepath.Base(marker), elapsed, readyBoundMs)
}
