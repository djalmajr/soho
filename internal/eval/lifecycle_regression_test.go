// Native owned-process lifecycle regressions (E1 findings F-3/F-4/F-5/F-6):
// shared, portable fixture and helpers. The per-OS mechanisms live in
// lifecycle_regression_unix_test.go (!windows: process-group kill) and
// lifecycle_regression_windows_test.go (windows: job-object containment).
package eval

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil"
)

// lifecycleFixtureSource is the fixture module: a green go test that re-execs
// its own compiled binary in two further generations. The middle generation
// spawns the child with stdio detached to devnull (no inherited pipes) and
// exits, so the child is re-parented out of the go test tree while it stays
// in the owned process group / job. The child holds a scratch file and
// blocks forever with no stdio: nothing the runner waits on (its pipes) is
// held by the child, so only the owned group / job containment can stop it.
// This is the exact class of the executed review probe S3.
const lifecycleFixtureSource = `package lifecycle

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func init() {
	pidFile := os.Getenv("HERDR_EVAL_LIFECYCLE_PIDFILE")
	marker := os.Getenv("HERDR_EVAL_LIFECYCLE_MARKER")
	// The CHILD branch takes precedence: the middle generation starts its
	// child with CHILD=1 APPENDED to the inherited environment, and that
	// environment still carries MIDDLE=1 (it is never cleared). Checking
	// MIDDLE first made every descendant repeat the middle branch — a
	// fixture fork loop that never reached the live child. With the child
	// branch first, a descendant that carries both flags is the live child.
	if os.Getenv("HERDR_EVAL_LIFECYCLE_CHILD") == "1" {
		if marker != "" {
			if f, err := os.OpenFile(marker+"-child-held", os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				f.Close()
			}
			// The child writes its own ready marker after it has survived
			// startup: a PID captured from a successful Start is not proof
			// the child stays alive, and the parent only completes after
			// seeing this marker.
			_ = os.WriteFile(marker+"-child-ready", []byte("ready"), 0o644)
		}
		// A live sleeping loop, never select{}: an empty select in the
		// only goroutine triggers the Go runtime fatal "all goroutines
		// are asleep - deadlock!" and the child dies immediately.
		for {
			time.Sleep(30 * time.Second)
		}
	}
	if os.Getenv("HERDR_EVAL_LIFECYCLE_MIDDLE") == "1" {
		bin, err := os.Executable()
		if err != nil {
			os.Exit(1)
		}
		cmd := exec.Command(bin)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnullFile(), devnullFile(), devnullFile()
		cmd.Env = append(os.Environ(),
			"HERDR_EVAL_LIFECYCLE_CHILD=1",
			"HERDR_EVAL_LIFECYCLE_PIDFILE="+pidFile,
			"HERDR_EVAL_LIFECYCLE_MARKER="+marker)
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// The real test generation (only): mark that the go test process ran.
	// A process refused before resume never reaches this line.
	if marker != "" {
		_ = os.WriteFile(marker, []byte("ran"), 0o644)
	}
}

func TestLifecycleParent(t *testing.T) {
	if os.Getenv("HERDR_EVAL_LIFECYCLE_NOCHILD") == "1" {
		return
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := os.Getenv("HERDR_EVAL_LIFECYCLE_PIDFILE")
	marker := os.Getenv("HERDR_EVAL_LIFECYCLE_MARKER")
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"HERDR_EVAL_LIFECYCLE_MIDDLE=1",
		"HERDR_EVAL_LIFECYCLE_PIDFILE="+pidFile,
		"HERDR_EVAL_LIFECYCLE_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the detached child pid file never appeared")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The detached child must be provably ALIVE, not merely started: the
	// child writes marker-child-ready from inside its own generation after
	// it has survived startup. The wait reuses the same bounded deadline
	// as the pid file wait.
	if marker != "" {
		for {
			if _, err := os.ReadFile(marker + "-child-ready"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the detached child ready marker never appeared: the child died after Start")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if os.Getenv("HERDR_EVAL_LIFECYCLE_HOLD") == "1" {
		time.Sleep(30 * time.Second)
	}
}

func devnullFile() *os.File {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		panic(err)
	}
	return f
}
`

// lifecycleGoExecutable resolves the host absolute `go` executable, the same
// resolution probes.go performs (a relative or missing executable is a
// refusal, not a fallback).
func lifecycleGoExecutable(t *testing.T) string {
	t.Helper()
	goExe, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("resolve go executable: %v", err)
	}
	if !filepath.IsAbs(goExe) {
		t.Fatalf("go executable is not absolute: %q", goExe)
	}
	return goExe
}

// lifecycleScratchEnv mirrors compilerTempEnv: the child environment with
// the Go toolchain's temporary work (TMPDIR/TEMP/TMP) and build cache inside
// the runner-owned scratch so the whole owned tree's work is removed with
// it. GOMAXPROCS is bounded the way the E1 review gates ran.
func lifecycleScratchEnv(scratch string) []string {
	workScratch := filepath.Join(scratch, ".eval-tmp")
	overrides := map[string]string{
		"TMPDIR":      workScratch,
		"TEMP":        workScratch,
		"TMP":         workScratch,
		"GOCACHE":     filepath.Join(scratch, ".eval-gocache"),
		"GOMAXPROCS":  "2",
		"GOTOOLCHAIN": "local",
		"GOPROXY":     "off",
	}
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !testutil.IsSessionVar(key) {
			env = append(env, item)
		}
	}
	for i, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if value, ok := overrides[kv[:eq]]; ok {
			env[i] = kv[:eq+1] + value
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// lifecycleEnv adds the fixture markers on top of the scratch environment.
func lifecycleEnv(env []string, pidFile, marker string, hold, noChild bool) []string {
	env = append(env, "HERDR_EVAL_LIFECYCLE_PIDFILE="+pidFile, "HERDR_EVAL_LIFECYCLE_MARKER="+marker)
	if hold {
		env = append(env, "HERDR_EVAL_LIFECYCLE_HOLD=1")
	}
	if noChild {
		env = append(env, "HERDR_EVAL_LIFECYCLE_NOCHILD=1")
	}
	return env
}

// writeLifecycleFixture writes the fixture module (go.mod + lifecycle test)
// into the scratch directory.
func writeLifecycleFixture(t *testing.T, scratch string) {
	t.Helper()
	files := map[string]string{
		"go.mod":            "module lifecyclefixture\n\ngo 1.25\n",
		"lifecycle_test.go": lifecycleFixtureSource,
	}
	for rel, content := range files {
		path := filepath.Join(scratch, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// lifecycleEchoSource is the startup probe: it writes its own argv and
// environment to the file named by HERDR_EVAL_LIFECYCLE_ECHO and exits
// green, so the native Windows ABI tests can prove the command line and
// environment block round-trip exactly through a process that actually
// reached startup.
const lifecycleEchoSource = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	out := os.Getenv("HERDR_EVAL_LIFECYCLE_ECHO")
	if out == "" {
		os.Exit(0)
	}
	var b strings.Builder
	for _, a := range os.Args {
		fmt.Fprintf(&b, "ARG\t%s\n", a)
	}
	for _, e := range os.Environ() {
		fmt.Fprintf(&b, "ENV\t%s\n", e)
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		os.Exit(1)
	}
}
`

// lifecycleEchoBinary builds the startup probe inside the scratch (hermetic:
// toolchain-local, proxy off, cache inside the scratch) so the native runner
// can be pointed at a known executable with a controlled argv.
func lifecycleEchoBinary(t *testing.T, scratch string) string {
	t.Helper()
	dir := filepath.Join(scratch, "echo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module lifecycleecho\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(lifecycleEchoSource), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "echo"
	if runtime.GOOS == "windows" {
		name = "echo.exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command(lifecycleGoExecutable(t), "build", "-o", bin, ".")
	cmd.Dir = dir
	cmd.Env = lifecycleScratchEnv(scratch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the echo probe: %v: %s", err, out)
	}
	return bin
}

// lifecycleScratch returns a runner-owned scratch directory (not
// t.TempDir's automatic cleanup: the tests must prove it is removable
// themselves) plus its base cleanup. The go tool's work dir (inside the
// scratch) must exist before the run: the runner owns it.
func lifecycleScratch(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	scratch := filepath.Join(base, "scratch")
	if err := os.MkdirAll(filepath.Join(scratch, ".eval-tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	return scratch
}

// lifecycleTestArgs runs the fixture exactly as the runner does (bounded
// test timeout, no cache).
func lifecycleTestArgs() []string {
	return []string{"test", "-count=1", "-timeout", "2m", "./..."}
}

// lifecycleBuildEnv returns the runner-owned environment for the Windows
// compiled-fixture preparation and the overflow probe build: the explicit
// trusted driver GOCACHE when the driver supplied one (replacing the
// entry, never duplicated), otherwise the existing per-scratch cache
// lifecycleScratchEnv already set. No global cache is ever created: the
// per-scratch cache is removed with the scratch, and a driver-supplied
// GOCACHE is owned by the driver.
func lifecycleBuildEnv(scratch string) []string {
	cache := testutil.HostGoCache()
	if cache == "" {
		return lifecycleScratchEnv(scratch)
	}
	env := lifecycleScratchEnv(scratch)
	replaced := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOCACHE=") {
			continue
		}
		replaced = append(replaced, kv)
	}
	return append(replaced, "GOCACHE="+cache)
}

// lifecycleCompiledFixtureBinary compiles the lifecycle fixture exactly the
// way the production consumer does: a bounded trusted native `go test -c`
// into an OWN persistent scratch binary under the scratch work dir, which
// the Windows lifecycle tests then run through `go tool test2json <binary>`
// with explicit test flags. The persistent binary is the fix for the
// native Windows unlink collision: the fixture's detached child
// generations keep executing this owned binary, which is never unlinked
// under it (the go-test-run shape unlinks the throwaway
// lifecyclefixture.test.exe while the child still runs it). Windows
// lifecycle fixtures only; preparation is the caller's concern and happens
// BEFORE any run/cancel timeout starts. The compile runs under an explicit
// bounded context (5 minutes) — never an unbounded Background.
func lifecycleCompiledFixtureBinary(t *testing.T, scratch string) string {
	t.Helper()
	binary := filepath.Join(filepath.Join(scratch, ".eval-tmp"), "lifecyclefixture.test.exe")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := runOwnedGoProcess(ctx, lifecycleGoExecutable(t), scratch,
		[]string{"test", "-c", "-o", binary, "."}, lifecycleBuildEnv(scratch), stdout, stderr); err != nil {
		t.Fatalf("compile the persistent fixture binary: %v (stderr %q)", err, stderr.String())
	}
	if info, statErr := os.Stat(binary); statErr != nil || info.Size() == 0 {
		t.Fatalf("the persistent fixture binary is missing or empty: %v", statErr)
	}
	return binary
}

// These builders run before a testing.T exists or serve relocated toolchains,
// so their explicitly allowlisted snapshots must still scrub session routing.
func TestToolchainFixtureEnvironmentsDropSessionValues(t *testing.T) {
	for _, key := range []string{"SOHO_BOUNDARY_CANARY", "HC_BOUNDARY_CANARY", "HERDR_BOUNDARY_CANARY", "CLAUDE_CODE_SESSION_ID", "CODEX_BOUNDARY_CANARY", "PI_BOUNDARY_CANARY"} {
		t.Setenv(key, "synthetic-session-value")
	}
	t.Setenv("TEST_TOOLCHAIN_KEEP", "fixture-value")
	root := t.TempDir()
	for name, env := range map[string][]string{
		"build":     hermeticEnv(root),
		"lifecycle": lifecycleScratchEnv(root),
		"relocated": childToolchainEnv("fixture-path", "fixture-goroot", root),
	} {
		if leaked := testutil.SessionVars(env); len(leaked) != 0 {
			t.Errorf("%s leaked session keys: %v", name, leaked)
		}
		kept := false
		for _, item := range env {
			if item == "TEST_TOOLCHAIN_KEEP=fixture-value" {
				kept = true
			}
		}
		if !kept {
			t.Errorf("%s dropped a non-session control", name)
		}
	}
}
