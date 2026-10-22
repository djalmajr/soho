package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type releaseRun struct {
	code       int
	stdout     string
	stderr     string
	calls      []fakecli.Call
	state      string
	configFile string
}

type releaseFixture struct {
	env        platform.Env
	cwd        string
	state      string
	fakeDir    string
	configFile string
}

func newReleaseFixture(t *testing.T, agentStatus string, agentError string) *releaseFixture {
	t.Helper()
	env, cwd := commandFixture(t)
	env["HERDR_ENV"] = "1"
	env["SOHO_REGRID"] = "off"
	state := filepath.Join(env.Get("SOHO_DIR"), "ws")
	if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{}
	if agentError != "" {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stderr: agentError, Code: 1})
	} else if agentStatus != "" {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"` + agentStatus + `"}}}`})
	}
	rules = append(rules,
		fakecli.Rule{Argv: []string{"pane", "close", "p-worker"}},
		fakecli.Rule{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		fakecli.Rule{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		fakecli.Rule{Argv: []string{"tab", "get", "t1"}, Stdout: `{"result":{"tab":{"label":"old"}}}`},
		fakecli.Rule{Argv: []string{"tab", "rename", "t1", "herd"}},
	)
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env["HERDR_ENV"] = "1"
	return &releaseFixture{env: env, cwd: cwd, state: state, fakeDir: fakeDir, configFile: filepath.Join(fakeDir, "herdr.json")}
}

func (f *releaseFixture) run(t *testing.T, args ...string) releaseRun {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(append([]string{"release"}, args...), f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	calls, err := fakecli.ReadCallsForConfig(f.configFile)
	if os.IsNotExist(err) {
		calls = []fakecli.Call{}
	} else if err != nil {
		t.Fatal(err)
	}
	return releaseRun{code, out.String(), stderr.String(), calls, f.state, f.configFile}
}

func releaseRow(name, pane, created, role string, burst bool, wdir string) string {
	fields := []string{name, pane, "grok", role, "xai", created, wdir, "now", "grok-4.7", "full", role, "build"}
	if burst {
		fields = append(fields, "burst")
	}
	return strings.Join(fields, "\t")
}

func addReleaseReport(t *testing.T, state string, agent string, pointerOnly bool) {
	t.Helper()
	report := filepath.Join(state, "reports", agent+".md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if !pointerOnly {
		if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(core.LastReportPath(state, agent), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func callsTo(calls []fakecli.Call, args ...string) []int {
	indexes := []int{}
	for i, call := range calls {
		if strings.Join(call.Argv, "\x00") == strings.Join(args, "\x00") {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func TestReleaseWorkingBurstReportGuard(t *testing.T) { // JS: "release: a working burst worker with a pending report dies 3"
	// Mutation captured: allowing release to close a working worker without its report destroys uncollected work.
	f := newReleaseFixture(t, "working", "")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", true, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addReleaseReport(t, f.state, "worker", true)
	r := f.run(t, "worker")
	if r.code != 3 || !strings.Contains(r.stderr, "closing now discards its work") || len(callsTo(r.calls, "pane", "close", "p-worker")) != 0 || core.RosterLine(f.state, "worker") == "" {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v roster=%q", r.code, r.stdout, r.stderr, r.calls, core.RosterLine(f.state, "worker"))
	}
}

func TestReleaseUnavailableNeedsForce(t *testing.T) { // JS: "release: an unqueryable worker is preserved unless --force"
	// Mutation captured: ignoring an unavailable Herdr state drops a worker whose liveness is unknown.
	f := newReleaseFixture(t, "", `{"error":{"code":"permission_denied","message":"not allowed"}}`)
	addReleaseReport(t, f.state, "worker", true)
	r := f.run(t, "worker")
	log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
	if err != nil || r.code != 4 || core.RosterLine(f.state, "worker") == "" || !strings.Contains(string(log), "error(exit 4)\trelease\t") || strings.Count(string(log), "error(exit") != 1 {
		t.Fatalf("release status=%d stderr=%q friction=%q err=%v roster=%q", r.code, r.stderr, log, err, core.RosterLine(f.state, "worker"))
	}
}

func TestReleaseMissingAgentDoesNotRecordFriction(t *testing.T) { // Mutation captured: routing plain die errors through friction logging records a bash usage error.
	f := newReleaseFixture(t, "", "")
	r := f.run(t)
	if r.code != 1 || r.stderr != "soho: agent: Parameter not set\n" {
		t.Fatalf("release status=%d stderr=%q", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(f.state, "friction.log")); !os.IsNotExist(err) {
		t.Fatalf("plain die created friction.log: %v", err)
	}
}

func TestReleaseForceBypassesUnavailableGuard(t *testing.T) { // JS: "release --force bypasses the unavailable worker guard"
	// Mutation captured: applying the unavailable-state refusal even with --force blocks the explicit override.
	f := newReleaseFixture(t, "", `{"error":{"code":"permission_denied","message":"not allowed"}}`)
	addReleaseReport(t, f.state, "worker", true)
	r := f.run(t, "worker", "--close", "--force")
	if r.code != 0 || core.RosterLine(f.state, "worker") != "" || len(callsTo(r.calls, "agent", "get", "worker")) != 0 || len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 || !strings.Contains(r.stdout, "released worker") {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v roster=%q", r.code, r.stdout, r.stderr, r.calls, core.RosterLine(f.state, "worker"))
	}
}

func TestReleaseNeverClosesExternalPane(t *testing.T) { // release --close refuses an operator-owned pane without losing its roster row.
	// Mutation captured: closing regardless of created_pane shuts down an operator-owned pane.
	f := newReleaseFixture(t, "", "")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "0", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addReleaseReport(t, f.state, "worker", false)
	r := f.run(t, "worker", "--close")
	if r.code != 4 || len(callsTo(r.calls, "pane", "close", "p-worker")) != 0 || !strings.Contains(r.stderr, "pane p-worker was not created by this skill; not closing it") || !strings.Contains(r.stderr, "soho release worker\n") || core.RosterLine(f.state, "worker") == "" || strings.Contains(r.stdout, "released worker") {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v roster=%q", r.code, r.stdout, r.stderr, r.calls, core.RosterLine(f.state, "worker"))
	}
}

func TestReleaseCloseFailureKeepsRosterAndReport(t *testing.T) {
	for _, name := range []string{"build", "build-6", "build-7", "review", "review-5"} {
		t.Run(name, func(t *testing.T) {
			f := newReleaseFixture(t, "", "")
			if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+releaseRow(name, "p-worker", "1", "implementer", false, f.cwd)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			addReleaseReport(t, f.state, name, false)
			dir := t.TempDir()
			if _, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"pane", "close", "p-worker"}, Code: 1, Stderr: "close refused"}}); err != nil {
				t.Fatal(err)
			}
			f.env = withFakeCLI(f.env, dir)
			f.configFile = filepath.Join(dir, "herdr.json")
			r := f.run(t, name, "--close")
			if r.code != 4 || strings.Contains(r.stdout, "released ") || !strings.Contains(r.stderr, "could not close the pane p-worker") || core.RosterLine(f.state, name) == "" {
				t.Fatalf("status=%d stdout=%q stderr=%q roster=%q", r.code, r.stdout, r.stderr, core.RosterLine(f.state, name))
			}
			if _, err := os.Stat(core.LastReportPath(f.state, name)); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != 2 || len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 || len(callsTo(r.calls, "pane", "get", "p-worker")) != 1 {
				t.Fatalf("effects after failed close: %#v", r.calls)
			}
			// A retry uses the retained registration and the same pane, then succeeds.
			f.env = withFakeCLI(f.env, f.fakeDir)
			f.configFile = filepath.Join(f.fakeDir, "herdr.json")
			r = f.run(t, name, "--close")
			if r.code != 0 || !strings.Contains(r.stdout, "released "+name+"\n") || core.RosterLine(f.state, name) != "" || len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 {
				t.Fatalf("retry status=%d stdout=%q stderr=%q roster=%q calls=%#v", r.code, r.stdout, r.stderr, core.RosterLine(f.state, name), r.calls)
			}
		})
	}
}

func TestReleaseCloseFailedQuery(t *testing.T) {
	for _, tc := range []struct {
		name, response  string
		queryCode, want int
	}{
		{"gone", `{"error":{"code":"pane_not_found","message":"gone"}}`, 1, 0},
		{"present", `{"result":{"pane":{"id":"p-worker"}}}`, 0, 4},
		{"permission", `{"error":{"code":"permission_denied","message":"denied"}}`, 1, 4},
		{"malformed", `invalid`, 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFixture(t, "", "")
			addReleaseReport(t, f.state, "worker", false)
			dir := t.TempDir()
			if _, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
				{Argv: []string{"pane", "close", "p-worker"}, Code: 1},
				{Argv: []string{"pane", "get", "p-worker"}, Code: tc.queryCode, Stdout: tc.response},
				{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
			}); err != nil {
				t.Fatal(err)
			}
			f.env = withFakeCLI(f.env, dir)
			f.configFile = filepath.Join(dir, "herdr.json")
			r := f.run(t, "worker", "--close")
			if r.code != tc.want || (core.RosterLine(f.state, "worker") == "") != (tc.want == 0) || strings.Contains(r.stdout, "released worker") != (tc.want == 0) {
				t.Fatalf("status=%d stdout=%q stderr=%q roster=%q", r.code, r.stdout, r.stderr, core.RosterLine(f.state, "worker"))
			}
			closed, queried := callsTo(r.calls, "pane", "close", "p-worker"), callsTo(r.calls, "pane", "get", "p-worker")
			if len(closed) != 1 || len(queried) != 1 || closed[0] >= queried[0] {
				t.Fatalf("close/query order: %#v", r.calls)
			}
		})
	}
}

func TestReleaseBurstCloseFailureWithoutFlagKeepsBehavior(t *testing.T) {
	f := newReleaseFixture(t, "", "")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", true, f.cwd)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addReleaseReport(t, f.state, "worker", false)
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "close", "p-worker"}, Code: 1},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	}); err != nil {
		t.Fatal(err)
	}
	f.env = withFakeCLI(f.env, dir)
	f.configFile = filepath.Join(dir, "herdr.json")
	r := f.run(t, "worker")
	if r.code != 0 || !strings.Contains(r.stdout, "released worker\n") || core.RosterLine(f.state, "worker") != "" || len(callsTo(r.calls, "pane", "get", "p-worker")) != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q roster=%q calls=%#v", r.code, r.stdout, r.stderr, core.RosterLine(f.state, "worker"), r.calls)
	}
}

func TestReleaseCopyRemovalTiming(t *testing.T) {
	for _, keep := range []bool{true, false} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`}})
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			rows := make([]core.CopyRow, 0, 268)
			for i := 0; i < 268; i++ {
				dir := filepath.Join(f.tmp, fmt.Sprintf("copy-%03d", i))
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for j := 0; j < 8; j++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(j)), make([]byte, 16384), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				rows = append(rows, core.CopyRow{Path: dir, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "mutation-copy"})
			}
			f.writeCopies(t, rows)
			addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
			args := []string{"release", "worker", "--keep-procs"}
			if keep {
				args = append(args, "--keep-copies")
			}
			started := time.Now()
			code, out, stderr := f.run(t, f.env, args...)
			elapsed := time.Since(started)
			want := 268
			if keep {
				want = 0
			}
			if code != 0 || strings.Count(out, "removed copy ") != want || len(f.copiesRows(t)) != 268-want {
				t.Fatalf("status=%d removed=%d stderr=%q", code, strings.Count(out, "removed copy "), stderr)
			}
			t.Logf("release keep-copies=%t copies=268 files=2144 bytes=35127296 elapsed=%s removed=%d", keep, elapsed, want)
		})
	}
}

func TestReleaseClosesBurstPaneWithoutFlag(t *testing.T) { // JS: "release closes a created burst pane without --close"
	// Mutation captured: requiring --close for a burst worker leaves the temporary panel occupying the grid.
	f := newReleaseFixture(t, "", "")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", true, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addReleaseReport(t, f.state, "worker", false)
	r := f.run(t, "worker")
	if r.code != 0 || !strings.Contains(r.stdout, "closed pane p-worker (temporary)\n") || len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 || core.RosterLine(f.state, "worker") != "" {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v roster=%q", r.code, r.stdout, r.stderr, r.calls, core.RosterLine(f.state, "worker"))
	}
}

func TestReleaseForceWorkingBurstAndCleanup(t *testing.T) { // JS: "release --force closes a working burst worker and clears its files"
	// Mutation captured: consulting the working guard despite --force prevents the deliberate forced release.
	f := newReleaseFixture(t, "working", "")
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", true, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addReleaseReport(t, f.state, "worker", true)
	if err := os.WriteFile(filepath.Join(f.state, "task-worker"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.run(t, "worker", "--force")
	if r.code != 0 || core.RosterLine(f.state, "worker") != "" || len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v roster=%q", r.code, r.stdout, r.stderr, r.calls, core.RosterLine(f.state, "worker"))
	}
	for _, name := range []string{"last-report-worker", "task-worker", filepath.Join("wait", "worker.json")} {
		if _, err := os.Stat(filepath.Join(f.state, name)); !os.IsNotExist(err) {
			t.Errorf("%s remains: %v", name, err)
		}
	}
}

func TestReleaseLayoutAndLabelsFollowClose(t *testing.T) { // JS: "release closes the pane before regridding and relabeling"
	// Mutation captured: running pane layout or herd tab label changes before the close leaves the grid stale.
	t.Run("regrid", func(t *testing.T) {
		f := newReleaseFixture(t, "", "")
		f.env["SOHO_REGRID"] = "on"
		addReleaseReport(t, f.state, "worker", false)
		r := f.run(t, "worker", "--close")
		closed := callsTo(r.calls, "pane", "close", "p-worker")
		listed := callsTo(r.calls, "pane", "list", "--workspace", "ws")
		if r.code != 0 || len(closed) != 1 || len(listed) == 0 || listed[0] < closed[0] {
			t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v", r.code, r.stdout, r.stderr, r.calls)
		}
	})
	t.Run("relabel", func(t *testing.T) {
		f := newReleaseFixture(t, "", "")
		if err := os.WriteFile(filepath.Join(f.state, "herd-tab"), []byte("t1\told\tauto\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		addReleaseReport(t, f.state, "worker", false)
		r := f.run(t, "worker", "--close")
		closed := callsTo(r.calls, "pane", "close", "p-worker")
		renamed := callsTo(r.calls, "tab", "rename", "t1", "herd")
		if r.code != 0 || len(closed) != 1 || len(renamed) != 1 || renamed[0] < closed[0] {
			t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v", r.code, r.stdout, r.stderr, r.calls)
		}
	})
}

func TestReleaseKeepsPlainPaneAndClearsTitle(t *testing.T) { // JS: "release keeps a plain worker pane open and clears its title"
	// Mutation captured: failing to clear the title leaves a stale task label after the worker leaves the roster.
	f := newReleaseFixture(t, "", "")
	addReleaseReport(t, f.state, "worker", false)
	if err := os.WriteFile(filepath.Join(f.state, "task-worker"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.run(t, "worker")
	if r.code != 0 || len(callsTo(r.calls, "pane", "close", "p-worker")) != 0 || len(callsTo(r.calls, "pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title")) != 1 || core.RosterLine(f.state, "worker") != "" {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v roster=%q", r.code, r.stdout, r.stderr, r.calls, core.RosterLine(f.state, "worker"))
	}
	for _, name := range []string{"last-report-worker", "task-worker", filepath.Join("wait", "worker.json")} {
		if _, err := os.Stat(filepath.Join(f.state, name)); !os.IsNotExist(err) {
			t.Errorf("%s remains: %v", name, err)
		}
	}
}

func TestReleaseClosesCreatedPaneWithFlag(t *testing.T) { // JS: "release --close closes the pane this skill created (plain message)"
	// Mutation captured: omitting --close from the release decision leaves an explicitly requested pane open.
	f := newReleaseFixture(t, "", "")
	addReleaseReport(t, f.state, "worker", false)
	r := f.run(t, "worker", "--close")
	if r.code != 0 || r.stdout != "closed pane p-worker\nreleased worker\n" || len(callsTo(r.calls, "pane", "close", "p-worker")) != 1 || len(callsTo(r.calls, "pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title")) != 0 {
		t.Fatalf("release status=%d stdout=%q stderr=%q calls=%#v", r.code, r.stdout, r.stderr, r.calls)
	}
}

func TestReleaseRosterAndOptionGuards(t *testing.T) { // JS: "release: guards — not in roster and unknown option"
	// Mutation captured: skipping roster membership or option validation releases the wrong row or accepts an invalid command.
	f := newReleaseFixture(t, "idle", "")
	missing := f.run(t, "nobody")
	if missing.code != 3 || !strings.Contains(missing.stderr, "agent 'nobody' is not in the roster") || len(missing.calls) != 0 {
		t.Fatalf("missing agent status=%d stderr=%q calls=%#v", missing.code, missing.stderr, missing.calls)
	}
	unknown := f.run(t, "worker", "--bogus")
	if unknown.code != 2 || !strings.Contains(unknown.stderr, "release: unknown option --bogus") || len(unknown.calls) != 0 {
		t.Fatalf("unknown option status=%d stderr=%q calls=%#v", unknown.code, unknown.stderr, unknown.calls)
	}
}

func TestReleaseGoneAgentRemovesRosterEntry(t *testing.T) {
	f := newReleaseFixture(t, "", `{"error":{"code":"agent_not_found","message":"gone"}}`)
	got := f.run(t, "worker", "--close")
	if got.code != 0 || !strings.Contains(got.stdout, "released worker") {
		t.Fatalf("gone release: code=%d out=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
	if row := core.RosterLine(f.state, "worker"); row != "" {
		t.Fatalf("gone worker retained in roster: %q", row)
	}
	for _, c := range got.calls {
		if len(c.Argv) > 1 && c.Argv[0] == "agent" && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
			t.Fatalf("gone worker received input: %#v", c)
		}
	}
}
