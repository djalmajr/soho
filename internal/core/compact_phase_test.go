package core

// F11: the compact-phase marker's contract (record shape, claim with the
// same-pane owner check, token-guarded removal, the dispatch-time check)
// and the by-pid owner classification. Liveness uses real processes: the
// test binary re-executed as a helper (portable; no /bin/sleep), and the
// platform's own by-pid read (ps on unix, kernel32 on windows). Only the
// cases that need a failing by-pid read for an alive pid use the unix
// fake ps (the shared fakecli: the re-executed test binary, no shell
// script behind the name), and those alone are skipped on windows.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const compactPhaseLivenessChildEnv = "COMPACT_PHASE_LIVENESS_CHILD"

// TestCompactPhaseLivenessChild is the re-executed liveness helper: it
// sleeps the duration named by COMPACT_PHASE_LIVENESS_CHILD (milliseconds)
// and exits. While it sleeps the parent reads the pid as a live owner;
// after the parent waits for it, the pid is proven absent.
func TestCompactPhaseLivenessChild(t *testing.T) {
	ms := os.Getenv(compactPhaseLivenessChildEnv)
	if ms == "" {
		t.Skip("runs only as the liveness helper's child")
	}
	d, err := time.ParseDuration(ms + "ms")
	if err != nil {
		t.Fatalf("%s = %q: %v", compactPhaseLivenessChildEnv, ms, err)
	}
	time.Sleep(d)
}

// compactPhaseLivenessChild re-executes the test binary as the liveness
// helper. The child keeps the parent's environment (PATH and SYSTEMROOT
// included) minus the HERDR_* context; nothing is printed.
func compactPhaseLivenessChild(t *testing.T, sleepMS string) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestCompactPhaseLivenessChild$", "-test.timeout=60s")
	childEnv := []string{}
	for _, entry := range testutil.CleanEnv(t) {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "HERDR_") {
			continue
		}
		childEnv = append(childEnv, entry)
	}
	childEnv = append(childEnv, compactPhaseLivenessChildEnv+"="+sleepMS)
	cmd.Env = childEnv
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// compactPhaseDeadPID gets a pid proven absent once the helper exits.
func compactPhaseDeadPID(t *testing.T) int {
	t.Helper()
	cmd := compactPhaseLivenessChild(t, "600")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// compactPhaseTestEnv builds the per-test environment: on unix a fake ps
// (the shared fakecli: it answers the recorded lstart for the test's own
// pid and fails for any other) on a PATH that holds nothing else; on
// windows the by-pid read is a direct kernel32 call that ignores the env,
// so nothing is faked. The by-pid read is the only ps form the code under
// test issues (`ps -o lstart=,state=,comm= -p <pid>`), so no fallback to
// the system ps is needed.
func compactPhaseTestEnv(t *testing.T) platform.Env {
	t.Helper()
	fakebin := filepath.Join(t.TempDir(), "fakebin")
	if runtime.GOOS == "windows" {
		return platform.Env{"PATH": fakebin + string(os.PathListSeparator) + `C:\Windows\System32`}
	}
	rules := []fakecli.Rule{
		{Argv: []string{"-o", "lstart=,state=,comm=", "-p", strconv.Itoa(os.Getpid())}, Stdout: "Sat Oct  3 12:00:00 2026 R fake-test\n"},
		{Argv: []string{"-o", "lstart=,state=,comm=", "-p"}, ArgvPrefix: true, Stderr: "no row for this pid\n", Code: 3},
	}
	if _, err := fakecli.Install(t, fakebin, "ps", rules); err != nil {
		t.Fatal(err)
	}
	return platform.Env{"PATH": fakebin, "SOHO_FAKECLI_CONFIG": fakebin}
}

func compactPhaseTestState(t *testing.T) string {
	t.Helper()
	sd := filepath.Join(t.TempDir(), "state", "ws")
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	return sd
}

func compactPhaseMarker(t *testing.T, token, pane, brief string, pid int, started, createdAt string) string {
	t.Helper()
	return jsonjs.Stringify(jsonjs.O(
		"version", 1,
		"token", token,
		"pane", pane,
		"brief", brief,
		"pid", pid,
		"started", started,
		"created_at", createdAt,
	)) + "\n"
}

func compactPhaseSelfStarted(t *testing.T, env platform.Env) string {
	t.Helper()
	started, live := platform.ReadProc(os.Getpid(), env)
	if live != platform.ProcRunning || started == "" {
		t.Fatalf("own identity read = %q/%v; want running with a start", started, live)
	}
	return started
}

func compactPhaseNowRFC3339(t *testing.T) string {
	t.Helper()
	return time.Now().UTC().Format(time.RFC3339)
}

func TestCompactIntegerPID(t *testing.T) {
	bound := math.Ldexp(1, strconv.IntSize-1)
	// The platform's by-pid API bound: unix pid_t is a signed 32-bit;
	// the windows pid is a DWORD, still limited by the int on 32-bit builds.
	max := float64(math.MaxInt32)
	if runtime.GOOS == "windows" && strconv.IntSize == 64 {
		max = float64(math.MaxUint32)
	}
	belowBound := math.Nextafter(bound, 0)
	cases := []struct {
		name string
		v    any
		want bool
	}{
		{"fractional", 1.5, false},
		{"zero", 0.0, false},
		{"negative", -3.0, false},
		{"the exclusive int bound itself", bound, false},
		{"above the int bound", bound * 2, false},
		{"a non-number", "17", false},
		{"one", 1.0, true},
		{"the largest representable value under the int bound", belowBound, belowBound <= max},
		{"the platform pid bound", max, true},
		{"the platform pid bound plus one", max + 1, false},
		{"the largest windows DWORD (2^32-1)", float64(math.MaxUint32), max == float64(math.MaxUint32)},
		{"2^32", float64(math.MaxUint32) + 1, false},
		{"2^32+1", float64(math.MaxUint32) + 2, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := compactIntegerPID(c.v)
			if ok != c.want {
				t.Fatalf("compactIntegerPID(%v) ok = %v; want %v", c.v, ok, c.want)
			}
			if c.want && got != int(c.v.(float64)) {
				t.Fatalf("compactIntegerPID(%v) = %d; want the exact value", c.v, got)
			}
		})
	}
}

func TestCompactPhaseClaimWritesTheContractRecord(t *testing.T) {
	sd := compactPhaseTestState(t)
	env := compactPhaseTestEnv(t)
	started := compactPhaseSelfStarted(t, env)
	token := CompactPhaseClaim(CompactPhaseClaimOptions{
		SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md",
		Env: env,
	})
	if token == "" {
		t.Fatal("the claim returned no token")
	}
	raw, err := os.ReadFile(CompactPendingPath(sd, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("marker is not a JSON object: %v; bytes %q", err, raw)
	}
	for _, key := range []string{"version", "token", "pane", "brief", "pid", "started", "created_at"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("marker is missing the key %q: %q", key, raw)
		}
	}
	rec, err := CompactPendingRead(CompactPendingPath(sd, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Version != 1 || rec.Token != token || rec.Pane != "p1" || rec.Brief != "/abs/brief.md" ||
		rec.PID != os.Getpid() || rec.Started != started {
		t.Fatalf("record = %+v; want the claim's fields with this process's identity", rec)
	}
	if _, err := time.Parse(time.RFC3339, rec.CreatedAt); err != nil {
		t.Fatalf("created_at %q is not RFC3339: %v", rec.CreatedAt, err)
	}
	// Atomic write: no stray temp file is left in the wait dir.
	entries, err := os.ReadDir(filepath.Join(sd, "wait"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "worker.compact-pending.json" {
			t.Fatalf("stray file %q next to the marker", entry.Name())
		}
	}
}

func TestCompactPhaseClaimRefusesLiveOwnerSamePane(t *testing.T) {
	sd := compactPhaseTestState(t)
	env := compactPhaseTestEnv(t)
	path := CompactPendingPath(sd, "worker")
	if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "other-token", "p1", "/abs/old.md", os.Getpid(), compactPhaseSelfStarted(t, env), compactPhaseNowRFC3339(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				exitErr, ok := r.(*platform.ExitError)
				if !ok {
					t.Fatalf("claim panic = %#v; want the platform exit error", r)
				}
				code = exitErr.Code
			}
		}()
		CompactPhaseClaim(CompactPhaseClaimOptions{SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md", Env: env})
	}()
	if code != 10 {
		t.Fatalf("claim over a live same-pane owner = %d; want the 10 refusal", code)
	}
	rec, err := CompactPendingRead(path)
	if err != nil {
		t.Fatalf("the live phase's record vanished: %v", err)
	}
	if rec.Token != "other-token" || rec.Brief != "/abs/old.md" {
		t.Fatalf("the live phase's record was replaced: %+v", rec)
	}
}

func TestCompactPhaseClaimRefusesUnknownOwnerSamePane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an unverifiable owner needs a failing by-pid read for an alive pid; the windows read is direct kernel32 and never fails that way")
	}
	sd := compactPhaseTestState(t)
	env := compactPhaseTestEnv(t)
	// The fake ps answers only for the test's own pid: the owner is a live
	// child the fake cannot read, so the owner read fails (the kernel
	// fallback proves the pid exists) and reads as unknown.
	owner := compactPhaseLivenessChild(t, "30000")
	defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
	ownerStarted, ownerLive := platform.ReadProc(owner.Process.Pid, env)
	if ownerLive != platform.ProcUnknown {
		t.Fatalf("owner read = %q/%v; want unknown", ownerStarted, ownerLive)
	}
	path := CompactPendingPath(sd, "worker")
	if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "other-token", "p1", "/abs/old.md", owner.Process.Pid, "Sat Oct  3 12:00:00 2026", compactPhaseNowRFC3339(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				exitErr, ok := r.(*platform.ExitError)
				if !ok {
					t.Fatalf("claim panic = %#v; want the platform exit error", r)
				}
				code = exitErr.Code
			}
		}()
		CompactPhaseClaim(CompactPhaseClaimOptions{SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md", Env: env})
	}()
	if code != 10 {
		t.Fatalf("claim over an unverifiable owner = %d; want the 10 refusal (unknown is not dead)", code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the unverifiable phase's record was removed: %v", err)
	}
}

func TestCompactPhaseClaimRefusesWhenSelfIdentityUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an unreadable own identity needs a failing by-pid read for an alive pid; the windows read is direct kernel32 and never fails that way")
	}
	sd := compactPhaseTestState(t)
	fakebin := t.TempDir()
	// The shared fakecli ps fails every query: the unreadable own identity
	// the 4 refusal asserts (no shell script behind the name).
	if _, err := fakecli.Install(t, fakebin, "ps", []fakecli.Rule{{AnyArgs: true, Code: 1}}); err != nil {
		t.Fatal(err)
	}
	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				exitErr, ok := r.(*platform.ExitError)
				if !ok {
					t.Fatalf("claim panic = %#v; want the platform exit error", r)
				}
				code = exitErr.Code
			}
		}()
		CompactPhaseClaim(CompactPhaseClaimOptions{
			SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md",
			Env: platform.Env{"PATH": fakebin, "SOHO_FAKECLI_CONFIG": fakebin},
		})
	}()
	if code != 4 {
		t.Fatalf("claim with an unreadable own identity = %d; want the 4 refusal", code)
	}
	if _, err := os.Stat(CompactPendingPath(sd, "worker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused claim left a marker: %v", err)
	}
}

func TestCompactPhaseClaimReplacesDeadAndForeignRecords(t *testing.T) {
	deadPID := compactPhaseDeadPID(t)
	cases := []struct {
		name    string
		pid     int
		started string
		pane    string
		live    bool
	}{
		{"a dead owner's record is replaced", deadPID, "Sat Oct  3 00:00:00 2026", "p1", false},
		{"a reused pid with a different start is replaced", os.Getpid(), "Thu Dec 25 23:59:59 1999", "p1", false},
		{"a foreign pane's record is replaced even with a live owner", os.Getpid(), "", "p-other", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sd := compactPhaseTestState(t)
			env := compactPhaseTestEnv(t)
			started := c.started
			if c.live {
				started = compactPhaseSelfStarted(t, env)
			}
			path := CompactPendingPath(sd, "worker")
			if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "old-token", c.pane, "/abs/old.md", c.pid, started, compactPhaseNowRFC3339(t))), 0o600); err != nil {
				t.Fatal(err)
			}
			token := CompactPhaseClaim(CompactPhaseClaimOptions{SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md", Env: env})
			rec, err := CompactPendingRead(path)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Token != token || rec.PID != os.Getpid() || rec.Brief != "/abs/brief.md" || rec.Pane != "p1" {
				t.Fatalf("replacement = %+v; want the new claim's record", rec)
			}
		})
	}
}

func TestCompactPhaseClaimRefusesCorruptedRecords(t *testing.T) {
	corrupt := []string{
		"not json",
		"[1,2]",
		`{"version":2,"token":"a","pane":"p1","brief":"/b","pid":1,"started":"s","created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1.9,"token":"a","pane":"p1","brief":"/b","pid":1,"started":"s","created_at":"2026-01-01T00:00:00Z"}`,
		`{"token":"a","pane":"p1","brief":"/b","pid":1,"started":"s","created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"token":"a","pane":"p1","brief":"/b","pid":1.5,"started":"s","created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"token":"a","pane":"p1","brief":"/b","pid":1e300,"started":"s","created_at":"2026-01-01T00:00:00Z"}`,
		fmt.Sprintf(`{"version":1,"token":"a","pane":"p1","brief":"/b","pid":%s,"started":"s","created_at":"2026-01-01T00:00:00Z"}`, strconv.FormatFloat(math.Ldexp(1, strconv.IntSize-1), 'f', -1, 64)),
		// Above the platform's by-pid API bound (pid_t / DWORD): the value
		// would truncate in the native call, so the record is corrupt.
		fmt.Sprintf(`{"version":1,"token":"a","pane":"p1","brief":"/b","pid":%s,"started":"s","created_at":"2026-01-01T00:00:00Z"}`, strconv.FormatFloat(float64(math.MaxUint32)+1, 'f', -1, 64)),
		`{"version":1,"token":"a","pane":"p1","brief":"/b","pid":1,"started":"s","created_at":"yesterday"}`,
	}
	for i, raw := range corrupt {
		t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {
			sd := compactPhaseTestState(t)
			env := compactPhaseTestEnv(t)
			path := CompactPendingPath(sd, "worker")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			code := 0
			func() {
				defer func() {
					if r := recover(); r != nil {
						exitErr, ok := r.(*platform.ExitError)
						if !ok {
							t.Fatalf("claim panic = %#v; want the platform exit error", r)
						}
						code = exitErr.Code
					}
				}()
				CompactPhaseClaim(CompactPhaseClaimOptions{SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md", Env: env})
			}()
			if code != 4 {
				t.Fatalf("claim over a corrupted record = %d; want the 4 refusal (never overwritten)", code)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != raw {
				t.Fatalf("the corrupted record was changed: %q err=%v", after, err)
			}
		})
	}
}

func TestCompactPhaseClaimConcurrentDoesNotOverwrite(t *testing.T) {
	sd := compactPhaseTestState(t)
	env := compactPhaseTestEnv(t)
	opts := CompactPhaseClaimOptions{SD: sd, Agent: "worker", Pane: "p1", Brief: "/abs/brief.md", Env: env}
	const workers = 6
	out := make(chan int, workers)
	tokens := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					exitErr, ok := r.(*platform.ExitError)
					if ok {
						out <- exitErr.Code
					} else {
						t.Errorf("claim panic = %#v", r)
					}
				}
			}()
			tokens <- CompactPhaseClaim(opts)
			out <- 0
		}()
	}
	wg.Wait()
	close(out)
	close(tokens)
	succeeded := 0
	refused := 0
	for code := range out {
		switch code {
		case 0:
			succeeded++
		case 10:
			refused++
		default:
			t.Fatalf("unexpected claim exit code %d", code)
		}
	}
	if succeeded != 1 || refused != workers-1 {
		t.Fatalf("concurrent claims = %d succeeded, %d refused; want 1 and %d", succeeded, refused, workers-1)
	}
	var winner string
	for tok := range tokens {
		if tok != "" {
			winner = tok
		}
	}
	if winner == "" {
		t.Fatal("no claim succeeded")
	}
	rec, err := CompactPendingRead(CompactPendingPath(sd, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Token != winner {
		t.Fatalf("the final token %q is not the winner's %q; a loser overwrote the record", rec.Token, winner)
	}
}

func TestCompactPhaseRemoveOnlyOwnToken(t *testing.T) {
	t.Run("the matching token removes the marker", func(t *testing.T) {
		sd := compactPhaseTestState(t)
		path := CompactPendingPath(sd, "worker")
		if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok-a", "p1", "/abs/brief.md", os.Getpid(), "Sat Oct  3 12:00:00 2026", compactPhaseNowRFC3339(t))), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CompactPhaseRemove(sd, "worker", "tok-a"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the marker still stands: %v", err)
		}
	})
	t.Run("a different token is never removed", func(t *testing.T) {
		sd := compactPhaseTestState(t)
		path := CompactPendingPath(sd, "worker")
		if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "other", "p1", "/abs/brief.md", os.Getpid(), "Sat Oct  3 12:00:00 2026", compactPhaseNowRFC3339(t))), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CompactPhaseRemove(sd, "worker", "tok-a"); err == nil || !strings.Contains(err.Error(), "another token") {
			t.Fatalf("removal = %v; want the another-token error", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the other token's marker was removed: %v", err)
		}
	})
	t.Run("an unreadable marker is a removal error, never a removal", func(t *testing.T) {
		sd := compactPhaseTestState(t)
		path := CompactPendingPath(sd, "worker")
		if err := os.WriteFile(path, []byte("corrupted\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CompactPhaseRemove(sd, "worker", "tok-a"); err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("removal = %v; want the unreadable error", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the unreadable marker was removed: %v", err)
		}
	})
	t.Run("absent marker is a clean no-op", func(t *testing.T) {
		sd := compactPhaseTestState(t)
		if err := CompactPhaseRemove(sd, "worker", "tok-a"); err != nil {
			t.Fatalf("removal = %v; want the no-op", err)
		}
	})
}

func TestCompactPhaseCheckCleansAndRefuses(t *testing.T) {
	deadPID := compactPhaseDeadPID(t)
	cases := []struct {
		name     string
		wantCode int
		wantGone bool
		keep     bool
	}{
		{"no marker proceeds", 0, false, false},
		{"a dead owner's record is removed", 0, true, false},
		{"a reused pid with a different start is removed", 0, true, false},
		{"a foreign pane's record is removed even with a live owner", 0, true, false},
		{"a live same-pane owner refuses", 10, false, true},
		{"an unverifiable same-pane owner refuses", 10, false, true},
		{"a corrupted marker refuses", 4, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sd := compactPhaseTestState(t)
			env := compactPhaseTestEnv(t)
			path := CompactPendingPath(sd, "worker")
			switch c.name {
			case "a dead owner's record is removed":
				if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p1", "/abs/old.md", deadPID, "Sat Oct  3 00:00:00 2026", compactPhaseNowRFC3339(t))), 0o600); err != nil {
					t.Fatal(err)
				}
			case "a reused pid with a different start is removed":
				if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p1", "/abs/old.md", os.Getpid(), "Thu Dec 25 23:59:59 1999", compactPhaseNowRFC3339(t))), 0o600); err != nil {
					t.Fatal(err)
				}
			case "a foreign pane's record is removed even with a live owner":
				if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p-other", "/abs/old.md", os.Getpid(), compactPhaseSelfStarted(t, env), compactPhaseNowRFC3339(t))), 0o600); err != nil {
					t.Fatal(err)
				}
			case "a live same-pane owner refuses":
				if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p1", "/abs/old.md", os.Getpid(), compactPhaseSelfStarted(t, env), compactPhaseNowRFC3339(t))), 0o600); err != nil {
					t.Fatal(err)
				}
			case "an unverifiable same-pane owner refuses":
				if runtime.GOOS == "windows" {
					t.Skip("an unverifiable owner needs a failing by-pid read for an alive pid; the windows read is direct kernel32 and never fails that way")
				}
				env = platform.Env{"PATH": t.TempDir()} // no ps on the PATH: the read fails for the alive pid
				if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p1", "/abs/old.md", os.Getpid(), "Sat Oct  3 12:00:00 2026", compactPhaseNowRFC3339(t))), 0o600); err != nil {
					t.Fatal(err)
				}
			case "a corrupted marker refuses":
				if err := os.WriteFile(path, []byte("corrupted\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code := 0
			func() {
				defer func() {
					if r := recover(); r != nil {
						exitErr, ok := r.(*platform.ExitError)
						if !ok {
							t.Fatalf("check panic = %#v; want the platform exit error", r)
						}
						code = exitErr.Code
					}
				}()
				CompactPhaseCheck(sd, "worker", "p1", env)
			}()
			if code != c.wantCode {
				t.Fatalf("check = %d; want %d", code, c.wantCode)
			}
			if c.wantGone {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the record was not removed: %v", err)
				}
			} else if c.keep {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the record was removed: %v", err)
				}
			}
		})
	}
}

func TestCompactPendingOwnerClassification(t *testing.T) {
	sd := compactPhaseTestState(t)
	env := compactPhaseTestEnv(t)
	selfStarted := compactPhaseSelfStarted(t, env)
	deadPID := compactPhaseDeadPID(t)
	cases := []struct {
		name    string
		pid     int
		started string
		env     platform.Env
		want    CompactOwner
	}{
		{"alive: the pid runs with the recorded start", os.Getpid(), selfStarted, env, CompactOwnerAlive},
		{"dead: the pid is proven absent", deadPID, selfStarted, env, CompactOwnerDead},
		{"dead: a reused pid with a different start", os.Getpid(), "Thu Dec 25 23:59:59 1999", env, CompactOwnerDead},
	}
	if runtime.GOOS != "windows" {
		// A PATH without ps: the by-pid read fails for the alive pid, which
		// reads as unknown, never as gone.
		cases = append(cases, struct {
			name    string
			pid     int
			started string
			env     platform.Env
			want    CompactOwner
		}{
			"unknown: the read fails while the pid is alive",
			os.Getpid(), selfStarted, platform.Env{"PATH": t.TempDir()}, CompactOwnerUnknown,
		})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := CompactPendingPath(sd, "worker")
			if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p1", "/abs/brief.md", c.pid, c.started, compactPhaseNowRFC3339(t))), 0o600); err != nil {
				t.Fatal(err)
			}
			rec, err := CompactPendingRead(path)
			if err != nil {
				t.Fatalf("classification read = %v", err)
			}
			if got := CompactPendingOwner(rec, c.env); got != c.want {
				t.Fatalf("owner = %v; want %v", got, c.want)
			}
		})
	}
}

func TestCompactPhaseReadPathWritesNothingUnderNowrite(t *testing.T) {
	sd := compactPhaseTestState(t)
	env := compactPhaseTestEnv(t)
	path := CompactPendingPath(sd, "worker")
	if err := os.WriteFile(path, []byte(compactPhaseMarker(t, "tok", "p1", "/abs/brief.md", os.Getpid(), compactPhaseSelfStarted(t, env), compactPhaseNowRFC3339(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	env["SOHO_NOWRITE"] = "1"
	snapshot := func() map[string]string {
		out := map[string]string{}
		var walk func(dir string)
		walk = func(dir string) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, entry := range entries {
				full := filepath.Join(dir, entry.Name())
				if entry.IsDir() {
					walk(full)
				} else {
					raw, err := os.ReadFile(full)
					if err != nil {
						continue
					}
					out[full] = string(raw)
				}
			}
		}
		walk(filepath.Dir(sd)) // the state root
		return out
	}
	before := snapshot()
	rec, err := CompactPendingRead(path)
	if err != nil {
		t.Fatal(err)
	}
	if owner := CompactPendingOwner(rec, env); owner != CompactOwnerAlive {
		t.Fatalf("owner = %v; want alive (the marker's own process)", owner)
	}
	after := snapshot()
	for key, value := range after {
		if before[key] != value {
			t.Fatalf("the read-only path created or changed %q", key)
		}
	}
	if _, err := os.Stat(filepath.Join(sd, "agents.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the read-only path left a lock: %v", err)
	}
}
