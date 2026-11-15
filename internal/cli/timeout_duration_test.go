package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const timeoutDurationPhrase = "expects milliseconds or a duration such as 90s, 30m or 2h"

func dieCodeMsg(t *testing.T, fn func()) (int, string) {
	t.Helper()
	code, msg := 0, ""
	func() {
		defer func() {
			r := recover()
			if ee, ok := r.(*platform.ExitError); ok {
				code, msg = ee.Code, ee.Msg
			} else if r != nil {
				t.Fatalf("unexpected panic: %v", r)
			}
		}()
		fn()
	}()
	return code, msg
}

func TestDispatchTimeoutDurationUnits(t *testing.T) {
	t.Run("--timeout 2s waits the full two seconds before exit 9", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "working", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		f.env["SOHO_WAIT_POLL_MS"] = "100"
		start := time.Now()
		code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "2s")
		elapsed := time.Since(start)
		if code != 9 || dispatchOutputStatus(t, out) != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%q; want the 2 s dispatch wait to time out with 9", code, out, stderr)
		}
		if elapsed < 1800*time.Millisecond || elapsed > 30*time.Second {
			t.Fatalf("elapsed=%v; the 2 s unit deadline was not applied", elapsed)
		}
		if strings.Contains(stderr, "--timeout is in milliseconds") {
			t.Fatalf("a unit value must not warn: stderr=%q", stderr)
		}
	})
	t.Run("an invalid duration exits 2 with the unit message", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "working", 1, 2, "", "0")
		code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "1h30m")
		if code != 2 || out != "" {
			t.Fatalf("code=%d out=%q; want exit 2 before the brief is read", code, out)
		}
		if !strings.Contains(stderr, "dispatch: --timeout "+timeoutDurationPhrase+", got '1h30m'") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}

func TestRunTimeoutDurationUnits(t *testing.T) {
	h := newTM3bHarness(t, tm3bFullBrief())
	prepareRunHarness(t, &h)
	t.Run("--timeout 2s is accepted and forwarded to the dispatch", func(t *testing.T) {
		code, out, stderr := runForTest(t, h, "--no-wait", "--pane", "w0test:p0b", "--timeout", "2s")
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q; want the run to proceed with the unit timeout", code, out, stderr)
		}
		if strings.Contains(stderr, "--timeout is in milliseconds") {
			t.Fatalf("a unit value must not warn: stderr=%q", stderr)
		}
	})
	t.Run("an invalid duration exits 2 with the dispatch unit message", func(t *testing.T) {
		code, out, stderr := runForTest(t, h, "--no-wait", "--pane", "w0test:p0b", "--timeout", "1h30m")
		if code != 2 || out != "" {
			t.Fatalf("code=%d out=%q; want exit 2", code, out)
		}
		if !strings.Contains(stderr, "dispatch: --timeout "+timeoutDurationPhrase+", got '1h30m'") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}

func TestCompactTimeoutDurationUnits(t *testing.T) {
	newFixture := func(t *testing.T) *compactFixture {
		t.Helper()
		return newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.noProof},
			// The polls without a proof also check the worker is alive.
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
		})
	}
	t.Run("--timeout 2s runs to its two second deadline with exit 9", func(t *testing.T) {
		f := newFixture(t)
		start := time.Now()
		code, out, stderr := f.run(t, "compact", "worker", "--timeout", "2s")
		elapsed := time.Since(start)
		if code != 9 {
			t.Fatalf("code=%d out=%q stderr=%q; want the 2 s compact timeout", code, out, stderr)
		}
		if elapsed < 1800*time.Millisecond || elapsed > 30*time.Second {
			t.Fatalf("elapsed=%v; the 2 s unit deadline was not applied", elapsed)
		}
		if strings.Contains(stderr, "--timeout is in milliseconds") {
			t.Fatalf("a unit value must not warn: stderr=%q", stderr)
		}
	})
	t.Run("an invalid duration exits 2 with the unit message before any herdr call", func(t *testing.T) {
		f := newFixture(t)
		code, out, stderr := f.run(t, "compact", "worker", "--timeout", "1h30m")
		if code != 2 || out != "" {
			t.Fatalf("code=%d out=%q; want exit 2", code, out)
		}
		if !strings.Contains(stderr, "compact: --timeout "+timeoutDurationPhrase) {
			t.Fatalf("stderr=%q", stderr)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Fatalf("herdr calls=%+v; the refusal must cost no subprocess", calls)
		}
	})
}

func TestJobTimeoutDurationUnits(t *testing.T) {
	t.Run("job wait --timeout accepts a unit", func(t *testing.T) {
		if got := jobMSValue("wait", "--timeout", map[string]string{"--timeout": "10s"}, 600000); got != 10000 {
			t.Fatalf("job wait --timeout 10s = %d, want 10000", got)
		}
		if got := jobMSValue("wait", "--timeout", map[string]string{"--timeout": "10m"}, 600000); got != 600000 {
			t.Fatalf("job wait --timeout 10m = %d, want 600000 (the bound, inclusive)", got)
		}
	})
	t.Run("job events --wait accepts a unit", func(t *testing.T) {
		if got := jobMSValue("events", "--wait", map[string]string{"--wait": "5m"}, 0); got != 300000 {
			t.Fatalf("job events --wait 5m = %d, want 300000", got)
		}
	})
	t.Run("the 0..600000 bound still applies to unit values", func(t *testing.T) {
		code, msg := dieCodeMsg(t, func() { jobMSValue("wait", "--timeout", map[string]string{"--timeout": "11m"}, 600000) })
		if code != 2 || msg != "job wait: --timeout "+timeoutDurationPhrase+", from 0 to 600000" {
			t.Fatalf("code=%d msg=%q; the bound keeps refusing over-bound unit values", code, msg)
		}
	})
	t.Run("an invalid duration exits 2 with the unit message", func(t *testing.T) {
		code, msg := dieCodeMsg(t, func() { jobMSValue("wait", "--timeout", map[string]string{"--timeout": "1h30m"}, 600000) })
		if code != 2 || msg != "job wait: --timeout "+timeoutDurationPhrase+", from 0 to 600000" {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		code, msg = dieCodeMsg(t, func() { jobMSValue("events", "--wait", map[string]string{"--wait": "1h30m"}, 0) })
		if code != 2 || msg != "job events: --wait "+timeoutDurationPhrase+", from 0 to 600000" {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
	})
}

func TestUsageDurationsLine(t *testing.T) {
	env, cwd := commandFixture(t)
	code, out, errOut := runIn(t, []string{"help"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("help code=%d err=%q", code, errOut)
	}
	const want = "\n\nDurations: --timeout (and job events --wait) takes milliseconds, or a number with ms, s, m or h (90s, 30m, 2h).\n\nCompletion contract:"
	if n := strings.Count(out, want); n != 1 {
		t.Fatalf("the Durations line must sit right before the Completion contract paragraph exactly once; got count=%d", n)
	}
	// The per-command usage lines keep the [timeout MS] spelling.
	for _, line := range []string{
		"[--ratio F] [--cwd DIR] [--pane ID] [--timeout MS]",
		"soho dispatch <agent> <brief.md> [--role R] [--timeout MS]",
		"soho compact <agent> [--timeout MS]",
		"soho wait <agent>... [--timeout MS] [--any] [--wake | --observe]",
		"# --wake (native Core, SOHO_TRANSPORT=hc, only) starts one child terminal of the caller that runs a non-settling wait",
		"on the legacy TUI it exits 2",
		"# --observe is the probe that child runs (not for direct use)",
		"a done report is left unsettled (no task state, metrics, warnings, mirror or git audit)",
		"soho job wait --id <id> [--timeout MS]",
		"soho job events --id <id> [--since <seq>] [--wait MS]",
		"soho find [words] [--machine <label>]... [--all] [--json] [--timeout MS]",
		"soho send <ref|name> <message…> | --file <path> [--now] [--timeout MS]",
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("usage lost or changed the line %q", line)
		}
	}
}
