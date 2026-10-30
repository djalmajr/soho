//go:build !windows

package platform

import (
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

// Flagged state tokens (P1): ps appends modifier flags after the state
// base letter (Ss, S+, R+, SN, S<, Sl); a zombie with modifiers (Z+, Zs)
// must still count as gone. The deterministic tier injects exact rows
// through a native fake ps compiled in the test sandbox (the re-executed
// test binary cannot play ps: the production argv starts with -o, which
// the test framework's flag parsing refuses); the native tier reads an
// owned setsid fixture through the real ps. Every process touched is an
// owned fixture, killed and reaped in a cleanup that also runs when an
// assertion fails; no foreign process is registered, signalled or altered.

const (
	// procFlagsFakePsMarkerEnv selects the fake ps behavior: the value is
	// the semicolon-separated per-format spec, one spec for each -o format
	// the production readers ask for: full:<row|FAIL|FAIL:<text>>;
	// ident:<row|FAIL|FAIL:<text>>;alive:<row|FAIL|FAIL:<text>>. A row
	// prints on success (exit 0); FAIL fails without output (exit 9); and
	// FAIL:<text> fails (exit 9) after printing the misleading <text>.
	procFlagsFakePsMarkerEnv = "HERDR_PROC_FLAGS_FAKE_PS"
	// procFlagsFakePsCanaryEnv names the file the fake ps appends one line
	// to per call (the format kind and the exit mode), so a test proves
	// the fake handled the invocation (not the real ps) and how it
	// exited.
	procFlagsFakePsCanaryEnv = "HERDR_PROC_FLAGS_FAKE_PS_CANARY"
	// procFlagsSessionEnv marks the re-executed session-leader sleeper.
	procFlagsSessionEnv = "HERDR_PROC_FLAGS_SESSION"
	// procFlagsSessionSleeperTest is the re-executed helper entry name.
	procFlagsSessionSleeperTest = "TestProcFlagsSessionSleeper"
)

// procFlagsFakePsSource is the native fake ps the deterministic tier
// compiles in its own sandbox: it prints the row the marker env selects
// for the -o format its argv asked for and exits 9 (the failed ps) when
// the marker says FAIL (without output) or FAIL:<text> (after printing
// the misleading <text>). It appends one canary line per call to the
// file the canary env names. It reads no process table and touches no
// process: every row is injected by the test, and nothing else is
// resolvable on its PATH.
const procFlagsFakePsSource = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	format := ""
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-o" && i+1 < len(os.Args) {
			format = os.Args[i+1]
		}
	}
	kind := ""
	switch format {
	case "lstart=,state=,comm=":
		kind = "full"
	case "lstart=,ppid=,state=":
		kind = "ident"
	case "pid=,state=":
		kind = "alive"
	}
	row, ok := rowFor(os.Getenv("HERDR_PROC_FLAGS_FAKE_PS"), kind)
	if path := os.Getenv("HERDR_PROC_FLAGS_FAKE_PS_CANARY"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fmt.Fprintf(f, "%s %s\n", kind, modeName(ok, row))
			_ = f.Close()
		}
	}
	if !ok || row == "FAIL" {
		os.Exit(9)
	}
	if strings.HasPrefix(row, "FAIL:") {
		fmt.Fprint(os.Stdout, row[len("FAIL:"):]+"\n")
		os.Exit(9)
	}
	if row != "" {
		fmt.Fprint(os.Stdout, row+"\n")
	}
}

func modeName(ok bool, row string) string {
	if !ok || row == "FAIL" || strings.HasPrefix(row, "FAIL:") {
		return "fail"
	}
	return "ok"
}

func rowFor(marker, kind string) (string, bool) {
	for _, part := range strings.Split(marker, ";") {
		key, value, found := strings.Cut(part, ":")
		if found && key == kind {
			return value, true
		}
	}
	return "", false
}
`

// procFlagsFakePsBin compiles the fake ps in a sandbox directory (nothing
// else is resolvable there: no real ps, no shell, no interpreter) and
// returns the sandbox path. The go build runs from the actual Go package
// directory (the module context the package belongs to) with a hermetic
// toolchain pin and no module proxy: the source is standard-library only.
func procFlagsFakePsBin(t *testing.T) string {
	t.Helper()
	goExe, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go toolchain is not on PATH; the deterministic fake ps cannot be built")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating the package dir: %v", err)
	}
	sandbox := t.TempDir()
	src := filepath.Join(sandbox, "fakeps.go")
	if err := os.WriteFile(src, []byte(procFlagsFakePsSource), 0o600); err != nil {
		t.Fatalf("writing the fake ps source: %v", err)
	}
	items := os.Environ()
	items = append(items, "GOTOOLCHAIN=local", "GOPROXY=off")
	build := exec.Command(goExe, "build", "-o", filepath.Join(sandbox, "ps"), src)
	build.Dir = cwd
	build.Env = items
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("building the fake ps: %v (%s)", err, out)
	}
	return sandbox
}

// procFlagsFakePsEnv returns the Env the readers must use for one fake ps
// row set and the canary file the fake appends one line to per call: the
// sandbox PATH (only the fake ps is resolvable), the marker selecting
// each reader's row, and the canary path in a real Go test-allocated
// directory, so the test proves the fake handled the call (not the real
// ps) and in which mode it exited.
func procFlagsFakePsEnv(t *testing.T, bin, marker string) (Env, string) {
	t.Helper()
	canary := filepath.Join(t.TempDir(), "fakeps-canary")
	return Env{"PATH": bin, procFlagsFakePsMarkerEnv: marker, procFlagsFakePsCanaryEnv: canary}, canary
}

// canaryContent reads the fake ps canary file (fatal when unreadable):
// its lines are the proof the fake handled the invocations, not the real
// ps.
func canaryContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the fake ps canary %s: %v", path, err)
	}
	return string(data)
}

// fakePsWarm drives the fake ps with a discarded env (its own canary)
// until one call is answered inside the production reader's 2 s deadline.
// The compiled fake's cold start (a Go binary: page-in plus runtime init)
// can outlive that deadline under host load; a TimedOut answer takes the
// reader's conservative path and the fake appends no canary line, so the
// subtests would then exercise the deadline path, not the failed-ps path
// (DJA-634). Once the fake has answered once, its binary pages are
// resident and the subtest calls start fast on any load.
func fakePsWarm(t *testing.T, bin string, pid int) {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for {
		env, canary := procFlagsFakePsEnv(t, bin, "alive:FAIL")
		procAliveStates([]int{pid}, env)
		if _, err := os.Stat(canary); err == nil {
			return
		}
		if time.Now().After(until) {
			t.Fatal("the fake ps never answered inside the reader's 2 s deadline; the host load is too high to exercise the failed-ps path")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestProcFlagsSessionSleeper is the re-executed helper entry: when the
// marker env is set the process calls setsid (becoming the session
// leader, the native stand-in for the session-leader ps flag s) and
// blocks in a real blocking read; in the main test run the marker is
// unset and the function returns without running anything.
func TestProcFlagsSessionSleeper(t *testing.T) {
	if os.Getenv(procFlagsSessionEnv) == "" {
		return
	}
	// setsid, as the raw syscall: the portable spelling the platform
	// supports on every unix the file builds for (the typed wrapper
	// returns different values on darwin and linux).
	if _, _, err := syscall.Syscall(syscall.SYS_SETSID, 0, 0, 0); err != 0 {
		fmt.Fprintf(os.Stderr, "fixture: setsid: %v\n", err)
		os.Exit(1)
	}
	blockForever()
}

// procFlagsSessionSleeper starts the setsid fixture under the restricted
// real-ps env and owns it for the whole test (killed and reaped in the
// cleanup, also when an assertion failed).
func procFlagsSessionSleeper(t *testing.T, env Env) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^"+procFlagsSessionSleeperTest+"$")
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
	items = append(items, procFlagsSessionEnv+"=1")
	cmd.Env = items
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the session-leader fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// procFlagsRawFlaggedState reads the owned pid's raw ps line (lstart,
// stat, comm) with the real ps and returns the full state token exactly as
// the system prints it: it polls (with a ceiling) until the settled
// fixture shows a modifier flag (the session-leader flag), so the read
// never races the fixture's start-up.
func procFlagsRawFlaggedState(t *testing.T, env Env, pid int) (string, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for {
		r := RunCli("ps", []string{"-o", "lstart=,stat=,comm=", "-p", strconv.Itoa(pid)}, RunOptions{Env: env, TimeoutMs: 5000})
		if r.Status != nil && *r.Status == 0 {
			fields := strings.Fields(r.Stdout)
			// lstart is five fields (day, month, day, time, year); the
			// state is the sixth.
			if len(fields) >= 6 {
				last = fields[5]
				if len(last) >= 2 {
					return strings.Join(fields[:5], " "), last
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the native ps never showed a flagged state for the owned pid %d (last: %q); setsid may have failed", pid, last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The flagged running states must read as running through every reader,
// with the exact lstart identity, the comm base name and the ppid —
// deterministic row injection through the fake ps (a clearly separate
// tier from the native read; no process is involved in this test).
func TestProcFlagsFlaggedStatesReadRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process info is unix")
	}
	bin := procFlagsFakePsBin(t)
	lstart := "Tue Oct  6 20:52:12 2026" // the real ps padding (the day field)
	for _, state := range []string{"Ss", "S+", "R+", "SN", "S<", "Sl", "Ssl"} {
		t.Run(state, func(t *testing.T) {
			marker := "full:" + lstart + "     " + state + "   /usr/local/bin/python3;" +
				"ident:" + lstart + " 4242 " + state + ";" +
				"alive:77 " + state
			env, _ := procFlagsFakePsEnv(t, bin, marker)
			started, name, live := ReadProcFull(77, env)
			if live != ProcRunning {
				t.Fatalf("ReadProcFull(77) live=%v; want running for the flagged state %s", live, state)
			}
			if started != lstart {
				t.Fatalf("ReadProcFull(77) started=%q; the lstart must be stored exactly as printed (%q)", started, lstart)
			}
			if name != "python3" {
				t.Fatalf("ReadProcFull(77) name=%q; want the comm base name python3", name)
			}
			if again, _, againLive := ReadProcFull(77, env); againLive != ProcRunning || again != started {
				t.Fatalf("second read = (%q, %v); the lstart identity must be stable", again, againLive)
			}
			if _, live := ReadProc(77, env); live != ProcRunning {
				t.Fatalf("ReadProc(77) live=%v; want running for the flagged state %s", live, state)
			}
			id, live := procReadIdentityReal(77, env)
			if live != ProcRunning || !id.ppidKnown || id.ppid != 4242 {
				t.Fatalf("procReadIdentityReal(77) = (%+v, %v); want running with ppid 4242", id, live)
			}
			if !SameStarted(id.started, lstart) {
				t.Fatalf("procReadIdentityReal(77) started=%q; want the same start as the lstart %q", id.started, lstart)
			}
			if !procAliveStates([]int{77}, env)[77] {
				t.Fatalf("procAliveStates: the flagged state %s must count as running", state)
			}
		})
	}
}

// A zombie base state with modifier flags must count as gone through
// every reader: a flagged zombie must not appear running and must not
// survive the cleanup poll (deterministic row injection, no process).
func TestProcFlagsFlaggedZombieIsGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process info is unix")
	}
	bin := procFlagsFakePsBin(t)
	lstart := "Tue Oct  6 20:52:12 2026"
	for _, state := range []string{"Z", "Z+", "Zs"} {
		t.Run(state, func(t *testing.T) {
			marker := "full:" + lstart + "     " + state + "   /usr/local/bin/python3;" +
				"ident:" + lstart + " 4242 " + state + ";" +
				"alive:77 " + state
			env, _ := procFlagsFakePsEnv(t, bin, marker)
			started, name, live := ReadProcFull(77, env)
			if live != ProcGone {
				t.Fatalf("ReadProcFull(77) live=%v; the flagged zombie %s must count as gone", live, state)
			}
			if started != "" || name != "" {
				t.Fatalf("ReadProcFull(77) = (%q, %q); a gone pid must not carry a positive identity", started, name)
			}
			if _, live := ReadProc(77, env); live != ProcGone {
				t.Fatalf("ReadProc(77) live=%v; the flagged zombie %s must count as gone", live, state)
			}
			if _, live := procReadIdentityReal(77, env); live != ProcGone {
				t.Fatalf("procReadIdentityReal(77) live=%v; the flagged zombie %s must count as gone", live, state)
			}
			// The cleanup gate: a flagged zombie must not keep the stop
			// polling (waitTreeGone treats alive as still running).
			if procAliveStates([]int{77}, env)[77] {
				t.Fatalf("procAliveStates: the flagged zombie %s must not count as running", state)
			}
		})
	}
}

// A state the parser does not know, a line that does not parse, and a
// failed ps must never read as a positive identity: with a live owned pid
// the kernel confirms the process exists and the read stays unknown
// (unknown is never signalled or dropped); with a dead owned pid the
// kernel confirms the absence and the read is the proven gone.
func TestProcFlagsMalformedAndFailedPs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process info is unix")
	}
	bin := procFlagsFakePsBin(t)
	real := procTestEnv(t)
	live := fixtureSpawn(t, real, "sleeper")
	livePid := live.Process.Pid
	deadCmd := fixtureSpawn(t, real, "sleeper")
	deadPid := deadCmd.Process.Pid
	if err := deadCmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = deadCmd.Wait() // reap: the entry leaves the table
	deadline := time.Now().Add(5 * time.Second)
	for procAliveStates([]int{deadPid}, real)[deadPid] && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if procAliveStates([]int{deadPid}, real)[deadPid] {
		t.Fatalf("the reaped fixture %d still reads alive", deadPid)
	}
	unknownFull := "full:"
	failed := "full:FAIL;ident:FAIL;alive:FAIL"
	emptyPathDir := t.TempDir()
	noPs := Env{"PATH": emptyPathDir}
	t.Run("an unknown state base is not a positive identity (live pid reads unknown)", func(t *testing.T) {
		env, _ := procFlagsFakePsEnv(t, bin, unknownFull+"Tue Oct  6 20:52:12 2026     Qs   /usr/local/bin/python3;ident:FAIL;alive:FAIL")
		_, _, liveRead := ReadProcFull(livePid, env)
		if liveRead != ProcUnknown {
			t.Fatalf("ReadProcFull(%d) live=%v; want unknown (the kernel says the pid exists)", livePid, liveRead)
		}
		if _, liveRead := ReadProc(livePid, env); liveRead != ProcUnknown {
			t.Fatalf("ReadProc(%d) live=%v; want unknown", livePid, liveRead)
		}
		// The identity reader with the unknown-base row: same outcome.
		env, _ = procFlagsFakePsEnv(t, bin, "full:FAIL;ident:Tue Oct  6 20:52:12 2026 4242 Qs;alive:FAIL")
		if _, liveRead := procReadIdentityReal(livePid, env); liveRead != ProcUnknown {
			t.Fatalf("procReadIdentityReal(%d) live=%v; want unknown", livePid, liveRead)
		}
	})
	t.Run("a line that does not parse is not a positive identity (live pid reads unknown)", func(t *testing.T) {
		env, _ := procFlagsFakePsEnv(t, bin, "full:garbage line without an lstart;ident:FAIL;alive:FAIL")
		_, _, liveRead := ReadProcFull(livePid, env)
		if liveRead != ProcUnknown {
			t.Fatalf("ReadProcFull(%d) live=%v; want unknown (the kernel says the pid exists)", livePid, liveRead)
		}
	})
	t.Run("a failed ps keeps a live pid unknown, never gone", func(t *testing.T) {
		env, _ := procFlagsFakePsEnv(t, bin, failed)
		_, _, liveRead := ReadProcFull(livePid, env)
		if liveRead != ProcUnknown {
			t.Fatalf("ReadProcFull(%d) live=%v; a failed ps must not read a live pid as gone", livePid, liveRead)
		}
		if _, liveRead := ReadProc(livePid, env); liveRead != ProcUnknown {
			t.Fatalf("ReadProc(%d) live=%v; a failed ps must not read a live pid as gone", livePid, liveRead)
		}
		if _, liveRead := procReadIdentityReal(livePid, env); liveRead != ProcUnknown {
			t.Fatalf("procReadIdentityReal(%d) live=%v; a failed ps must not read a live pid as gone", livePid, liveRead)
		}
	})
	t.Run("a failed ps still confirms the proven absence of a dead pid", func(t *testing.T) {
		env, _ := procFlagsFakePsEnv(t, bin, failed)
		if _, _, liveRead := ReadProcFull(deadPid, env); liveRead != ProcGone {
			t.Fatalf("ReadProcFull(%d) live=%v; the kernel ESRCH is the proven absence", deadPid, liveRead)
		}
		if _, liveRead := ReadProc(deadPid, env); liveRead != ProcGone {
			t.Fatalf("ReadProc(%d) live=%v; the kernel ESRCH is the proven absence", deadPid, liveRead)
		}
	})
	t.Run("a missing ps never reads as gone (the stop is not claimed on a doubt)", func(t *testing.T) {
		if procAliveStates([]int{livePid}, noPs)[livePid] != true {
			t.Fatalf("procAliveStates(%d) with a missing ps; want alive (a failed ps keeps the pid)", livePid)
		}
		if procAliveStates([]int{deadPid}, noPs)[deadPid] != true {
			t.Fatalf("procAliveStates(%d) with a missing ps; want alive (a failed ps keeps the pid)", deadPid)
		}
	})
}

// The native tier reads an owned session leader through the real ps.
// The stat field includes modifiers on both supported Unix systems; Linux's
// state field used by production only includes the base letter, while macOS
// also supplies modifiers there. This tier verifies native identity and
// liveness on both systems; the deterministic tier above proves modifier
// parsing, including the real macOS shape the single-letter parser refused.
func TestProcFlagsNativeFlaggedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process info is unix; the windows read is the native kernel32 path")
	}
	env := procTestEnv(t)
	cmd := procFlagsSessionSleeper(t, env)
	pid := cmd.Process.Pid
	rawLstart, state := procFlagsRawFlaggedState(t, env, pid)
	t.Logf("native ps state of the owned session-leader fixture %d: %q (lstart %q)", pid, state, rawLstart)
	if len(state) < 2 {
		t.Fatalf("the native ps state %q carries no modifier flag; the setsid fixture must print a flagged token (want e.g. Ss)", state)
	}
	switch state[0] {
	case 'S', 'R':
	default:
		t.Fatalf("the native ps state %q has the unexpected base %q; the blocked session leader must sleep or run", state, state[:1])
	}
	started, name, live := ReadProcFull(pid, env)
	if live != ProcRunning {
		t.Fatalf("ReadProcFull(%d) live=%v; the flagged native state %q must read as running", pid, live, state)
	}
	if started == "" || name == "" {
		t.Fatalf("ReadProcFull(%d) = (%q, %q); the lstart and the comm base name come with the read", pid, started, name)
	}
	if !SameStarted(started, rawLstart) {
		t.Fatalf("ReadProcFull(%d) started=%q; the lstart identity must be the native lstart %q", pid, started, rawLstart)
	}
	again, againName, againLive := ReadProcFull(pid, env)
	if againLive != ProcRunning || again != started || againName != name {
		t.Fatalf("second read = (%q, %q, %v); the flagged-state identity must be stable", again, againName, againLive)
	}
	if _, live := ReadProc(pid, env); live != ProcRunning {
		t.Fatalf("ReadProc(%d) live=%v; the flagged native state %q must read as running", pid, live, state)
	}
	if !procAliveStates([]int{pid}, env)[pid] {
		t.Fatalf("procAliveStates: the flagged native state %q must count as running", state)
	}
}

// A failed ps (a non-zero status) must not prove an absence: its stdout —
// empty or a misleading partial table — is not liveness proof and is not
// parsed; every queried pid is confirmed against the kernel instead, and
// only ESRCH establishes gone. A normal non-zero empty ps (every queried
// pid genuinely exited) still reads as gone, through the kernel. The
// fake ps drops a canary line per call, so the cases prove the failure
// came from the fake (not the real ps) and in which mode it exited.
func TestProcFlagsFailedPsAliveStates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process info is unix")
	}
	bin := procFlagsFakePsBin(t)
	real := procTestEnv(t)
	liveCmd := fixtureSpawn(t, real, "sleeper")
	livePid := liveCmd.Process.Pid
	deadCmd := fixtureSpawn(t, real, "sleeper")
	deadPid := deadCmd.Process.Pid
	if err := deadCmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = deadCmd.Wait() // reap: the entry leaves the table
	deadline := time.Now().Add(5 * time.Second)
	for procAliveStates([]int{deadPid}, real)[deadPid] && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if procAliveStates([]int{deadPid}, real)[deadPid] {
		t.Fatalf("the reaped fixture %d still reads alive through the real ps", deadPid)
	}
	// Warm the compiled fake before the subtests assert on its canary:
	// under host load its cold start can outlive the reader's 2 s
	// deadline (DJA-634).
	fakePsWarm(t, bin, livePid)
	t.Run("an empty failed ps keeps a live pid alive and confirms a dead pid gone", func(t *testing.T) {
		env, canary := procFlagsFakePsEnv(t, bin, "full:FAIL;ident:FAIL;alive:FAIL")
		if !procAliveStates([]int{livePid}, env)[livePid] {
			t.Fatalf("procAliveStates(%d) with the empty failed ps; a failed ps must not prove a live pid gone", livePid)
		}
		if procAliveStates([]int{deadPid}, env)[deadPid] {
			t.Fatalf("procAliveStates(%d) with the empty failed ps; the kernel ESRCH is the proven absence", deadPid)
		}
		if got := canaryContent(t, canary); got != "alive fail\nalive fail\n" {
			t.Fatalf("canary = %q; want the two failed alive-format calls handled by the fake", got)
		}
	})
	t.Run("a misleading partial failed ps is not liveness proof", func(t *testing.T) {
		// The failed ps prints a row for the dead pid only: the live pid
		// is unprinted in the failed table, but a failed stdout is not an
		// absence and the kernel says the live pid exists.
		env, canary := procFlagsFakePsEnv(t, bin, "full:FAIL;ident:FAIL;alive:FAIL:"+strconv.Itoa(deadPid)+" Ss")
		if !procAliveStates([]int{livePid}, env)[livePid] {
			t.Fatalf("procAliveStates(%d); the failed row for the dead %d must not read the live pid as gone", livePid, deadPid)
		}
		// The failed ps prints a zombie row for the live pid: the flagged
		// zombie token inside a failed table is still not liveness proof;
		// the kernel says the pid exists.
		env, canary2 := procFlagsFakePsEnv(t, bin, "full:FAIL;ident:FAIL;alive:FAIL:"+strconv.Itoa(livePid)+" Z+")
		if !procAliveStates([]int{livePid}, env)[livePid] {
			t.Fatalf("procAliveStates(%d); a zombie row inside a failed ps must not establish gone", livePid)
		}
		if got := canaryContent(t, canary) + canaryContent(t, canary2); got != "alive fail\n"+"alive fail\n" {
			t.Fatalf("canaries = %q; want the two failed alive-format calls handled by the fake", got)
		}
	})
	t.Run("a successful ps still decides from its rows", func(t *testing.T) {
		// Control: with a zero status the rows are the answer — the live
		// pid's own row keeps it alive and a printed flagged zombie row
		// reads it gone (the successful-read semantics are unchanged).
		env, canary := procFlagsFakePsEnv(t, bin, "full:FAIL;ident:FAIL;alive:"+strconv.Itoa(livePid)+" Ss")
		if !procAliveStates([]int{livePid}, env)[livePid] {
			t.Fatalf("procAliveStates(%d) with the successful ps; the printed live row must count as running", livePid)
		}
		env, canary2 := procFlagsFakePsEnv(t, bin, "full:FAIL;ident:FAIL;alive:"+strconv.Itoa(livePid)+" Z+")
		if procAliveStates([]int{livePid}, env)[livePid] {
			t.Fatalf("procAliveStates(%d) with the successful ps; the printed flagged zombie row must count as gone", livePid)
		}
		if got := canaryContent(t, canary) + canaryContent(t, canary2); got != "alive ok\n"+"alive ok\n" {
			t.Fatalf("canaries = %q; want the two successful alive-format calls handled by the fake", got)
		}
	})
}
