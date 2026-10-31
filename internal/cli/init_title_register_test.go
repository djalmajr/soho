package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// recordJSON is the private orchestrator record as committed to disk.
type recordJSON struct {
	Schema      int    `json:"schema"`
	ProjectRoot string `json:"project_root"`
	SessionID   string `json:"session_id"`
	Terminal    string `json:"terminal"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	StartedMS   int64  `json:"started_ms"`
	Task        string `json:"task"`
}

// readRecordJSON loads one session's committed orchestrator.json.
func readRecordJSON(t *testing.T, state, session string) recordJSON {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, session, "orchestrator.json"))
	if err != nil {
		t.Fatalf("read the orchestrator record: %v", err)
	}
	var rec recordJSON
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("the orchestrator record is not valid JSON: %v: %s", err, data)
	}
	return rec
}

// assertRecordEquals fails when the record differs from want, with the
// started_ms only required positive.
func assertRecordFields(t *testing.T, got, want recordJSON) {
	t.Helper()
	if got.Schema != want.Schema || got.ProjectRoot != want.ProjectRoot || got.SessionID != want.SessionID || got.Terminal != want.Terminal || got.Name != want.Name || got.Kind != want.Kind || got.Task != want.Task {
		t.Fatalf("record=%+v want %+v", got, want)
	}
	if got.StartedMS <= 0 {
		t.Fatalf("started_ms %d must be a positive start", got.StartedMS)
	}
}

// herdrInitRules is the fake-herdr rule set of the init lifecycle: the
// caller pane p1 of workspace ws runs claude from repo, the agent is
// already named orchestrator (no rename) and the pane has no title yet.
// agentListBody is the result.agents body of the All=true observation.
func herdrInitRules(repo, agentListBody string) []fakecli.Rule {
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"orchestrator","agent_status":"working"}}}`},
		{Argv: []string{"pane", "get", "p1"}, Stdout: `{"result":{"pane":{"title":"","workspace_id":"ws"}}}`},
		{Argv: []string{"pane", "report-metadata", "p1", "--source", "soho", "--title", "orchestrator: " + filepath.Base(repo)}, Stdout: `{}`},
		{Argv: []string{"pane", "report-metadata", "p1", "--source", "soho", "--clear-title"}, Stdout: `{}`},
	}
	if agentListBody != "" {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + agentListBody + `]}}`})
	}
	return rules
}

func herdrAgentEntry(name, pane, agent, session, cwd string) string {
	return `{"name":"` + name + `","pane_id":"` + pane + `","agent":"` + agent + `","workspace_id":"` + session + `","cwd":"` + cwd + `"}`
}

func TestInitRegistersOrchestratorRecordThroughFakeHerdr(t *testing.T) {
	env, repo := commandFixture(t)
	env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo))
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	if !strings.Contains(out.String(), `"orchestrator": "orchestrator"`) || !strings.Contains(out.String(), `"pane_id": "p1"`) || !strings.Contains(out.String(), `"title": "orchestrator: `+filepath.Base(repo)+`"`) {
		t.Fatalf("the init JSON changed: %s (stderr %s)", out.String(), stderr.String())
	}
	rec := readRecordJSON(t, env["SOHO_DIR"], "ws")
	assertRecordFields(t, rec, recordJSON{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "p1", Name: "orchestrator", Kind: "claude", Task: filepath.Base(repo)})
}

func TestInitReinitPreservesStartAndExplicitClear(t *testing.T) {
	env, repo := commandFixture(t)
	env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo))
	state := env["SOHO_DIR"]
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	first := readRecordJSON(t, state, "ws")
	// An explicit clear stores no task.
	var out2, stderr2 strings.Builder
	runAndCapture(t, env, []string{"title", "--clear"}, &out2, &stderr2)
	if !strings.Contains(out2.String(), `"title": ""`) {
		t.Fatalf("the clear output changed: %s (stderr %s)", out2.String(), stderr2.String())
	}
	if cleared := readRecordJSON(t, state, "ws"); cleared.Task != "" {
		t.Fatalf("the cleared task is %q, want empty", cleared.Task)
	}
	// The re-init preserves the first start and the explicit clear.
	var out3, stderr3 strings.Builder
	runAndCapture(t, env, []string{"init"}, &out3, &stderr3)
	reinit := readRecordJSON(t, state, "ws")
	if reinit.StartedMS != first.StartedMS {
		t.Fatalf("the re-init replaced the first start: %d want %d", reinit.StartedMS, first.StartedMS)
	}
	if reinit.Task != "" {
		t.Fatalf("the re-init reset the explicit clear: %q", reinit.Task)
	}
	if reinit.Terminal != "p1" || reinit.Name != "orchestrator" {
		t.Fatalf("the re-init changed the identity: %+v", reinit)
	}
}

func TestTitleUpdatesMatchingRecordThroughFakeHerdr(t *testing.T) {
	env, repo := commandFixture(t)
	state := env["SOHO_DIR"]
	// Seed the matching record directly (the init path is covered above).
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed, _ := json.Marshal(recordJSON{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "p1", Name: "orchestrator", Kind: "claude", StartedMS: 111, Task: filepath.Base(repo)})
	if err := os.WriteFile(filepath.Join(state, "ws", "orchestrator.json"), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeDir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"orchestrator","agent_status":"working"}}}`},
		{Argv: []string{"pane", "report-metadata", "p1", "--source", "soho", "--title", "orchestrator: Fix the parser"}, Stdout: `{}`},
	}
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env = withHostGit(t, env, fakeDir)
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "p1"
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"title", "Fix", "the", "parser"}, &out, &stderr)
	if !strings.Contains(out.String(), `"title": "orchestrator: Fix the parser"`) {
		t.Fatalf("the title output changed: %s (stderr %s)", out.String(), stderr.String())
	}
	rec := readRecordJSON(t, state, "ws")
	if rec.Task != "Fix the parser" || rec.StartedMS != 111 || rec.Terminal != "p1" {
		t.Fatalf("the matching record update changed more than the task: %+v", rec)
	}
}

func TestTitleNoRecordKeepsNormalUIBehavior(t *testing.T) {
	env, _ := commandFixture(t)
	fakeDir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"pane", "report-metadata", "p1", "--source", "soho", "--title", "orchestrator: Fresh goal"}, Stdout: `{}`},
	}
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env = withHostGit(t, env, fakeDir)
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "p1"
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"title", "Fresh", "goal"}, &out, &stderr)
	if !strings.Contains(out.String(), `"title": "orchestrator: Fresh goal"`) {
		t.Fatalf("the no-record title output changed: %s (stderr %s)", out.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "ws", "orchestrator.json")); !os.IsNotExist(err) {
		t.Fatalf("a no-record title must not create the record: %v", err)
	}
}

// workerRosterFixture writes a session roster holding pane p1 in a row
// named name and an existing orchestrator record.
func workerRosterFixture(t *testing.T, state, name string, seed recordJSON) {
	t.Helper()
	sess := filepath.Join(state, "ws")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + name + "\tp1\tclaude\timplementer\n"
	if err := os.WriteFile(filepath.Join(sess, "agents.tsv"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(sess, "orchestrator.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInitRosterWorkerNeverReplacesOrchestratorRecord(t *testing.T) {
	for _, tc := range []struct{ name, workerName string }{
		{"worker", "worker1"},
		{"sub-orchestrator", "orchestrator-9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := commandFixture(t)
			env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo))
			seed := recordJSON{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "p0", Name: "orchestrator", Kind: "claude", StartedMS: 222, Task: "keep me"}
			workerRosterFixture(t, env["SOHO_DIR"], tc.workerName, seed)
			before, _ := os.ReadFile(filepath.Join(env["SOHO_DIR"], "ws", "orchestrator.json"))
			var out, stderr strings.Builder
			runAndCapture(t, env, []string{"init"}, &out, &stderr)
			after, err := os.ReadFile(filepath.Join(env["SOHO_DIR"], "ws", "orchestrator.json"))
			if err != nil {
				t.Fatalf("the orchestrator record is missing: %v", err)
			}
			if string(before) != string(after) {
				t.Fatalf("the %s replaced the orchestrator record:\nbefore %s\nafter %s", tc.name, before, after)
			}
			if !strings.Contains(stderr.String(), "keeping its name") {
				t.Fatalf("the roster naming line is missing: %s", stderr.String())
			}
		})
	}
}

func TestInitUnknownKindOrListingOrRootOrPaneRefusesRecord(t *testing.T) {
	for _, tc := range []struct {
		name       string
		repo       string
		agentList  string
		wantStderr string
	}{
		{name: "unknown kind", agentList: "AGENT", wantStderr: "names no known kind"},
		{name: "failed listing", agentList: "", wantStderr: "the live terminal listing failed"},
		{name: "root mismatch", agentList: "OTHER", wantStderr: "does not match the project root"},
		{name: "pane not observed", agentList: "PANE9", wantStderr: "was not observed live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := commandFixture(t)
			body := ""
			switch tc.agentList {
			case "AGENT":
				body = herdrAgentEntry("orchestrator", "p1", "mystery-app", "ws", repo)
			case "OTHER":
				other := t.TempDir()
				if out, err := exec.Command("git", "init", "-q", other).CombinedOutput(); err != nil {
					t.Fatalf("git init: %s: %v", out, err)
				}
				body = herdrAgentEntry("orchestrator", "p1", "claude", "ws", other)
			case "PANE9":
				body = herdrAgentEntry("orchestrator", "p9", "claude", "ws", repo)
			}
			env = herdrInitFixtureOver(t, env, repo, body)
			var out, stderr strings.Builder
			runAndCapture(t, env, []string{"init"}, &out, &stderr)
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr %s does not name %q", stderr.String(), tc.wantStderr)
			}
			if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "ws", "orchestrator.json")); !os.IsNotExist(err) {
				t.Fatalf("the %s case created a record: %v", tc.name, err)
			}
		})
	}
}

func TestInitBadRosterFailsAuxiliaryRegistration(t *testing.T) {
	env, repo := commandFixture(t)
	env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo))
	sess := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sess, "agents.tsv"), 0o755); err != nil {
		t.Fatalf("cannot build the unsafe roster fixture: %v", err)
	}
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	if !strings.Contains(stderr.String(), "the session roster is unreadable") {
		t.Fatalf("the unsafe roster must fail the registration: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(sess, "orchestrator.json")); !os.IsNotExist(err) {
		t.Fatalf("the unsafe roster must not grant a record: %v", err)
	}
}

func TestInitCorruptRecordRefusedAndUntouched(t *testing.T) {
	env, repo := commandFixture(t)
	env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo))
	sess := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(env["SOHO_DIR"], "outside.json")
	if err := os.WriteFile(target, []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(sess, "orchestrator.json")); err != nil {
		t.Fatalf("symlinks unavailable: %v", err)
	}
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	if !strings.Contains(stderr.String(), "the orchestrator record could not be registered") {
		t.Fatalf("the corrupt record must fail the registration: %s", stderr.String())
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != `{"schema":1}` {
		t.Fatalf("the link target changed: %v %q", err, data)
	}
	if st, err := os.Lstat(filepath.Join(sess, "orchestrator.json")); err != nil || st.Mode().Type()&os.ModeSymlink == 0 {
		t.Fatalf("the linked record was replaced: %v", err)
	}
}

func TestInitNowriteRefusedWithoutState(t *testing.T) {
	env, repo := commandFixture(t)
	env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo))
	env["SOHO_NOWRITE"] = "1"
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := Run([]string{"init"}, env); code != 2 || !strings.Contains(stderr.String(), "SOHO_NOWRITE=1 is read-only") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "ws")); !os.IsNotExist(err) {
		t.Fatalf("NOWRITE init must not initialize state: %v", err)
	}
}

// hcRecord is one fake-hc terminal record.
func hcRecord(address, name, cwd, app, session string) string {
	rec := map[string]any{
		"address": address,
		"name":    name,
		"cwd":     cwd,
		"status":  map[string]any{"state": "working", "app": app},
		"labels":  map[string]any{"soho.session": session},
	}
	data, _ := json.Marshal(rec)
	return string(data)
}

// hcFixture installs the fake hc (and a logging-only fake herdr) and
// returns the env pointed at the caller terminal.
func hcFixture(t *testing.T, env platform.Env, repo, caller string) (platform.Env, string) {
	t.Helper()
	fakeDir := t.TempDir()
	t1 := hcRecord("t1", "orchestrator", repo, "claude", "ws")
	t2 := hcRecord("t2", "worker", repo, "codex", "ws")
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: t1},
		{Argv: []string{"get", "t2", "--json"}, Stdout: t2},
		{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[` + t1 + `,` + t2 + `]}`},
	}
	if _, err := fakecli.Install(t, fakeDir, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, fakeDir, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env = withHostGit(t, env, fakeDir)
	env["SOHO_TRANSPORT"] = "hc"
	env["HC_TERMINAL_ID"] = caller
	return env, fakeDir
}

func herdrCallCount(t *testing.T, fakeDir string) int {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(fakeDir, "herdr.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read the herdr calls: %v", err)
	}
	return len(calls)
}

func TestHcInitRegistersActualNameAndKindThroughFakeHc(t *testing.T) {
	env, repo := commandFixture(t)
	env, fakeDir := hcFixture(t, env, repo, "t1")
	state := env["SOHO_DIR"]
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	if !strings.Contains(out.String(), `"orchestrator": "orchestrator"`) || !strings.Contains(out.String(), `"pane_id": "t1"`) || !strings.Contains(out.String(), `"workspace_id": "ws"`) || !strings.Contains(out.String(), `"title": ""`) {
		t.Fatalf("the HC init JSON changed: %s (stderr %s)", out.String(), stderr.String())
	}
	rec := readRecordJSON(t, state, "ws")
	assertRecordFields(t, rec, recordJSON{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "t1", Name: "orchestrator", Kind: "claude", Task: filepath.Base(repo)})
	// Only the minimum base/session is created: no roster, no reports.
	sess := filepath.Join(state, "ws")
	for _, absent := range []string{"agents.tsv", "reports", "briefs", "wait"} {
		if _, err := os.Stat(filepath.Join(sess, absent)); !os.IsNotExist(err) {
			t.Fatalf("the HC init created %s; only the minimum base/session may be initialized", absent)
		}
	}
	// No Herdr call: the HC path never reaches the Herdr CLI.
	if n := herdrCallCount(t, fakeDir); n != 0 {
		t.Fatalf("the HC init made %d herdr calls; it must make none", n)
	}
}

func TestHcReinitAndReplacementThroughFakeHc(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	state := env["SOHO_DIR"]
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	first := readRecordJSON(t, state, "ws")
	// Same-terminal re-init preserves the first start.
	var out2, stderr2 strings.Builder
	runAndCapture(t, env, []string{"init"}, &out2, &stderr2)
	reinit := readRecordJSON(t, state, "ws")
	if reinit.StartedMS != first.StartedMS || reinit.Terminal != "t1" {
		t.Fatalf("the HC re-init changed the start or terminal: %+v want start %d", reinit, first.StartedMS)
	}
	// A replacement terminal keeps the session start, takes the new
	// identity and the default objective.
	env2 := cloneEnv(env)
	env2["HC_TERMINAL_ID"] = "t2"
	var out3, stderr3 strings.Builder
	runAndCapture(t, env2, []string{"init"}, &out3, &stderr3)
	replaced := readRecordJSON(t, state, "ws")
	if replaced.StartedMS != first.StartedMS {
		t.Fatalf("the replacement lost the first start: %d want %d", replaced.StartedMS, first.StartedMS)
	}
	if replaced.Terminal != "t2" || replaced.Name != "worker" || replaced.Kind != "codex" || replaced.Task != filepath.Base(repo) {
		t.Fatalf("the replacement identity or objective is wrong: %+v", replaced)
	}
}

func TestHcTitleUpdatesMatchingRecordThroughFakeHc(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	state := env["SOHO_DIR"]
	var out, stderr strings.Builder
	runAndCapture(t, env, []string{"init"}, &out, &stderr)
	var out2, stderr2 strings.Builder
	runAndCapture(t, env, []string{"title", "New", "goal"}, &out2, &stderr2)
	if !strings.Contains(out2.String(), `"title": "New goal"`) {
		t.Fatalf("the HC title output changed: %s (stderr %s)", out2.String(), stderr2.String())
	}
	if rec := readRecordJSON(t, state, "ws"); rec.Task != "New goal" {
		t.Fatalf("the HC title stored %q, want the objective", rec.Task)
	}
	var out3, stderr3 strings.Builder
	runAndCapture(t, env, []string{"title", "--clear"}, &out3, &stderr3)
	if !strings.Contains(out3.String(), `"title": ""`) {
		t.Fatalf("the HC clear output changed: %s (stderr %s)", out3.String(), stderr3.String())
	}
	if cleared := readRecordJSON(t, state, "ws"); cleared.Task != "" {
		t.Fatalf("the HC clear stored %q, want empty", cleared.Task)
	}
}

func TestHcTitleRefusalsCreateNoState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       string // "", "absent", "mismatch", "corrupt"
		wantStderr string
	}{
		{name: "absent", seed: "absent", wantStderr: "no initialized orchestrator record"},
		{name: "mismatch", seed: "mismatch", wantStderr: "does not match this session's current terminal"},
		{name: "corrupt", seed: "corrupt", wantStderr: "the orchestrator record is unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := commandFixture(t)
			env, _ = hcFixture(t, env, repo, "t1")
			state := env["SOHO_DIR"]
			if tc.seed != "absent" {
				sess := filepath.Join(state, "ws")
				if err := os.MkdirAll(sess, 0o755); err != nil {
					t.Fatal(err)
				}
				if tc.seed == "mismatch" {
					seed, _ := json.Marshal(recordJSON{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "t0", Name: "orchestrator", Kind: "claude", StartedMS: 1, Task: "old"})
					if err := os.WriteFile(filepath.Join(sess, "orchestrator.json"), seed, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(sess, "orchestrator.json"), []byte("{not json"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr strings.Builder
			platform.Stdout, platform.Stderr = &out, &stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			code := Run([]string{"title", "goal"}, env)
			if code != 4 {
				t.Fatalf("the refused HC title exited %d; it must fail explicitly (stderr %s)", code, stderr.String())
			}
			if out.Len() != 0 {
				t.Fatalf("the refused HC title printed success: %s", out.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr %s does not name %q", stderr.String(), tc.wantStderr)
			}
			if tc.seed == "absent" {
				if _, err := os.Stat(filepath.Join(state, "ws")); !os.IsNotExist(err) {
					t.Fatalf("the absent-record title must not initialize the session state: %v", err)
				}
			}
		})
	}
}

func TestHcInitRefusalsBeforeAnyStateWrite(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lsBody     string
		worker     bool
		wantStderr string
	}{
		{name: "caller not live", lsBody: "T2", wantStderr: "was not observed live"},
		{name: "roster worker", lsBody: "BOTH", worker: true, wantStderr: "worker row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := commandFixture(t)
			state := env["SOHO_DIR"]
			fakeDir := t.TempDir()
			t1 := hcRecord("t1", "orchestrator", repo, "claude", "ws")
			t2 := hcRecord("t2", "worker", repo, "codex", "ws")
			ls := `{"terminals":[` + t1 + `,` + t2 + `]}`
			if tc.lsBody == "T2" {
				ls = `{"terminals":[` + t2 + `]}`
			}
			rules := []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: t1},
				{Argv: []string{"ls", "--json"}, Stdout: ls},
			}
			if _, err := fakecli.Install(t, fakeDir, "hc", rules); err != nil {
				t.Fatal(err)
			}
			env = withFakeCLI(env, fakeDir)
			env = withHostGit(t, env, fakeDir)
			env["SOHO_TRANSPORT"] = "hc"
			env["HC_TERMINAL_ID"] = "t1"
			if tc.worker {
				sess := filepath.Join(state, "ws")
				if err := os.MkdirAll(sess, 0o755); err != nil {
					t.Fatal(err)
				}
				rows := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker1\tt1\tclaude\timplementer\n"
				if err := os.WriteFile(filepath.Join(sess, "agents.tsv"), []byte(rows), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr strings.Builder
			platform.Stdout, platform.Stderr = &out, &stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			code := Run([]string{"init"}, env)
			if code != 2 {
				t.Fatalf("the refused HC init exited %d; it must fail explicitly (stderr %s)", code, stderr.String())
			}
			if out.Len() != 0 {
				t.Fatalf("the refused HC init printed success: %s", out.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr %s does not name %q", stderr.String(), tc.wantStderr)
			}
			if _, err := os.Stat(filepath.Join(state, "ws", "orchestrator.json")); !os.IsNotExist(err) {
				t.Fatalf("the refused HC init created a record: %v", err)
			}
		})
	}
}

func TestHcInitNowriteRefused(t *testing.T) {
	env, _ := commandFixture(t)
	env, _ = hcFixture(t, env, "", "t1")
	env["SOHO_NOWRITE"] = "1"
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := Run([]string{"init"}, env); code != 2 || !strings.Contains(stderr.String(), "SOHO_NOWRITE=1 is read-only") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

// herdrInitFixtureOver installs the fake herdr over an already built
// fixture env.
func herdrInitFixtureOver(t *testing.T, env platform.Env, repo, agentListBody string) platform.Env {
	t.Helper()
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", herdrInitRules(repo, agentListBody)); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env = withHostGit(t, env, fakeDir)
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "p1"
	return env
}

// withHostGit appends the host's git directory to the fake-CLI PATH so the
// strict canonical root runs real read-only Git in the fixture repo; the
// fake-CLI PATH otherwise holds only the fake binaries.
func withHostGit(t *testing.T, env platform.Env, fakeDir string) platform.Env {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("the host has no git: %v", err)
	}
	env["PATH"] = fakeDir + string(filepath.ListSeparator) + filepath.Dir(gitPath)
	return env
}

func cloneEnv(in platform.Env) platform.Env {
	out := make(platform.Env, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func runInitCaptured(t *testing.T, env platform.Env) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run([]string{"init"}, env)
	return code, out.String(), stderr.String()
}

func TestInitMalformedRosterCannotReplaceOrchestrator(t *testing.T) {
	for _, host := range []string{"herdr", "hc"} {
		for _, shape := range []string{"complete", "pane-only", "missing-role", "empty-name", "unresolved-name-only"} {
			t.Run(host+"/"+shape, func(t *testing.T) {
				env, repo := commandFixture(t)
				pane := "p1"
				if host == "herdr" {
					env = herdrInitFixtureOver(t, env, repo, herdrAgentEntry("orchestrator", pane, "claude", "ws", repo))
				} else {
					env, _ = hcFixture(t, env, repo, "t1")
					pane = "t1"
				}
				sess := filepath.Join(env["SOHO_DIR"], "ws")
				if err := os.MkdirAll(sess, 0o700); err != nil {
					t.Fatal(err)
				}
				row := "worker1\t" + pane
				switch shape {
				case "complete":
					row += "\tclaude\timplementer"
				case "missing-role":
					row += "\tclaude"
				case "empty-name":
					row = "\t" + pane
				case "unresolved-name-only":
					row = "worker1"
				}
				if err := os.WriteFile(filepath.Join(sess, "agents.tsv"), []byte(row+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				seed, err := json.Marshal(recordJSON{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "p0", Name: "orchestrator", Kind: "claude", StartedMS: 42, Task: "preserve"})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(sess, "orchestrator.json")
				if err := os.WriteFile(path, seed, 0o600); err != nil {
					t.Fatal(err)
				}
				code, out, stderr := runInitCaptured(t, env)
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(seed) {
					t.Fatalf("roster caller replaced record: err=%v after=%s exit=%d stderr=%s", err, after, code, stderr)
				}
				if host == "hc" && (code != 2 || out != "") {
					t.Fatalf("HC refusal exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if host == "herdr" && (code != 0 || !strings.Contains(out, `"pane_id": "p1"`)) {
					t.Fatalf("Herdr auxiliary refusal exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if shape == "unresolved-name-only" && !strings.Contains(stderr, "roster is unreadable") {
					t.Fatalf("unresolved roster must refuse explicitly: %s", stderr)
				}
			})
		}
	}
}

func TestHcInitValidatesRecordBeforeCreatingState(t *testing.T) {
	for _, name := range []string{"owner α", "", "bad\nname", "bad\rname", "bad\tname", "bad\x00name", "bad\u0085name"} {
		t.Run(name, func(t *testing.T) {
			env, repo := commandFixture(t)
			missing := filepath.Join(env["SOHO_DIR"], "missing")
			env["SOHO_DIR"] = filepath.Join(missing, "base")
			fakeDir := t.TempDir()
			rec := hcRecord("t1", name, repo, "claude", "ws")
			if _, err := fakecli.Install(t, fakeDir, "hc", []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: rec}, {Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[` + rec + `]}`}}); err != nil {
				t.Fatal(err)
			}
			if _, err := fakecli.Install(t, fakeDir, "herdr", nil); err != nil {
				t.Fatal(err)
			}
			env = withHostGit(t, withFakeCLI(env, fakeDir), fakeDir)
			env["SOHO_TRANSPORT"], env["HC_TERMINAL_ID"] = "hc", "t1"
			code, out, stderr := runInitCaptured(t, env)
			if name == "owner α" {
				if code != 0 {
					t.Fatalf("valid-name control exit=%d stderr=%s", code, stderr)
				}
				if got := readRecordJSON(t, env["SOHO_DIR"], "ws"); got.Name != name {
					t.Fatalf("actual HC name=%q want=%q", got.Name, name)
				}
			} else {
				if code != 2 || out != "" {
					t.Fatalf("invalid-name refusal exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if _, err := os.Stat(missing); !os.IsNotExist(err) {
					t.Fatalf("invalid-name refusal initialized a state ancestor: %v", err)
				}
			}
			if n := herdrCallCount(t, fakeDir); n != 0 {
				t.Fatalf("HC init made %d Herdr calls", n)
			}
		})
	}
}

// runAndCapture runs one command with the platform writers captured and
// requires the success exit.
func runAndCapture(t *testing.T, env platform.Env, args []string, out, stderr *strings.Builder) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = out, stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := Run(args, env); code != 0 {
		t.Fatalf("soho %v exited %d (stderr %s)", args, code, stderr.String())
	}
}
