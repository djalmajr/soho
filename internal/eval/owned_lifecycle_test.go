package eval

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// ownedFixtureSource is the fixture of the RunOwnedProcess lifecycle
// regression (DJA-695 security amendment, F1): the child mode spawns a
// grandchild that outlives it (a 300 s sleep), writes the grandchild's PID
// to a file and exits 7 — the unsuccessful exit that must still end the
// whole owned tree. The sleep mode is the grandchild itself (the same
// binary, so no second fixture is needed).
const ownedFixtureSource = `package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(64)
	}
	switch os.Args[1] {
	case "sleep":
		time.Sleep(300 * time.Second)
	case "child":
		pidFile := os.Args[2]
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command(os.Args[0], "sleep")
		} else {
			cmd = exec.Command("sleep", "300")
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fixture grandchild:", err)
			os.Exit(65)
		}
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "fixture pid file:", err)
			os.Exit(66)
		}
		os.Exit(7)
	}
	os.Exit(64)
}
`

// compileOwnedFixture builds the fixture inside the scratch, hermetic the
// way the package's own probe fixtures are built (toolchain-local, proxy
// off, cache inside the scratch).
func compileOwnedFixture(t *testing.T, scratch string) string {
	t.Helper()
	dir := filepath.Join(scratch, "ownedfixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module ownedfixture\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(ownedFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "ownedfixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command(lifecycleGoExecutable(t), "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = lifecycleScratchEnv(scratch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the owned fixture: %v: %s", err, out)
	}
	return bin
}

// TestOwnedProcessUnsuccessfulRunEndsTheGrandchild covers the security
// amendment scenario 3 (F1), natively on both systems: the child (the
// fixture) spawns a grandchild that outlives it and exits 7 — the
// unsuccessful exit must still end the whole owned tree. The grandchild is
// not alive after the return, and the exit code 7 comes back.
func TestOwnedProcessUnsuccessfulRunEndsTheGrandchild(t *testing.T) {
	scratch := lifecycleScratch(t)
	bin := compileOwnedFixture(t, scratch)
	pidFile := filepath.Join(scratch, "grandchild.pid")
	logFile, err := os.Create(filepath.Join(scratch, "owned.log"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, runErr := RunOwnedProcess(ctx, bin, []string{"child", pidFile}, OwnedProcessOptions{Dir: scratch, Output: logFile})
	_ = logFile.Close()
	if !res.Started {
		raw, _ := os.ReadFile(filepath.Join(scratch, "owned.log"))
		t.Fatalf("the fixture did not start: %v\n%s", runErr, raw)
	}
	if res.ExitCode != 7 {
		raw, _ := os.ReadFile(filepath.Join(scratch, "owned.log"))
		t.Fatalf("exit code = %d, want 7 (err %v)\n%s", res.ExitCode, runErr, raw)
	}
	if runtime.GOOS == "windows" {
		if runErr != nil {
			t.Fatalf("an exit-7 run must return without a run error on Windows: %v", runErr)
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 7 {
			t.Fatalf("the unsuccessful run must surface as an exit-7 error: %v (type %T)", runErr, runErr)
		}
	}
	pid := readPIDFile(pidFile)
	if pid <= 0 {
		t.Fatal("the grandchild pid file is missing: the fixture never reached the grandchild")
	}
	if !waitWhile(10*time.Second, func() bool { return processAlive(pid) }) {
		t.Fatalf("grandchild %d is still alive after the unsuccessful owned run returned (F1)", pid)
	}
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatalf("scratch not removable after the unsuccessful run: %v", err)
	}
}
