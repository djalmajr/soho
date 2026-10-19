package wait

// DJA-434: a wait that ends settled-no-report carries the last lines of the
// visible screen in `screen`, and names codex's hooks review screen on
// stderr.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const waitHooksTableScreen = "Hooks\n" +
	"Lifecycle hooks from config and enabled plugins.\n" +
	"⚠ 3 hooks need review before they can run.\n" +
	"SessionStart  4 installed  2 active  2 review\n" +
	"SessionEnd    1 installed  0 active  1 review\n" +
	"t trust all · enter review · esc close\n"

func newSettledFixture(t *testing.T, kind, screen string) *backgroundFixture {
	t.Helper()
	f := newBackgroundFixture(t, screen, nil)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\t" + kind + "\timplementer\t\t\t\t\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	platform.Now = time.Now
	f.ctx.Entries["settled_grace"] = core.ConfigEntry{Value: "0"}
	return f
}

func settledLine(t *testing.T, out string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"status":"settled-no-report"`) {
			var value map[string]any
			if err := json.Unmarshal([]byte(line), &value); err != nil {
				t.Fatalf("JSON %q: %v", line, err)
			}
			return value
		}
	}
	t.Fatalf("no settled-no-report line: %q", out)
	return nil
}

const waitHooksMenuScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"› 1. Review hooks\n" +
	"  2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

func TestWaitSettledNoReportCarriesScreenAndNamesHooksReview(t *testing.T) {
	for _, screen := range []string{waitHooksTableScreen, waitHooksMenuScreen} {
		f := newSettledFixture(t, "codex", screen)
		code, out := f.waitForRun(t, 20000)
		if code != 6 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, f.stderr.String())
		}
		line := settledLine(t, out)
		if got, want := line["screen"], lastNonEmpty(screen, 20); got != want || !strings.Contains(want, "review") {
			t.Fatalf("screen = %q, want %q", got, want)
		}
		stderr := f.stderr.String()
		if !strings.Contains(stderr, "soho: warning: agent 'worker' is on codex's hooks review screen; ") || !strings.Contains(stderr, "hooks_review=trust (now trust)") || !strings.Contains(stderr, "With hooks_review=block nothing is typed") {
			t.Fatalf("stderr = %q", stderr)
		}
	}
}

func TestWaitSettledNoReportOtherScreenHasNoHooksLine(t *testing.T) {
	// Control: a plain idle screen gets the screen field and no hooks line;
	// the same hooks screen on another kind gets no hooks line either.
	for _, tc := range []struct{ kind, screen string }{{"codex", idleScreenNoBackground}, {"claude", waitHooksTableScreen}} {
		f := newSettledFixture(t, tc.kind, tc.screen)
		code, out := f.waitForRun(t, 20000)
		if code != 6 {
			t.Fatalf("%s: code=%d out=%q", tc.kind, code, out)
		}
		if got := settledLine(t, out)["screen"]; got != lastNonEmpty(tc.screen, 20) {
			t.Fatalf("%s: screen = %q", tc.kind, got)
		}
		if strings.Contains(f.stderr.String(), "hooks review") {
			t.Fatalf("%s: stderr names the hooks screen: %q", tc.kind, f.stderr.String())
		}
	}
}
