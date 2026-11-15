package job

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

// own_checkout_query_test.go: ownCheckoutGitQuery runs a real child
// process. This test binary stands in for git (a copy or link named git on
// the fixture PATH) in the modes below, so the bounds are proven on an
// executed process: the stdout cap while capturing, the WaitDelay when a
// descendant keeps stdout open, and the deadline.

const ownQueryModeVar = "OWN_CHECKOUT_QUERY_MODE"

func init() {
	mode := os.Getenv(ownQueryModeVar)
	if mode == "" {
		return
	}
	switch mode {
	case "answer":
		_, _ = os.Stdout.WriteString("/a/top\n/a/top/.git\n/a/top/.git\n")
		os.Exit(0)
	case "flood":
		chunk := strings.Repeat("x", 64<<10)
		for i := 0; i < 64; i++ {
			if _, err := os.Stdout.WriteString(chunk); err != nil {
				os.Exit(3)
			}
		}
		os.Exit(0)
	case "leak":
		// Answer at once and leave a descendant holding stdout open.
		self, err := os.Executable()
		if err != nil {
			os.Exit(90)
		}
		child := exec.Command(self)
		child.Env = append(os.Environ(), ownQueryModeVar+"=hold")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(91)
		}
		_ = os.WriteFile(os.Getenv("OWN_CHECKOUT_QUERY_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		_, _ = os.Stdout.WriteString("/a/top\n/a/top/.git\n/a/top/.git\n")
		os.Exit(0)
	case "hold", "hang":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

// ownQueryFakeGit puts this test binary on a fresh PATH as git and returns
// the env for mode.
func ownQueryFakeGit(t *testing.T, mode string) platform.Env {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	if err := os.Symlink(self, filepath.Join(bin, name)); err != nil {
		data, readErr := os.ReadFile(self)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(bin, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{"PATH": bin, ownQueryModeVar: mode}
	if runtime.GOOS == "windows" {
		env["PATHEXT"] = ".EXE"
		env["SystemRoot"] = testutil.HostSystemRoot()
	}
	return env
}

func TestOwnCheckoutQueryRunsTheChild(t *testing.T) {
	// The answer of the process on PATH is returned whole: the query reads
	// the child's stdout (removing the capture would return "").
	out, ok := ownCheckoutGitQuery(ownQueryFakeGit(t, "answer"), t.TempDir())
	if !ok || out != "/a/top\n/a/top/.git\n/a/top/.git\n" {
		t.Fatalf("answer: ok=%v out=%q", ok, out)
	}
}

func TestOwnCheckoutQueryCapsStdoutWhileCapturing(t *testing.T) {
	// 4 MiB of stdout: refused, and never more than the cap is held (the
	// writer stores up to the cap and drains the rest).
	out, ok := ownCheckoutGitQuery(ownQueryFakeGit(t, "flood"), t.TempDir())
	if ok || out != "" {
		t.Fatalf("flood: ok=%v len=%d, want refused", ok, len(out))
	}
	w := &cappedOutput{max: 8}
	if n, err := w.Write([]byte("0123456789")); n != 10 || err != nil || !w.over || w.buf.String() != "01234567" {
		t.Fatalf("cappedOutput: n=%d err=%v over=%v kept=%q", n, err, w.over, w.buf.String())
	}
	if n, _ := w.Write([]byte("more")); n != 4 || w.buf.Len() != 8 {
		t.Fatalf("cappedOutput after the cap: n=%d kept=%d", n, w.buf.Len())
	}
}

func TestOwnCheckoutQueryDoesNotWaitForADescendantHoldingStdout(t *testing.T) {
	// The child answers and exits while a descendant keeps the stdout pipe
	// open for 60 s: the query returns after WaitDelay (refusing the
	// answer it could not confirm complete), not after the descendant.
	env := ownQueryFakeGit(t, "leak")
	pidFile := filepath.Join(t.TempDir(), "pid")
	env["OWN_CHECKOUT_QUERY_PID"] = pidFile
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(string(raw)); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
					_, _ = p.Wait()
				}
			}
		}
	})
	start := time.Now()
	_, ok := ownCheckoutGitQuery(env, t.TempDir())
	elapsed := time.Since(start)
	if ok {
		t.Fatalf("leak: the answer was accepted though stdout never closed")
	}
	if elapsed >= 30*time.Second {
		t.Fatalf("leak: the query waited %v for the descendant", elapsed)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("leak: the descendant never started: %v", err)
	}
}

func TestOwnCheckoutQueryDeadline(t *testing.T) {
	// A child that never answers is ended by the 10 s deadline.
	start := time.Now()
	_, ok := ownCheckoutGitQuery(ownQueryFakeGit(t, "hang"), t.TempDir())
	elapsed := time.Since(start)
	if ok || elapsed >= 30*time.Second {
		t.Fatalf("hang: ok=%v after %v", ok, elapsed)
	}
}

func TestOwnCheckoutQueryWithoutGit(t *testing.T) {
	// No git on PATH: no answer, no anchor.
	env := platform.Env{"PATH": t.TempDir()}
	if _, ok := ownCheckoutGitQuery(env, t.TempDir()); ok {
		t.Fatal("a query without git answered")
	}
	if store := OwnCheckoutStore(env, t.TempDir(), "job-1"); store != nil {
		t.Fatalf("an anchor without git: %v", store.Root)
	}
}
