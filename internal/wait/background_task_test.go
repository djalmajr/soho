package wait

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// DJA-405: a claude worker that ends its turn with a background shell or
// Monitor looks idle to Herdr but resumes on its own, so a still screen that
// shows the background-task counter is not settled-no-report.

// backgroundShellScreen is the screen of the Soho case: the turn summary and
// the footer both count one shell.
const backgroundShellScreen = "● Gates are running in the background; the report comes after them.\n" +
	"\n" +
	"✻ Cooked for 7m 37s · done 2:47 PM · 1 shell still running\n" +
	"\n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  ~/Developer/djalmajr/herdr-soho  fix/dja-405  ctx 31%\n" +
	"  ⏵⏵ bypass permissions on · PR #121 · 1 shell\n"

// backgroundMonitorScreen is the screen of the Pinar case: blank lines, the
// separator with the empty prompt and a free statusline sit above the footer,
// which is the last line.
const backgroundMonitorScreen = "● Watching the deploy; I will write the report when it ends.\n" +
	"\n" +
	"✻ Cogitated for 40s · done 11:47 AM · 2 monitors still running\n" +
	"\n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  pinar  main  $0.42  12k tokens\n" +
	"\n" +
	"  ⏵⏵ bypass permissions on · 2 monitors · ← 1 agent\n"

const idleScreenNoBackground = "● Done. The report is at .soho/ws/reports/worker.md\n" +
	"\n" +
	"✻ Cooked for 7m 37s · done 2:47 PM\n" +
	"\n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on · PR #121\n"

// DJA-438: two real claude screens the DJA-405 pattern missed.
//
// backgroundWrappedScreen is a narrow pane: the turn summary counts a shell
// and a Monitor and wraps, so "still running" sits alone on the next line.
const backgroundWrappedScreen = "● Waiting on the gates' Monitor; the report comes after it.\n" +
	"\n" +
	"✻ Cooked for 3m 12s · done 7:28 PM · 1 shell, 1 monitor still\n" +
	"running\n" +
	"\n" +
	"──────────────────────────────────────────\n" +
	"❯ \n" +
	"──────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on · PR #121\n"

// backgroundCommaFooter is the footer that lists both tasks with a comma.
const backgroundCommaFooter = "  ⏵⏵ bypass permissions on · 1 shell, 1 monitor · ← 3 agents\n"

// backgroundFixture is a claude worker whose screen is still, with the
// claude pattern configured and the clock under the test's control.
type backgroundFixture struct {
	*queuedProbeFixture
	now      *time.Time
	stderr   *bytes.Buffer
	friction string
}

func newBackgroundFixture(t *testing.T, screen string, entries map[string]string) *backgroundFixture {
	t.Helper()
	f := &backgroundFixture{queuedProbeFixture: newQueuedProbeFixture(t, "idle", "5", screen, nil)}
	for key, value := range entries {
		f.ctx.Entries[key] = core.ConfigEntry{Value: value}
	}
	fixed := time.Unix(2_000_000_000, 0)
	f.now = &fixed
	oldNow := platform.Now
	platform.Now = func() time.Time { return *f.now }
	f.stderr = &bytes.Buffer{}
	oldErr := platform.Stderr
	platform.Stderr = f.stderr
	f.friction = filepath.Join(t.TempDir(), "friction.log")
	oldFriction := frictionLogFile
	SetFrictionLogFile(f.friction)
	t.Cleanup(func() {
		platform.Now = oldNow
		platform.Stderr = oldErr
		SetFrictionLogFile(oldFriction)
	})
	return f
}

// settleProbe takes the first reading, lets the grace pass and probes again,
// the way a wait on a still screen reaches its settled verdict.
func (f *backgroundFixture) settleProbe(t *testing.T) string {
	t.Helper()
	if got := f.probe(""); got != "working" {
		t.Fatalf("first probe = %q, want working", got)
	}
	*f.now = f.now.Add(90 * time.Second)
	return f.probe("")
}

func (f *backgroundFixture) noticeLines() []string {
	var lines []string
	for _, line := range strings.Split(f.stderr.String(), "\n") {
		if strings.Contains(line, "background task still running") {
			lines = append(lines, line)
		}
	}
	return lines
}

// shippedClaudePattern is the background_patterns.claude value the skill
// ships, read from config.defaults so the screens test the shipped value.
func shippedClaudePattern(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "config.defaults"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "background_patterns.claude="); ok {
			return value
		}
	}
	t.Fatal("config.defaults ships no background_patterns.claude")
	return ""
}

func claudePattern(t *testing.T) map[string]string {
	return map[string]string{"background_patterns_claude": shippedClaudePattern(t)}
}

func TestBackgroundTaskKeepsAClaudeWorkerWorking(t *testing.T) {
	cases := []struct {
		name    string
		screen  string
		snippet string
	}{
		{"a shell still running (Soho)", backgroundShellScreen, `1 shell still running`},
		{"monitors still running with a statusline and the footer last (Pinar)", backgroundMonitorScreen, `2 monitors still running`},
		{"only the footer counts the shell", strings.Replace(backgroundShellScreen, "✻ Cooked for 7m 37s · done 2:47 PM · 1 shell still running\n", "✻ Cooked for 7m 37s · done 2:47 PM\n", 1), `· 1 shell`},
		{"only the footer counts the monitors, followed by another segment", strings.Replace(backgroundMonitorScreen, "✻ Cogitated for 40s · done 11:47 AM · 2 monitors still running\n", "", 1), `· 2 monitors ·`},
		{"only the footer counts the monitors, at the end of the line", strings.Replace(idleScreenNoBackground, "· PR #121\n", "· PR #121 · 2 monitors\n", 1), `· 2 monitors`},
		{"the summary wraps before running (DJA-438)", backgroundWrappedScreen, `1 shell, 1 monitor still running`},
		{"the footer lists a shell and a monitor with a comma (DJA-438)", strings.Replace(idleScreenNoBackground, "  ⏵⏵ bypass permissions on · PR #121\n", backgroundCommaFooter, 1), `· 1 shell, 1 monitor ·`},
		{"the wrapped summary and the comma footer: the first from the top (DJA-438)", strings.Replace(backgroundWrappedScreen, "  ⏵⏵ bypass permissions on · PR #121\n", backgroundCommaFooter, 1), `1 shell, 1 monitor still running`},
		{"the comma list still running on one line (DJA-438)", strings.Replace(idleScreenNoBackground, "done 2:47 PM\n", "done 2:47 PM · 2 shells, 1 monitor still running\n", 1), `2 shells, 1 monitor still running`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newBackgroundFixture(t, c.screen, claudePattern(t))
			if got := f.settleProbe(t); got != "working" {
				t.Fatalf("still screen with a background task = %q, want working", got)
			}
			for i := 0; i < 3; i++ {
				if got := f.probe(""); got != "working" {
					t.Fatalf("probe %d after the first = %q, want working", i+2, got)
				}
			}
			want := "soho: warning: 'worker' is idle with a background task still running (\"" + c.snippet + "\"); waiting for its report"
			lines := f.noticeLines()
			if len(lines) != 1 || lines[0] != want {
				t.Fatalf("stderr notice lines = %q, want exactly [%q]\nstderr=%q", lines, want, f.stderr.String())
			}
			friction, err := os.ReadFile(f.friction)
			if err != nil || strings.Count(string(friction), "'worker' is idle with a background task still running (\""+c.snippet+"\"); waiting for its report") != 1 {
				t.Fatalf("friction log = %q err=%v, want the notice once", friction, err)
			}
		})
	}
}

func TestBackgroundTaskLeavesOtherScreensSettled(t *testing.T) {
	t.Run("a still screen without the counters settles as before", func(t *testing.T) {
		f := newBackgroundFixture(t, idleScreenNoBackground, claudePattern(t))
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("still screen without a background task = %q, want settled", got)
		}
		if lines := f.noticeLines(); len(lines) != 0 {
			t.Fatalf("notice for a screen without a background task: %q", lines)
		}
	})
	t.Run("the counters words in prose are not the status", func(t *testing.T) {
		screen := "● I ran 2 monitors and 1 shell in the test, they are still running fine.\n" + idleScreenNoBackground
		f := newBackgroundFixture(t, screen, claudePattern(t))
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("prose mentioning shells and monitors = %q, want settled", got)
		}
	})
	t.Run("still running outside the footer does not count (DJA-438)", func(t *testing.T) {
		for _, prose := range []string{
			"● the server is still running fine\n",
			"● I started 2 monitors\n  the server is still running fine\n",
			"● 1 shell, the server\n  is still running fine\n",
		} {
			f := newBackgroundFixture(t, prose+idleScreenNoBackground, claudePattern(t))
			if got := f.settleProbe(t); got != "settled" {
				t.Fatalf("prose %q = %q, want settled", prose, got)
			}
			if lines := f.noticeLines(); len(lines) != 0 {
				t.Fatalf("notice for prose %q: %q", prose, lines)
			}
		}
	})
	t.Run("the same text for a kind without a pattern (codex) settles", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, claudePattern(t))
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tcodex\timplementer\t\t\t\t\t\t\t\t\n"
		if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("codex with the claude pattern only = %q, want settled", got)
		}
		if lines := f.noticeLines(); len(lines) != 0 {
			t.Fatalf("notice for a kind without a pattern: %q", lines)
		}
	})
	t.Run("no value for the kind changes nothing", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, nil)
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("claude without background_patterns.claude = %q, want settled", got)
		}
	})
	t.Run("an empty value changes nothing", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, map[string]string{"background_patterns_claude": ""})
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("claude with an empty pattern = %q, want settled", got)
		}
	})
	t.Run("an invalid expression is ignored", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, map[string]string{"background_patterns_claude": `(shell`})
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("claude with an invalid pattern = %q, want settled", got)
		}
		if lines := f.noticeLines(); len(lines) != 0 {
			t.Fatalf("notice for an invalid pattern: %q", lines)
		}
	})
	t.Run("a counter above the last 15 non-empty lines does not count", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("✻ Cooked for 7m 37s · done 2:47 PM · 1 shell still running\n")
		for i := 0; i < 15; i++ {
			b.WriteString("  plain line without the counter\n")
		}
		f := newBackgroundFixture(t, b.String(), claudePattern(t))
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("counter 16 lines up = %q, want settled", got)
		}
	})
	t.Run("a counter on the 15th non-empty line from the end still counts", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("✻ Cooked for 7m 37s · done 2:47 PM · 1 shell still running\n")
		for i := 0; i < 14; i++ {
			b.WriteString("\n  plain line without the counter\n")
		}
		f := newBackgroundFixture(t, b.String(), claudePattern(t))
		if got := f.settleProbe(t); got != "working" {
			t.Fatalf("counter on the 15th non-empty line = %q, want working", got)
		}
	})
}

func TestBackgroundTaskJoinsWrappedLinesForAnyPattern(t *testing.T) {
	// A pattern the user configures for another kind also matches a line
	// the pane wraps in two (DJA-438).
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tcodex\timplementer\t\t\t\t\t\t\t\t\n"
	pattern := map[string]string{"background_patterns_codex": `build queue still busy`}
	t.Run("wrapped in two lines: working, the joined match quoted", func(t *testing.T) {
		f := newBackgroundFixture(t, "● Waiting.\n\n  build queue still\n  busy\n❯ \n", pattern)
		if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.settleProbe(t); got != "working" {
			t.Fatalf("codex pattern wrapped in two lines = %q, want working", got)
		}
		want := "soho: warning: 'worker' is idle with a background task still running (\"build queue still busy\"); waiting for its report"
		if lines := f.noticeLines(); len(lines) != 1 || lines[0] != want {
			t.Fatalf("notice lines = %q, want [%q]", lines, want)
		}
	})
	t.Run("the two halves not consecutive: settled", func(t *testing.T) {
		f := newBackgroundFixture(t, "● Waiting.\n\n  build queue still\n  done\n  busy\n❯ \n", pattern)
		if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.settleProbe(t); got != "settled" {
			t.Fatalf("halves apart = %q, want settled", got)
		}
	})
}

func TestBackgroundTaskEnvOverridesTheFileLayers(t *testing.T) {
	f := newBackgroundFixture(t, backgroundShellScreen, nil)
	f.env["SOHO_BACKGROUND_PATTERNS_CLAUDE"] = shippedClaudePattern(t)
	if got := f.settleProbe(t); got != "working" {
		t.Fatalf("pattern from the environment = %q, want working", got)
	}
}

// waitForRun runs WaitFor on the fixture and returns its exit code, stdout and
// the notice lines it printed.
func (f *backgroundFixture) waitForRun(t *testing.T, timeoutMs float64) (int, string) {
	t.Helper()
	var stdout bytes.Buffer
	oldOut := platform.Stdout
	platform.Stdout = &stdout
	t.Cleanup(func() { platform.Stdout = oldOut })
	code := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, timeoutMs, false, t.TempDir())
	platform.Stdout = oldOut
	return code, stdout.String()
}

func TestBackgroundTaskWaitEndToEnd(t *testing.T) {
	t.Run("the report appears after the background task: done, exit 0", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, claudePattern(t))
		// A real clock: the grace is over at once, so only the background
		// rule keeps the wait from ending settled-no-report.
		platform.Now = time.Now
		f.ctx.Entries["settled_grace"] = core.ConfigEntry{Value: "0"}
		report := filepath.Join(f.sd, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["HERDR_WORKSPACE_ID"] = "ws"
		done := make(chan struct{})
		go func() {
			defer close(done)
			// The report lands only once the wait has announced the
			// background task (read from the friction file, not from the
			// stderr buffer the wait writes to), so a slow runner cannot
			// end the wait done before the notice. A notice that never
			// comes still writes the report after 15 s, and the assertion
			// below fails.
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				if raw, err := os.ReadFile(f.friction); err == nil && strings.Contains(string(raw), "background task still running") {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			tmp := report + ".tmp"
			_ = os.WriteFile(tmp, []byte("# Report\n"), 0o600)
			_ = os.Rename(tmp, report)
		}()
		code, out := f.waitForRun(t, 20000)
		<-done
		if code != 0 || !strings.Contains(out, `"status":"done"`) || strings.Contains(out, "settled-no-report") {
			t.Fatalf("code=%d out=%q, want done / exit 0", code, out)
		}
		if lines := f.noticeLines(); len(lines) != 1 {
			t.Fatalf("notice lines over the whole wait = %q, want 1", lines)
		}
	})
	t.Run("without the pattern the same wait ends settled-no-report, exit 6", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, nil)
		platform.Now = time.Now
		f.ctx.Entries["settled_grace"] = core.ConfigEntry{Value: "0"}
		code, out := f.waitForRun(t, 20000)
		if code != 6 || !strings.Contains(out, `"status":"settled-no-report"`) {
			t.Fatalf("code=%d out=%q, want settled-no-report / exit 6", code, out)
		}
	})
	t.Run("each wait announces the background task once", func(t *testing.T) {
		f := newBackgroundFixture(t, backgroundShellScreen, claudePattern(t))
		platform.Now = time.Now
		f.ctx.Entries["settled_grace"] = core.ConfigEntry{Value: "0"}
		for i := 1; i <= 2; i++ {
			// Long enough for a loaded Windows runner to start the fake
			// herdr calls and reach the settled check at least once.
			code, out := f.waitForRun(t, 2000)
			if code == 0 || strings.Contains(out, "settled-no-report") {
				t.Fatalf("wait %d: code=%d out=%q, want a timeout, never a settled verdict", i, code, out)
			}
			if lines := f.noticeLines(); len(lines) != i {
				t.Fatalf("after wait %d the notice lines = %q, want %d", i, lines, i)
			}
		}
	})
}
