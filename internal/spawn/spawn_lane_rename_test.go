package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/wait"
)

// laneReuseRow is a 14-column roster line for an idle review-lane worker on
// the codex kind; the model column is empty so a flagless spawn matches the
// session (no kind-mismatch) and the reuse path is reached.
func laneReuseRow(name, cwd string) string {
	return strings.Join([]string{name, "w0test:p0a", "codex", "reviewer", "openai", "1", cwd, "now", "", "auto", "reviewer", "review", "", ""}, "\t")
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

func TestSpawnLaneReuseRenameJavaScriptCases(t *testing.T) {
	t.Run("spawn: a lane reuse with --name renames the idle worker and reports the new name", func(t *testing.T) {
		// A4.1: lanes on, reuse_workers=on, idle 'review-2' on the review lane,
		// spawn reviewer --name review -> herdr agent rename, roster renamed,
		// JSON name review, the renamed warning.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
			{Argv: []string{"agent", "rename", "w0test:p0a", "review"}, Stdout: `{"result":{}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer", "--name", "review"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		var renames []fakecli.Call
		for _, call := range calls {
			if len(call.Argv) >= 4 && call.Argv[0] == "agent" && call.Argv[1] == "rename" {
				renames = append(renames, call)
			}
		}
		if len(renames) != 1 || strings.Join(renames[0].Argv, " ") != "agent rename w0test:p0a review" {
			t.Fatalf("rename calls=%#v", renames)
		}
		if !strings.Contains(stdout, `"name": "review"`) || !strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("JSON=%q", stdout)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "review\tw0test:p0a") || strings.Contains(string(roster), "review-2") {
			t.Fatalf("roster=%q", roster)
		}
		want := "soho: warning: reusing idle lane 'review' worker 'review-2' as reviewer, renamed to 'review'; existing session context is retained"
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr=%q want %q", stderr, want)
		}
	})

	t.Run("spawn: the inverse pair — idle 'review', --name review-2", func(t *testing.T) {
		// A4.1, the friction.log:407 pair: the lane worker is named review and
		// the requested name is review-2.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review"}, Stdout: `{"result":{"agent":{"name":"review","agent_status":"idle"}}}`},
			{Argv: []string{"agent", "rename", "w0test:p0a", "review-2"}, Stdout: `{"result":{}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review", f.cwd))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer", "--name", "review-2"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renameCalls(calls) != 1 {
			t.Fatalf("rename calls=%#v", calls)
		}
		if !strings.Contains(stdout, `"name": "review-2"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "review-2\tw0test:p0a") || strings.Contains(string(roster), "review\tw0test:p0a") {
			t.Fatalf("roster=%q", roster)
		}
		want := "soho: warning: reusing idle lane 'review' worker 'review' as reviewer, renamed to 'review-2'; existing session context is retained"
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr=%q want %q", stderr, want)
		}
	})

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
		if !strings.Contains(stdout, `"name": "review-2"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		want := "soho: warning: reusing idle lane 'review' worker 'review-2' as reviewer; existing session context is retained"
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr=%q want %q", stderr, want)
		}
	})

	t.Run("spawn: a failing rename dies 4 and leaves the roster untouched", func(t *testing.T) {
		// A4.1: herdr agent rename status != 0 -> exit 4, the cause in the
		// message, the roster line of the old name intact.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
			{Argv: []string{"agent", "rename", "w0test:p0a", "review"}, Stderr: `{"error":{"code":"agent_name_taken","message":"an agent named review already exists"}}`, Code: 1},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		code, message, _ := runCmdSpawnExpectError(t, f, []string{"reviewer", "--name", "review"})
		want := "spawn: could not rename worker 'review-2' to 'review': agent_name_taken: an agent named review already exists"
		if code != 4 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "review-2\tw0test:p0a") || strings.Contains(string(roster), "review\tw0test:p0a") {
			t.Fatalf("roster changed after the failed rename: %q", roster)
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
		// line (15 columns) after the rename; the validation at the top of
		// CmdSpawn refuses it before any herdr call or roster write.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"reviewer", "--name", "rev\tiew"})
		want := "invalid agent name 'rev\tiew' (must match [a-z][a-z0-9_-]{0,31})"
		if code != 2 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
		if renameCalls(calls) != 0 {
			t.Fatalf("herdr agent rename was called for a tab name: %#v", calls)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(roster), "\n"), "\n") {
			if len(strings.Split(line, "\t")) > 15 {
				t.Fatalf("roster line gained columns from the tab: %q", line)
			}
		}
		if !strings.Contains(string(roster), "review-2\tw0test:p0a") {
			t.Fatalf("roster changed for a tab name: %q", roster)
		}
	})

	t.Run("spawn: a successful rename moves the state files to the new name", func(t *testing.T) {
		// R2-P2: after the rename, the per-agent state files follow the new
		// name, and status under the new name shows the earlier report.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
			{Argv: []string{"agent", "rename", "w0test:p0a", "review"}, Stdout: `{"result":{}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		sd := filepath.Join(f.state, "ws")
		report := filepath.Join(sd, "reports", "review-2-old.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		for file, content := range map[string]string{
			"last-report-review-2":                   report + "\n",
			"task-review-2":                          "ship the fix\n",
			"task-report-review-2.json":              `{"version":1,"task_report":"reports/review-2-old.md","current":"reports/review-2-old.md","history":[]}` + "\n",
			filepath.Join("wait", "review-2.queued"): "1 123 brief.md\n",
		} {
			if err := os.WriteFile(filepath.Join(sd, file), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"reviewer", "--name", "review"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renameCalls(calls) != 1 {
			t.Fatalf("rename calls=%#v", calls)
		}
		if !strings.Contains(stdout, `"name": "review"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if strings.Contains(stderr, "could not move") {
			t.Fatalf("unexpected move warning: %q", stderr)
		}
		for _, file := range []string{"last-report-review", "task-review", "task-report-review.json", filepath.Join("wait", "review.queued")} {
			data, err := os.ReadFile(filepath.Join(sd, file))
			if err != nil {
				t.Fatalf("state file did not move to the new name: %s (%v)", file, err)
			}
			if strings.TrimSpace(string(data)) == "" {
				t.Fatalf("moved file %s is empty", file)
			}
		}
		if got, _ := os.ReadFile(filepath.Join(sd, "last-report-review")); strings.TrimSpace(string(got)) != report {
			t.Fatalf("moved pointer=%q want %q", got, report)
		}
		if got, _ := os.ReadFile(filepath.Join(sd, "task-review")); strings.TrimSpace(string(got)) != "ship the fix" {
			t.Fatalf("moved task file=%q", got)
		}
		for _, file := range []string{"last-report-review-2", "task-review-2", "task-report-review-2.json", filepath.Join("wait", "review-2.queued")} {
			if _, err := os.Stat(filepath.Join(sd, file)); !os.IsNotExist(err) {
				t.Fatalf("old state file %s is still there (err=%v)", file, err)
			}
		}
		if got := core.LastReport(sd, "review"); got != report {
			t.Fatalf("LastReport(review)=%q want %q", got, report)
		}
		if got := core.LastReport(sd, "review-2"); got != "" {
			t.Fatalf("LastReport(review-2)=%q want empty", got)
		}
		// status under the new name shows the report that the old name held.
		oldOut := platform.Stdout
		var out strings.Builder
		platform.Stdout = &out
		rc := wait.CmdStatus([]string{"review"}, f.ctx, f.env, f.cwd)
		platform.Stdout = oldOut
		if rc != 0 {
			t.Fatalf("status rc=%d out=%q", rc, out.String())
		}
		if !strings.Contains(out.String(), "review\tdone\t"+report) {
			t.Fatalf("status output=%q want 'review\tdone\t<report>'", out.String())
		}
	})

	t.Run("spawn: a state file that cannot move becomes a warning and the spawn continues", func(t *testing.T) {
		// R2-P2, the failure branch: a rename that fails warns
		// "spawn: could not move '<file>' to the name '<X>'" and the spawn
		// still finishes with the new name; the other files still move.
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "review-2"}, Stdout: `{"result":{"agent":{"name":"review-2","agent_status":"idle"}}}`},
			{Argv: []string{"agent", "rename", "w0test:p0a", "review"}, Stdout: `{"result":{}}`},
		})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.roster(t, laneReuseRow("review-2", f.cwd))
		sd := filepath.Join(f.state, "ws")
		// a directory where the pointer file should land blocks os.Rename.
		if err := os.MkdirAll(filepath.Join(sd, "last-report-review"), 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(sd, "reports", "x.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "last-report-review-2"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "task-review-2"), []byte("t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"reviewer", "--name", "review"})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, `"name": "review"`) {
			t.Fatalf("JSON=%q", stdout)
		}
		if !strings.Contains(stderr, "spawn: could not move 'last-report-review-2' to the name 'review':") {
			t.Fatalf("stderr=%q", stderr)
		}
		if _, err := os.Stat(filepath.Join(sd, "task-review")); err != nil {
			t.Fatalf("the task file did not move after the pointer failure: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sd, "last-report-review-2")); err != nil {
			t.Fatalf("the old pointer was removed without landing: %v", err)
		}
	})
}
