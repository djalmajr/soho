package cli

// SOH-433: wait can settle a worker on a provider error while Herdr/hc
// still reports it as working. Its report was never written, so release
// --close and the release half of reopen must not require --force when the
// worker's own wait markers prove that the task ended that way.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	releaseProviderPrefix  = "soho: release: 'worker' ended in a provider error ("
	releaseProviderSuffix  = "); closing it without a report\n"
	releaseProviderMarker  = "provider"
	releaseProviderCause   = "provider-cause"
	releaseProviderValue   = "checksum\nprovider-error\n"
	releaseProviderCauseV  = "provider refused the task\n"
	releaseProviderErrLine = "soho: release: 'worker' ended in a provider error (provider refused the task); closing it without a report\n"

	releaseRefusalScreen  = "■ We take extra care with some cybersecurity requests"
	releaseAuthScreen     = "Error: 401 Unauthorized: invalid API key"
	releaseLostScreen     = "■ Server connection could not be restored"
	releaseCapacityScreen = "■ Error: 529 overloaded"
)

func releaseProviderPaths(sd, agent string) (string, string) {
	wait := filepath.Join(sd, "wait")
	return filepath.Join(wait, agent+"."+releaseProviderMarker), filepath.Join(wait, agent+"."+releaseProviderCause)
}

func writeReleaseProviderMarker(t *testing.T, sd, agent, markerBody, causeBody string) {
	t.Helper()
	marker, cause := releaseProviderPaths(sd, agent)
	for path, body := range map[string]string{marker: markerBody, cause: causeBody} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Field case: wait clears .provider for refusals and auth errors, leaving
// only the cause. The fixture never writes the other file.
func writeReleaseProviderCause(t *testing.T, sd, agent, causeBody string) {
	t.Helper()
	_, cause := releaseProviderPaths(sd, agent)
	if err := os.MkdirAll(filepath.Dir(cause), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cause, []byte(causeBody), 0o600); err != nil {
		t.Fatal(err)
	}
}

// prependReleaseFakeRule adds a rule ahead of the fixture's scripts; the
// fake CLI picks the first matching rule. The binary is already installed,
// so the JSON script is edited in place.
func prependReleaseFakeRule(t *testing.T, config string, rule fakecli.Rule) {
	t.Helper()
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var script map[string]json.RawMessage
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	var rules []fakecli.Rule
	if err := json.Unmarshal(script["rules"], &rules); err != nil {
		t.Fatal(err)
	}
	rulesJSON, err := json.Marshal(append([]fakecli.Rule{rule}, rules...))
	if err != nil {
		t.Fatal(err)
	}
	script["rules"] = rulesJSON
	body, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func setReleaseScreen(t *testing.T, f *releaseFixture, screen string, fails bool) {
	t.Helper()
	rule := fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: screen}
	if fails {
		rule.Code = 1
		rule.Stderr = "screen unavailable"
	}
	prependReleaseFakeRule(t, f.configFile, rule)
}

func setReleaseHcScreen(t *testing.T, f *releaseHcFixture, screen string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"text": screen, "complete": true})
	if err != nil {
		t.Fatal(err)
	}
	prependReleaseFakeRule(t, filepath.Join(f.bin, "hc.json"), fakecli.Rule{
		Argv: []string{"read", "worker", "--lines", "40", "--json"}, Stdout: string(body),
	})
}

func assertReleaseProviderSkipped(t *testing.T, r releaseRun) {
	t.Helper()
	if r.code != 0 || r.stderr != releaseProviderErrLine || !strings.Contains(r.stdout, "released worker") {
		t.Fatalf("code=%d stdout=%q stderr=%q; want the provider line and a release", r.code, r.stdout, r.stderr)
	}
	if len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 {
		t.Fatalf("pane close calls=%#v", r.calls)
	}
	if core.RosterLine(r.state, "worker") != "" {
		t.Fatal("roster row kept after the provider-error release")
	}
	marker, cause := releaseProviderPaths(r.state, "worker")
	for _, path := range []string{marker, cause} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("wait marker %s was not retired: %v", path, err)
		}
	}
}

func releaseWorkingRefusalLine(state string) string {
	return fmt.Sprintf("soho: agent 'worker' is still working and has not written %s; closing now discards its work. Run 'wait worker' first, or release --close --force\n", filepath.Join(state, "reports", "worker.md"))
}

func assertReleaseProviderWorkingRefusal(t *testing.T, code int, out, stderr, roster string, calls []fakecli.Call, state, agent string) {
	t.Helper()
	if code != 3 || stderr != releaseWorkingRefusalLine(state) {
		t.Fatalf("code=%d out=%q stderr=%q; want the literal working refusal %q", code, out, stderr, releaseWorkingRefusalLine(state))
	}
	if roster == "" {
		t.Fatal("roster row was removed on the refusal")
	}
	if len(callsTo(calls, "pane", "close", "p-worker")) != 0 {
		t.Fatalf("pane was closed on the refusal: %#v", calls)
	}
	if _, err := os.ReadFile(core.LastReportPath(state, agent)); err != nil {
		t.Fatalf("report pointer lost on the refusal: %v", err)
	}
}

// Mutation captured: ignoring the cause left by wait keeps the working
// refusal and forces the orchestrator to pass --force.
func TestReleaseCloseProviderErrorSkipsWorkingRefusal(t *testing.T) {
	f := newReleaseFixture(t, "working", "")
	addReleaseReport(t, f.state, "worker", true)
	writeReleaseProviderCause(t, f.state, "worker", releaseProviderCauseV)
	marker, _ := releaseProviderPaths(f.state, "worker")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fixture wrote .provider (not the field case): %v", err)
	}
	setReleaseScreen(t, f, releaseRefusalScreen, false)

	r := f.run(t, "worker", "--close")
	assertReleaseProviderSkipped(t, r)
	reads := callsTo(r.calls, "agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40")
	if len(reads) != 1 {
		t.Fatalf("worker screen reads=%v; want exactly one 40-line read: %#v", reads, r.calls)
	}
}

// Mutation captured: skipping the refusal without reading the marker closes
// a worker that may still deliver its report.
func TestReleaseCloseWorkingWithoutProviderMarkerStillRefuses(t *testing.T) {
	f := newReleaseFixture(t, "working", "")
	addReleaseReport(t, f.state, "worker", true)
	r := f.run(t, "worker", "--close")
	assertReleaseProviderWorkingRefusal(t, r.code, r.stdout, r.stderr, core.RosterLine(f.state, "worker"), r.calls, f.state, "worker")
}

func TestReleaseHcCloseProviderErrorSkipsWorkingRefusal(t *testing.T) {
	working := `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"working"}}`
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: working},
		{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","state":"finished"}`},
		{Argv: []string{"rm", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"removed":"t7"}`},
	})
	addReleaseHcReport(t, f, true)
	writeReleaseProviderCause(t, f.sd, "worker", releaseProviderCauseV)
	marker, cause := releaseProviderPaths(f.sd, "worker")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fixture wrote .provider (not the field case): %v", err)
	}
	setReleaseHcScreen(t, f, releaseRefusalScreen)

	code, out, stderr := f.run(t, "worker", "--close")
	reads := hcCallIndexes(f.hcCalls(t), []string{"read", "worker", "--lines", "40", "--json"})
	if len(reads) != 1 {
		t.Fatalf("worker screen reads=%v; want exactly one 40-line read: %#v", reads, f.hcCalls(t))
	}
	if code != 0 || stderr != releaseProviderErrLine || !strings.Contains(out, "closed terminal t7") {
		t.Fatalf("code=%d out=%q stderr=%q; want the provider line and a closed terminal", code, out, stderr)
	}
	calls := f.hcCalls(t)
	kills, rms := hcCallIndexes(calls, []string{"kill", "t7"}), hcCallIndexes(calls, []string{"rm", "t7"})
	if len(kills) != 1 || len(rms) != 1 || rms[0] < kills[0] {
		t.Fatalf("kill/rm calls: kills=%v rms=%v calls=%#v", kills, rms, calls)
	}
	if core.RosterLine(f.sd, "worker") != "" {
		t.Fatal("roster row kept after the provider-error release")
	}
	for _, path := range []string{marker, cause} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("wait marker %s was not retired: %v", path, err)
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}

func TestReleaseHcCloseWorkingWithoutProviderMarkerStillRefuses(t *testing.T) {
	working := `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"working"}}`
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: working},
	})
	addReleaseHcReport(t, f, true)
	code, out, stderr := f.run(t, "worker", "--close")
	if code != 3 || stderr != releaseWorkingRefusalLine(f.sd) {
		t.Fatalf("code=%d out=%q stderr=%q; want the literal working refusal %q", code, out, stderr, releaseWorkingRefusalLine(f.sd))
	}
	if core.RosterLine(f.sd, "worker") == "" {
		t.Fatal("roster row removed on the refusal")
	}
	for _, verb := range []string{"kill", "rm"} {
		if indexes := hcCallIndexes(f.hcCalls(t), []string{verb, "t7"}); len(indexes) != 0 {
			t.Fatalf("%s calls on the refusal: %#v", verb, f.hcCalls(t))
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}

func TestReleaseProviderCauseDoesNotOverrideUnavailableState(t *testing.T) {
	t.Run("herdr", func(t *testing.T) {
		f := newReleaseFixture(t, "", `{"error":{"code":"permission_denied","message":"not allowed"}}`)
		addReleaseReport(t, f.state, "worker", true)
		writeReleaseProviderCause(t, f.state, "worker", releaseProviderCauseV)
		r := f.run(t, "worker", "--close")
		if r.code != 4 || !strings.Contains(r.stderr, "herdr agent get failed") || strings.Contains(r.stderr, "ended in a provider error") {
			t.Fatalf("code=%d stderr=%q; want the unavailable refusal", r.code, r.stderr)
		}
		if core.RosterLine(f.state, "worker") == "" {
			t.Fatal("roster row removed on the unavailable refusal")
		}
	})
	t.Run("hc", func(t *testing.T) {
		f := newReleaseHcFixture(t, []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"permission_denied","message":"denied"}}`},
		})
		addReleaseHcReport(t, f, true)
		writeReleaseProviderCause(t, f.sd, "worker", releaseProviderCauseV)
		code, out, stderr := f.run(t, "worker", "--close")
		if code != 4 || !strings.Contains(stderr, "hc get failed") || strings.Contains(stderr, "ended in a provider error") {
			t.Fatalf("code=%d out=%q stderr=%q; want the unavailable refusal", code, out, stderr)
		}
		if core.RosterLine(f.sd, "worker") == "" {
			t.Fatal("roster row removed on the unavailable refusal")
		}
		f.assertHcNeverTouchedHerdr(t)
	})
}

func TestReleaseProviderCauseBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		skipped   bool
		screen    string
		readFails bool
		config    map[string]string
		setup     func(t *testing.T, sd string)
	}{
		{name: "empty cause", screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", " \n ")
		}},
		{name: "unreadable cause", screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			marker, cause := releaseProviderPaths(sd, "worker")
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte(releaseProviderValue), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(cause, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "marker only", screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			marker, _ := releaseProviderPaths(sd, "worker")
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte(releaseProviderValue), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "other worker cause", screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderMarker(t, sd, "other", releaseProviderValue, releaseProviderCauseV)
		}},
		{name: "other state dir cause", screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, filepath.Join(filepath.Dir(sd), "other-state"), "worker", releaseProviderCauseV)
		}},
		{name: "cause without .provider", skipped: true, screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "both present", skipped: true, screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderMarker(t, sd, "worker", releaseProviderValue, releaseProviderCauseV)
		}},
		{name: "empty .provider with cause", skipped: true, screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderMarker(t, sd, "worker", "", releaseProviderCauseV)
		}},
		{name: "auth screen", skipped: true, screen: releaseAuthScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "lost connection screen", skipped: true, screen: releaseLostScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "configured error text", skipped: true, screen: "custom vendor failure", config: map[string]string{"SOHO_PROVIDER_ERROR_TEXTS": "custom vendor failure"}, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "configured capacity text keeps refusal", screen: "server busy", config: map[string]string{"SOHO_PROVIDER_CAPACITY_TEXTS": "server busy"}, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "screen read failure", readFails: true, screen: releaseRefusalScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "empty screen", screen: "", setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "capacity-only screen", screen: releaseCapacityScreen, setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "unconfirmed stream log retains working refusal", screen: "Error: The response stopped arriving.", setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
		{name: "ordinary working screen", screen: "still working on the brief", setup: func(t *testing.T, sd string) {
			writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFixture(t, "working", "")
			addReleaseReport(t, f.state, "worker", true)
			tc.setup(t, f.state)
			for key, value := range tc.config {
				f.env[key] = value
			}
			setReleaseScreen(t, f, tc.screen, tc.readFails)
			r := f.run(t, "worker", "--close")
			if tc.skipped {
				assertReleaseProviderSkipped(t, r)
				return
			}
			assertReleaseProviderWorkingRefusal(t, r.code, r.stdout, r.stderr, core.RosterLine(f.state, "worker"), r.calls, f.state, "worker")
		})
	}
}

// The screen is evidence, not output: only the cause already on disk reaches
// the operator line, even when the screen carries secrets and control bytes.
func TestReleaseProviderScreenTextNeverPrinted(t *testing.T) {
	f := newReleaseFixture(t, "working", "")
	addReleaseReport(t, f.state, "worker", true)
	writeReleaseProviderCause(t, f.state, "worker", releaseProviderCauseV)
	setReleaseScreen(t, f, "secret=sk_live_abcdefghij\x1b[31m\n"+releaseRefusalScreen, false)

	r := f.run(t, "worker", "--close")
	if r.code != 0 || r.stderr != releaseProviderErrLine {
		t.Fatalf("code=%d stderr=%q; want the fixed provider line", r.code, r.stderr)
	}
	if strings.Contains(r.stderr, "sk_live_abcdefghij") || strings.Contains(r.stderr, "\x1b") {
		t.Fatalf("screen text leaked into stderr: %q", r.stderr)
	}
}

func TestReleaseProviderCauseIsEscapedAndBounded(t *testing.T) {
	f := newReleaseFixture(t, "working", "")
	addReleaseReport(t, f.state, "worker", true)
	writeReleaseProviderCause(t, f.state, "worker", "first line\n\x1b[31msecret=sk_live_abcdefghij\tmore\n"+strings.Repeat("x", 250)+"\n")
	setReleaseScreen(t, f, releaseRefusalScreen, false)

	r := f.run(t, "worker", "--close")
	if r.code != 0 || !strings.HasPrefix(r.stderr, releaseProviderPrefix) || !strings.HasSuffix(r.stderr, releaseProviderSuffix) {
		t.Fatalf("code=%d stderr=%q; want one provider line", r.code, r.stderr)
	}
	cause := strings.TrimSuffix(strings.TrimPrefix(r.stderr, releaseProviderPrefix), releaseProviderSuffix)
	if strings.ContainsAny(cause, "\n\r\t\x00\x1b") || strings.Contains(cause, "sk_live_abcdefghij") || !strings.Contains(cause, "secret=[redacted]") {
		t.Fatalf("cause=%q; want one escaped, redacted line", cause)
	}
	if len([]rune(cause)) > 200 {
		t.Fatalf("cause length=%d, want at most 200: %q", len([]rune(cause)), cause)
	}
	if strings.Count(r.stderr, "\n") != 1 {
		t.Fatalf("stderr had %d lines: %q", strings.Count(r.stderr, "\n"), r.stderr)
	}
}

func TestDispatchClearsProviderCauseBeforeNewTask(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	sd := filepath.Join(f.state, "ws")
	writeReleaseProviderCause(t, sd, "worker", releaseProviderCauseV)
	firstDispatch(t, f)
	marker, cause := releaseProviderPaths(sd, "worker")
	for _, path := range []string{marker, cause} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dispatch left %s behind: %v", path, err)
		}
	}
}

// The roster reset in dispatch is the lifetime guard: every new task removes
// both files from this list before the brief is sent (dispatch.go:619-622).
func TestDispatchResetsProviderErrorMarkerFiles(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range dispatchWaitFiles {
		seen[name] = true
	}
	for _, name := range []string{releaseProviderMarker, releaseProviderCause} {
		if !seen[name] {
			t.Fatalf("dispatchWaitFiles=%q does not reset %s", dispatchWaitFiles, name)
		}
	}
}

func TestReopenProviderErrorReleaseHalfNeedsNoForce(t *testing.T) {
	f := newReopenFixture(t, "working")
	addReleaseReport(t, f.state, "worker", true)
	writeReleaseProviderCause(t, f.state, "worker", releaseProviderCauseV)
	setReopenRules(t, f)
	setReleaseScreen(t, f, releaseRefusalScreen, false)

	run := f.reopen(t, "worker")
	if run.code != 0 || !strings.Contains(run.stderr, releaseProviderErrLine) {
		t.Fatalf("code=%d stdout=%q stderr=%q; want the provider line and a completed reopen", run.code, run.stdout, run.stderr)
	}
	if len(callsTo(run.calls, "pane", "close", "p-worker")) != 1 {
		t.Fatalf("reopen did not close the old pane: %#v", run.calls)
	}
	starts := 0
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("reopen did not spawn the replacement (%d starts): %#v", starts, run.calls)
	}
}
