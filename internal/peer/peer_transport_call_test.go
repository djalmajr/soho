package peer

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionref"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// transportCallFixture installs the fake herdr CLI and returns the env a
// per-invocation herdr.New runs over: only the fake on PATH, a throwaway
// TMPDIR and the race exit-sleep removed, so a timed call measures the
// deadline, not the race shutdown.
func transportCallFixture(t *testing.T, rules []fakecli.Rule) platform.Env {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	if _, err := fakecli.Install(t, binDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	list := fakecli.Env([]string{"PATH=" + binDir, "TMPDIR=" + t.TempDir(), "GORACE=atexit_sleep_ms=0"}, binDir)
	env := platform.Env{}
	for _, item := range list {
		if k, v, ok := strings.Cut(item, "="); ok {
			env[k] = v
		}
	}
	return env
}

// TestWaitUntilIdleKeepsTheSummedProcessTimeout: the peer's wait passes the
// logical bound plus the call budget as the process timeout to the backend,
// so a herdr call that outlives the logical bound but fits the sum settles
// instead of timing out. The fake sleeps past the logical bound and well
// within the sum: with the budget dropped, the same call would be the
// timeout the peer reports.
func TestWaitUntilIdleKeepsTheSummedProcessTimeout(t *testing.T) {
	const logical = 200
	argv := []string{"agent", "wait", "w0test:p0a", "--until", "idle", "--until", "done", "--timeout", fmt.Sprint(logical)}
	env := transportCallFixture(t, []fakecli.Rule{{Argv: argv, Delay: 1500}})
	svc := herdr.New(env)
	res := waitUntilIdle(svc, sessionref.LocalMachine, "w0test:p0a", logical)
	if res.Code != "settled" {
		t.Fatalf("res=%#v; a 1.5s call fits the %dms process timeout (logical %dms + call budget %dms), not the %dms logical bound alone",
			res, logical+HerdrCallTimeoutMS, logical, HerdrCallTimeoutMS, logical)
	}
}

// TestWaitUntilIdleMachinePrefixGoesThroughTheBackend: a remote target keeps
// the --machine prefix on the wait, end to end, and the raw structured error
// of a failed wait still surfaces as the peer's cause.
func TestWaitUntilIdleMachinePrefixGoesThroughTheBackend(t *testing.T) {
	env := transportCallFixture(t, []fakecli.Rule{{
		Argv:   []string{"--machine", "box1", "agent", "wait", "w9:p1", "--until", "idle", "--until", "done", "--timeout", "30000"},
		Code:   1,
		Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`,
	}})
	svc := herdr.New(env)
	res := waitUntilIdle(svc, "box1", "w9:p1", 30000)
	if res.Code != "agent_not_found" || res.Cause == "" {
		t.Fatalf("res=%#v; the raw structured error must surface through the backend", res)
	}
}
