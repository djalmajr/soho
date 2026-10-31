package wait

// DJA-605 stage 2c: under transport=hc the pause between polls is one
// `hc wait` on the first still-pending worker — bounded to 10 s and to the
// wait's remaining timeout, no-spin guarded, sleep on a wake error — while
// the report file stays the only completion signal and the herdr path is
// untouched. The fake herdr is a sentinel: any launch of it is a failure.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcWakeCaller    = `{"address":"t1","state":"live","labels":{"soho.session":"sW"}}`
	hcWakeWorking5  = `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"working"}}`
	hcWakeWorking6  = `{"address":"t7","name":"worker","state":"live","turn":6,"reported":true,"status":{"state":"working"}}`
	hcWakeIdle7     = `{"address":"t7","name":"worker","state":"live","turn":7,"reported":true,"status":{"state":"idle"}}`
	hcWakeBlocked5  = `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"blocked"}}`
	hcWakeError5    = `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"error"}}`
	hcWakeError5Msg = `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"error","msg":"the model refused the request"}}`
)

// hcWakeWaitTurn is the hc wait news record: the turn ended.
func hcWakeWaitTurn(turn int) string {
	return `{"terminal":"worker","ended":true,"reason":"status","turn":` + fmt.Sprint(turn) + `,"children":[]}`
}

// hcWakeFixture is one wait probe for transport=hc: the caller terminal t1
// of session sW, the worker roster row, the fake hc (installed per test)
// and the sentinel fake herdr. reportMode controls the completion file:
// "none" leaves it absent, "pointer" writes last-report-worker without the
// file (it appears later, while a wake is in flight) and "file" writes
// both before the run.
type hcWakeFixture struct {
	base   string
	sd     string
	bin    string
	env    platform.Env
	ctx    *core.Config
	report string
}

func newHcWakeFixture(t *testing.T, pollMs, reportMode string) *hcWakeFixture {
	t.Helper()
	base := t.TempDir()
	sd := filepath.Join(base, "state", "sW")
	bin := filepath.Join(base, "bin")
	for _, dir := range []string{filepath.Join(sd, "wait"), filepath.Join(base, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &hcWakeFixture{base: base, sd: sd, bin: bin, report: filepath.Join(sd, "reports", "worker.md")}
	switch reportMode {
	case "none":
	case "pointer":
		if err := os.WriteFile(filepath.Join(sd, "last-report-worker"), []byte(f.report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "file":
		if err := os.MkdirAll(filepath.Dir(f.report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.report, []byte("Done: the work is done.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "last-report-worker"), []byte(f.report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("reportMode %q", reportMode)
	}
	f.env = platform.Env{
		"PATH": f.bin, "HOME": filepath.Join(base, "home"), "USERPROFILE": filepath.Join(base, "home"),
		"TMPDIR": base, "XDG_CONFIG_HOME": filepath.Join(base, "home"),
		"SOHO_DIR": filepath.Join(base, "state"), "SOHO_FAKECLI_CONFIG": f.bin,
		"SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_WAIT_POLL_MS": pollMs, "FAKECLI_WAIT_FILE_TIMEOUT_MS": "15000",
	}
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{"settled_grace": {Value: "0"}}}
	if _, err := fakecli.Install(t, f.bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	return f
}

// installHc installs the fake hc: the session read, the three screen reads
// wait may issue, and the test's get/wait rules.
func (f *hcWakeFixture) installHc(t *testing.T, rules ...fakecli.Rule) {
	t.Helper()
	all := []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: hcWakeCaller}}
	all = append(all, rules...)
	for _, argv := range [][]string{{"read", "worker", "--lines", "0", "--json"}, {"read", "worker", "--lines", "20", "--json"}, {"read", "worker", "--lines", "40", "--json"}} {
		all = append(all, fakecli.Rule{Argv: argv, Stdout: `{"text":"command output"}`})
	}
	if _, err := fakecli.Install(t, f.bin, "hc", all); err != nil {
		t.Fatal(err)
	}
}

func (f *hcWakeFixture) hcCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return calls
}

// waitCalls is the subset of the recorded hc calls that are the wake.
func (f *hcWakeFixture) waitCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	out := []fakecli.Call{}
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) > 0 && c.Argv[0] == "wait" {
			out = append(out, c)
		}
	}
	return out
}

func (f *hcWakeFixture) getPolls(t *testing.T) int {
	t.Helper()
	polls := 0
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) == 3 && c.Argv[0] == "get" && c.Argv[1] == "worker" && c.Argv[2] == "--json" {
			polls++
		}
	}
	return polls
}

func (f *hcWakeFixture) assertNoHerdr(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); !os.IsNotExist(err) {
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		t.Fatalf("herdr ran under transport=hc: %#v", calls)
	}
}

// runWait runs WaitFor with the fake writers. fakeClock advances one fake
// minute per worker state read (the budget is then a generous 100 fake
// minutes); otherwise the real clock and the given real-time budget run,
// so the pause and the timeout are measured in wall time.
func (f *hcWakeFixture) runWait(t *testing.T, fakeClock bool, timeoutMs float64) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	oldNow, oldOut, oldErr, oldFriction := platform.Now, platform.Stdout, platform.Stderr, frictionLogFile
	friction := filepath.Join(f.base, "friction.log")
	platform.Stdout, platform.Stderr = &out, &stderr
	SetFrictionLogFile(friction)
	if fakeClock {
		platform.Now = func() time.Time {
			n := 0
			for _, c := range f.hcCalls(t) {
				if len(c.Argv) == 3 && c.Argv[0] == "get" && c.Argv[1] == "worker" && c.Argv[2] == "--json" {
					n++
				}
			}
			return time.Unix(1700000000, 0).Add(time.Duration(n) * time.Minute)
		}
	}
	t.Cleanup(func() {
		platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
		SetFrictionLogFile(oldFriction)
	})
	var timeout float64
	if fakeClock {
		timeout = float64((100 * time.Minute) / time.Millisecond)
	} else {
		timeout = timeoutMs
	}
	code := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, timeout, false, f.base)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

// writeReportOnce records nothing; it writes the report file once the fake
// hc call log shows at least wantWaits wake calls (the log line is written
// before a wake rule blocks on WaitFile, so this unblocks a blocked wake
// without racing the loop's own report read).
func (f *hcWakeFixture) writeReportOnce(t *testing.T, wantWaits int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl")); err == nil {
			n := 0
			for _, c := range calls {
				if len(c.Argv) > 0 && c.Argv[0] == "wait" {
					n++
				}
			}
			if n >= wantWaits {
				if err := os.MkdirAll(filepath.Dir(f.report), 0o700); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(f.report, []byte("Done: the work is done.\n"), 0o600); err != nil {
					t.Error(err)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the report writer saw fewer than %d wake calls", wantWaits)
}

// DJA-605 acceptance (a): a turn ends (the wake), the report appears, and
// the wait returns done — one hc wait per pause, each bounded to 10 s.
func TestWaitHcWakeTurnEndsThenReportDone(t *testing.T) {
	f := newHcWakeFixture(t, "1", "pointer")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWakeWorking5},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWakeWorking6},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeIdle7},
		fakecli.Rule{Argv: []string{"wait", "worker", "--timeout", "10", "--json"}, Call: 1, Stdout: hcWakeWaitTurn(6)},
		fakecli.Rule{Argv: []string{"wait", "worker", "--timeout", "10", "--json"}, Call: 2, WaitFile: f.report, Stdout: hcWakeWaitTurn(7)},
		fakecli.Rule{Argv: []string{"wait", "worker", "--timeout", "10", "--json"}, Stdout: hcWakeWaitTurn(7)},
	)
	go f.writeReportOnce(t, 2)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q, want a done wait", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"done"`) {
		t.Fatalf("out=%q, want the done line", out)
	}
	waits := f.waitCalls(t)
	if len(waits) != 3 {
		t.Fatalf("wakes=%d, want one per pause (three pauses): %#v", len(waits), waits)
	}
	for _, w := range waits {
		if want := []string{"wait", "worker", "--timeout", "10", "--json"}; !equalArgv(w.Argv, want) {
			t.Fatalf("wake argv %#v, want %#v (one bounded wait per pause)", w.Argv, want)
		}
	}
	f.assertNoHerdr(t)
}

// DJA-605 acceptance (b): a turn ends without a report — the existing
// no-report-yet outcome and exit, and the loop kept waking (each pause was
// a wake, not a sleep the test could not see).
func TestWaitHcWakeTurnEndsWithoutReport(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWakeWorking5},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeIdle7},
		fakecli.Rule{Argv: []string{"wait", "worker"}, ArgvPrefix: true, Stdout: hcWakeWaitTurn(6)},
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 6 {
		t.Fatalf("code=%d out=%q stderr=%q, want the existing no-report-yet exit 6", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"settled-no-report"`) {
		t.Fatalf("out=%q, want the settled-no-report line", out)
	}
	waits := f.waitCalls(t)
	if len(waits) != 1 {
		t.Fatalf("wakes=%d, want one (the wake between the working poll and the settling poll): %#v", len(waits), waits)
	}
	for _, w := range waits {
		if want := []string{"wait", "worker", "--timeout", "10", "--json"}; !equalArgv(w.Argv, want) {
			t.Fatalf("wake argv %#v, want %#v", w.Argv, want)
		}
	}
	f.assertNoHerdr(t)
}

// DJA-605 acceptance (c): a blocked worker surfaces as today — the
// blocked line, the existing exit, and the dialog's existing second-probe
// confirmation pause is a wake, not a sleep.
func TestWaitHcWakeBlockedSurfaces(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeBlocked5},
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 7 {
		t.Fatalf("code=%d out=%q stderr=%q, want the blocked exit 7", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"blocked"`) {
		t.Fatalf("out=%q, want the blocked line", out)
	}
	if got := len(f.waitCalls(t)); got != 1 {
		t.Fatalf("wakes=%d, want one (the confirmation pause before the blocked line): %#v", got, f.waitCalls(t))
	}
	f.assertNoHerdr(t)
}

// DJA-605 amendment: an error-state worker takes wait's existing
// provider-error path — the provider-error line with the hc status
// message as its cause, the warning, and the existing provider-error
// exit (14) — one poll, no pause, no herdr.
func TestWaitHcWakeErrorStateSurfaces(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeError5Msg},
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d out=%q stderr=%q, want the existing provider-error exit 14", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"provider-error"`) {
		t.Fatalf("out=%q, want the provider-error line", out)
	}
	if !strings.Contains(out, `"cause":"the model refused the request"`) {
		t.Fatalf("out=%q, want the hc status message as the cause", out)
	}
	if !strings.Contains(stderr, "provider error: agent 'worker'") || !strings.Contains(stderr, ": the model refused the request") {
		t.Fatalf("stderr=%q, want the provider error warning with the message", stderr)
	}
	if cause, ok := readWaitFile(f.sd, "worker", "provider-cause"); !ok || cause != "the model refused the request" {
		t.Fatalf("provider-cause=%q ok=%v, want the hc status message", cause, ok)
	}
	if got := f.getPolls(t); got != 1 {
		t.Fatalf("polls=%d, want one (the error word decides on the first read)", got)
	}
	if got := len(f.waitCalls(t)); got != 0 {
		t.Fatalf("wakes=%d, want none (the provider-error worker leaves the loop before the pause)", got)
	}
	f.assertNoHerdr(t)
}

// DJA-605 acceptance (e): an exited worker (the terminal is gone) surfaces
// as today — the gone line, the existing exit, no pause.
func TestWaitHcWakeExitedWorkerIsGone(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 6 {
		t.Fatalf("code=%d out=%q stderr=%q, want the gone exit 6", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"gone"`) {
		t.Fatalf("out=%q, want the gone line", out)
	}
	if got := len(f.waitCalls(t)); got != 0 {
		t.Fatalf("wakes=%d, want none (the gone worker leaves the loop before the pause)", got)
	}
	f.assertNoHerdr(t)
}

// DJA-605 acceptance (f) and decision 3: the no-spin guard. The fake hc
// wait returns at once on every call (an already-settled terminal), the
// real clock runs a 2 s budget with a 200 ms poll interval, and the polls
// in the window are at most window / PollIntervalMs + 1 — each pause takes
// at least the full poll interval (the wake plus the rest of it slept).
func TestWaitHcWakeNoSpinGuard(t *testing.T) {
	f := newHcWakeFixture(t, "200", "none")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeWorking5},
		fakecli.Rule{Argv: []string{"wait", "worker"}, ArgvPrefix: true, Stdout: hcWakeWaitTurn(6)},
	)
	const budgetMs = 2000
	code, out, stderr := f.runWait(t, false, float64(budgetMs))
	if code != 9 {
		t.Fatalf("code=%d out=%q stderr=%q, want the timeout exit 9", code, out, stderr)
	}
	polls := f.getPolls(t)
	if max := budgetMs/200 + 1; polls > max {
		t.Fatalf("polls=%d in a %dms window, want at most %d (window / PollIntervalMs + 1)", polls, budgetMs, max)
	}
	// One wake per pause: the pause is the wake, so the wake count matches
	// the pause count (polls minus the last poll that left the loop).
	if wakes := len(f.waitCalls(t)); wakes < polls-1 || wakes > polls {
		t.Fatalf("wakes=%d for polls=%d, want one wake per pause", wakes, polls)
	}
	f.assertNoHerdr(t)
}

// DJA-605 acceptance (h) and decision 2: the bound never exceeds the
// wait's remaining timeout. The real clock runs a 2 s budget, so every
// wake's --timeout is at most the ceil of the whole budget (2) and at
// least 1 — never the 10 s cap.
func TestWaitHcWakeBoundStaysWithinTheRemainingTimeout(t *testing.T) {
	f := newHcWakeFixture(t, "200", "none")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeWorking5},
		fakecli.Rule{Argv: []string{"wait", "worker"}, ArgvPrefix: true, Stdout: hcWakeWaitTurn(6)},
	)
	code, out, stderr := f.runWait(t, false, 2000)
	if code != 9 {
		t.Fatalf("code=%d out=%q stderr=%q, want the timeout exit 9", code, out, stderr)
	}
	waits := f.waitCalls(t)
	if len(waits) == 0 {
		t.Fatal("no wake was issued; the pause is the wake under hc")
	}
	for _, w := range waits {
		joined := strings.Join(w.Argv, " ")
		if !strings.Contains(joined, "wait worker --timeout ") {
			t.Fatalf("wake argv %q, want wait worker --timeout <n> --json", joined)
		}
		sec := 0
		for i, part := range w.Argv {
			if part == "--timeout" && i+1 < len(w.Argv) {
				_, _ = fmt.Sscanf(w.Argv[i+1], "%d", &sec)
			}
		}
		if sec < 1 || sec > 2 {
			t.Fatalf("wake %q used --timeout %d, want within the remaining 2s budget (1..2)", joined, sec)
		}
	}
	f.assertNoHerdr(t)
}

// DJA-605 acceptance (g) and decision 4: a wake error falls back to the
// sleep and the loop goes on deciding as today — the report still ends it.
func TestWaitHcWakeErrorFallsBackToTheSleep(t *testing.T) {
	f := newHcWakeFixture(t, "1", "file")
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWakeWorking5},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWakeWorking6},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeIdle7},
		fakecli.Rule{Argv: []string{"wait", "worker"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"internal","message":"boom"}}`},
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q, want a done wait", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"done"`) {
		t.Fatalf("out=%q, want the done line", out)
	}
	if got := len(f.waitCalls(t)); got < 1 {
		t.Fatalf("wakes=%d, want the failed wakes that fell back to the sleep", got)
	}
	f.assertNoHerdr(t)
}

// equalArgv is an exact argv comparison for the report.
func equalArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
