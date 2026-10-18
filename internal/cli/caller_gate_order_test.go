package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-394 R3: where the caller-pane check sits in each command. A refusal
// comes before anything the command would change, and a usage error never
// reaches Herdr.

const forkedClaim = "HERDR_PANE_ID='%s' is a live pane but this process does not run in it"

// claimShell gives the claimed pane its shell (4000) in the fake herdr that
// the config file drives, and scripts the ancestry (OverrideAncestorParents): forked is a
// process tree that never reaches the shell.
func claimShell(t *testing.T, config, pane string, forked bool) {
	t.Helper()
	var c struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c.Rules = append([]fakecli.Rule{{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: `{"result":{"process_info":{"shell_pid":4000}}}`}}, c.Rules...)
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	scriptCallerTree(t, forked)
}

// callerGateEnv is a command fixture whose fake herdr answers the
// process-info of w1:p9 (shell 4000) and whose ancestry is a forked tree.
func callerGateEnv(t *testing.T) (platform.Env, string, string) {
	t.Helper()
	env, cwd := commandFixture(t)
	dir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"pane", "process-info"}, ArgvPrefix: true, Stdout: `{"result":{"process_info":{"shell_pid":4000}}}`},
		{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true},
	}
	if _, err := fakecli.Install(t, dir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	scriptCallerTree(t, true)
	env["PATH"] = dir
	env["SOHO_FAKECLI_CONFIG"] = dir
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "w1:p9"
	herdr.ResetCallerCheck()
	return env, cwd, dir
}

func processInfoCalls(calls []fakecli.Call) int {
	n := 0
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[0] == "pane" && call.Argv[1] == "process-info" {
			n++
		}
	}
	return n
}

// The reopen refuses before its release half closes the worker's pane and
// drops its roster line.
func TestReopenCallerRefusalKeepsTheWorker(t *testing.T) {
	f := newReopenFixture(t, "working")
	setReopenRules(t, f)
	f.env["HERDR_PANE_ID"] = "w1:p9"
	delete(f.env, "SOHO_DELEGATED_PANE")
	claimShell(t, f.configFile, "w1:p9", true)
	before, err := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}

	run := f.reopen(t, "worker", "--force")
	after, _ := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if run.code != 2 || !strings.Contains(run.stderr, "soho: reopen: "+strings.Replace(forkedClaim, "%s", "w1:p9", 1)) || !strings.Contains(run.stderr, "Nothing was changed.") {
		t.Fatalf("code=%d stderr=%q", run.code, run.stderr)
	}
	for _, call := range run.calls {
		if len(call.Argv) > 1 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
			t.Fatalf("the refused reopen closed a pane: %v", call.Argv)
		}
		if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			t.Fatalf("the refused reopen opened a worker: %v", call.Argv)
		}
	}
	if string(after) != string(before) || !strings.Contains(string(after), "worker\tp-worker") {
		t.Fatalf("roster changed:\n%q\n%q", before, after)
	}
	if n := processInfoCalls(run.calls); n != 1 {
		t.Fatalf("process-info calls=%d want 1", n)
	}
}

// A verified tree reopens as before, and the spawn half reuses the memo of
// the check made before the release: one process-info call in all.
func TestReopenVerifiedCallerChecksOnce(t *testing.T) {
	f := newReopenFixture(t, "working")
	setReopenRules(t, f)
	f.env["SOHO_LAYOUT"] = "split"
	f.env["HERDR_PANE_ID"] = "w1:p9"
	delete(f.env, "SOHO_DELEGATED_PANE")
	claimShell(t, f.configFile, "w1:p9", false)

	run := f.reopen(t, "worker", "--force")
	if run.code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	if n := processInfoCalls(run.calls); n != 1 {
		t.Fatalf("process-info calls=%d want 1", n)
	}
	started := false
	for _, call := range run.calls {
		if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			started = true
		}
	}
	if !started {
		t.Fatalf("the reopen did not open the worker: %v", run.calls)
	}
}

// The event refusal comes before PrepareEvent writes the snapshot, phase,
// revision and event, and also for escalate, which has no delivery.
func TestCollaborateEventCallerRefusalKeepsTheAssignment(t *testing.T) {
	for _, event := range []string{"ready", "escalate"} {
		t.Run(event, func(t *testing.T) {
			herdr.ResetCallerCheck()
			f, brief := collaborationFixture(t, false)
			a := startCollaboration(t, f, brief)
			env := f.env.Clone()
			env["HERDR_PANE_ID"] = "ws:p1"
			delete(env, "SOHO_DELEGATED_PANE")
			claimShell(t, filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"), "ws:p1", true)
			r, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
			if err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(f.root, "event-report.md")
			if err := os.WriteFile(report, []byte("Ready\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := core.LoadConfig(env, f.repo)
			store := collaboration.Store{StateDir: core.StateDirPath(&cfg, env, f.repo)}
			before, err := store.Read(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			checks := titleCallCount(t, env["SOHO_FAKECLI_CONFIG"], "pane", "process-info")

			code, out, stderr := f.run(t, env, "collaborate", "event", a.ID, event, "--report", report, "--revision", r.Fingerprint)
			after, err := store.Read(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if code != 2 || out != "" || !strings.Contains(stderr, "soho: collaborate: "+strings.Replace(forkedClaim, "%s", "ws:p1", 1)) || !strings.Contains(stderr, "Nothing was changed.") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the refusal changed the assignment:\nbefore %+v\nafter  %+v", before, after)
			}
			if n := titleCallCount(t, env["SOHO_FAKECLI_CONFIG"], "pane", "process-info") - checks; n != 1 {
				t.Fatalf("process-info calls=%d want 1", n)
			}
		})
	}
}

// A verified tree publishes the event as before; the delivery reuses the
// check made before PrepareEvent: one process-info call in all.
func TestCollaborateEventVerifiedCallerChecksOnce(t *testing.T) {
	herdr.ResetCallerCheck()
	f, brief := collaborationFixture(t, false)
	a := startCollaboration(t, f, brief)
	env := f.env.Clone()
	env["HERDR_PANE_ID"] = "ws:p1"
	delete(env, "SOHO_DELEGATED_PANE")
	claimShell(t, filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"), "ws:p1", false)
	r, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(f.root, "event-report.md")
	if err := os.WriteFile(report, []byte("Ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := titleCallCount(t, env["SOHO_FAKECLI_CONFIG"], "pane", "process-info")
	code, out, stderr := f.run(t, env, "collaborate", "event", a.ID, "ready", "--report", report, "--revision", r.Fingerprint)
	if code != 0 || !strings.Contains(out, `"phase":"reviewing"`) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if n := titleCallCount(t, env["SOHO_FAKECLI_CONFIG"], "pane", "process-info") - checks; n != 1 {
		t.Fatalf("process-info calls=%d want 1", n)
	}
}

// A usage error is answered without Herdr: the values that only the argv
// decides are checked before the caller-pane check.
func TestUsageErrorsDoNotCallHerdr(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"spawn", "implementer", "--timeout", "invalid"}, 2, "soho: spawn: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got 'invalid'"},
		{[]string{"spawn", "implementer", "--approvals", "invalid"}, 2, "soho: invalid approvals 'invalid' (ask|edits|full)"},
		{[]string{"spawn", "implementer", "--effort", "invalid"}, 2, "soho: invalid effort 'invalid' (low|medium|high|xhigh|max)"},
		{[]string{"spawn", "planner"}, 12, "soho: spawn planner: the orchestrator is the planner and does not open a pane. Plan in this session."},
		{[]string{"collaborate", "finalize", "missing", "--verdict", "invalid"}, 2, "soho: collaborate: verdict must be accept or reject"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			env, cwd, dir := callerGateEnv(t)
			code, _, stderr := runIn(t, tc.args, env, cwd)
			if code != tc.code || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d want %d stderr=%q", code, tc.code, stderr)
			}
			if strings.Contains(stderr, "Nothing was changed.") {
				t.Fatalf("the usage error became a caller refusal: %q", stderr)
			}
			if n := titleCallCount(t, dir, "pane", "process-info"); n != 0 {
				t.Fatalf("process-info calls=%d want 0", n)
			}
		})
	}
	t.Run("valid arguments still reach the check", func(t *testing.T) {
		for _, args := range [][]string{
			{"spawn", "implementer", "--timeout", "90s", "--approvals", "ask", "--effort", "high"},
			{"collaborate", "finalize", "missing", "--verdict", "reject"},
		} {
			env, cwd, dir := callerGateEnv(t)
			code, _, stderr := runIn(t, args, env, cwd)
			if code != 2 || !strings.Contains(stderr, "Nothing was changed.") {
				t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
			}
			if n := titleCallCount(t, dir, "pane", "process-info"); n != 1 {
				t.Fatalf("%v: process-info calls=%d want 1", args, n)
			}
		}
	})
}

// With no state yet, the send refusal is still recorded: only the directory
// that holds friction.log is created, no roster and no other file.
func TestSendCallerRefusalRecordsFrictionWithoutState(t *testing.T) {
	env, cwd, _ := callerGateEnv(t)
	code, _, stderr := runIn(t, []string{"send", "w0test:p0a", "hello"}, env, cwd)
	if code != 2 || !strings.Contains(stderr, "soho: send: "+strings.Replace(forkedClaim, "%s", "w1:p9", 1)) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	dir := filepath.Join(env["SOHO_DIR"], "ws")
	friction, err := os.ReadFile(filepath.Join(dir, "friction.log"))
	if err != nil || !strings.Contains(string(friction), "error(exit 2)\tsend\t"+"send: "+strings.Replace(forkedClaim, "%s", "w1:p9", 1)) {
		t.Fatalf("friction.log=%q err=%v", friction, err)
	}
	if got := dirNames(t, dir); !reflect.DeepEqual(got, []string{"friction.log"}) {
		t.Fatalf("state dir holds %v, want only friction.log", got)
	}
	if got := dirNames(t, env["SOHO_DIR"]); !reflect.DeepEqual(got, []string{"ws"}) {
		t.Fatalf("state root holds %v, want only ws", got)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// The state refusals come before the caller-pane check. The state dir
// inside the skill keeps the send's own refusal; the former state dir
// waiting for the setup is refused by the gate on stderr only, resolving no
// state dir and writing no friction.log.
func TestSendStateRefusalsComeBeforeTheCallerGate(t *testing.T) {
	t.Run("state inside the skill", func(t *testing.T) {
		env, cwd, dir := callerGateEnv(t)
		env["SOHO_SKILL_DIR"] = filepath.Dir(env["SOHO_DIR"])
		code, _, stderr := runIn(t, []string{"send", "w0test:p0a", "hello"}, env, cwd)
		if code != 2 || !strings.Contains(stderr, "would be inside the soho skill") || strings.Contains(stderr, "Nothing was changed.") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if n := titleCallCount(t, dir, "pane", "process-info"); n != 0 {
			t.Fatalf("process-info calls=%d want 0", n)
		}
		if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "ws")); !os.IsNotExist(err) {
			t.Fatalf("the refusal resolved the state dir: %v", err)
		}
	})
	t.Run("former state dir waiting for the setup", func(t *testing.T) {
		env, cwd, dir := callerGateEnv(t)
		delete(env, "SOHO_DIR")
		if err := os.MkdirAll(filepath.Join(cwd, ".herdr-soho"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runIn(t, []string{"send", "w0test:p0a", "hello"}, env, cwd)
		if code != 2 || !strings.Contains(stderr, "soho: send: "+strings.Replace(forkedClaim, "%s", "w1:p9", 1)) || !strings.Contains(stderr, "Nothing was changed.") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if n := titleCallCount(t, dir, "pane", "process-info"); n != 1 {
			t.Fatalf("process-info calls=%d want 1", n)
		}
		if _, err := os.Stat(filepath.Join(cwd, ".soho")); !os.IsNotExist(err) {
			t.Fatalf("the refusal created the state dir: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cwd, ".herdr-soho", "ws")); !os.IsNotExist(err) {
			t.Fatalf("the refusal wrote into the former state dir: %v", err)
		}
	})
}
