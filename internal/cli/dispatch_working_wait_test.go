package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const waitingForReport = "Waiting for its report as soho wait would."

func noSendKeys(t *testing.T, f *dispatchArrivalFixture) {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
			t.Fatalf("occupied target got a key: %#v", call.Argv)
		}
	}
}

// TestDispatchBlockingWaitsForTheReportWhenAWorkingTargetShowsNoPrompt: a
// target that was already working (a codex right after spawn --fresh) took
// the prompt without showing it. The blocking dispatch records the
// not-received marker and the warning, then waits for the report like soho
// wait; it does not exit 15 before the report exists.
func TestDispatchBlockingWaitsForTheReportWhenAWorkingTargetShowsNoPrompt(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\n", "0")
	f.env["SOHO_WAIT_POLL_MS"] = "100"
	sd := filepath.Join(f.state, "ws")
	marker := filepath.Join(sd, "wait", "worker.not-received")
	// The report is written when the dispatch enters the real wait, after it
	// recorded the marker: the wait seam reads the marker there (the wait's
	// first probe removes it for a target that stays working), writes the
	// report, then runs the real wait.
	var seen string
	var markerErr, reportErr error
	realWait := dispatchWaitFor
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, quiet bool, cwd string) int {
		raw, err := os.ReadFile(marker)
		seen, markerErr = string(raw), err
		if report := core.LastReport(sd, "worker"); report != "" {
			reportErr = os.WriteFile(report, []byte("<!-- report: completed -->\n\nfinished report text\n"), 0o600)
		} else {
			reportErr = os.ErrNotExist
		}
		return realWait(agents, sd, ctx, env, timeout, quiet, cwd)
	}
	t.Cleanup(func() { dispatchWaitFor = realWait })
	code, out, errText := f.run(t, "worker", f.brief, "--timeout", "30000")
	if markerErr != nil {
		t.Fatalf("the dispatch entered the wait without the not-received marker: %v", markerErr)
	}
	if reportErr != nil {
		t.Fatalf("could not write the report at the wait's entry: %v", reportErr)
	}
	if code != 0 || dispatchOutputStatus(t, out) != "done" || !strings.Contains(out, `"report_exists":true`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "not confirmed: it was working and shows no sign of the prompt; no key was sent.") || !strings.Contains(errText, waitingForReport) {
		t.Fatalf("warning without the waiting sentence: %s", errText)
	}
	if fields := strings.Fields(seen); len(fields) != 3 || !strings.HasPrefix(fields[2], filepath.Join(sd, "briefs", "worker-")) {
		t.Fatalf("not-received marker without the prompt path: %q", seen)
	}
	sidecars, err := filepath.Glob(filepath.Join(sd, "briefs", "worker-*.dispatch.json"))
	if err != nil || len(sidecars) == 0 {
		t.Fatalf("dispatch sidecar missing: %v", err)
	}
	if !strings.Contains(mustRead(t, sidecars[0]), `"arrival":"not-received"`) {
		t.Fatalf("sidecar did not record the not-received arrival: %s", mustRead(t, sidecars[0]))
	}
	noSendKeys(t, f)
}

// TestDispatchNoWaitKeepsNotReceivedForAWorkingTarget: --no-wait does not
// wait, so it keeps the not-received JSON, exit 15 and the warning text.
func TestDispatchNoWaitKeepsNotReceivedForAWorkingTarget(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\n", "0")
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "not confirmed: it was working and shows no sign of the prompt; no key was sent. Read the pane before sending anything else.") || strings.Contains(errText, waitingForReport) {
		t.Fatalf("--no-wait warning changed: %s", errText)
	}
	if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); err != nil {
		t.Fatalf("not-received marker missing: %v", err)
	}
	noSendKeys(t, f)
}

// TestDispatchBlockingStillExitsNotReceivedWhenThePromptStuckInTheInputBox: a
// prompt left typed in the input box certainly did not enter, so the blocking
// dispatch keeps exiting 15 without waiting (the wait would hit the timeout).
func TestDispatchBlockingStillExitsNotReceivedWhenThePromptStuckInTheInputBox(t *testing.T) {
	base := time.Date(2026, 10, 1, 16, 17, 14, 0, time.Local)
	began := time.Now()
	oldNow := platform.Now
	platform.Now = func() time.Time { return base.Add(time.Since(began)) }
	t.Cleanup(func() { platform.Now = oldNow })
	sep := strings.Repeat("─", 66)
	screen := "history: the previous step finished\n" +
		sep + "\n" +
		"Read the file " + filepath.Join("$ROOT", "state", "ws", "briefs", "worker-20261001T161714.md") + " in full and execute it.\n" +
		sep + "\n"
	f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "boot\n"},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: screen},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
	)
	arrivalRosterKind(t, f, "pi")
	f.env["SOHO_WAIT_POLL_MS"] = "100"
	start := time.Now()
	code, out, errText := f.run(t, "worker", f.brief, "--timeout", "30000")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "after an Enter on the text left in its input box") || strings.Contains(errText, waitingForReport) {
		t.Fatalf("warning=%q", errText)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("the dispatch waited %v before exiting not-received", elapsed)
	}
}
