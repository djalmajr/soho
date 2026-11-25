package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// laneReuseRow is a roster line for an idle review-lane worker on
// the codex kind; the model column is empty so a flagless spawn matches the
// session (no kind-mismatch) and the reuse path is reached.
func laneReuseRow(name, cwd string) string {
	return strings.Join([]string{name, "w0test:p0a", "codex", "reviewer", "openai", "1", cwd, "now", "", "auto", "reviewer", "review", "", "", "high", "none"}, "\t")
}

func renameCalls(calls []fakecli.Call) int {
	n := 0
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "rename" {
			n++
		}
	}
	return n
}

func TestSpawnLaneReuseNameValidation(t *testing.T) {
	t.Run("spawn: --name equal to the idle worker reuses without a rename", func(t *testing.T) {
		// A4.1: the pre-existing reuse behavior when the name already matches.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer", "--name", "review-2"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renameCalls(calls) != 0 {
			t.Fatalf("unexpected rename calls: %#v", calls)
		}
		if !spawnJSONHasField(t, stdout, `"name": "review-2"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		want := "soho: warning: reusing idle lane 'reviewers' worker 'review-2' as reviewer; existing session context is retained"
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr=%q want %q", stderr, want)
		}
	})

	t.Run("spawn: an invalid --name on a lane reuse dies 2 without renaming", func(t *testing.T) {
		// R2-P1: the reuse branch used to rename without the invalid-agent-name
		// check; the validation now runs at the top of CmdSpawn, before any
		// decision, with the same message and exit 2 as the new-pane path.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"reviewer", "--name", "Review"})
		want := "invalid agent name 'Review' (must match [a-z][a-z0-9_-]{0,31})"
		if code != 2 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
		if renameCalls(calls) != 0 {
			t.Fatalf("herdr agent rename was called for an invalid name: %#v", calls)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "review-2\tw0test:p0a") || strings.Contains(string(roster), "Review\t") {
			t.Fatalf("roster touched for an invalid name: %q", roster)
		}
	})

	t.Run("spawn: a --name with a tab on a lane reuse dies 2 without renaming", func(t *testing.T) {
		// R2-P1, the tab variant: a tab in the name used to corrupt the roster
		// line after the rename; the validation at the top of
		// CmdSpawn refuses it before any herdr call or roster write.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		before := rosterBytes(t, f)
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"reviewer", "--name", "rev\tiew"})
		want := "invalid agent name 'rev\tiew' (must match [a-z][a-z0-9_-]{0,31})"
		if code != 2 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
		if renameCalls(calls) != 0 {
			t.Fatalf("herdr agent rename was called for a tab name: %#v", calls)
		}
		if after := rosterBytes(t, f); after != before {
			t.Fatalf("roster changed for a tab name: %q", after)
		}
	})
}

func TestSpawnLaneNewNameRefusalPreservesTaskState(t *testing.T) {
	for _, tc := range []struct {
		label, worker, requested, state, reportBody string
	}{
		{"idle worker with a report", "review-2", "review", "idle", "# done\n"},
		{"inverse names with a report", "review", "review-2", "idle", "# done\n"},
		{"idle worker with a pending report", "review-2", "review", "idle", ""},
		{"working session with a pending report", "review-2", "review", "working", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			agent := fmt.Sprintf(`{"name":%q,"pane_id":"w0test:p0a","agent_status":%q}`, tc.worker, tc.state)
			f := newSpawnFixture(t, []fakecli.Rule{
				{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + agent + `]}}`},
				{Argv: []string{"agent", "get", tc.worker}, Stdout: `{"result":{"agent":` + agent + `}}`},
				{Argv: []string{"agent", "rename", "w0test:p0a", tc.requested}, Stdout: `{"result":{}}`},
			})
			configureSpawnFixture(t, &f)
			f.env["SOHO_LANES"] = "on"
			f.env["SOHO_PANES"] = "4"
			f.env["SOHO_PANE_MODE"] = "strict"
			f.roster(t, laneReuseRow(tc.worker, f.cwd))

			sd := filepath.Join(f.state, "ws")
			report := filepath.Join(sd, "reports", tc.worker+".md")
			before := map[string]string{filepath.Join(sd, "agents.tsv"): rosterBytes(t, f)}
			files := map[string]string{report: tc.reportBody}
			for _, name := range []string{tc.worker, tc.requested} {
				files[filepath.Join(sd, "task-"+name)] = "task for " + name + "\n"
				files[filepath.Join(sd, "wait", name+".queued")] = "1 123 " + name + ".md\n"
				pointerReport := report
				if name == tc.requested {
					pointerReport = filepath.Join(sd, "reports", name+".md")
				}
				files[core.LastReportPath(sd, name)] = pointerReport + "\n"
				files[taskreport.TaskReportPointerPath(sd, name)] = fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[]}`, pointerReport, pointerReport) + "\n"
			}
			for path, content := range files {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				before[path] = content
			}

			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer", "--name", tc.requested})
			if code != 10 || !spawnJSONHasField(t, stdout, `"status": "busy"`) || !spawnJSONHasField(t, stdout, fmt.Sprintf(`"name": %q`, tc.worker)) {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want busy on %s", code, stdout, stderr, tc.worker)
			}
			if renameCalls(calls) != 0 || len(herdrActs(calls)) != 0 ||
				dja659AgentCalls(calls, "send-keys") != 0 || dja659AgentCalls(calls, "prompt") != 0 {
				t.Fatalf("refusal touched the live worker: %#v", calls)
			}
			for path, want := range before {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("%s changed: got %q want %q, err=%v", path, got, want, err)
				}
			}
			if hint := core.RosterRenameHint(sd, tc.worker); hint != "" {
				t.Fatalf("unexpected rename history: %q", hint)
			}
		})
	}
}
