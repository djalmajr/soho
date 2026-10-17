package job

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// teamOpsFix installs the fake soho executable the selfCLI adapter
// runs and returns the adapter pointed at it, plus a call-log reader.
type teamOpsFix struct {
	t     *testing.T
	exe   string
	dir   string
	env   platform.Env
	calls func() []fakecli.Call
}

func newTeamOpsFix(t *testing.T, rules []fakecli.Rule) *teamOpsFix {
	t.Helper()
	fakeDir := t.TempDir()
	exe, err := fakecli.Install(t, fakeDir, "soho", rules)
	if err != nil {
		t.Fatalf("install the fake soho: %v", err)
	}
	f := &teamOpsFix{t: t, exe: exe, dir: t.TempDir()}
	var pairs []string
	for _, item := range fakecli.Env(nil, fakeDir) {
		pairs = append(pairs, item)
	}
	f.env = envFromPairs(t, pairs)
	configDir := f.env["SOHO_FAKECLI_CONFIG"]
	f.calls = func() []fakecli.Call {
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(configDir, "soho.json"))
		if err != nil {
			t.Fatalf("read the fake call log: %v", err)
		}
		return calls
	}
	return f
}

func envFromPairs(t *testing.T, pairs []string) platform.Env {
	t.Helper()
	env := platform.Env{}
	for _, item := range pairs {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			env[key] = value
		}
	}
	return env
}

func (f *teamOpsFix) cli() selfCLI {
	return selfCLI{Exe: f.exe, Dir: f.dir, Env: f.env}
}

// configRow renders one config output row the way core.CmdConfig does: the
// key padded to 18 columns, the value to 30, then the source.
func configRow(key, value, source string) string {
	return key + strings.Repeat(" ", 18-len(key)) + " " + value + strings.Repeat(" ", 30-len(value)) + " " + source
}

func TestTeamOpsEnsureJobLane(t *testing.T) {
	t.Run("explicit max_workers bumps it by one", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"session", "set", "lane.job.roles", "job-orchestrator"}},
			{Argv: []string{"session", "set", "lane.job.panes", "1"}},
			{Argv: []string{"config"}, Stdout: "KEY  VALUE  SOURCE\n" + configRow("max_workers", "3", "session") + "\n"},
			{Argv: []string{"session", "set", "max_workers", "4"}},
		})
		if err := f.cli().EnsureJobLane(); err != nil {
			t.Fatalf("EnsureJobLane: %v", err)
		}
		want := [][]string{
			{"session", "set", "lane.job.roles", "job-orchestrator"},
			{"session", "set", "lane.job.panes", "1"},
			{"config"},
			{"session", "set", "max_workers", "4"},
		}
		assertTeamCalls(t, f, want)
	})

	t.Run("the default max_workers is not bumped", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"session", "set", "lane.job.roles", "job-orchestrator"}},
			{Argv: []string{"session", "set", "lane.job.panes", "1"}},
			{Argv: []string{"config"}, Stdout: "KEY  VALUE  SOURCE\n" + configRow("max_workers", "3", "defaults") + "\n"},
		})
		if err := f.cli().EnsureJobLane(); err != nil {
			t.Fatalf("EnsureJobLane: %v", err)
		}
		want := [][]string{
			{"session", "set", "lane.job.roles", "job-orchestrator"},
			{"session", "set", "lane.job.panes", "1"},
			{"config"},
		}
		assertTeamCalls(t, f, want)
	})

	t.Run("a failed step errors with the fixed message", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"session", "set", "lane.job.roles", "job-orchestrator"}, Code: 1},
		})
		err := f.cli().EnsureJobLane()
		wantExit(t, err, ExitHerdr, "job: cannot ensure the job lane")
	})

	t.Run("a config without a max_workers row errors", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"session", "set", "lane.job.roles", "job-orchestrator"}},
			{Argv: []string{"session", "set", "lane.job.panes", "1"}},
			{Argv: []string{"config"}, Stdout: "KEY  VALUE  SOURCE\n"},
		})
		wantExit(t, f.cli().EnsureJobLane(), ExitHerdr, "job: cannot ensure the job lane")
	})
}

func assertTeamCalls(t *testing.T, f *teamOpsFix, want [][]string) {
	t.Helper()
	calls := f.calls()
	if len(calls) != len(want) {
		t.Fatalf("calls = %d, want %d: %v", len(calls), len(want), calls)
	}
	for i, wantArgv := range want {
		if !reflect.DeepEqual(calls[i].Argv, wantArgv) {
			t.Fatalf("call %d argv = %v, want %v", i, calls[i].Argv, wantArgv)
		}
	}
}

func TestTeamOpsSpawnOrchestrator(t *testing.T) {
	// The spawn rule matches by prefix: the cwd is the fixture's own worktree
	// dir, known only after the fake is installed.

	t.Run("the name comes from the last JSON object after log lines", func(t *testing.T) {
		out := "spawning the job orchestrator\n" +
			"lane job is ready\n" +
			`{"name":"orch-1","pane_id":"w1:p2","status":"ready"}` + "\n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Stdout: out},
		})
		name, err := f.cli().SpawnOrchestrator()
		if err != nil {
			t.Fatalf("SpawnOrchestrator: %v", err)
		}
		if name != "orch-1" {
			t.Fatalf("name = %q, want orch-1", name)
		}
		assertTeamCalls(t, f, [][]string{{"spawn", "job-orchestrator", "--cwd", f.dir, "--approvals", "full", "--fresh"}})
	})

	t.Run("an indented object over several lines is the spawn output form", func(t *testing.T) {
		out := "spawning the job orchestrator\n{\n  \"name\": \"orch-2\",\n  \"pane_id\": \"w1:p2\",\n  \"status\": \"ready\"\n}\n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Stdout: out},
		})
		name, err := f.cli().SpawnOrchestrator()
		if err != nil {
			t.Fatalf("SpawnOrchestrator: %v", err)
		}
		if name != "orch-2" {
			t.Fatalf("name = %q, want orch-2", name)
		}
	})

	t.Run("the later object wins when two are printed", func(t *testing.T) {
		out := `{"name":"orch-first"}` + "\n" + `{"name":"orch-second"}` + "\n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Stdout: out},
		})
		name, err := f.cli().SpawnOrchestrator()
		if err != nil {
			t.Fatalf("SpawnOrchestrator: %v", err)
		}
		if name != "orch-second" {
			t.Fatalf("name = %q, want orch-second", name)
		}
	})

	t.Run("a missing name errors with the fixed message", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Stdout: "spawned\n"},
		})
		_, err := f.cli().SpawnOrchestrator()
		wantExit(t, err, ExitHerdr, "job: cannot spawn the job orchestrator")
	})

	t.Run("a failed spawn errors with the fixed message", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Code: 7},
		})
		_, err := f.cli().SpawnOrchestrator()
		wantExit(t, err, ExitHerdr, "job: cannot spawn the job orchestrator")
	})

	t.Run("a failed spawn keeps its exit and stderr tail as the error's detail (DJA-395)", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Code: 7, Stderr: "first\n\nsecond\nthird\r\nsoho: spawn: agent start failed\n"},
		})
		_, err := f.cli().SpawnOrchestrator()
		wantExit(t, err, ExitHerdr, "job: cannot spawn the job orchestrator")
		if got := err.(*ExitError).Detail; got != "exit 7: second | third | soho: spawn: agent start failed" {
			t.Fatalf("detail = %q", got)
		}
		if strings.Contains(err.Error(), "agent start failed") {
			t.Fatalf("the detail leaked into the message: %q", err.Error())
		}
	})
}

func TestTeamOpsRoster(t *testing.T) {
	t.Run("the header is skipped and the rows parse until the # block", func(t *testing.T) {
		out := "NAME                 ROLE               KIND     PANE     TAB              STATE     REPORT       CWD TASK\n" +
			"orch-1               job-orchestrator   job      w1:p2    Job              working   none         /work brief\n" +
			"worker-1             implementer        worker   w1:p3    Impl             working   ready        /work brief\n" +
			"\n# other live agents in workspace w1 (not spawned by this skill)\n" +
			"other-agent          -                  null     w1:p4    Other            working            \n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Stdout: out},
		})
		rows, err := f.cli().Roster()
		if err != nil {
			t.Fatalf("Roster: %v", err)
		}
		want := []rosterRow{
			{Name: "orch-1", Role: "job-orchestrator", Pane: "w1:p2"},
			{Name: "worker-1", Role: "implementer", Pane: "w1:p3"},
		}
		if !reflect.DeepEqual(rows, want) {
			t.Fatalf("rows = %+v, want %+v (the # block is not rostered workers)", rows, want)
		}
		assertTeamCalls(t, f, [][]string{{"roster"}})
	})

	t.Run("a spaced tab label keeps the identity columns exact", func(t *testing.T) {
		// The tab label impl+rev 2 carries a space: it shifts the STATE
		// column (and everything after), so the parser reads only the
		// identity columns and stays exact for the job-orchestrator row.
		out := "NAME                 ROLE               KIND     PANE     TAB              STATE     REPORT       CWD TASK\n" +
			"orch-1               job-orchestrator   job      w1:p2    impl+rev 2       gone      none         /work brief\n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Stdout: out},
		})
		rows, err := f.cli().Roster()
		if err != nil {
			t.Fatalf("Roster: %v", err)
		}
		want := []rosterRow{{Name: "orch-1", Role: "job-orchestrator", Pane: "w1:p2"}}
		if !reflect.DeepEqual(rows, want) {
			t.Fatalf("rows = %+v, want %+v (the spaced label must not shift the identity)", rows, want)
		}
	})

	t.Run("a row with fewer than four fields fails the whole read", func(t *testing.T) {
		out := "NAME                 ROLE               KIND     PANE     TAB              STATE     REPORT       CWD TASK\n" +
			"orch-1               job-orchestrator\n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Stdout: out},
		})
		rows, err := f.cli().Roster()
		if err == nil {
			t.Fatalf("Roster = %+v, want the short-row parse error", rows)
		}
		wantExit(t, err, ExitHerdr, "job: cannot read the team roster")
	})

	t.Run("a non-zero exit is the fixed error", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Code: 7},
		})
		_, err := f.cli().Roster()
		wantExit(t, err, ExitHerdr, "job: cannot read the team roster")
	})
}

func TestTeamOpsDispatchAndRelease(t *testing.T) {
	t.Run("dispatch uses --no-wait and --amend only when asked", func(t *testing.T) {
		brief := filepath.Join(t.TempDir(), "orchestrator-brief.md")
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"dispatch", "orch-1", brief, "--no-wait"}},
			{Argv: []string{"dispatch", "orch-1", brief, "--no-wait", "--amend"}},
		})
		cli := f.cli()
		if err := cli.Dispatch("orch-1", brief, false); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if err := cli.Dispatch("orch-1", brief, true); err != nil {
			t.Fatalf("Dispatch (amend): %v", err)
		}
		assertTeamCalls(t, f, [][]string{
			{"dispatch", "orch-1", brief, "--no-wait"},
			{"dispatch", "orch-1", brief, "--no-wait", "--amend"},
		})
	})

	t.Run("release closes and forces", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"release", "orch-1", "--close", "--force"}},
		})
		if err := f.cli().Release("orch-1"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		assertTeamCalls(t, f, [][]string{{"release", "orch-1", "--close", "--force"}})
	})

	t.Run("a failed dispatch errors with the fixed message", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"dispatch", "orch-1", "brief.md", "--no-wait"}, Code: 10},
		})
		wantExit(t, f.cli().Dispatch("orch-1", "brief.md", false), ExitHerdr, "job: cannot dispatch the job orchestrator")
	})
}

func TestTeamOpsReleaseTeam(t *testing.T) {
	// The roster the team prints: the header row, the job orchestrator's
	// row, three workers, then the "other live agents" block that runs to
	// the end of the output.
	roster := "NAME                 ROLE               KIND     PANE     TAB              STATE     REPORT       CWD TASK\n" +
		"orch-1               job-orchestrator   job      w1:p2    Job              working   none         /work brief\n" +
		"w-1                  implementer        worker   w1:p3    Impl             working   ready        /work brief\n" +
		"w-2                  reviewer           worker   w1:p4    Rev              working   ready        /work brief\n" +
		"w-3                  implementer        worker   w1:p5    Impl2            working   ready        /work brief\n" +
		"\n# other live agents in workspace w1 (not spawned by this skill)\n" +
		"other-agent          -                  null     w1:p6    Other            working            \n"

	t.Run("a refused release does not skip the remaining workers", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Stdout: roster},
			{Argv: []string{"release", "w-1", "--close", "--force"}, Code: 10},
			{Argv: []string{"release", "w-2", "--close", "--force"}},
			{Argv: []string{"release", "w-3", "--close", "--force"}},
		})
		err := f.cli().ReleaseTeam()
		wantExit(t, err, ExitHerdr, "job: cannot release the team workers")
		assertTeamCalls(t, f, [][]string{
			{"roster"},
			{"release", "w-1", "--close", "--force"},
			{"release", "w-2", "--close", "--force"},
			{"release", "w-3", "--close", "--force"},
		})
	})

	t.Run("every worker released returns nil", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Stdout: roster},
			{Argv: []string{"release", "w-1", "--close", "--force"}},
			{Argv: []string{"release", "w-2", "--close", "--force"}},
			{Argv: []string{"release", "w-3", "--close", "--force"}},
		})
		if err := f.cli().ReleaseTeam(); err != nil {
			t.Fatalf("ReleaseTeam: %v", err)
		}
		assertTeamCalls(t, f, [][]string{
			{"roster"},
			{"release", "w-1", "--close", "--force"},
			{"release", "w-2", "--close", "--force"},
			{"release", "w-3", "--close", "--force"},
		})
	})

	t.Run("a failed roster read releases nothing", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"roster"}, Code: 7},
		})
		err := f.cli().ReleaseTeam()
		wantExit(t, err, ExitHerdr, "job: cannot release the team workers")
		assertTeamCalls(t, f, [][]string{{"roster"}})
	})
}

func TestTeamOpsStatus(t *testing.T) {
	t.Run("the state and report come from the agent's tab line", func(t *testing.T) {
		out := "other-agent\tworking\t-\t42\t-\n" +
			"orch-1\tblocked\t/path/report.md\tapproval dialog\t-\t-\n"
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"status", "orch-1"}, Stdout: out},
		})
		state, report, err := f.cli().Status("orch-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if state != "blocked" || report != "/path/report.md" {
			t.Fatalf("state=%q report=%q, want blocked /path/report.md", state, report)
		}
	})

	t.Run("a non-zero exit still returns the parsed state", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"status", "orch-1"}, Code: 7, Stdout: "orch-1\tblocked\t\tapproval dialog\t-\t-\n"},
		})
		state, report, err := f.cli().Status("orch-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if state != "blocked" || report != "" {
			t.Fatalf("state=%q report=%q, want blocked with an empty report", state, report)
		}
	})

	t.Run("the JSON line carries the question, quota and provider states", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"status", "orch-1"}, Stdout: `{"agent":"orch-1","status":"question","report":"/path/report.md","question":"which base?"}` + "\n"},
		})
		state, report, err := f.cli().Status("orch-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if state != "question" || report != "/path/report.md" {
			t.Fatalf("state=%q report=%q, want question /path/report.md", state, report)
		}
	})

	t.Run("a JSON line for another agent is not the agent's state", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"status", "orch-1"}, Stdout: `{"agent":"other","status":"question","report":"/x"}` + "\n"},
		})
		_, _, err := f.cli().Status("orch-1")
		wantExit(t, err, ExitHerdr, "job: cannot read the job orchestrator status")
	})

	t.Run("no matching line is an error", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"status", "orch-1"}, Stdout: "other-agent\tworking\t-\t42\t-\n"},
		})
		_, _, err := f.cli().Status("orch-1")
		wantExit(t, err, ExitHerdr, "job: cannot read the job orchestrator status")
	})

	t.Run("a failed call is an error", func(t *testing.T) {
		f := newTeamOpsFix(t, []fakecli.Rule{
			{Argv: []string{"status", "orch-1"}, Code: 4},
		})
		_, _, err := f.cli().Status("orch-1")
		wantExit(t, err, ExitHerdr, "job: cannot read the job orchestrator status")
	})
}

// TestTeamOpsWorkingDirectory proves the subprocess runs in the job
// worktree: a real git rev-parse through runStepIn from the worktree
// answers the worktree, not the test process's directory. fakecli cannot
// observe the cwd, so the real binary is the proof.
func TestTeamOpsWorkingDirectory(t *testing.T) {
	gitDir := prepareGitOnlyDir(t)
	env := platform.Env{"PATH": gitDir, "HOME": t.TempDir()}
	repos := map[string]string{}
	for _, name := range []string{"a", "b"} {
		repo := t.TempDir()
		if _, err := runStepIn(repo, "git", []string{"init", "-q"}, 60*time.Second, env); err != nil {
			t.Fatalf("git init in %s: %v", name, err)
		}
		repos[name] = repo
	}
	sub := filepath.Join(repos["a"], "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalA, err := filepath.EvalSymlinks(repos["a"])
	if err != nil {
		t.Fatal(err)
	}
	// The nested directory resolves to repository a, proving the subprocess
	// started inside it; repository b is the control. git rev-parse prints
	// forward slashes on every OS, so the separators are cleaned before the
	// comparison.
	out, err := runStepIn(sub, "git", []string{"rev-parse", "--show-toplevel"}, 60*time.Second, env)
	if err != nil {
		t.Fatalf("runStepIn: %v", err)
	}
	if got := filepath.Clean(strings.TrimSpace(out)); got != canonicalA {
		t.Fatalf("runStepIn toplevel = %q, want %q", got, canonicalA)
	}
	out, err = runStepIn(repos["b"], "git", []string{"rev-parse", "--show-toplevel"}, 60*time.Second, env)
	if err != nil {
		t.Fatalf("runStepIn (control): %v", err)
	}
	canonicalB, err := filepath.EvalSymlinks(repos["b"])
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Clean(strings.TrimSpace(out)); got != canonicalB {
		t.Fatalf("runStepIn (control) toplevel = %q, want %q", got, canonicalB)
	}
	// selfCLI passes the same directory: the spawn argv carries it and the
	// subprocess ran there.
	cli := selfCLI{Exe: "git", Dir: repos["b"], Env: env, Timeout: 60 * time.Second}
	if _, err := cli.step("spawn the job orchestrator", "rev-parse", "--show-toplevel"); err != nil {
		t.Fatalf("selfCLI.step: %v", err)
	}
}

// TestTeamOpsTimeout proves every subprocess is bounded: a delayed command
// past the timeout fails with the fixed error.
func TestTeamOpsTimeout(t *testing.T) {
	f := newTeamOpsFix(t, []fakecli.Rule{
		{AnyArgs: true, Delay: 5000},
	})
	cli := f.cli()
	cli.Timeout = 200 * time.Millisecond
	start := time.Now()
	err := cli.EnsureJobLane()
	if err == nil {
		t.Fatal("EnsureJobLane finished, want the timeout error")
	}
	wantExit(t, err, ExitHerdr, "job: cannot ensure the job lane")
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the bounded subprocess took %s", time.Since(start))
	}
}

// TestTeamOpsDefaultTimeout proves a zero Timeout uses the 120 s default.
func TestTeamOpsDefaultTimeout(t *testing.T) {
	if (selfCLI{}).timeout() != 120*time.Second {
		t.Fatalf("zero Timeout = %s, want 120 s", (selfCLI{}).timeout())
	}
	if (selfCLI{Timeout: 30 * time.Second}).timeout() != 30*time.Second {
		t.Fatal("an explicit Timeout is not used")
	}
}
