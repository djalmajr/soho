package cli

// SOH-421: --no-wait must keep its promise to free the orchestrator's turn.
// These fixtures use one- and two-second configured budgets and fake workers
// that never settle or never prove a submission; no test sleeps for a role
// timeout.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func assertNoWaitCeilingLine(t *testing.T, stderr, seconds, phase, inspect string) {
	t.Helper()
	want := fmt.Sprintf("soho: dispatch: --no-wait gave up after %s s waiting for %s; inspect with: %s\n", seconds, phase, inspect)
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr=%q; want it to contain %q", stderr, want)
	}
}

func assertWithinCeiling(t *testing.T, started time.Time, ceiling, margin time.Duration) {
	t.Helper()
	if elapsed := time.Since(started); elapsed > ceiling+margin {
		t.Fatalf("dispatch took %s; want at most the %s ceiling plus %s margin", elapsed, ceiling, margin)
	}
}

// These guard fixtures zero the fixed margin so the shared ceiling binds in a
// short run. The production margin and its effect on bounded waits are
// asserted separately in TestDispatchCeilingTotalIncludesFixedMargin.
func zeroCeilingMargin(t *testing.T) {
	t.Helper()
	old := dispatchCeilingMargin
	dispatchCeilingMargin = 0
	t.Cleanup(func() { dispatchCeilingMargin = old })
}

// The fixed margin belongs to the published ceiling, not to any wait's own
// budget: every configured window is still measured from its own start.
func TestDispatchCeilingTotalIncludesFixedMargin(t *testing.T) {
	budget := newDispatchWaitBudget(20, 15, "worker", platform.Env{})
	if budget.total != 45*time.Second {
		t.Fatalf("total=%s, want 45 s for 20 + 15 + 10", budget.total)
	}
	zero := newDispatchWaitBudget(0, 0, "worker", platform.Env{})
	if zero.total != 10*time.Second {
		t.Fatalf("zero-budget total=%s, want 10 s", zero.total)
	}
	if w := budget.window(15*time.Second, "prompt arrival", 15); w.bounded {
		t.Fatal("a 15 s arrival window inside a 45 s ceiling must keep its own budget")
	}
	spent := newDispatchWaitBudget(0, 0, "worker", platform.Env{})
	spent.deadline = time.Now().Add(-time.Second)
	if w := spent.window(15*time.Second, "prompt arrival", 15); !w.bounded {
		t.Fatal("a wait whose own deadline is past the ceiling must be bounded")
	}
}

// A legacy worker that never leaves working must not consume the role's
// --when-idle timeout when --no-wait caps the wait. Nothing is sent.
func TestDispatchNoWaitCeilingLegacyNeverIdle(t *testing.T) {
	zeroCeilingMargin(t)
	f := newDispatchArrivalFixture(t, "working", 5, 5, "", "1")
	started := time.Now()
	code, out, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait")
	assertWithinCeiling(t, started, 2*time.Second, 4*time.Second)
	if code != 10 || out != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "2", "worker to become idle", "herdr agent get worker && herdr agent read worker")
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countDispatchCalls(calls, "prompt"); n != 0 {
		t.Fatalf("prompt calls=%d, want none after the ceiling: %#v", n, calls)
	}
}

func TestDispatchNoWaitCeilingLegacySubmissionNeverProves(t *testing.T) {
	for _, tc := range []struct {
		name         string
		expireOnRead bool
		wantEnters   int
	}{
		{name: "exact draft before the ceiling", wantEnters: 1},
		{name: "exact draft read reaches the ceiling", expireOnRead: true, wantEnters: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zeroCeilingMargin(t)
			base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
			briefName := "worker-" + base.Format("20060102T150405") + ".md"
			draft := "Read the file " + filepath.Join("$ROOT", "state", "ws", "briefs", briefName) + " in full and execute it."
			screen := "SECRET_SCREEN_MARKER\n› " + draft + "\n"
			visible := []string{"agent", "read", "worker", "--source", "visible"}
			enter := []string{"agent", "send-keys", "worker", "enter"}
			f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
				fakecli.Rule{Argv: visible, Call: 1, Stdout: "boot\n"},
				fakecli.Rule{Argv: visible, Stdout: screen},
				fakecli.Rule{Argv: enter},
			)
			f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "1"
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "2"
			readCalls := func() []fakecli.Call {
				calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				return calls
			}
			isPrompt := func(c fakecli.Call) bool {
				return len(c.Argv) == 4 && slices.Equal(c.Argv[:3], []string{"agent", "prompt", "worker"})
			}
			postPrompt := func(calls []fakecli.Call) []fakecli.Call {
				i := slices.IndexFunc(calls, isPrompt)
				if i < 0 {
					return nil
				}
				return calls[i+1:]
			}
			isGet := func(c fakecli.Call) bool {
				return slices.Equal(c.Argv, []string{"agent", "get", "worker"})
			}
			isVisible := func(c fakecli.Call) bool { return slices.Equal(c.Argv, visible) }
			isEnter := func(c fakecli.Call) bool { return slices.Equal(c.Argv, enter) }

			// Keep the composed stamp fixed. Expire the first arrival window
			// after an actual worker poll. The shared clock still has room for
			// the composer read, then reaches 3 s at the selected milestone.
			oldNow, oldCeilingNow := platform.Now, dispatchCeilingNow
			t.Cleanup(func() { platform.Now, dispatchCeilingNow = oldNow, oldCeilingNow })
			platform.Now = func() time.Time {
				if slices.ContainsFunc(postPrompt(readCalls()), isGet) {
					return base.Add(2 * time.Second)
				}
				return base
			}
			dispatchCeilingNow = func() time.Time {
				tail := postPrompt(readCalls())
				switch {
				case slices.ContainsFunc(tail, isEnter), tc.expireOnRead && slices.ContainsFunc(tail, isVisible):
					return base.Add(3 * time.Second)
				case slices.ContainsFunc(tail, isGet):
					return base.Add(2500 * time.Millisecond)
				default:
					return base
				}
			}

			started := time.Now()
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			assertWithinCeiling(t, started, 3*time.Second, 4*time.Second)
			if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			assertNoWaitCeilingLine(t, stderr, "3", "prompt arrival", "herdr agent get worker && herdr agent read worker")
			if strings.Count(stderr, "--no-wait gave up after") != 1 || strings.Contains(out+stderr, "SECRET_SCREEN_MARKER") {
				t.Fatalf("want one ceiling line and no echoed screen: out=%q stderr=%q", out, stderr)
			}
			calls := readCalls()
			prompt := slices.IndexFunc(calls, isPrompt)
			if n := countDispatchCalls(calls, "prompt"); n != 1 || prompt < 0 ||
				!strings.Contains(calls[prompt].Argv[3], strings.ReplaceAll(draft, "$ROOT", f.root)) {
				t.Fatalf("prompt calls=%d, want the original composed draft only: %#v", n, calls)
			}
			tail := calls[prompt+1:]
			get, read, key := slices.IndexFunc(tail, isGet), slices.IndexFunc(tail, isVisible), slices.IndexFunc(tail, isEnter)
			if get < 0 || read <= get || (tc.wantEnters == 1 && key <= read) {
				t.Fatalf("want prompt, poll, composer read, then any Enter: %#v", calls)
			}
			if n := countDispatchCalls(calls, "send-keys"); n != tc.wantEnters {
				t.Fatalf("send-keys calls=%d, want %d corrective Enter commands: %#v", n, tc.wantEnters, calls)
			}
		})
	}
}

// Under hc a busy worker's guarded send is refused and the idle retry must
// stop at the shared ceiling rather than the role timeout. Only the first
// send was ever attempted.
func TestDispatchNoWaitCeilingHcNeverIdle(t *testing.T) {
	zeroCeilingMargin(t)
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "1"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
	started := time.Now()
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	assertWithinCeiling(t, started, 2*time.Second, 4*time.Second)
	if code != 10 || out != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "2", "worker to become idle", "hc get worker && hc read worker")
	if sends := hcSends(f.hcCalls(t)); len(sends) != 1 {
		t.Fatalf("sends=%d, want only the refused first send: %#v", len(sends), sends)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// A delayed hc send leaves no started proof; the arrival wait reaches the
// ceiling and returns 15 with the phase and the hc inspect command. No second
// send follows it.
func TestDispatchNoWaitCeilingHcSubmissionUnproven(t *testing.T) {
	zeroCeilingMargin(t)
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Delay: 2500, Stdout: `{"turn":7,"started":false}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "1"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
	started := time.Now()
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	assertWithinCeiling(t, started, 2*time.Second, 4*time.Second)
	if code != 15 || dispatchHcOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "2", "prompt arrival", "hc get worker && hc read worker")
	if sends := hcSends(f.hcCalls(t)); len(sends) != 1 {
		t.Fatalf("sends=%d, want one: %#v", len(sends), sends)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// With the production margin the configured arrival window keeps its own
// budget: an unproven hc send ends in the ordinary 15 before the 11 s ceiling,
// with no ceiling line and no extra probe past the one-second window.
func TestDispatchNoWaitKeepsConfiguredWindowInsideMargin(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	started := time.Now()
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	assertWithinCeiling(t, started, time.Second, 4*time.Second)
	if code != 15 || dispatchHcOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if strings.Contains(stderr, "gave up after") {
		t.Fatalf("the configured window was shortened by the shared ceiling: %s", stderr)
	}
}

// A worker idle at the first read is unaffected by a zero ceiling: no wait
// starts, so no ceiling line is printed and the prompt is accepted.
func TestDispatchNoWaitCeilingZeroBudgetsIdleWorker(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":8,"started":true}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" || strings.Contains(stderr, "--no-wait gave up") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 1 {
		t.Fatalf("sends=%d, want one: %#v", len(sends), sends)
	}
}

// The task state of an unsubmitted busy dispatch is restored: the orchestrator
// must not find a pointer to a report no worker will write.
func TestDispatchNoWaitCeilingRestoresUnsubmittedTask(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 5, 5, "", "1")
	code, _, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait")
	if code != 10 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	pointer := filepath.Join(f.state, "ws", "task-report-worker.json")
	if _, err := os.Stat(pointer); !os.IsNotExist(err) {
		t.Fatalf("task pointer survived the ceiling: %v", err)
	}
}

// newHcAmendCeilingFixture removes only the fixed margin (a test seam) so an
// hc amendment reaches its guarded-send ceiling in about a second. The prior
// report, pointer and task title stand for the open task an amendment needs.
func newHcAmendCeilingFixture(t *testing.T, rules []fakecli.Rule) (*dispatchHcFixture, string, []byte, string) {
	t.Helper()
	return newHcAmendCeilingFixtureWithProbes(t, guardProbeRules(), rules)
}

func newHcAmendCeilingFixtureWithProbes(t *testing.T, probes, rules []fakecli.Rule) (*dispatchHcFixture, string, []byte, string) {
	t.Helper()
	oldMargin := dispatchCeilingMargin
	dispatchCeilingMargin = 0
	t.Cleanup(func() { dispatchCeilingMargin = oldMargin })
	f := newDispatchHcFixture(t, append(probes, rules...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
	old := filepath.Join(f.sd, "reports", "worker-previous.md")
	pointer := []byte(fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[],"role":"implementer","pane":"t7"}`+"\n", old, old))
	pointerPath := taskreport.TaskReportPointerPath(f.sd, "worker")
	if err := os.WriteFile(pointerPath, pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	title := "implementer: Earlier task\n"
	if err := os.WriteFile(filepath.Join(f.sd, "task-worker"), []byte(title), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, pointerPath, pointer, title
}

// Two individually timely probes exceed their shared guarded-send bound.
// The schema launch is cancelled before any input can be sent.
func TestDispatchNoWaitCeilingAmendGuardProbeAbortDoesNotSend(t *testing.T) {
	probes := guardProbeRules()
	probes[0].Delay, probes[1].Delay = 600, 600
	f, _, _, _ := newHcAmendCeilingFixtureWithProbes(t, probes, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
	})
	f.scriptDispatchHc(t)
	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 15 || dispatchHcOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "1", "send to be accepted", "hc get worker && hc read worker")
	calls := f.hcCalls(t)
	var launchedProbes []string
	for _, call := range calls {
		if slices.Equal(call.Argv, probes[0].Argv) || slices.Equal(call.Argv, probes[1].Argv) {
			launchedProbes = append(launchedProbes, strings.Join(call.Argv, " "))
		}
	}
	if !slices.Equal(launchedProbes, []string{"send --help", "schema --json"}) || len(hcSends(calls)) != 0 {
		t.Fatalf("probes=%v sends=%v, want two probes and no input", launchedProbes, hcSends(calls))
	}
}

// F2: an hc amendment whose unproven send reaches the ceiling leaves exactly
// the state of any other not-received: one JSON line, the wait marker, an
// accepted/not-received sidecar and an untouched open task. The bare exit 15
// left none of these.
func TestDispatchNoWaitCeilingAmendSendAbortKeepsOrdinaryNotReceived(t *testing.T) {
	f, pointerPath, _, title := newHcAmendCeilingFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Delay: 1300, Code: 1, Stderr: "send did not finish\n"},
	})
	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 15 || dispatchHcOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "1", "send to be accepted", "hc get worker && hc read worker")
	if !strings.Contains(out, `"wait_status":"not-received"`) || !strings.Contains(stderr, "was not received") {
		t.Fatalf("ordinary not-received JSON or warning missing: out=%s stderr=%s", out, stderr)
	}
	marker, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.not-received"))
	if err != nil {
		t.Fatalf("not-received marker missing: %v", err)
	}
	fields := strings.Fields(string(marker))
	if len(fields) != 2 || fields[1] != "7" {
		t.Fatalf("marker=%q, want the pre-send sequence 7", marker)
	}
	sidecars, err := filepath.Glob(filepath.Join(f.sd, "briefs", "worker-*.dispatch.json"))
	if err != nil || len(sidecars) != 1 {
		t.Fatalf("sidecars=%v err=%v", sidecars, err)
	}
	side := mustRead(t, sidecars[0])
	if !strings.Contains(side, `"submission":"accepted"`) || !strings.Contains(side, `"arrival":"not-received"`) {
		t.Fatalf("sidecar=%s", side)
	}
	afterPointer, err := os.ReadFile(pointerPath)
	newReport := filepath.Join(f.sd, "reports", "worker-20260929T000000.md")
	if err != nil || !strings.Contains(string(afterPointer), fmt.Sprintf(`"current":%q`, newReport)) || !strings.Contains(string(afterPointer), fmt.Sprintf(`"history":[%q]`, filepath.Join(f.sd, "reports", "worker-previous.md"))) {
		t.Fatalf("pointer=%q, want the ordinary amend 15 state err=%v", afterPointer, err)
	}
	if last, err := os.ReadFile(filepath.Join(f.sd, "last-report-worker")); err != nil || strings.TrimSpace(string(last)) != newReport {
		t.Fatalf("last-report=%q, want %q err=%v", last, newReport, err)
	}
	if task, err := os.ReadFile(filepath.Join(f.sd, "task-worker")); err != nil || string(task) != title {
		t.Fatalf("task title changed: %q err=%v", task, err)
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.submit-failed")); !os.IsNotExist(err) {
		t.Fatalf("an uncertain send wrote submit-failed: %v", err)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 1 {
		t.Fatalf("sends=%d, want one: %#v", len(sends), sends)
	}
}

// Control for F2: a reply that proves a started turn after the ceiling is not
// downgraded to 15. The slow pre-send read consumes the margin, not the send.
func TestDispatchNoWaitCeilingAmendProvenSendBeatsCeiling(t *testing.T) {
	f, _, _, _ := newHcAmendCeilingFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Delay: 1300, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":8,"started":true}`},
	})
	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("proven send code=%d out=%s stderr=%s", code, out, stderr)
	}
	if strings.Contains(stderr, "gave up after") {
		t.Fatalf("a proven send reported the ceiling: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.not-received")); !os.IsNotExist(err) {
		t.Fatalf("a proven send wrote a not-received marker: %v", err)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 1 {
		t.Fatalf("sends=%d, want one: %#v", len(sends), sends)
	}
}
