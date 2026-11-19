package setup

// R329/SOH-450: `soho setup` migrates the native Core bullet in the file it
// writes: the canonical instruction file and the --local one go through the
// same writer, so the exact v6 bullet or the exact pre-wake one becomes the
// current v7 one, an absent one is added, a bullet the project edited is
// kept, and a second run changes nothing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/setuptext"
)

// v6NativeBullet is the v6 bullet (the wake arming, with the foreground wait
// kept), pinned here independently of the setuptext implementation.
const v6NativeBullet = "- **Native Core** (`SOHO_TRANSPORT=hc`): workers and long runs are child\n  terminals of the orchestrator, never background tasks of the assistant's\n  harness. Builds, tests and scripts run as the child's own program\n  (`hc run --bg --outlive-parent --name <project>-<role> -- sh -c '<cmd>'`):\n  its exit note wakes the orchestrator. A soho worker stays alive after its\n  report and sends no note, so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one, or arms\n  `soho wait <agent> --wake` and ends its turn: the note wakes it, and it\n  runs `soho wait <agent>` once.\n"

// preWakeNativeBullet is the bullet of the generation before the wake arming,
// pinned the same way.
const preWakeNativeBullet = "- **Native Core** (`SOHO_TRANSPORT=hc`): workers and long runs are child\n  terminals of the orchestrator, never background tasks of the assistant's\n  harness. Builds, tests and scripts run as the child's own program\n  (`hc run --bg --outlive-parent --name <project>-<role> -- sh -c '<cmd>'`):\n  its exit note wakes the orchestrator. A soho worker stays alive after its\n  report and sends no note, so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one.\n"

func TestSetupWriteBlockMigratesTheNativeBullet(t *testing.T) {
	current := setuptext.SetupBlock()
	bullet := current[strings.Index(current, "- **Native Core**"):]
	bullet = bullet[:strings.Index(bullet, "- Quota")]
	if !strings.Contains(bullet, "The orchestrator's prompt never\n  blocks") || !strings.Contains(v6NativeBullet, "keeps its turn open") || strings.Contains(preWakeNativeBullet, "--wake") {
		t.Fatalf("fixtures: current bullet %q", bullet)
	}
	legacy := func(block string) string { return strings.ReplaceAll(block, "soho", "herdr-soho") }
	// A continuation line the project appended to the item, indented with a
	// tab, is part of the edited item: it must survive the in-place
	// migration the same way a two-space continuation does.
	tabContinuation := "\tOur house rule applies.\n"
	withTab := func(previous string) string {
		return legacy(strings.Replace(current, bullet, previous+tabContinuation, 1))
	}
	keptTab := func(c string) bool {
		return strings.Contains(c, tabContinuation) && strings.Count(c, "- **Native Core**") == 1 && !strings.Contains(c, "prompt never\n  blocks")
	}
	cases := []struct {
		name       string
		block      string
		want       func(content string) bool
		secondVerb string
	}{
		{"v6 bullet is replaced", legacy(strings.Replace(current, bullet, v6NativeBullet, 1)), func(c string) bool { return c == "before\n"+current+"after\n" }, "unchanged"},
		{"pre-wake bullet is replaced", legacy(strings.Replace(current, bullet, preWakeNativeBullet, 1)), func(c string) bool { return c == "before\n"+current+"after\n" }, "unchanged"},
		{"v6 bullet with a tab continuation is kept", withTab(v6NativeBullet), keptTab, "updated"},
		{"pre-wake bullet with a tab continuation is kept", withTab(preWakeNativeBullet), keptTab, "updated"},
		{"absent bullet is added", legacy(strings.Replace(current, bullet, "", 1)), func(c string) bool { return c == "before\n"+current+"after\n" }, "unchanged"},
		// A current-marker block is managed text: the edit survives the
		// migration and the next setup replaces the block as usual.
		{"edited bullet is kept", legacy(strings.Replace(current, bullet, strings.Replace(v6NativeBullet, "keeps its turn open", "never closes its turn", 1), 1)), func(c string) bool {
			return strings.Contains(c, "never closes its turn") && strings.Count(c, "- **Native Core**") == 1 && !strings.Contains(c, "prompt never\n  blocks")
		}, "updated"},
	}
	// The canonical instruction file and the --local one use one writer.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md"} {
		for _, tc := range cases {
			t.Run(name+" "+tc.name, func(t *testing.T) {
				file := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(file, []byte("before\n"+tc.block+"after\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if verb := setupWriteBlock(file); verb != "updated" {
					t.Fatalf("verb %q, want updated", verb)
				}
				got, err := os.ReadFile(file)
				if err != nil || !tc.want(string(got)) {
					t.Fatalf("content after setup (err=%v):\n%s", err, got)
				}
				if verb := setupWriteBlock(file); verb != tc.secondVerb {
					t.Fatalf("second setup run: verb %q, want %q", verb, tc.secondVerb)
				}
			})
		}
	}
}
