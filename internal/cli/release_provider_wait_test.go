package cli

// SOH-433 field proof: drive the same WaitFor that `soho wait` calls until
// wait reports a terminal provider outcome, then run `release --close` on
// that still-working worker from the wait state dir. The provider-refusal
// case is the cybersecurity screen from the Kashes rc.18 report; wait clears
// .provider for it, so a fixture that writes both markers would miss it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func TestReleaseCloseAfterRealWaitProviderOutcome(t *testing.T) {
	const refusalScreen = "■ We take extra care with some cybersecurity requests"
	const authScreen = "Error: 401 Unauthorized: invalid API key"
	const lostScreen = "■ Server connection could not be restored"
	const lostCause = "codex lost its server connection; run soho reopen <agent> --force and dispatch the task again with a resume note (the worktree keeps its diff)"
	for _, tc := range []struct {
		name, state, screen, status, cause string
	}{
		{"cybersecurity refusal", "idle", refusalScreen, "provider-refusal", "We take extra care with some cybersecurity requests"},
		{"auth provider error", "idle", authScreen, "provider-error", "Error: 401 Unauthorized: invalid API key"},
		{"lost connection while working", "working", lostScreen, "provider-error", lostCause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseEnv, cwd := commandFixture(t)
			sd := filepath.Join(baseEnv.Get("SOHO_DIR"), "ws")
			if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			waitBin := t.TempDir()
			waitConfig := filepath.Join(waitBin, "herdr.json")
			if _, err := fakecli.Install(t, waitBin, "herdr", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: fmt.Sprintf(`{"result":{"agent":{"name":"worker","agent_status":%q,"state_change_seq":5}}}`, tc.state)},
				{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: tc.screen},
				{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: tc.screen},
			}); err != nil {
				t.Fatal(err)
			}
			waitEnv := withFakeCLI(baseEnv, waitBin)
			waitEnv["HERDR_ENV"] = "1"
			waitEnv["HERDR_SOCKET_PATH"] = filepath.Join(waitBin, "none.sock")
			waitEnv["SOHO_WAIT_POLL_MS"] = "1"
			waitEnv["GORACE"] = "atexit_sleep_ms=0"

			var waitOut, waitErr bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			platform.Stdout, platform.Stderr = &waitOut, &waitErr
			waitpkg.SetFrictionLogFile(filepath.Join(sd, "wait-friction.log"))
			t.Cleanup(func() { waitpkg.SetFrictionLogFile(frictionLogPath) })
			rc := waitpkg.WaitFor([]string{"worker"}, sd, &core.Config{Entries: map[string]core.ConfigEntry{}}, waitEnv, 3000, false, cwd)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if rc != 14 {
				t.Fatalf("wait exit=%d stdout=%q stderr=%q; want the provider-error exit 14", rc, waitOut.String(), waitErr.String())
			}
			var obj map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(waitOut.Bytes()), &obj); err != nil {
				t.Fatalf("wait stdout=%q stderr=%q: %v", waitOut.String(), waitErr.String(), err)
			}
			if obj["status"] != tc.status {
				t.Fatalf("wait status=%v output=%q; want %q", obj["status"], waitOut.String(), tc.status)
			}
			marker, causePath := releaseProviderPaths(sd, "worker")
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("wait left .provider behind (fixture would not match the field case): %v", err)
			}
			causeBody, err := os.ReadFile(causePath)
			if err != nil {
				t.Fatalf("wait left no cause: %v", err)
			}
			if got := strings.TrimSpace(string(causeBody)); got != tc.cause {
				t.Fatalf("wait cause=%q; want %q", got, tc.cause)
			}
			waitCalls, err := fakecli.ReadCallsForConfig(waitConfig)
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range waitCalls {
				if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "send-keys" {
					t.Fatalf("wait sent input to the worker: %#v", call.Argv)
				}
			}

			// The release half sees a working worker with a report pointer
			// but no report body, exactly the refusal this guard relaxes.
			addReleaseReport(t, sd, "worker", true)
			releaseBin := t.TempDir()
			releaseConfig := filepath.Join(releaseBin, "herdr.json")
			if _, err := fakecli.Install(t, releaseBin, "herdr", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"working"}}}`},
				{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: tc.screen},
				{Argv: []string{"pane", "close", "p-worker"}},
				{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
				{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
				{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"old"}}}`},
				{Argv: []string{"tab", "rename", "t1", "herd"}},
			}); err != nil {
				t.Fatal(err)
			}
			releaseEnv := withFakeCLI(baseEnv, releaseBin)
			releaseEnv["HERDR_ENV"] = "1"
			releaseEnv["SOHO_REGRID"] = "off"
			f := &releaseFixture{env: releaseEnv, cwd: cwd, state: sd, fakeDir: releaseBin, configFile: releaseConfig}
			r := f.run(t, "worker", "--close")
			wantLine := "soho: release: 'worker' ended in a provider error (" + tc.cause + "); closing it without a report\n"
			if r.code != 0 || r.stderr != wantLine || !strings.Contains(r.stdout, "released worker") {
				t.Fatalf("release code=%d stdout=%q stderr=%q; want %q and a release", r.code, r.stdout, r.stderr, wantLine)
			}
			if len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 {
				t.Fatalf("pane close calls=%#v", r.calls)
			}
			reads := callsTo(r.calls, "agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40")
			if len(reads) != 1 {
				t.Fatalf("worker screen reads=%v; want one fresh 40-line read: %#v", reads, r.calls)
			}
			if core.RosterLine(sd, "worker") != "" {
				t.Fatal("roster row kept after the provider-error release")
			}
			for _, path := range []string{marker, causePath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("wait marker %s was not retired: %v", path, err)
				}
			}
		})
	}
}

// Round-2 review P1: wait writes provider-cause before a capacity retry and
// never clears it on recovery. Drive that real wait sequence, then prove
// release --close still refuses the live worker on its fresh working screen.
func waitProviderObservation(t *testing.T, baseEnv map[string]string, cwd, sd, state, screen string, timeoutMs float64) string {
	t.Helper()
	waitBin := t.TempDir()
	if _, err := fakecli.Install(t, waitBin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: fmt.Sprintf(`{"result":{"agent":{"name":"worker","agent_status":%q,"state_change_seq":5}}}`, state)},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: screen},
	}); err != nil {
		t.Fatal(err)
	}
	waitEnv := withFakeCLI(baseEnv, waitBin)
	waitEnv["HERDR_ENV"] = "1"
	waitEnv["HERDR_SOCKET_PATH"] = filepath.Join(waitBin, "none.sock")
	waitEnv["SOHO_WAIT_POLL_MS"] = "1"

	var waitOut, waitErr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &waitOut, &waitErr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	waitpkg.SetFrictionLogFile(filepath.Join(sd, "wait-friction.log"))
	t.Cleanup(func() { waitpkg.SetFrictionLogFile(frictionLogPath) })
	rc := waitpkg.WaitFor([]string{"worker"}, sd, &core.Config{Entries: map[string]core.ConfigEntry{}}, waitEnv, timeoutMs, false, cwd)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	status := ""
	var obj map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(waitOut.Bytes()), &obj); err == nil {
		status, _ = obj["status"].(string)
	}
	t.Logf("wait state=%q screen=%q rc=%d status=%q stderr=%q", state, screen, rc, status, waitErr.String())
	return status
}

func waitProviderMarkerState(t *testing.T, sd string) (bool, string) {
	t.Helper()
	marker, cause := releaseProviderPaths(sd, "worker")
	_, markerErr := os.Stat(marker)
	causeBody, causeErr := os.ReadFile(cause)
	if causeErr != nil {
		return markerErr == nil, ""
	}
	return markerErr == nil, strings.TrimSpace(string(causeBody))
}

func TestReleaseStaleProviderCauseAfterCapacityRecoveryStillRefuses(t *testing.T) {
	const recoveredScreen = "still working on the brief"
	baseEnv, cwd := commandFixture(t)
	sd := filepath.Join(baseEnv.Get("SOHO_DIR"), "ws")
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The capacity stop writes both markers but does not settle the task.
	waitProviderObservation(t, baseEnv, cwd, sd, "idle", releaseCapacityScreen, 600)
	hasMarker, cause := waitProviderMarkerState(t, sd)
	if !hasMarker || cause == "" {
		t.Fatalf("probe setup failed: want both marker files, got .provider=%v cause=%q", hasMarker, cause)
	}

	// The provider recovered and wait now observes the worker working: it
	// clears .provider but keeps the stale capacity cause.
	status := waitProviderObservation(t, baseEnv, cwd, sd, "working", recoveredScreen, 400)
	hasMarker, cause = waitProviderMarkerState(t, sd)
	t.Logf("after recovery to working (wait status=%q): .provider exists=%v cause=%q", status, hasMarker, cause)
	if hasMarker {
		t.Fatal("probe setup failed: wait did not clear .provider on the working observation")
	}
	if cause == "" {
		t.Fatal("probe setup failed: the stale cause is gone, so the finding is void")
	}

	addReleaseReport(t, sd, "worker", true)
	releaseBin := t.TempDir()
	releaseConfig := filepath.Join(releaseBin, "herdr.json")
	if _, err := fakecli.Install(t, releaseBin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"working"}}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recoveredScreen},
		{Argv: []string{"pane", "close", "p-worker"}},
		{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"old"}}`},
		{Argv: []string{"tab", "rename", "t1", "herd"}},
	}); err != nil {
		t.Fatal(err)
	}
	releaseEnv := withFakeCLI(baseEnv, releaseBin)
	releaseEnv["HERDR_ENV"] = "1"
	releaseEnv["SOHO_REGRID"] = "off"
	f := &releaseFixture{env: releaseEnv, cwd: cwd, state: sd, fakeDir: releaseBin, configFile: releaseConfig}
	r := f.run(t, "worker", "--close")
	assertReleaseProviderWorkingRefusal(t, r.code, r.stdout, r.stderr, core.RosterLine(sd, "worker"), r.calls, sd, "worker")
	reads := callsTo(r.calls, "agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40")
	if len(reads) != 1 {
		t.Fatalf("worker screen reads=%v; want one fresh 40-line read: %#v", reads, r.calls)
	}
	if _, err := os.ReadFile(filepath.Join(sd, "wait", "worker.provider-cause")); err != nil {
		t.Fatalf("refused release removed the cause: %v", err)
	}
}
