package inventory

// Focused consumer tests of the read-only collector: the real backend.New
// seams with bounded fake herdr binaries, real local Git fixtures through
// the reference Git runner, and independent expected assertions (no
// mirrored implementation-generated JSON).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) {
	// RunTests re-executes the fake CLI, runs the package and exits with
	// its code.
	fakecli.RunTests(m)
}

// --- fixture helpers -----------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	canon, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canon
}

// gitFixtureHome is the hermetic HOME the fixture Git runs under.
func gitFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func git(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("the host has no git: %v", err)
	}
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"HOME=" + home,
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitRepo makes a real local repository with one commit; worktree, when
// nonempty, is added as a linked worktree of it.
func gitRepo(t *testing.T, home, repo, worktree string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, home, repo, "init")
	git(t, home, repo, "config", "user.name", "fixture")
	git(t, home, repo, "config", "user.email", "fixture@example.invalid")
	writeFile(t, filepath.Join(repo, "README.md"), "fixture project\n")
	git(t, home, repo, "add", ".")
	git(t, home, repo, "commit", "-m", "fixture")
	if worktree != "" {
		git(t, home, repo, "worktree", "add", worktree)
	}
}

// hostGitDir returns the directory that holds the host git, or skips.
func hostGitDir(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("the host has no git: %v", err)
	}
	return filepath.Dir(gitBin)
}

// inventoryFixture builds the hermetic collector environment: a pinned
// HOME/USERPROFILE/XDG_CONFIG_HOME/TMPDIR and a PATH that a test extends
// with its fake CLI and the host git. It returns the environment, the
// fixture root and the hermetic home.
func inventoryFixture(t *testing.T) (platform.Env, string, string) {
	t.Helper()
	fixture := t.TempDir()
	home := gitFixtureHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"TMPDIR":          fixture,
	}
	return env, fixture, home
}

// withFakeHerdr installs the fake herdr in a fresh directory on env's
// PATH (with the host git appended for the reference Git runner) and
// returns the extended env and the call log path.
func withFakeHerdr(t *testing.T, env platform.Env, rules []fakecli.Rule) (platform.Env, string) {
	t.Helper()
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	return fakeEnv(t, env, fakeDir), filepath.Join(fakeDir, "herdr.calls.jsonl")
}

// fakeEnv rebuilds env through fakecli's environment handling: the fake
// directory first on PATH, the host git directory after it, and the
// fakecli config environment set.
func fakeEnv(t *testing.T, env platform.Env, fakeDir string) platform.Env {
	t.Helper()
	list := fakecli.Env(env.List(), fakeDir, fakecli.EnvOptions{SystemPath: hostGitDir(t)})
	out := platform.Env{}
	for _, item := range list {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func herdrAgentEntry(name, pane, agent, session, cwd string) string {
	body, _ := json.Marshal(map[string]string{
		"name": name, "pane_id": pane, "agent": agent, "workspace_id": session, "cwd": cwd,
	})
	return string(body)
}

func herdrList(t *testing.T, entries ...string) fakecli.Rule {
	t.Helper()
	return fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + strings.Join(entries, ",") + `]}}`}
}

func herdrGet(name, status string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "get", name}, Stdout: `{"result":{"agent":{"agent_status":"` + status + `","state_change_seq":7}}}`}
}

func herdrReadVisible(name, screen string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "read", name, "--source", "visible"}, Stdout: screen}
}

// rosterRow renders one full spawn-format roster row (16 columns) from the
// named fields; the remaining columns are empty.
func rosterRow(name, pane, kind, role, started, model, effort string) string {
	f := make([]string, 16)
	f[0], f[1], f[2], f[3], f[4], f[5], f[6] = name, pane, kind, role, "claude", "1", ""
	f[7], f[8], f[9], f[14], f[15] = started, model, "auto", effort, ""
	return strings.Join(f, "\t")
}

// writeState plants one session's state directory under the canonical
// project root: the roster, the optional orchestrator record (JSON
// fields), the task title files and the report pointer with its report.
func writeState(t *testing.T, root, session, roster string, record map[string]any, taskFiles, reportFiles map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, ".soho", session)
	if roster != "" {
		writeFile(t, filepath.Join(dir, "agents.tsv"), roster)
	}
	if record != nil {
		body := string(mustMarshal(t, record))
		writeFile(t, filepath.Join(dir, "orchestrator.json"), body)
	}
	for name, title := range taskFiles {
		writeFile(t, filepath.Join(dir, "task-"+name), title)
	}
	for name, content := range reportFiles {
		report := filepath.Join(dir, "reports", name)
		writeFile(t, report, content)
		writeFile(t, filepath.Join(dir, "last-report-"+name), report+"\n")
	}
	return dir
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// readCalls returns the fake CLI's recorded calls, or an empty slice when
// the log is absent.
func readCalls(t *testing.T, log string) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read the call log: %v", err)
	}
	return calls
}

func countCalls(calls []fakecli.Call, prefix ...string) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) >= len(prefix) {
			ok := true
			for i, p := range prefix {
				if c.Argv[i] != p {
					ok = false
					break
				}
			}
			if ok {
				n++
			}
		}
	}
	return n
}

// collect runs Collect with the fixture's version and time.
func collect(t *testing.T, env platform.Env, generatedMS int64) (Document, error) {
	t.Helper()
	return Collect(Request{
		Env:         env,
		Version:     "v0.9-test",
		User:        core.UserSelection{},
		GeneratedMS: generatedMS,
	})
}

// startedStamp is the deterministic zone-less spawn stamp of a fixed local
// instant; ParseStartedMS must recover its exact milliseconds.
var startedInstant = time.Date(2026, 10, 31, 8, 30, 0, 0, time.Local)

func startedStamp() string { return stampOf(startedInstant) }
func startedMS() int64     { return startedInstant.UnixMilli() }
func stampOf(instant time.Time) string {
	return instant.Format("20060102T150405")
}

// --- tests ----------------------------------------------------------------

// TestInventoryCollectShowDocument: the real herdr backend seam over a
// real Git repo with a linked worktree. The orchestrator record is
// verified live and listed first; the roster workers bind exactly by
// pane, name, kind, canonical root and session; the multiple cwds group
// under one canonical project; the machine discovery makes no caller
// call and the snapshot runtime issues the unfiltered list exactly once.
func TestInventoryCollectShowDocument(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	work := filepath.Join(fixture, "work")
	gitRepo(t, home, repo, work)
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\ninventory_name=acme/web\n")
	sub := filepath.Join(canon, "sub")
	writeFile(t, filepath.Join(sub, "sub.txt"), "sub\n")
	orchMS := int64(1793372400000)
	writeState(t, canon, "ws1",
		"# roster\n"+
			rosterRow("build", "p2", "claude", "implementer", startedStamp(), "example/model-1", "high")+"\n"+
			rosterRow("review", "p3", "codex", "reviewer", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws1",
			"terminal": "p1", "name": "orchestrator", "kind": "claude",
			"started_ms": orchMS, "task": "Run the checkout work",
		},
		map[string]string{"build": "implementer: Fix the checkout ✓\n"},
		nil,
	)
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("orchestrator", "p1", "claude", "ws1", canon),
			herdrAgentEntry("build", "p2", "claude", "ws1", sub),
			herdrAgentEntry("review", "p3", "codex", "ws1", work),
		),
		herdrGet("orchestrator", "working"),
		herdrReadVisible("orchestrator", "planning the next move\n"),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
		herdrGet("review", "idle"),
		fakecli.Rule{Argv: []string{"agent", "read", "review", "--source", "visible", "--lines", "20"}, Stdout: "waiting\n"},
		fakecli.Rule{Argv: []string{"agent", "read", "review", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "waiting\n"},
	}
	fakeEnv, callsLog := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if doc.Schema != 1 || doc.SohoVersion != "v0.9-test" || doc.GeneratedMS != 1793376000000 {
		t.Fatalf("top level = %+v, want schema 1, the version and the generated time", doc)
	}
	if doc.Truncated {
		t.Fatalf("truncated = true, want the omitted key for %d agents", len(doc.Projects))
	}
	if len(doc.Projects) != 1 {
		t.Fatalf("projects = %+v, want the one canonical project (cwd and worktree grouped)", doc.Projects)
	}
	p := doc.Projects[0]
	if p.Name != "acme/web" || len(p.Sessions) != 1 || p.Sessions[0].ID != "ws1" {
		t.Fatalf("project = %+v, want the inventory_name and the one session", p)
	}
	agents := p.Sessions[0].Agents
	if len(agents) != 3 {
		t.Fatalf("agents = %+v, want the orchestrator plus the two live workers", agents)
	}
	orch := agents[0]
	if orch.Terminal != "p1" || orch.Name != "orchestrator" || orch.Role != "orchestrator" || orch.Kind != "claude" {
		t.Fatalf("orchestrator identity = %+v, want the verified record binding", orch)
	}
	if orch.Model != nil || orch.Effort != nil || orch.ReportReady || orch.SinceMS != nil {
		t.Fatalf("orchestrator optional fields = %+v, want null model/effort, no report-ready and null since", orch)
	}
	if orch.StartedMS != orchMS || orch.Task == nil || *orch.Task != "Run the checkout work" {
		t.Fatalf("orchestrator start/task = %d %v, want the record start and its literal objective", orch.StartedMS, orch.Task)
	}
	if orch.State != StateWorking || orch.Reason != nil {
		t.Fatalf("orchestrator state = %s %v, want working", orch.State, orch.Reason)
	}
	build := agents[1]
	if build.Terminal != "p2" || build.Name != "build" || build.Role != "implementer" || build.Kind != "claude" {
		t.Fatalf("build identity = %+v, want the exact roster binding", build)
	}
	if build.Model == nil || *build.Model != "example/model-1" || build.Effort == nil || *build.Effort != "high" {
		t.Fatalf("build model/effort = %v %v, want the persisted columns", build.Model, build.Effort)
	}
	if build.Task == nil || *build.Task != "Fix the checkout" {
		t.Fatalf("build task = %v, want the role prefix and done mark removed", build.Task)
	}
	if build.StartedMS != startedMS() || build.ReportReady || build.SinceMS != nil {
		t.Fatalf("build start/report/since = %d %v %v, want the parsed stamp, no report and null since", build.StartedMS, build.ReportReady, build.SinceMS)
	}
	if build.State != StateWorking {
		t.Fatalf("build state = %s, want working", build.State)
	}
	review := agents[2]
	if review.Terminal != "p3" || review.Name != "review" || review.Role != "reviewer" || review.Kind != "codex" {
		t.Fatalf("review identity = %+v, want the worktree worker under the same project", review)
	}
	if review.Model != nil || review.Effort != nil || review.Task != nil {
		t.Fatalf("review optionals = %v %v %v, want explicit nulls", review.Model, review.Effort, review.Task)
	}
	if review.State != StateIdle || review.Reason != nil {
		t.Fatalf("review state = %s %v, want idle", review.State, review.Reason)
	}
	if doc.Hidden != (Counts{}) {
		t.Fatalf("hidden = %+v, want all five zeros", doc.Hidden)
	}
	// The document's JSON carries the explicit nulls and omits truncated.
	body := string(mustMarshal(t, doc))
	for _, want := range []string{`"model":null`, `"effort":null`, `"reason":null`, `"since_ms":null`, `"task":null`, `"report_ready":false`, `"schema":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the document JSON lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"truncated"`) {
		t.Fatalf("the document JSON must omit truncated when false:\n%s", body)
	}
	// Machine discovery: no caller/current-pane call, and the unfiltered
	// list was issued exactly once (the snapshot runtime answers the
	// inspection's stale-name listing from the original snapshot).
	calls := readCalls(t, callsLog)
	if got := countCalls(calls, "agent", "list"); got != 1 {
		t.Fatalf("agent list calls = %d, want exactly one (the snapshot runtime)", got)
	}
	for _, c := range calls {
		for _, arg := range c.Argv {
			if arg == "current" || arg == "caller" {
				t.Fatalf("the discovery made a caller call: %v", c.Argv)
			}
		}
	}
}

// TestInventoryCollectOffSkipsPrivateInputs: a project in off mode
// delivers nothing, and the collector stops BEFORE any session.conf,
// roster, private record, status or screen read: the fake herdr sees only
// the machine listing, and the poisoned private inputs never leave.
func TestInventoryCollectOffSkipsPrivateInputs(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=off\n")
	writeState(t, canon, "ws1",
		"# roster\n"+rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws1",
			"terminal": "p1", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "SECRET-ORCHESTRATOR-TASK",
		},
		map[string]string{"build": "implementer: SECRET-BUILD-TITLE\n"},
		map[string]string{"build-report": "SECRET-REPORT-BODY\n"},
	)
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("orchestrator", "p1", "claude", "ws1", canon),
			herdrAgentEntry("build", "p2", "claude", "ws1", canon),
		),
		herdrGet("orchestrator", "working"),
		herdrReadVisible("orchestrator", "SECRET-SCREEN\n"),
		herdrGet("build", "working"),
		herdrReadVisible("build", "SECRET-SCREEN\n"),
	}
	fakeEnv, callsLog := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	body := string(mustMarshal(t, doc))
	if strings.Contains(body, "SECRET") {
		t.Fatalf("the off-mode document leaked private content:\n%s", body)
	}
	if len(doc.Projects) != 0 || doc.Hidden != (Counts{}) {
		t.Fatalf("off-mode document = %+v, want no projects and zero counts", doc)
	}
	// The stop BEFORE the private reads: only the machine listing ran.
	calls := readCalls(t, callsLog)
	if got := len(calls); got != 1 || len(calls[0].Argv) != 2 || calls[0].Argv[0] != "agent" || calls[0].Argv[1] != "list" {
		t.Fatalf("calls = %+v, want exactly the one machine listing", calls)
	}
}

// TestInventoryCollectNoOptInDeliversNothing: without a project key the
// user and environment layers may only lower, never raise: a user or
// environment show still delivers nothing.
func TestInventoryCollectNoOptInDeliversNothing(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeState(t, canon, "ws1",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	rules := []fakecli.Rule{
		herdrList(t, herdrAgentEntry("build", "p2", "claude", "ws1", canon)),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)

	t.Run("user_show_cannot_raise", func(t *testing.T) {
		userBase := filepath.Join(fixture, "userbase")
		writeFile(t, filepath.Join(userBase, "soho", "config"), "inventory=show\n")
		doc, err := Collect(Request{Env: fakeEnv, Version: "v0.9-test", User: core.UserSelection{Base: userBase}, GeneratedMS: 1})
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(doc.Projects) != 0 || doc.Hidden != (Counts{}) {
			t.Fatalf("user-raised document = %+v, want nothing", doc)
		}
	})

	t.Run("env_show_cannot_raise", func(t *testing.T) {
		raised := fakeEnv.Clone()
		raised["SOHO_INVENTORY"] = "show"
		doc, err := collect(t, raised, 1)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(doc.Projects) != 0 || doc.Hidden != (Counts{}) {
			t.Fatalf("env-raised document = %+v, want nothing", doc)
		}
	})
}

// TestInventoryCollectLowerOnly: an opted-in project may only be lowered,
// by the environment or the user and session layers, never raised.
func TestInventoryCollectLowerOnly(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	writeState(t, canon, "ws1",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	rules := []fakecli.Rule{
		herdrList(t, herdrAgentEntry("build", "p2", "claude", "ws1", canon)),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)

	t.Run("user_hidden_lowers_show", func(t *testing.T) {
		userBase := filepath.Join(fixture, "userbase")
		writeFile(t, filepath.Join(userBase, "soho", "config"), "inventory=hidden\n")
		doc, err := Collect(Request{Env: fakeEnv, Version: "v0.9-test", User: core.UserSelection{Base: userBase}, GeneratedMS: 1})
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(doc.Projects) != 0 {
			t.Fatalf("the lowered project must not appear: %+v", doc.Projects)
		}
		if doc.Hidden.Working != 1 {
			t.Fatalf("hidden counts = %+v, want the one working worker counted", doc.Hidden)
		}
	})

	t.Run("env_off_lowers_show", func(t *testing.T) {
		off := fakeEnv.Clone()
		off["SOHO_INVENTORY"] = "off"
		doc, err := collect(t, off, 1)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(doc.Projects) != 0 || doc.Hidden != (Counts{}) {
			t.Fatalf("env-off document = %+v, want nothing", doc)
		}
	})

	t.Run("session_cannot_raise_hidden", func(t *testing.T) {
		writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=hidden\n")
		writeFile(t, filepath.Join(canon, ".soho", "ws1", "session.conf"), "inventory=show\n")
		doc, err := collect(t, fakeEnv, 1)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(doc.Projects) != 0 {
			t.Fatalf("the session layer raised the mode: %+v", doc.Projects)
		}
		if doc.Hidden.Working != 1 {
			t.Fatalf("hidden counts = %+v, want the worker still counted hidden", doc.Hidden)
		}
	})

	t.Run("session_off_lowers_one_session", func(t *testing.T) {
		writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
		// ws1 lowers to off, ws2 stays show.
		writeState(t, canon, "ws2",
			rosterRow("review", "p3", "codex", "reviewer", startedStamp(), "", "")+"\n",
			nil, nil, nil,
		)
		writeFile(t, filepath.Join(canon, ".soho", "ws1", "session.conf"), "inventory=off\n")
		extra := []fakecli.Rule{
			herdrList(t,
				herdrAgentEntry("build", "p2", "claude", "ws1", canon),
				herdrAgentEntry("review", "p3", "codex", "ws2", canon),
			),
			herdrGet("review", "idle"),
			fakecli.Rule{Argv: []string{"agent", "read", "review", "--source", "visible", "--lines", "20"}, Stdout: "waiting\n"},
			fakecli.Rule{Argv: []string{"agent", "read", "review", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "waiting\n"},
		}
		fakeEnv2, _ := withFakeHerdr(t, fakeEnv, extra)
		// The first install's log is superseded by the second; rebuild the
		// env over the fresh fixture directory.
		doc, err := collect(t, fakeEnv2, 1)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 {
			t.Fatalf("projects = %+v, want only the ws2 session", doc.Projects)
		}
		if doc.Projects[0].Sessions[0].ID != "ws2" || len(doc.Projects[0].Sessions[0].Agents) != 1 {
			t.Fatalf("session = %+v, want ws2 with its one worker", doc.Projects[0].Sessions)
		}
		if doc.Projects[0].Name != filepath.Base(canon) {
			t.Fatalf("project name = %q, want the canonical basename without an inventory_name", doc.Projects[0].Name)
		}
	})
}

// TestInventoryCollectHiddenCounts: hidden projects contribute each
// eligible agent to exactly one of the five counts, across the folded
// states, and nothing else leaves: no names, sessions or titles. The
// CRLF malformed-last roster row is excluded from the counts (its older
// valid duplicate is not resurrected), and the freshly gone worker is
// omitted even with a report.
func TestInventoryCollectHiddenCounts(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repoA := filepath.Join(fixture, "repoA")
	repoB := filepath.Join(fixture, "repoB")
	gitRepo(t, home, repoA, "")
	gitRepo(t, home, repoB, "")
	canonA, canonB := canonical(t, repoA), canonical(t, repoB)
	writeFile(t, filepath.Join(repoA, ".agents", "soho.conf"), "inventory=hidden\ninventory_name=must-not-leave\n")
	writeFile(t, filepath.Join(repoB, ".agents", "soho.conf"), "inventory=hidden\n")
	// A: the verified orchestrator (working) and an idle worker.
	writeState(t, canonA, "wsA",
		rosterRow("w1", "a2", "claude", "implementer", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": canonA, "session_id": "wsA",
			"terminal": "a1", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "A-SECRET-TASK",
		},
		map[string]string{"w1": "implementer: A-SECRET-W1\n"},
		nil,
	)
	// B: needs_you (not-received marker on an idle probe), done (a
	// nonempty report), unknown (a failed probe), the CRLF malformed-last
	// exclusion and the gone worker with a report.
	writeState(t, canonB, "wsB",
		rosterRow("w2", "b2", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("w3", "b3", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("w4", "b4", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("w5", "b5", "claude", "implementer", startedStamp(), "", "")+"\r\n"+
			"w5\r\n"+
			rosterRow("ghost", "b6", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil,
		map[string]string{"w2": "implementer: B-SECRET-W2\n", "w3": "implementer: B-SECRET-W3\n", "ghost": "implementer: B-SECRET-GHOST\n"},
		map[string]string{"w3": "B-SECRET-W3-REPORT\n", "ghost": "B-SECRET-GHOST-REPORT\n"},
	)
	if err := os.MkdirAll(filepath.Join(canonB, ".soho", "wsB", "wait"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(canonB, ".soho", "wsB", "wait", "w2.not-received"), "1793376000\n")
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("orchestrator", "a1", "claude", "wsA", canonA),
			herdrAgentEntry("w1", "a2", "claude", "wsA", canonA),
			herdrAgentEntry("w2", "b2", "claude", "wsB", canonB),
			herdrAgentEntry("w3", "b3", "claude", "wsB", canonB),
			herdrAgentEntry("w4", "b4", "claude", "wsB", canonB),
			herdrAgentEntry("w5", "b5", "claude", "wsB", canonB),
			herdrAgentEntry("ghost", "b6", "claude", "wsB", canonB),
		),
		herdrGet("orchestrator", "working"),
		herdrReadVisible("orchestrator", "planning\n"),
		herdrGet("w1", "idle"),
		herdrGet("w2", "idle"),
		herdrGet("w3", "done"),
		herdrGet("w5", "working"),
		herdrGet("ghost", "gone"),
		fakecli.Rule{Argv: []string{"agent", "read", "w5", "--source", "visible"}, Stdout: "typing\n"},
	}
	// w4's probe fails like an unreadable server: a failed get with a
	// non-JSON error code reads as unavailable.
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "w4"}, Code: 1, Stderr: `{"error":{"code":"probe_failed","message":"unavailable"}}`})
	// w3's done read: the report observation re-reads the state.
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	body := string(mustMarshal(t, doc))
	for _, secret := range []string{"must-not-leave", "wsA", "wsB", "B-SECRET", "A-SECRET", "ghost"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the hidden document leaked %q:\n%s", secret, body)
		}
	}
	if len(doc.Projects) != 0 {
		t.Fatalf("hidden projects must not appear: %+v", doc.Projects)
	}
	want := Counts{Working: 1, NeedsYou: 1, Done: 1, Idle: 1, Unknown: 1}
	if doc.Hidden != want {
		t.Fatalf("hidden counts = %+v, want %+v (the CRLF malformed-last and the gone worker excluded)", doc.Hidden, want)
	}
}

// TestInventoryCollectBindingAndPrecedence: the exact live bindings and
// the precedence rules: a roster row on the record's terminal suppresses
// the stale orchestrator record; a record without a live verified binding
// never invents an orchestrator; a roster row without its live terminal
// is omitted; the unknown state with a proven live identity stays
// unknown; the freshly gone worker is omitted even with a report.
func TestInventoryCollectBindingAndPrecedence(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	// ws1: the record points at the worker's terminal p2 -> suppressed.
	writeState(t, canon, "ws1",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws1",
			"terminal": "p2", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "stale",
		},
		nil, nil,
	)
	// ws2: the record binds p1 (no roster row on p1) and a sub-
	// orchestrator row binds p4.
	writeState(t, canon, "ws2",
		rosterRow("assist", "p4", "claude", "sub-orchestrator", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws2",
			"terminal": "p1", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "run ws2",
		},
		nil, nil,
	)
	// ws3: the record's terminal is not live.
	writeState(t, canon, "ws3",
		"",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws3",
			"terminal": "p9", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "stale",
		},
		nil, nil,
	)
	// ws4: the record's name does not match the live terminal's name.
	writeState(t, canon, "ws4",
		rosterRow("old-name", "p1", "claude", "implementer", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws4",
			"terminal": "p1", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "stale",
		},
		nil, nil,
	)
	// ws5: the gone worker with a report.
	writeState(t, canon, "ws5",
		rosterRow("ghost", "p5", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil,
		map[string]string{"ghost": "implementer: ghost task\n"},
		map[string]string{"ghost-report": "ghost report body\n"},
	)
	// ws6: the unknown probe with a proven live identity.
	writeState(t, canon, "ws6",
		rosterRow("weird", "p6", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("build", "p2", "claude", "ws1", canon),
			herdrAgentEntry("orchestrator", "p1", "claude", "ws2", canon),
			herdrAgentEntry("assist", "p4", "claude", "ws2", canon),
			herdrAgentEntry("ghost", "p5", "claude", "ws5", canon),
			herdrAgentEntry("weird", "p6", "claude", "ws6", canon),
		),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
		herdrGet("orchestrator", "working"),
		herdrReadVisible("orchestrator", "planning\n"),
		herdrGet("assist", "working"),
		herdrReadVisible("assist", "typing\n"),
		herdrGet("ghost", "gone"),
		fakecli.Rule{Argv: []string{"agent", "get", "weird"}, Code: 1, Stderr: `{"error":{"code":"probe_failed","message":"unreadable"}}`},
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(doc.Projects) != 1 {
		t.Fatalf("projects = %+v, want the one project", doc.Projects)
	}
	sessions := map[string][]Agent{}
	for _, s := range doc.Projects[0].Sessions {
		sessions[s.ID] = s.Agents
	}
	// ws1: the stale record is suppressed by the roster row on p2.
	if got := sessions["ws1"]; len(got) != 1 || got[0].Name != "build" || got[0].Role != "implementer" {
		t.Fatalf("ws1 = %+v, want only the worker (the record on p2 is stale)", sessions["ws1"])
	}
	// ws2: the verified orchestrator first, then the sub-orchestrator row.
	if got := sessions["ws2"]; len(got) != 2 || got[0].Role != "orchestrator" || got[0].Name != "orchestrator" || got[1].Name != "assist" || got[1].Role != "sub-orchestrator" {
		t.Fatalf("ws2 = %+v, want the orchestrator first then the roster row", sessions["ws2"])
	}
	// ws3 and ws4: no live verified binding, no invented orchestrator,
	// and the unbound roster row is omitted: the sessions are absent.
	if _, ok := sessions["ws3"]; ok {
		t.Fatalf("ws3 = %+v, want the session omitted (no live terminal for the record)", sessions["ws3"])
	}
	if _, ok := sessions["ws4"]; ok {
		t.Fatalf("ws4 = %+v, want the session omitted (the record name mismatches and the row is unbound)", sessions["ws4"])
	}
	// ws5: the freshly gone worker is omitted even with a report.
	if _, ok := sessions["ws5"]; ok {
		t.Fatalf("ws5 = %+v, want the session omitted (gone worker)", sessions["ws5"])
	}
	// ws6: the unknown state with a proven live identity stays unknown.
	if got := sessions["ws6"]; len(got) != 1 || got[0].Name != "weird" || got[0].State != StateUnknown || got[0].Reason != nil {
		t.Fatalf("ws6 = %+v, want the unknown agent without a reason", sessions["ws6"])
	}
}

// TestInventoryCollectSubOrchestratorReportReadyFalse: the strict reader
// refuses report_ready=true for BOTH orchestrator and sub-orchestrator
// roles. A verified roster sub-orchestrator with a nonempty safe current
// report keeps its recorded role and its shared effective state but emits
// report_ready=false; the ordinary worker with the same nonempty report
// keeps the resolver's ready observation.
func TestInventoryCollectSubOrchestratorReportReadyFalse(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	writeState(t, canon, "ws1",
		rosterRow("assist", "p1", "claude", "sub-orchestrator", startedStamp(), "", "")+"\n"+
			rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil,
		nil,
		map[string]string{
			"assist": "SUB-ORCH-CURRENT-REPORT\n",
			"build":  "WORKER-CURRENT-REPORT\n",
		},
	)
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("assist", "p1", "claude", "ws1", canon),
			herdrAgentEntry("build", "p2", "claude", "ws1", canon),
		),
		herdrGet("assist", "working"),
		herdrReadVisible("assist", "planning\n"),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 {
		t.Fatalf("projects = %+v, want the one session", doc.Projects)
	}
	agents := doc.Projects[0].Sessions[0].Agents
	if len(agents) != 2 {
		t.Fatalf("agents = %+v, want the sub-orchestrator and the worker", agents)
	}
	var sub, worker Agent
	for _, a := range agents {
		if a.Role == "sub-orchestrator" {
			sub = a
		} else if a.Role == "implementer" {
			worker = a
		}
	}
	if sub.Name != "assist" || worker.Name != "build" {
		t.Fatalf("roles = %+v, %+v, want the recorded roles kept", sub, worker)
	}
	// The nonempty report is observed by both: the shared effective state
	// is the same, and only the orchestrator role loses the ready flag.
	if sub.State != worker.State || sub.State != StateWorking {
		t.Fatalf("states = %s %s, want the shared effective state", sub.State, worker.State)
	}
	if sub.ReportReady {
		t.Fatalf("the sub-orchestrator with a ready report emitted report_ready=true: %+v", sub)
	}
	if !worker.ReportReady {
		t.Fatalf("the ordinary worker readiness was suppressed: %+v", worker)
	}
	// The strict-reader-facing literals: the same ready report flips the
	// flag only for the ordinary worker.
	body := string(mustMarshal(t, doc))
	for _, want := range []string{`"role":"sub-orchestrator"`, `"report_ready":true`, `"report_ready":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the document JSON lacks %s:\n%s", want, body)
		}
	}
}

// TestInventoryCollectCRLLastRow: the roster's CRLF line endings are
// shared with the normal reader's normalization before the last-name
// selection: a regular CRLF row keeps no CR in its fields, and the
// literal malformed-last bytes select the malformed last row, never
// resurrecting the older valid duplicate.
func TestInventoryCollectCRLLastRow(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	// Regular CRLF roster: one valid row.
	writeState(t, canon, "ws1",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "example/model-1", "")+"\r\n",
		nil, nil, nil,
	)
	// CRLF malformed-last: the older valid row must NOT be resurrected.
	writeState(t, canon, "ws2",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\r\n"+
			"build\r\n",
		nil, nil, nil,
	)
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("build", "p2", "claude", "ws1", canon),
		),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 {
		t.Fatalf("projects = %+v, want only the ws1 session (the CRLF malformed-last drops ws2)", doc.Projects)
	}
	sess := doc.Projects[0].Sessions[0]
	if sess.ID != "ws1" || len(sess.Agents) != 1 {
		t.Fatalf("session = %+v, want ws1 with its one worker", sess)
	}
	a := sess.Agents[0]
	if a.Model == nil || strings.ContainsRune(*a.Model, '\r') {
		t.Fatalf("model = %v, want the clean CRLF-normalized persisted column", a.Model)
	}
	if a.Name != "build" || a.Role != "implementer" || strings.ContainsRune(a.Role, '\r') {
		t.Fatalf("identity = %+v, want no CR in any required column", a)
	}
}

// TestInventoryCollectMissingAndUnsafeState: missing or unsafe roots,
// configs and state skip their terminal or session and never fall back to
// path readers or cwd guesses.
func TestInventoryCollectMissingAndUnsafeState(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	// Observed sessions:
	//   ws-live: a live verified binding (the control).
	//   ws-nostate: no state directory at all.
	//   ws-link: a symlinked session directory (unsafe acquisition).
	//   ws-big: an oversize roster (beyond the 4 MiB bound).
	//   ws-ambig: an ambiguous identity (two rows of the same name and
	//     pane).
	writeState(t, canon, "ws-live",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	if err := os.MkdirAll(filepath.Join(canon, ".soho", "ws-link-target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(canon, ".soho", "ws-link-target"), filepath.Join(canon, ".soho", "ws-link")); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("pad\tp9\tclaude\timplementer\tf\t1\t\t"+startedStamp()+"\t\t\t\t\t\t\t\n", 90000)
	writeFile(t, filepath.Join(canon, ".soho", "ws-big", "agents.tsv"), big)
	writeFile(t, filepath.Join(canon, ".soho", "ws-ambig", "agents.tsv"),
		rosterRow("dup", "p9", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("dup", "p9", "claude", "implementer", startedStamp(), "", "")+"\n",
	)
	// The terminal outside any Git repository is dropped without a
	// fallback to its cwd.
	noGit := filepath.Join(fixture, "nogit")
	writeFile(t, filepath.Join(noGit, "x.txt"), "x\n")
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("build", "p2", "claude", "ws-live", canon),
			herdrAgentEntry("a1", "n1", "claude", "ws-nostate", canon),
			herdrAgentEntry("a2", "n2", "claude", "ws-link", canon),
			herdrAgentEntry("a3", "n3", "claude", "ws-big", canon),
			herdrAgentEntry("a4", "n4", "claude", "ws-ambig", canon),
			herdrAgentEntry("a5", "n5", "claude", "ws-nogit", noGit),
		),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 {
		t.Fatalf("projects = %+v, want only the ws-live session", doc.Projects)
	}
	sess := doc.Projects[0].Sessions[0]
	if sess.ID != "ws-live" || len(sess.Agents) != 1 || sess.Agents[0].Name != "build" {
		t.Fatalf("session = %+v, want ws-live with its one worker", sess)
	}
}

// TestInventoryCollectTitleAndStartValidity: the bounded task title
// metadata and the required started stamp: the worker's role prefix and
// done mark are removed, the 120 code point bound holds, a missing or bad
// title is null, and an invalid or missing started stamp drops the row
// (no now or mtime fallback).
func TestInventoryCollectTitleAndStartValidity(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	longTitle := strings.Repeat("x", 200)
	writeState(t, canon, "ws1",
		rosterRow("a", "p1", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("b", "p2", "claude", "implementer", "not-a-stamp", "", "")+"\n"+
			rosterRow("c", "p3", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("d", "p4", "claude", "implementer", startedStamp(), "", "turbo")+"\n"+
			// a row without the started column at all:
			"e\tp5\tclaude\timplementer\tf\t1\n",
		nil,
		map[string]string{
			"a": "implementer: " + longTitle + " ✓\n",
			// b has a title but an invalid stamp: it must be dropped
			// anyway, so the title never leaves.
			"b": "implementer: B-SHOULD-NOT-LEAVE\n",
			// c has no title file: null.
			"d": "implementer: plain\n",
		},
		nil,
	)
	rules := []fakecli.Rule{
		herdrList(t,
			herdrAgentEntry("a", "p1", "claude", "ws1", canon),
			herdrAgentEntry("b", "p2", "claude", "ws1", canon),
			herdrAgentEntry("c", "p3", "claude", "ws1", canon),
			herdrAgentEntry("d", "p4", "claude", "ws1", canon),
			herdrAgentEntry("e", "p5", "claude", "ws1", canon),
		),
		herdrGet("a", "working"),
		herdrReadVisible("a", "typing\n"),
		herdrGet("c", "working"),
		herdrReadVisible("c", "typing\n"),
		herdrGet("d", "working"),
		herdrReadVisible("d", "typing\n"),
		herdrGet("e", "working"),
		herdrReadVisible("e", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	body := string(mustMarshal(t, doc))
	if strings.Contains(body, "B-SHOULD-NOT-LEAVE") {
		t.Fatalf("the invalid-stamp row leaked its title:\n%s", body)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 {
		t.Fatalf("projects = %+v, want the one session", doc.Projects)
	}
	agents := map[string]Agent{}
	for _, a := range doc.Projects[0].Sessions[0].Agents {
		agents[a.Name] = a
	}
	if len(agents) != 3 {
		t.Fatalf("agents = %+v, want a, c and d (b has no valid stamp, e lacks the column)", agents)
	}
	if _, ok := agents["a"]; !ok {
		t.Fatalf("agent a missing: %+v", agents)
	}
	got := *agents["a"].Task
	if len([]rune(got)) != 120 || got != strings.Repeat("x", 120) {
		t.Fatalf("title = %q, want the 120 code point bound without the done mark", got)
	}
	if agents["a"].Effort != nil {
		t.Fatalf("agent a effort = %v, want null for an empty effort column", agents["a"].Effort)
	}
	if c := agents["c"]; c.Task != nil {
		t.Fatalf("agent c task = %v, want null for a missing title", c.Task)
	}
	if d := agents["d"]; d.Effort != nil {
		t.Fatalf("agent d effort = %v, want null for the invalid persisted effort", d.Effort)
	}
}

// TestInventoryCollectOrderAndPhysicalDedup: the first-observed project
// and session order is preserved (not alphabetical), the same physical
// terminal listed twice is one agent, and unregistered or unknown-kind
// terminals are ignored.
func TestInventoryCollectOrderAndPhysicalDedup(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repoB := filepath.Join(fixture, "repoB")
	repoA := filepath.Join(fixture, "repoA")
	gitRepo(t, home, repoB, "")
	gitRepo(t, home, repoA, "")
	canonA, canonB := canonical(t, repoA), canonical(t, repoB)
	writeFile(t, filepath.Join(repoA, ".agents", "soho.conf"), "inventory=show\n")
	writeFile(t, filepath.Join(repoB, ".agents", "soho.conf"), "inventory=show\n")
	writeState(t, canonA, "sA",
		rosterRow("wa", "pa", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	writeState(t, canonB, "sB",
		rosterRow("wb", "pb", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	rules := []fakecli.Rule{
		herdrList(t,
			// B is observed first: it must come first in the document.
			herdrAgentEntry("wb", "pb", "claude", "sB", canonB),
			herdrAgentEntry("wa", "pa", "claude", "sA", canonA),
			// The same physical terminal twice: one agent.
			herdrAgentEntry("wb", "pb", "claude", "sB", canonB),
			// An unregistered terminal (no workspace): ignored.
			herdrAgentEntry("shell", "px", "claude", "", canonA),
			// An unknown kind: never guessed from the name.
			herdrAgentEntry("weird", "pw", "some-unknown-cli", "sA", canonA),
		),
		herdrGet("wa", "working"),
		herdrReadVisible("wa", "typing\n"),
		herdrGet("wb", "working"),
		herdrReadVisible("wb", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(doc.Projects) != 2 {
		t.Fatalf("projects = %+v, want both show projects", doc.Projects)
	}
	if doc.Projects[0].Name != filepath.Base(canonB) || doc.Projects[1].Name != filepath.Base(canonA) {
		t.Fatalf("project order = %q, %q, want the first-observed order (B then A)", doc.Projects[0].Name, doc.Projects[1].Name)
	}
	for i, p := range doc.Projects {
		if len(p.Sessions) != 1 || len(p.Sessions[0].Agents) != 1 {
			t.Fatalf("project %d = %+v, want one session with one agent (the duplicate pane deduped)", i, p)
		}
	}
}

// TestInventoryCollectRequestValidation: the request misuse refusals are
// separate from the terminal server refusal, and the failed listing
// refusal is the stable generic sentinel without the backend error text.
func TestInventoryCollectRequestValidation(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	writeState(t, canon, "ws1",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)

	t.Run("empty_version", func(t *testing.T) {
		_, err := Collect(Request{Env: env, Version: "", GeneratedMS: 1})
		if !errors.Is(err, ErrEmptyVersion) {
			t.Fatalf("err = %v, want ErrEmptyVersion", err)
		}
	})
	t.Run("negative_generated", func(t *testing.T) {
		_, err := Collect(Request{Env: env, Version: "v0.9-test", GeneratedMS: -1})
		if !errors.Is(err, ErrNegativeGeneratedMS) {
			t.Fatalf("err = %v, want ErrNegativeGeneratedMS", err)
		}
	})
	t.Run("failed_listing_is_stable_generic", func(t *testing.T) {
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Code: 1, Stderr: `{"error":{"code":"server_unreachable","message":"the token SECRET-9999 was refused"}}`},
		}
		fakeEnv, _ := withFakeHerdr(t, env, rules)
		_, err := collect(t, fakeEnv, 1)
		if !errors.Is(err, ErrTerminalUnavailable) {
			t.Fatalf("err = %v, want ErrTerminalUnavailable", err)
		}
		if strings.Contains(err.Error(), "SECRET-9999") {
			t.Fatalf("the refusal leaked the backend error text: %v", err)
		}
	})
	t.Run("invalid_listing_is_stable_generic", func(t *testing.T) {
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{}}`},
		}
		fakeEnv, _ := withFakeHerdr(t, env, rules)
		_, err := collect(t, fakeEnv, 1)
		if !errors.Is(err, ErrTerminalUnavailable) {
			t.Fatalf("err = %v, want ErrTerminalUnavailable for an invalid listing", err)
		}
	})
}

// TestInventoryCollectNowriteReadonly: under SOHO_NOWRITE=1 the
// collection still works and writes nothing: the fixture tree is
// byte-identical in its file set before and after.
func TestInventoryCollectNowriteReadonly(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	writeState(t, canon, "ws1",
		rosterRow("build", "p2", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	rules := []fakecli.Rule{
		herdrList(t, herdrAgentEntry("build", "p2", "claude", "ws1", canon)),
		herdrGet("build", "working"),
		herdrReadVisible("build", "typing\n"),
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	fakeEnv["SOHO_NOWRITE"] = "1"
	before := treeFiles(t, fixture)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect under SOHO_NOWRITE: %v", err)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions[0].Agents) != 1 {
		t.Fatalf("NOWRITE document = %+v, want the read-only collection to still work", doc)
	}
	after := treeFiles(t, fixture)
	if len(before) != len(after) {
		t.Fatalf("the collection created or removed files: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Fatalf("the file %s was missing or changed", path)
		}
	}
}

// TestInventoryCollectConflictingPhysicalIdentity: conflicting
// observations of one physical terminal are rejected before any binding,
// in both row orders. A first good row is never blessed by an unknown or
// malformed conflicting row and a stale matching identity is never
// accepted from a later row; identical duplicate rows stay one physical
// agent. The fixture and the caller environment are unchanged in every
// shape.
func TestInventoryCollectConflictingPhysicalIdentity(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	root := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	// A real directory with no Git repository: its root is unresolvable.
	plain := t.TempDir()
	orchMS := int64(1793372400000)
	writeState(t, root, "ws",
		rosterRow("build", "p1", "claude", "implementer", startedStamp(), "", "")+"\n",
		map[string]any{
			"schema": 1, "project_root": root, "session_id": "ws",
			"terminal": "p1", "name": "orchestrator", "kind": "claude",
			"started_ms": orchMS, "task": "own the work",
		},
		nil, nil,
	)
	good := herdrAgentEntry("build", "p1", "claude", "ws", root)
	conflictName := herdrAgentEntry("other", "p1", "claude", "ws", root)
	// Same name, unknown kind: a conflicting physical identity, never a
	// name-history blessing of the good row.
	conflictKind := herdrAgentEntry("build", "p1", "weird", "ws", root)
	// A different name on a cwd with no resolvable root.
	conflictRoot := herdrAgentEntry("other", "p1", "claude", "ws", plain)
	shapes := []struct {
		name    string
		entries []string
		want    int
	}{
		{"single", []string{good}, 1},
		{"identical", []string{good, good}, 1},
		{"conflict", []string{good, conflictName}, 0},
		{"conflict-reversed", []string{conflictName, good}, 0},
		{"conflict-unknown-kind", []string{good, conflictKind}, 0},
		{"conflict-root", []string{good, conflictRoot}, 0},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			before := treeFiles(t, fixture)
			rules := []fakecli.Rule{
				herdrList(t, shape.entries...),
				herdrGet("build", "working"),
				herdrReadVisible("build", "typing\n"),
			}
			fakeEnv, _ := withFakeHerdr(t, env, rules)
			doc, err := collect(t, fakeEnv, 1)
			if err != nil {
				t.Fatalf("Collect (%s): %v", shape.name, err)
			}
			var shown int
			for _, p := range doc.Projects {
				if len(p.Sessions) == 0 {
					t.Errorf("%s: an empty project was emitted", shape.name)
				}
				for _, s := range p.Sessions {
					if len(s.Agents) == 0 {
						t.Errorf("%s: an empty session was emitted", shape.name)
					}
					shown += len(s.Agents)
				}
			}
			if shown != shape.want {
				t.Errorf("%s: shown agents = %d, want %d (the conflicting physical identity is omitted in both row orders, without a name or caller history fallback):\n%s",
					shape.name, shown, shape.want, head(string(mustMarshal(t, doc))))
			}
			if shape.want == 1 && (len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 ||
				doc.Projects[0].Sessions[0].ID != "ws" || len(doc.Projects[0].Sessions[0].Agents) != 1 ||
				doc.Projects[0].Sessions[0].Agents[0].Name != "build") {
				t.Errorf("%s: document = %+v, want the single bound worker (the record on the same terminal is superseded)", shape.name, doc)
			}
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
				if fakeEnv[key] != env[key] {
					t.Errorf("%s: the caller environment changed %s", shape.name, key)
				}
			}
			after := treeFiles(t, fixture)
			if len(before) != len(after) {
				t.Fatalf("%s: the fixture tree changed: %d -> %d files", shape.name, len(before), len(after))
			}
			for path, content := range before {
				if after[path] != content {
					t.Fatalf("%s: the file %s was missing or changed", shape.name, path)
				}
			}
		})
	}
}

// TestInventoryCollectWireBudgetPrefix: the reader's 4 MiB wire budget
// (the encoded JSON object plus the CLI's final newline) cuts the shown
// candidates to an in-order prefix. Four valid 900KiB-label projects fit
// and are all accepted; five do not, and the document keeps the first
// four in order with truncated=true, never skipping the oversized
// candidate to fit a later one. The hidden project's 501 agents all
// count and never consume the bound.
func TestInventoryCollectWireBudgetPrefix(t *testing.T) {
	const wireBudget = 4 << 20 // the pinned reader's largest input
	const label = 921600       // a valid 900KiB project label
	env, fixture, home := inventoryFixture(t)
	labels := make([]string, 5)
	canons := make([]string, 5)
	for i := 0; i < 5; i++ {
		labels[i] = strings.Repeat("a", label)
		repo := filepath.Join(fixture, fmt.Sprintf("repo%d", i))
		gitRepo(t, home, repo, "")
		canons[i] = canonical(t, repo)
		writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\ninventory_name="+labels[i]+"\n")
		writeState(t, canons[i], fmt.Sprintf("s%d", i),
			rosterRow(fmt.Sprintf("w%d", i), fmt.Sprintf("w%d", i), "claude", "implementer", startedStamp(), "", "")+"\n",
			nil, nil, nil,
		)
	}
	repoH := filepath.Join(fixture, "repoHidden")
	gitRepo(t, home, repoH, "")
	canonH := canonical(t, repoH)
	writeFile(t, filepath.Join(repoH, ".agents", "soho.conf"), "inventory=hidden\n")
	var hidden strings.Builder
	entriesH := make([]string, 0, 501)
	for i := 0; i < 501; i++ {
		name := fmt.Sprintf("h%03d", i)
		hidden.WriteString(rosterRow(name, name, "claude", "implementer", startedStamp(), "", "") + "\n")
		entriesH = append(entriesH, herdrAgentEntry(name, name, "claude", "sHidden", canonH))
	}
	writeState(t, canonH, "sHidden", hidden.String(), nil, nil, nil)
	for _, n := range []int{4, 5} {
		t.Run(fmt.Sprintf("shown%d", n), func(t *testing.T) {
			entries := make([]string, 0, n+1)
			for i := 0; i < n; i++ {
				entries = append(entries, herdrAgentEntry(fmt.Sprintf("w%d", i), fmt.Sprintf("w%d", i), "claude", fmt.Sprintf("s%d", i), canons[i]))
			}
			rules := []fakecli.Rule{
				herdrList(t, append(entries, entriesH...)...),
				// Every state probe and screen read answers the same way:
				// the prefix rules keep the 500+ workers from 1000 exact
				// rules.
				{ArgvPrefix: true, Argv: []string{"agent", "get"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":7}}}`},
				{ArgvPrefix: true, Argv: []string{"agent", "read"}, Stdout: "typing\n"},
			}
			fakeEnv, _ := withFakeHerdr(t, env, rules)
			doc, err := collect(t, fakeEnv, 1)
			if err != nil {
				t.Fatalf("Collect (shown%d): %v", n, err)
			}
			body := mustMarshal(t, doc)
			if len(body)+1 > wireBudget {
				t.Fatalf("shown%d: the encoded JSON plus the CLI newline is %d bytes, beyond the reader's %d", n, len(body)+1, wireBudget)
			}
			for i, p := range doc.Projects {
				if len(p.Sessions) == 0 {
					t.Fatalf("shown%d: project %d has no session, want no empty project", n, i)
				}
				for _, s := range p.Sessions {
					if len(s.Agents) == 0 {
						t.Fatalf("shown%d: project %d session %q has no agent, want no empty session", n, i, s.ID)
					}
				}
			}
			if doc.Hidden.Working != 501 {
				t.Fatalf("shown%d: hidden = %+v, want all 501 hidden workers counted beyond the shown bound", n, doc.Hidden)
			}
			if n == 4 {
				if doc.Truncated {
					t.Fatalf("shown4: truncated = true, want the omitted key: the four 900KiB-label projects fit")
				}
				if len(doc.Projects) != 4 {
					t.Fatalf("shown4: projects = %d, want all four", len(doc.Projects))
				}
				for i, p := range doc.Projects {
					if p.Name != labels[i] || len(p.Sessions) != 1 || p.Sessions[0].ID != fmt.Sprintf("s%d", i) || len(p.Sessions[0].Agents) != 1 {
						t.Fatalf("shown4: project %d = %+v, want label %d with its one worker", i, p, i)
					}
				}
				return
			}
			if !doc.Truncated {
				t.Fatalf("shown5: truncated = false, want the bounded prefix marked truncated")
			}
			if len(doc.Projects) != 4 {
				t.Fatalf("shown5: projects = %d, want the first four in order: the fifth project's first candidate cannot fit the wire budget", len(doc.Projects))
			}
			for i, p := range doc.Projects {
				if p.Name != labels[i] {
					t.Fatalf("shown5: project %d name is wrong, want the in-order prefix (no skipping of the oversized candidate)", i)
				}
				if len(p.Sessions) != 1 || len(p.Sessions[0].Agents) != 1 {
					t.Fatalf("shown5: project %d = %+v, want its one worker", i, p)
				}
			}
		})
	}
}

// TestInventoryCollectWireBudgetEscaping: the budget accounting uses the
// exact encoded length, not the raw byte length. The labels are all
// literal `<` (a non-whitespace printable project value that survives the
// configuration parser), and this toolchain's encoding/json expands each
// rune to \u003c: six encoded bytes per raw byte. The first three labels
// are 200000 raw bytes (1200000 encoded each) and stay selected; the
// fourth is 921600 raw bytes (900KiB, under the 1 MiB config bound,
// 5529600 encoded) and its first candidate cannot fit the wire budget.
// The document keeps the in-order prefix of three with truncated=true,
// names the configured labels verbatim, and the actual marshaled document
// plus the CLI newline stays within the reader's budget.
func TestInventoryCollectWireBudgetEscaping(t *testing.T) {
	const wireBudget = 4 << 20
	const small = 200000
	env, fixture, home := inventoryFixture(t)
	labels := []string{
		strings.Repeat("<", small),
		strings.Repeat("<", small),
		strings.Repeat("<", small),
		strings.Repeat("<", 921600),
	}
	canons := make([]string, 4)
	for i := 0; i < 4; i++ {
		repo := filepath.Join(fixture, fmt.Sprintf("repo%d", i))
		gitRepo(t, home, repo, "")
		canons[i] = canonical(t, repo)
		writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\ninventory_name="+labels[i]+"\n")
		writeState(t, canons[i], fmt.Sprintf("s%d", i),
			rosterRow(fmt.Sprintf("w%d", i), fmt.Sprintf("w%d", i), "claude", "implementer", startedStamp(), "", "")+"\n",
			nil, nil, nil,
		)
	}
	entries := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		entries = append(entries, herdrAgentEntry(fmt.Sprintf("w%d", i), fmt.Sprintf("w%d", i), "claude", fmt.Sprintf("s%d", i), canons[i]))
	}
	rules := []fakecli.Rule{
		herdrList(t, entries...),
		{ArgvPrefix: true, Argv: []string{"agent", "get"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":7}}}`},
		{ArgvPrefix: true, Argv: []string{"agent", "read"}, Stdout: "typing\n"},
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	body := mustMarshal(t, doc)
	if len(body)+1 > wireBudget {
		t.Fatalf("the encoded JSON plus the CLI newline is %d bytes, beyond the reader's %d", len(body)+1, wireBudget)
	}
	if !doc.Truncated {
		t.Fatalf("truncated = false, want the bounded prefix: the fourth project's encoded cost cannot fit")
	}
	if len(doc.Projects) != 3 {
		t.Fatalf("projects = %d, want the in-order prefix of three: the encoded cost, not the raw length, decides the prefix", len(doc.Projects))
	}
	for i, p := range doc.Projects {
		if p.Name != labels[i] {
			t.Fatalf("project %d name is not the configured label verbatim (the config parser trimmed it or the collector fell back to the basename): raw %d bytes, selected %d bytes", i, len(labels[i]), len(p.Name))
		}
		if len(p.Sessions) != 1 || len(p.Sessions[0].Agents) != 1 {
			t.Fatalf("project %d = %+v, want its one worker", i, p)
		}
	}
	// The actual marshal shows the encoding/json expansion: every label
	// rune is the six-byte \u003c sequence, never a raw <.
	if !strings.Contains(string(body), `\u003c`) {
		t.Fatalf("the marshaled document lacks the escaped label form:\n%s", head(string(body)))
	}
	if len(body) < 3*6*small {
		t.Fatalf("marshaled body is %d bytes, want at least the three expanded 1200000-byte labels", len(body))
	}
}

// TestInventoryCollectPrefixStopsAcrossProjects: the size and count
// limits stop the ENTIRE candidate sequence, not just the current
// project. An oversized first project followed by a small project
// yields the empty prefix (projects[]/truncated=true, no panic on the
// project indexing); 500 agents in the first project plus one in the
// second keep exactly the first 500 in order with truncated=true and
// never show the next project's agent.
func TestInventoryCollectPrefixStopsAcrossProjects(t *testing.T) {
	const wireBudget = 4 << 20
	env, fixture, home := inventoryFixture(t)
	repos := make([]string, 2)
	canons := make([]string, 2)
	for i := 0; i < 2; i++ {
		repos[i] = filepath.Join(fixture, fmt.Sprintf("repo%d", i))
		gitRepo(t, home, repos[i], "")
		canons[i] = canonical(t, repos[i])
	}

	t.Run("first-too-large-then-small", func(t *testing.T) {
		// The first project's label expands to 5529600 encoded bytes and
		// cannot fit; the second project is small and must not be reached.
		writeFile(t, filepath.Join(repos[0], ".agents", "soho.conf"), "inventory=show\ninventory_name="+strings.Repeat("<", 921600)+"\n")
		writeFile(t, filepath.Join(repos[1], ".agents", "soho.conf"), "inventory=show\ninventory_name=small\n")
		writeState(t, canons[0], "sA",
			rosterRow("wa000", "wa000", "claude", "implementer", startedStamp(), "", "")+"\n",
			nil, nil, nil,
		)
		writeState(t, canons[1], "sB",
			rosterRow("wb000", "wb000", "claude", "implementer", startedStamp(), "", "")+"\n",
			nil, nil, nil,
		)
		rules := []fakecli.Rule{
			herdrList(t,
				herdrAgentEntry("wa000", "wa000", "claude", "sA", canons[0]),
				herdrAgentEntry("wb000", "wb000", "claude", "sB", canons[1]),
			),
			{ArgvPrefix: true, Argv: []string{"agent", "get"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":7}}}`},
			{ArgvPrefix: true, Argv: []string{"agent", "read"}, Stdout: "typing\n"},
		}
		fakeEnv, _ := withFakeHerdr(t, env, rules)
		doc, err := collect(t, fakeEnv, 1)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if !doc.Truncated {
			t.Fatalf("truncated = false, want the empty prefix marked truncated")
		}
		if len(doc.Projects) != 0 {
			t.Fatalf("projects = %+v, want the empty prefix (the small later project is never reached)", doc.Projects)
		}
		body := mustMarshal(t, doc)
		if len(body)+1 > wireBudget {
			t.Fatalf("the encoded JSON plus the CLI newline is %d bytes, beyond the reader's %d", len(body)+1, wireBudget)
		}
		if !strings.Contains(string(body), `"projects":[]`) {
			t.Fatalf("the empty prefix must marshal as projects[], not null:\n%s", head(string(body)))
		}
	})
	t.Run("limit-then-next-project", func(t *testing.T) {
		// 500 agents in the first project consume the display bound; the
		// second project's single agent is never shown.
		writeFile(t, filepath.Join(repos[0], ".agents", "soho.conf"), "inventory=show\ninventory_name=big\n")
		writeFile(t, filepath.Join(repos[1], ".agents", "soho.conf"), "inventory=show\ninventory_name=small\n")
		var rowsA strings.Builder
		entries := make([]string, 0, 501)
		for j := 0; j < 500; j++ {
			name := fmt.Sprintf("wa%03d", j)
			rowsA.WriteString(rosterRow(name, name, "claude", "implementer", startedStamp(), "", "") + "\n")
			entries = append(entries, herdrAgentEntry(name, name, "claude", "sA", canons[0]))
		}
		writeState(t, canons[0], "sA", rowsA.String(), nil, nil, nil)
		writeState(t, canons[1], "sB",
			rosterRow("wb000", "wb000", "claude", "implementer", startedStamp(), "", "")+"\n",
			nil, nil, nil,
		)
		entries = append(entries, herdrAgentEntry("wb000", "wb000", "claude", "sB", canons[1]))
		rules := []fakecli.Rule{
			herdrList(t, entries...),
			{ArgvPrefix: true, Argv: []string{"agent", "get"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":7}}}`},
			{ArgvPrefix: true, Argv: []string{"agent", "read"}, Stdout: "typing\n"},
		}
		fakeEnv, _ := withFakeHerdr(t, env, rules)
		doc, err := collect(t, fakeEnv, 1)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if !doc.Truncated {
			t.Fatalf("truncated = false, want the 500-bound marked truncated")
		}
		var shown int
		for _, p := range doc.Projects {
			for _, s := range p.Sessions {
				shown += len(s.Agents)
			}
		}
		if shown != 500 {
			t.Fatalf("shown agents = %d, want exactly the 500 bound (the next project's agent is never reached)", shown)
		}
		if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 || len(doc.Projects[0].Sessions[0].Agents) != 500 {
			t.Fatalf("document = %+v, want only the first project's 500 agents in order", doc.Projects)
		}
		body := mustMarshal(t, doc)
		if len(body)+1 > wireBudget {
			t.Fatalf("the encoded JSON plus the CLI newline is %d bytes, beyond the reader's %d", len(body)+1, wireBudget)
		}
		if strings.Contains(string(body), "wb000") {
			t.Fatalf("the next project's agent leaked into the prefix:\n%s", head(string(body)))
		}
	})
}

// treeFiles maps every file under root to its content hash.
func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		files[filepath.ToSlash(strings.TrimPrefix(path, root))] = fmt.Sprintf("%x", fnv(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// fnv is a small content hash for the tree comparison (no crypto needed).
func fnv(data []byte) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range data {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}
