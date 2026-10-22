package spawn

// Issue 58: the idle-reuse warning must describe the retained session
// context without claiming any previous brief exists. The fixtures run the
// real CmdSpawn entry point for lane and non-lane reuse, a worker with no
// prior dispatch (fresh state) and a previously used settled worker (done,
// a finished report on disk). Each case fails when the historical
// "its session already holds earlier briefs" suffix is restored.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const retainedWarning = "; existing session context is retained"

// reuseWarn asserts the exact reuse warning line (prefix + message + the
// trailing newline) and that the historical brief claim is gone.
func reuseWarn(t *testing.T, stderr, want string) {
	t.Helper()
	line := "soho: warning: " + want + "\n"
	if !strings.Contains(stderr, line) {
		t.Fatalf("stderr=%q want line %q", stderr, line)
	}
	if strings.Contains(stderr, "its session already holds earlier briefs") {
		t.Fatalf("the historical brief claim came back: %q", stderr)
	}
}

// assertNoStart reports when a reuse opened a new pane (identity/capacity
// behavior changed).
func assertNoStart(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			t.Fatalf("reuse opened a new pane: %#v", calls)
		}
	}
}

// reuseLaneRow is a 15-column roster line for a review-lane worker on the
// codex kind; the model column is empty so a flagless spawn matches the
// session (no kind-mismatch) and the reuse decision is reached.
func reuseLaneRow(name, wdir string) string {
	return strings.Join([]string{name, "w0test:p0a", "codex", "reviewer", "openai", "1", wdir, "now", "", "auto", "reviewer", "review", "", "", "high"}, "\t")
}

// reuse8Row is an 8-column roster line left by historical writers.
// It cannot establish the opening effort and must not be reused.
func reuse8Row(name, role, wdir string) string {
	return strings.Join([]string{name, "p-" + name, "grok", role, "xai", "1", wdir, "now"}, "\t")
}

// reuseFullRow is a 15-column roster line for a cross-role non-lane reuse:
// the recorded model must equal the requested one and the recorded
// approvals must rank at least the requested ones (ask).
func reuseFullRow(name, role, wdir string) string {
	return strings.Join([]string{name, "p-" + name, "grok", role, "xai", "1", wdir, "now", "grok-4.7", "full", role, "", "", "", "high"}, "\t")
}

// finishedReport writes a non-empty report and its last-report pointer:
// the state of a previously used worker that settled (done) with a report.
func finishedReport(t *testing.T, state, name string) {
	t.Helper()
	sd := filepath.Join(state, "ws")
	report := filepath.Join(sd, "reports", name+"-final.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "last-report-"+name), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnIdleReuseWarningDescribesRetainedContext(t *testing.T) {
	t.Run("spawn: a lane reuse of an idle worker with no prior dispatch warns the retained context", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("review-2")})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, reuseLaneRow("review-2", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"name": "review-2"`) || !strings.Contains(stdout, `"reused": true`) ||
			!strings.Contains(stdout, `"previous_role": "reviewer"`) || !strings.Contains(stdout, `"status": "ready"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		reuseWarn(t, stderr, "reusing idle lane 'review' worker 'review-2' as reviewer"+retainedWarning)
		assertNoStart(t, calls)
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "review-2\tw0test:p0a") || !strings.Contains(string(roster), "\treviewer\treview\t\t") {
			t.Fatalf("roster=%q", roster)
		}
	})

	t.Run("spawn: a lane reuse of a settled (done) worker with a finished report warns the retained context", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"done"}}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, reuseLaneRow("review-2", f.cwd))
		finishedReport(t, f.state, "review-2")
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"name": "review-2"`) || !strings.Contains(stdout, `"reused": true`) ||
			!strings.Contains(stdout, `"previous_role": "reviewer"`) || !strings.Contains(stdout, `"status": "ready"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		reuseWarn(t, stderr, "reusing idle lane 'review' worker 'review-2' as reviewer"+retainedWarning)
		assertNoStart(t, calls)
	})

	t.Run("spawn: explicit ask does not reuse a worker without recorded effort", func(t *testing.T) {
		f := newSpawnFixture(t, append([]fakecli.Rule{idleHerdrRule("implementer")}, freshSpawnRules()...))
		configureSpawnFixture(t, &f)
		f.roster(t, reuse8Row("implementer", "implementer", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--approvals", "ask"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"created_pane": true`) {
			t.Fatalf("legacy worker was reused: %s calls=%#v", stdout, calls)
		}
	})

	t.Run("spawn: a non-lane reuse of a settled (done) worker retargets the role and warns the retained context", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "cross"}, Stdout: `{"result":{"agent":{"name":"cross","agent_status":"done"}}}`},
		})
		configureSpawnFixture(t, &f)
		f.roster(t, reuseFullRow("cross", "scouter", f.cwd))
		finishedReport(t, f.state, "cross")
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--model", "grok-4.7", "--effort", "high"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"name": "cross"`) || !strings.Contains(stdout, `"reused": true`) ||
			!strings.Contains(stdout, `"previous_role": "scouter"`) || !strings.Contains(stdout, `"status": "ready"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		reuseWarn(t, stderr, "reusing idle worker 'cross' (grok, was scouter, now implementer; dispatch to 'cross')"+retainedWarning)
		assertNoStart(t, calls)
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "cross\tp-cross\tgrok\timplementer") || !strings.Contains(string(roster), "scouter,implementer") {
			t.Fatalf("roster=%q", roster)
		}
	})
}

func TestSpawnReuseRequiresRecordedEffort(t *testing.T) {
	for _, tc := range []struct {
		name, effort, kind string
		legacy, reuse      bool
	}{
		{name: "different", effort: "high"},
		{name: "equal", effort: "medium", reuse: true},
		{name: "missing", legacy: true},
		{name: "codex different", effort: "high", kind: "codex"},
		{name: "codex equal", effort: "medium", kind: "codex", reuse: true},
		{name: "codex missing", kind: "codex", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, append([]fakecli.Rule{idleHerdrRule("reviewer")}, freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			kind, model := "grok", "grok-4.7"
			if tc.kind == "codex" {
				kind, model = "codex", "gpt-5.4"
				if _, err := fakecli.Install(t, f.bin, kind, nil); err != nil {
					t.Fatal(err)
				}
			}
			f.env["SOHO_MULTI_ROLE"] = "on"
			f.env["SOHO_ROLE_IMPLEMENTER_EFFORT"] = "medium"
			row := strings.Split(reuseFullRow("reviewer", "reviewer", f.cwd), "\t")
			row[2], row[8] = kind, model
			row[14] = tc.effort
			if tc.legacy {
				row = row[:14]
			}
			f.roster(t, strings.Join(row, "\t"))
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"implementer", "--kind", kind, "--model", model})
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if tc.reuse {
				if !strings.Contains(stdout, `"name": "reviewer"`) || !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"effort": "medium"`) {
					t.Fatalf("reuse JSON=%s", stdout)
				}
				reuseWarn(t, stderr, "reusing idle worker 'reviewer' ("+kind+", was reviewer, now implementer; dispatch to 'reviewer')"+retainedWarning)
				assertNoStart(t, calls)
			} else {
				if strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"created_pane": true`) || !strings.Contains(stdout, `"effort": "medium"`) {
					t.Fatalf("expected fresh medium worker: %s", stdout)
				}
				started := false
				for _, call := range calls {
					if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
						started = true
					}
				}
				if !started {
					t.Fatalf("no agent start: %#v", calls)
				}
			}
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "rename" {
					t.Fatalf("renamed worker: %#v", call)
				}
			}
		})
	}
}
