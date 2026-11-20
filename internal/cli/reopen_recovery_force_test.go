package cli

import "testing"

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
