package cli

// DJA-399: the compact_timeout key — read by `soho compact` without
// --timeout and by the dispatch's compact phase in place of the constant,
// the --timeout flag still winning, and the dispatch's compact-phase timeout
// (exit 9) recorded in the friction log with the effective limit.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// compactTimeoutRules is the hermetic codex compact that never compacts:
// the screen keeps the /compact echo with no proof, so the wait runs to the
// deadline (the F11 timeout rules).
func compactTimeoutRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactPhaseIdleJSON},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "> /compact\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}` + "}"},
	}
}

// compactTimeoutRun drives a plain `soho compact` through the fixture.
func compactTimeoutRun(t *testing.T, f *compactPhaseDispatchFixture, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"compact"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

// compactTimeoutReads counts the fixture's screen reads: one before the
// send, one per deadline poll, one after the deadline.
func compactTimeoutReads(t *testing.T, f *compactPhaseDispatchFixture) int {
	t.Helper()
	return countArgvPrefix(f.calls(t), compactReadArgv("worker"))
}

func TestCompactCommandReadsCompactTimeoutKey(t *testing.T) {
	// The pinned clock advances 30 s per read, so the 90 s cap runs out on
	// the third poll and the 300000 ms default on the tenth: the number of
	// screen reads is the deadline the phase actually got.
	t.Run("compact_timeout=90s reaches cmdCompact as 90000 ms", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 30*time.Second)
		f.install(t, compactTimeoutRules())
		f.env["SOHO_COMPACT_TIMEOUT"] = "90s"
		code, out, errText := compactTimeoutRun(t, f, "worker")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) {
			t.Fatalf("code=%d out=%q err=%q; want the 9 timeout with its JSON", code, out, errText)
		}
		if got := compactTimeoutReads(t, f); got != 5 { // 1 before + 3 polls + 1 after
			t.Fatalf("screen reads = %d; want 5 (the 90 s deadline): %#v", got, f.calls(t))
		}
	})
	t.Run("without the key the 300000 ms default runs", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 30*time.Second)
		f.install(t, compactTimeoutRules())
		code, out, errText := compactTimeoutRun(t, f, "worker")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) {
			t.Fatalf("code=%d out=%q err=%q; want the 9 timeout with its JSON", code, out, errText)
		}
		if got := compactTimeoutReads(t, f); got != 12 { // 1 before + 10 polls + 1 after
			t.Fatalf("screen reads = %d; want 12 (the 300 s deadline): %#v", got, f.calls(t))
		}
	})
	t.Run("the --timeout flag beats the key", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 30*time.Second)
		f.install(t, compactTimeoutRules())
		f.env["SOHO_COMPACT_TIMEOUT"] = "90s"
		code, out, errText := compactTimeoutRun(t, f, "worker", "--timeout", "300s")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) {
			t.Fatalf("code=%d out=%q err=%q; want the 9 timeout with its JSON", code, out, errText)
		}
		if got := compactTimeoutReads(t, f); got != 12 { // the flag's 300 s wins over the key's 90 s
			t.Fatalf("screen reads = %d; want 12 (the flag's 300 s deadline): %#v", got, f.calls(t))
		}
	})
}

func TestDispatchCompactTimeoutFrictionLine(t *testing.T) {
	// A timeout in the phase (exit 9) keeps today's stderr line and adds the
	// friction line with the effective limit, in the numberSeconds form. The
	// pinned clock advances 30 s per read, as in the cmdCompact test, so the
	// number of screen reads is the deadline the dispatch's phase actually
	// got: 5 for 90 s, 12 for the 300 s default (the old constant).
	t.Run("the timeout exit 9 records the friction line with the effective ms", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 30*time.Second)
		f.install(t, compactTimeoutRules())
		f.env["SOHO_COMPACT_TIMEOUT"] = "90s"
		code, out, errText := f.run(t, "worker", f.brief, "--compact", "--no-wait")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) {
			t.Fatalf("code=%d out=%q err=%q; want the 9 timeout", code, out, errText)
		}
		if got := compactTimeoutReads(t, f); got != 5 { // 1 before + 3 polls + 1 after
			t.Fatalf("screen reads = %d; want 5 (the configured 90 s deadline, not 300 s): %#v", got, f.calls(t))
		}
		if !strings.Contains(errText, "soho: dispatch: the compact step of 'worker' did not finish (exit 9); the brief was not sent") {
			t.Fatalf("stderr=%q; want today's did-not-finish line", errText)
		}
		log, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
		if err != nil {
			t.Fatalf("the friction log is missing: %v", err)
		}
		want := "warning\tdispatch\t" +
			"dispatch: compact of 'worker' timed out after 90s (compact_timeout=90000); the brief was not sent"
		if !strings.Contains(string(log), want) {
			t.Fatalf("friction.log lacks the timeout line:\n%s\nwant a line with %q", log, want)
		}
	})
	t.Run("the default limit records 300s and 300000", func(t *testing.T) {
		f := newCompactPhaseDispatchFixture(t, "", 30*time.Second)
		f.install(t, compactTimeoutRules())
		code, out, errText := f.run(t, "worker", f.brief, "--compact", "--no-wait")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) {
			t.Fatalf("code=%d out=%q err=%q; want the 9 timeout", code, out, errText)
		}
		if got := compactTimeoutReads(t, f); got != 12 { // 1 before + 10 polls + 1 after
			t.Fatalf("screen reads = %d; want 12 (the 300 s default deadline): %#v", got, f.calls(t))
		}
		log, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
		if err != nil {
			t.Fatalf("the friction log is missing: %v", err)
		}
		want := "warning\tdispatch\t" +
			"dispatch: compact of 'worker' timed out after 300s (compact_timeout=300000); the brief was not sent"
		if !strings.Contains(string(log), want) {
			t.Fatalf("friction.log lacks the timeout line:\n%s\nwant a line with %q", log, want)
		}
	})
}

func TestConfigSetCompactTimeout(t *testing.T) {
	env, cwd := commandFixture(t)
	seed := "max_workers=3\n"
	conf := filepath.Join(cwd, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		b, err := os.ReadFile(conf)
		if err != nil {
			t.Fatalf("read the config file: %v", err)
		}
		return string(b)
	}
	// Invalid or <= 0 values are refused with exit 2, file untouched — the
	// invalid-value refusal, not the unknown-key one, once the key exists.
	for _, raw := range []string{"abc", "0", "-1"} {
		code, out, errOut := runIn(t, []string{"config", "set", "compact_timeout", raw}, env, cwd)
		if code != 2 {
			t.Fatalf("config set compact_timeout %s code=%d; want 2 (out=%q err=%q)", raw, code, out, errOut)
		}
		if !strings.Contains(errOut, "config set: invalid value") {
			t.Fatalf("config set compact_timeout %s err=%q; want the invalid-value refusal", raw, errOut)
		}
		if got := read(); got != seed {
			t.Fatalf("the file changed after a refused set: %q", got)
		}
	}
	// The accepted grammar: plain milliseconds and the unit forms.
	for _, raw := range []string{"600000", "10m"} {
		code, out, errOut := runIn(t, []string{"config", "set", "compact_timeout", raw}, env, cwd)
		if code != 0 {
			t.Fatalf("config set compact_timeout %s code=%d out=%q err=%q; want 0", raw, code, out, errOut)
		}
		got := read()
		if !strings.Contains(got, "compact_timeout="+raw+"\n") || !strings.Contains(got, "max_workers=3\n") {
			t.Fatalf("the file lacks the set pair (or lost the seed): %q", got)
		}
	}
}
