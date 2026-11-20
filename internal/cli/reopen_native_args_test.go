package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// reopenSetNativeRow writes one roster row with the given kind and native
// args so the test controls exactly what the replacement spawn would inherit.
func reopenSetNativeRow(t *testing.T, f *releaseFixture, kind, native string) {
	t.Helper()
	model := map[string]string{"grok": "grok-4.7", "claude": "opus", "codex": "gpt-5"}[kind]
	family := map[string]string{"grok": "xai", "claude": "anthropic", "codex": "openai"}[kind]
	row := strings.Join([]string{"worker", "p-worker", kind, "implementer", family, "1", f.cwd, "now", model, "ask", "implementer", "build", "", native, "high"}, "\t")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reopenHasArg(args []string, arg string) bool {
	for _, value := range args {
		if value == arg {
			return true
		}
	}
	return false
}

func TestReopenNativeArgsFollowTheKindSwap(t *testing.T) {
	const dropped = "soho: reopen: 'worker' was a grok worker; its native args are not passed to claude\n"
	for _, tc := range []struct {
		name, kind, native string
		args               []string
		wantNative         bool
		wantWarning        string
	}{
		{"different kind drops native args", "claude", "--flag val", []string{"--kind", "claude"}, false, dropped},
		{"same kind keeps native args", "grok", "--flag val", []string{"--kind", "grok"}, true, ""},
		{"no override keeps native args", "grok", "--flag val", nil, true, ""},
		{"different kind with empty native args", "claude", "", []string{"--kind", "claude"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReopenFixture(t, "idle")
			reopenAddReport(t, f)
			reopenSetNativeRow(t, f, "grok", tc.native)
			setReopenRules(t, f)
			f.env["SOHO_LAYOUT"] = "split"
			run := f.reopen(t, append([]string{"worker"}, tc.args...)...)
			if run.code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
			}
			argv := reopenStartArgv(run)
			if !tc.wantNative && (reopenHasArg(argv, "--flag") || reopenHasArg(argv, "val")) {
				t.Fatalf("the old kind's native args reached the new spawn: %#v", argv)
			}
			if tc.wantNative && !containsReopenArgPair(argv, "--flag", "val") {
				t.Fatalf("same-kind reopen lost its native args: %#v", argv)
			}
			if tc.wantWarning == "" {
				if strings.Contains(run.stderr, dropped) {
					t.Fatalf("stderr=%q, want no dropped-args line", run.stderr)
				}
			} else if got := strings.Count(run.stderr, tc.wantWarning); got != 1 {
				t.Fatalf("stderr=%q, want exactly one %q (got %d)", run.stderr, tc.wantWarning, got)
			}
			closed := false
			for _, call := range run.calls {
				if len(call.Argv) >= 3 && call.Argv[0] == "pane" && call.Argv[1] == "close" && call.Argv[2] == "p-worker" {
					closed = true
				}
			}
			if !closed {
				t.Fatalf("the old worker was not closed: %#v", run.calls)
			}
		})
	}
}

func TestReopenKindSwapDropsAResumeTokenBeforeTheSpawnSeesIt(t *testing.T) {
	f := newReopenFixture(t, "idle")
	reopenAddReport(t, f)
	reopenSetNativeRow(t, f, "claude", "-c")
	setReopenRules(t, f)
	f.env["SOHO_LAYOUT"] = "split"
	run := f.reopen(t, "worker", "--kind", "grok")
	if run.code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	argv := reopenStartArgv(run)
	if reopenHasArg(argv, "-c") {
		t.Fatalf("the claude resume token reached the grok spawn: %#v", argv)
	}
	want := "soho: reopen: 'worker' was a claude worker; its native args are not passed to grok\n"
	if strings.Count(run.stderr, want) != 1 {
		t.Fatalf("stderr=%q, want exactly one %q", run.stderr, want)
	}
	// Control: the token still refuses a same-kind reopen, so the swap did
	// not disable the spawn's resume-token guard.
	g := newReopenFixture(t, "idle")
	reopenAddReport(t, g)
	reopenSetNativeRow(t, g, "claude", "-c")
	setReopenRules(t, g)
	g.env["SOHO_LAYOUT"] = "split"
	before := g.rosterFile(t)
	control := g.reopen(t, "worker")
	if control.code == 0 || !strings.Contains(control.stderr, "would resume the last one") {
		t.Fatalf("control exit=%d stdout=%q stderr=%q", control.code, control.stdout, control.stderr)
	}
	if after := g.rosterFile(t); after != before {
		t.Fatalf("the refused control touched the roster\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func failingReopenFixture(t *testing.T, redispatch bool) *releaseFixture {
	t.Helper()
	f := newReopenFixture(t, "idle")
	if redispatch {
		reopenRecordBrief(t, f)
	} else {
		reopenAddReport(t, f)
	}
	reopenSetNativeRow(t, f, "grok", "--flag val")
	setReopenRulesWith(t, f, []fakecli.Rule{{
		Argv:       []string{"agent", "start"},
		ArgvPrefix: true,
		Code:       1,
		Stderr:     `{"error":{"code":"start_failed","message":"no capacity"}}`,
	}})
	f.env["SOHO_LAYOUT"] = "split"
	return f
}

func TestReopenFailedSpawnPrintsRecoveryCommands(t *testing.T) {
	for _, tc := range []struct {
		name       string
		redispatch bool
	}{
		{"without redispatch", false},
		{"with redispatch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := failingReopenFixture(t, tc.redispatch)
			args := []string{"worker"}
			if tc.redispatch {
				args = append(args, "--redispatch")
			}
			run := f.reopen(t, args...)
			if run.code == 0 {
				t.Fatalf("a failed spawn returned 0: stdout=%q stderr=%q", run.stdout, run.stderr)
			}
			want := "soho: reopen: to recover, run: soho spawn implementer --name worker --fresh --kind grok --cwd " + reopenShellQuote(f.cwd) + " --model grok-4.7 --effort high -- --flag val\n"
			if !strings.Contains(run.stderr, want) {
				t.Fatalf("stderr=%q, want the literal recovery line %q", run.stderr, want)
			}
			if !tc.redispatch {
				if !strings.Contains(run.stderr, "to recover, run:") || strings.Contains(run.stderr, "then run: soho dispatch") {
					t.Fatalf("stderr=%q, want only the spawn recovery line", run.stderr)
				}
			} else {
				dispatchPath := filepath.Join(f.state, "briefs", "worker-20261119T200000.md")
				dispatchWant := "soho: reopen: then run: soho dispatch worker " + reopenShellQuote(dispatchPath) + "\n"
				if strings.Count(run.stderr, dispatchWant) != 1 {
					t.Fatalf("stderr=%q, want exactly one %q", run.stderr, dispatchWant)
				}
				if body, err := os.ReadFile(dispatchPath); err != nil || !strings.Contains(string(body), "Redispatch this brief.") {
					t.Fatalf("the printed brief path is not usable: %q err=%v", body, err)
				}
				if strings.Index(run.stderr, want) > strings.Index(run.stderr, dispatchWant) {
					t.Fatalf("the dispatch line preceded the spawn line: %q", run.stderr)
				}
			}
			closed := false
			for _, call := range run.calls {
				if len(call.Argv) >= 3 && call.Argv[0] == "pane" && call.Argv[1] == "close" && call.Argv[2] == "p-worker" {
					closed = true
				}
			}
			if !closed {
				t.Fatalf("the recovery line printed without a close: %#v", run.calls)
			}
		})
	}
}

func TestReopenRecoveryLineQuotesHostileTokens(t *testing.T) {
	f := newReopenFixture(t, "idle")
	cwd := filepath.Join(filepath.Dir(f.cwd), "dir with spaces")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	reopenAddReport(t, f)
	row := strings.Join([]string{"worker", "p-worker", "grok", "implementer", "xai", "1", cwd, "now", "grok-4.7", "ask", "implementer", "build", "", "--flag a'b", "high"}, "\t")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setReopenRulesWith(t, f, []fakecli.Rule{{
		Argv:       []string{"agent", "start"},
		ArgvPrefix: true,
		Code:       1,
		Stderr:     `{"error":{"code":"start_failed","message":"no capacity"}}`,
	}})
	f.env["SOHO_LAYOUT"] = "split"
	run := f.reopen(t, "worker")
	if run.code == 0 {
		t.Fatalf("a failed spawn returned 0: stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	want := "soho: reopen: to recover, run: soho spawn implementer --name worker --fresh --kind grok --cwd '" + cwd + "' --model grok-4.7 --effort high -- --flag 'a'\\''b'\n"
	if !strings.Contains(run.stderr, want) {
		t.Fatalf("stderr=%q, want the shell-quoted line %q", run.stderr, want)
	}
}

func TestReopenShellQuoteHostileName(t *testing.T) {
	// Names with spaces or quotes are refused by the CLI's name validator
	// before reopen runs, so the quoting of that field is checked here.
	got := reopenRecoveryCommand("wo'rker space", []string{"implementer", "--fresh", "--kind", "claude", "--name", "wo'rker space"})
	want := "soho spawn implementer --name 'wo'\\''rker space' --fresh --kind claude"
	if got != want {
		t.Fatalf("recovery command=%q, want %q", got, want)
	}
}

func TestReopenRefusalBeforeClosePrintsNoRecoveryLine(t *testing.T) {
	f := newReopenFixture(t, "working")
	addReleaseReport(t, f.state, "worker", true)
	setReopenRules(t, f)
	run := f.reopen(t, "worker")
	if run.code == 0 || strings.Contains(run.stderr, "to recover, run:") || strings.Contains(run.stderr, "then run: soho dispatch") {
		t.Fatalf("refused before close: exit=%d stderr=%q", run.code, run.stderr)
	}
	if got, err := os.ReadFile(filepath.Join(f.state, "agents.tsv")); err != nil || !strings.Contains(string(got), "worker\tp-worker") {
		t.Fatalf("the refused reopen changed the roster: %q err=%v", got, err)
	}
}

func TestReopenDefaultsCommentNamesTheConcreteFallbackKind(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "skills", "soho", "config.defaults"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"empty names `--kind claude` (or `--kind codex` when the refused worker runs claude)",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("config.defaults fallback comment lacks %q", want)
		}
	}
}
