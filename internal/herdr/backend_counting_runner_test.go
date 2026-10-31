package herdr

import (
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// launch is one process launch the counting decorator recorded: the exact
// exe, argv and run options the backend passed to its runner.
type launch struct {
	exe  string
	args []string
	opts platform.RunOptions
}

// launchCount records the process launches the counting decorator wraps
// around the backend's per-instance runner. The count is taken where the
// backend makes the launch, before the process starts: unlike the fake's
// call log, a deadline that kills the fake cannot zero it.
type launchCount struct {
	launches []launch
}

// count is the number of launches recorded so far.
func (c *launchCount) count() int { return len(c.launches) }

// countingServices builds the backend with New and replaces its per-instance
// runner with a counting decorator that delegates to the real
// platform.RunCli: the real fake subprocess still runs, so a TimedOut
// outcome is real, and the launch count is the decorator's, not the fake's
// call log. Test-only; the runner is set per instance, never
// package-global.
func countingServices(t *testing.T, env platform.Env) (transport.Services, *launchCount) {
	t.Helper()
	svc := New(env)
	b, ok := svc.Legacy.(*backend)
	if !ok {
		t.Fatalf("New built a non-herdr Legacy backend: %#v", svc.Legacy)
	}
	c := &launchCount{}
	b.runner = func(exe string, args []string, opts platform.RunOptions) platform.RunResult {
		c.launches = append(c.launches, launch{exe: exe, args: append([]string(nil), args...), opts: opts})
		return platform.RunCli(exe, args, opts)
	}
	return svc, c
}
