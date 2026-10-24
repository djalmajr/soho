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

// typedWhileWorking is the warning for a working target that shows no sign of
// the prompt (DJA-404): the text was typed, so the brief must not go to
// another worker.
func typedWhileWorking(agent string) string {
	return "prompt to '" + agent + "' was typed while it was working and shows no sign of it yet: its CLI may run it when the current turn ends. Do not send this brief to another worker; run soho wait " + agent + ", or interrupt the agent and cancel the brief."
}

// paneTitleRule lets the fake herdr take the title the dispatch gives the
// pane: the fixture's default rule fails any pane title call.
var paneTitleRule = fakecli.Rule{Argv: []string{"pane", "report-metadata", "w0test:p0a", "--source", "soho"}, ArgvPrefix: true}

// paneTitleCalls returns the --title values of the fake herdr's
// pane report-metadata calls.
func paneTitleCalls(t *testing.T, f *dispatchArrivalFixture) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, call := range calls {
		if len(call.Argv) > 6 && call.Argv[0] == "pane" && call.Argv[1] == "report-metadata" && call.Argv[4] == "soho" && call.Argv[5] == "--title" {
			titles = append(titles, call.Argv[6])
		}
	}
	return titles
}

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
	f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\n", "0", paneTitleRule)
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
	// At report publication the worker finishes its turn. Keep arrival's
	// working responses separate from the wait's idle response.
	idleBin := t.TempDir()
	if _, err := fakecli.Install(t, idleBin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":6}}}`}}); err != nil {
		t.Fatal(err)
	}
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, quiet bool, cwd string) int {
		raw, err := os.ReadFile(marker)
		seen, markerErr = string(raw), err
		if report := core.LastReport(sd, "worker"); report != "" {
			reportErr = os.WriteFile(report, []byte("<!-- report: completed -->\n\nfinished report text\n"), 0o600)
		} else {
			reportErr = os.ErrNotExist
		}
		waitEnv := platform.Env{}
		for key, value := range env {
			waitEnv[key] = value
		}
		waitEnv["PATH"], waitEnv["SOHO_FAKECLI_CONFIG"] = idleBin, idleBin
		return realWait(agents, sd, ctx, waitEnv, timeout, quiet, cwd)
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
	if !strings.Contains(errText, typedWhileWorking("worker")+" "+waitingForReport) || strings.Contains(errText, "no key was sent") {
		t.Fatalf("warning without the typed text and the waiting sentence: %s", errText)
	}
	// The wait ended done, so the task carries the done mark.
	wantTitle := "implementer: " + core.BriefTask(f.brief)
	if got := strings.TrimSpace(mustRead(t, filepath.Join(sd, "task-worker"))); got != wantTitle+" ✓" {
		t.Fatalf("task-worker=%q want %q", got, wantTitle+" ✓")
	}
	if titles := paneTitleCalls(t, f); len(titles) == 0 || titles[0] != wantTitle {
		t.Fatalf("pane titles=%q: the first must be %q", titles, wantTitle)
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
// wait, so it keeps the not-received JSON and exit 15, but the warning says the
// text was typed (DJA-404) and the dispatch records the new task: task-<agent>
// and the pane title show it, as the normal path does.
func TestDispatchNoWaitKeepsNotReceivedForAWorkingTarget(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\n", "0", paneTitleRule)
	sd := filepath.Join(f.state, "ws")
	if err := os.WriteFile(filepath.Join(sd, "task-worker"), []byte("implementer: the old task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, typedWhileWorking("worker")) || strings.Contains(errText, waitingForReport) || strings.Contains(errText, "no key was sent") {
		t.Fatalf("--no-wait warning: %s", errText)
	}
	if _, err := os.Stat(filepath.Join(sd, "wait", "worker.not-received")); err != nil {
		t.Fatalf("not-received marker missing: %v", err)
	}
	wantTitle := "implementer: " + core.BriefTask(f.brief)
	if got := strings.TrimSpace(mustRead(t, filepath.Join(sd, "task-worker"))); got != wantTitle {
		t.Fatalf("task-worker=%q want %q", got, wantTitle)
	}
	if titles := paneTitleCalls(t, f); len(titles) != 1 || titles[0] != wantTitle {
		t.Fatalf("pane titles=%q want [%q]", titles, wantTitle)
	}
	noSendKeys(t, f)
}

// TestDispatchBlockingStillExitsNotReceivedWhenThePromptStuckInTheInputBox: a
// prompt left typed in the input box certainly did not enter, so the blocking
// dispatch keeps exiting 15 without waiting (the wait would hit the timeout).
func TestDispatchBlockingStillExitsNotReceivedWhenThePromptStuckInTheInputBox(t *testing.T) {
	for _, setupElapsed := range []time.Duration{0, 2 * time.Second} {
		t.Run(setupElapsed.String(), func(t *testing.T) {
			base := time.Date(2026, 10, 1, 16, 17, 14, 0, time.Local)
			// Model preparation crossing a stamp second without a wall-clock sleep.
			// Advance the clock on reads so the arrival deadlines still terminate.
			clock := base.Add(setupElapsed)
			stamp := core.NowStamp(clock)
			oldNow := platform.Now
			platform.Now = func() time.Time {
				now := clock
				clock = clock.Add(2 * time.Second)
				return now
			}
			t.Cleanup(func() { platform.Now = oldNow })
			sep := strings.Repeat("─", 66)
			screen := "history: the previous step finished\n" +
				sep + "\n" +
				"Read the file " + filepath.Join("$ROOT", "state", "ws", "briefs", "worker-"+stamp+".md") + " in full and execute it.\n" +
				sep + "\n"
			f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
				fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "boot\n"},
				fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
				fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: screen},
				fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
			)
			arrivalRosterKind(t, f, "pi")
			f.env["SOHO_WAIT_POLL_MS"] = "100"
			taskFile := filepath.Join(f.state, "ws", "task-worker")
			if err := os.WriteFile(taskFile, []byte("implementer: the old task\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The fake clock makes elapsed time meaningless, so the report wait
			// is watched directly: a prompt that did not enter never waits.
			waits := 0
			oldWait := dispatchWaitFor
			dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, quiet bool, cwd string) int {
				waits++
				return oldWait(agents, sd, ctx, env, timeout, quiet, cwd)
			}
			t.Cleanup(func() { dispatchWaitFor = oldWait })
			start := time.Now()
			code, out, errText := f.run(t, "worker", f.brief, "--timeout", "30000")
			if waits != 0 {
				t.Fatalf("the dispatch waited for the report %d time(s) before exiting not-received", waits)
			}
			if _, err := os.Stat(filepath.Join(f.state, "ws", "briefs", "worker-"+stamp+".md")); err != nil {
				t.Fatalf("fixture screen did not name the composed brief: %v; out=%s", err, out)
			}
			if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
			}
			if !strings.Contains(errText, "after an Enter on the text left in its input box") || strings.Contains(errText, waitingForReport) || strings.Contains(errText, "was typed while it was working") {
				t.Fatalf("setupElapsed=%s out=%s warning=%q", setupElapsed, out, errText)
			}
			// The prompt certainly did not enter: the task title stays the old one.
			if got := strings.TrimSpace(mustRead(t, taskFile)); got != "implementer: the old task" {
				t.Fatalf("task-worker=%q: a prompt that did not enter changed the title", got)
			}
			if titles := paneTitleCalls(t, f); len(titles) != 0 {
				t.Fatalf("pane titles=%q: a prompt that did not enter retitled the pane", titles)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if prompts, keys := countDispatchCalls(calls, "prompt"), countDispatchCalls(calls, "send-keys"); prompts != 1 || keys != 1 {
				t.Fatalf("prompt calls=%d send-keys calls=%d, want one prompt and one Enter; calls=%#v", prompts, keys, calls)
			}
			if elapsed := time.Since(start); elapsed > 20*time.Second {
				t.Fatalf("the dispatch waited %v before exiting not-received", elapsed)
			}
		})
	}
}
