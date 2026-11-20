package cli

import (
	"strings"
	"testing"
)

// The printed recovery command runs after reopen closed the old worker, so
// it keeps spawn's memory-pressure guard: reopen's own --force is dropped,
// while a --force among the native args after -- belongs to the CLI and stays.
func TestReopenRecoveryCommandDropsReopensForceOnly(t *testing.T) {
	got := reopenRecoveryCommand("worker", []string{"implementer", "--fresh", "--kind", "pi", "--name", "worker", "--cwd", "/w", "--force", "--effort", "high", "--", "--force", "x y"})
	want := "soho spawn implementer --name worker --fresh --kind pi --cwd /w --effort high -- --force 'x y'"
	if got != want {
		t.Fatalf("recovery command:\n got %q\nwant %q", got, want)
	}
}

// A kind swap in a lane pinned to another kind skipped the lane check inside
// reopen; a plain spawn would refuse with exit 13, so the printed recovery
// command opts out of lanes. Without the swap, or without the conflict, it
// stays a plain spawn.
func TestReopenRecoveryCommandOptsOutOfAPinnedLaneOnlyForTheSwap(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"swap across the pinned lane", []string{"worker", "--kind", "claude"}, "soho: reopen: to recover, run: SOHO_LANES=off soho spawn implementer --name worker --fresh --kind claude --cwd "},
		{"same kind", []string{"worker"}, "soho: reopen: to recover, run: soho spawn implementer --name worker --fresh --kind grok --cwd "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := failingReopenFixture(t, false)
			f.env["SOHO_LANES"] = "on"
			f.env["SOHO_LANE_BUILD_ROLES"] = "implementer"
			f.env["SOHO_LANE_BUILD_KIND"] = "grok"
			run := f.reopen(t, tc.args...)
			if run.code == 0 {
				t.Fatalf("a failed spawn returned 0: stdout=%q stderr=%q", run.stdout, run.stderr)
			}
			if !strings.Contains(run.stderr, tc.want+f.cwd) {
				t.Fatalf("stderr=%q, want the recovery line starting %q", run.stderr, tc.want+f.cwd)
			}
		})
	}
}
