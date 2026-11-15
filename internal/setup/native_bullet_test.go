package setup

// R329: `soho setup` migrates the native Core bullet in the file it writes:
// the canonical instruction file and the --local one go through the same
// writer, so the exact previous bullet becomes the current one, an absent one
// is added, and a bullet the project edited is kept.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/setuptext"
)

// previousNativeBullet is the bullet of the generation before the wake
// arming, pinned here independently of the setuptext implementation.
const previousNativeBullet = "- **Native Core** (`SOHO_TRANSPORT=hc`): workers and long runs are child\n  terminals of the orchestrator, never background tasks of the assistant's\n  harness. Builds, tests and scripts run as the child's own program\n  (`hc run --bg --outlive-parent --name <project>-<role> -- sh -c '<cmd>'`):\n  its exit note wakes the orchestrator. A soho worker stays alive after its\n  report and sends no note, so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one.\n"

func TestSetupWriteBlockMigratesTheNativeBullet(t *testing.T) {
	current := setuptext.SetupBlock()
	bullet := current[strings.Index(current, "- **Native Core**"):]
	bullet = bullet[:strings.Index(bullet, "- Quota")]
	if !strings.Contains(bullet, "--wake") || strings.Contains(previousNativeBullet, "--wake") {
		t.Fatalf("fixtures: current bullet %q", bullet)
	}
	legacy := func(block string) string { return strings.ReplaceAll(block, "soho", "herdr-soho") }
	cases := []struct {
		name  string
		block string
		want  func(content string) bool
	}{
		{"previous bullet is replaced", legacy(strings.Replace(current, bullet, previousNativeBullet, 1)), func(c string) bool { return c == "before\n"+current+"after\n" }},
		{"absent bullet is added", legacy(strings.Replace(current, bullet, "", 1)), func(c string) bool { return c == "before\n"+current+"after\n" }},
		{"edited bullet is kept", legacy(strings.Replace(current, bullet, strings.Replace(previousNativeBullet, "keeps its turn open", "never closes its turn", 1), 1)), func(c string) bool {
			return strings.Contains(c, "never closes its turn") && strings.Count(c, "- **Native Core**") == 1 && !strings.Contains(c, "--wake")
		}},
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
			})
		}
	}
}
