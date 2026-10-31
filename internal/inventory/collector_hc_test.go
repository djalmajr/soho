package inventory

// Companion focused tests: the herdr-core (hc) backend seam and the
// literal 500/501 shown truncation with hidden overflow.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// withFakeHc installs the fake hc binary in a fresh directory on env's
// PATH (with the host git appended) and returns the extended env and the
// call log path.
func withFakeHc(t *testing.T, env platform.Env, rules []fakecli.Rule) (platform.Env, string) {
	t.Helper()
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "hc", rules); err != nil {
		t.Fatal(err)
	}
	return fakeEnv(t, env, fakeDir), filepath.Join(fakeDir, "hc.calls.jsonl")
}

func hcTerminal(address, name, state, statusState, app, cwd, session string) string {
	body, _ := json.Marshal(map[string]any{
		"address": address, "name": name, "state": state, "cwd": cwd,
		"status": map[string]string{"state": statusState, "app": app},
		"labels": map[string]string{"soho.session": session},
	})
	return string(body)
}

func hcListRule(entries ...string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[` + strings.Join(entries, ",") + `]}`}
}

// TestInventoryCollectHCBackend: the same collector over the real hc
// backend seam: the unfiltered local listing, the remote (machine-
// prefixed) and the finished records excluded, the exact bindings, the
// terminal-only orchestrator inspection and the null since_ms.
func TestInventoryCollectHCBackend(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	canon := canonical(t, repo)
	writeFile(t, filepath.Join(repo, ".agents", "soho.conf"), "inventory=show\n")
	orchMS := int64(1793372400000)
	writeState(t, canon, "ws1",
		rosterRow("build", "t2", "claude", "implementer", startedStamp(), "example/model-1", "high")+"\n",
		map[string]any{
			"schema": 1, "project_root": canon, "session_id": "ws1",
			"terminal": "t1", "name": "orchestrator", "kind": "claude",
			"started_ms": orchMS, "task": "Run the checkout work",
		},
		map[string]string{"build": "implementer: Fix the checkout\n"},
		nil,
	)
	// The machine-prefixed address is a remote terminal and the finished
	// record is gone: both are excluded from the local listing.
	rules := []fakecli.Rule{
		hcListRule(
			hcTerminal("t1", "orchestrator", "running", "working", "claude", canon, "ws1"),
			hcTerminal("t2", "build", "running", "idle", "claude", canon, "ws1"),
			hcTerminal("m1/t9", "remote", "running", "working", "claude", canon, "ws1"),
			hcTerminal("t0", "finished", "finished", "idle", "claude", canon, "ws1"),
		),
		{Argv: []string{"get", "orchestrator", "--json"}, Stdout: `{"address":"t1","name":"orchestrator","state":"running","status":{"state":"working"}}`},
		{Argv: []string{"read", "orchestrator", "--lines", "0", "--json"}, Stdout: `{"text":"planning the next move"}`},
		{Argv: []string{"get", "build", "--json"}, Stdout: `{"address":"t2","name":"build","state":"running","status":{"state":"idle"}}`},
	}
	fakeEnv, _ := withFakeHc(t, env, rules)
	fakeEnv["SOHO_TRANSPORT"] = "hc"
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect (hc): %v", err)
	}
	if len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 {
		t.Fatalf("projects = %+v, want the one project and session", doc.Projects)
	}
	agents := doc.Projects[0].Sessions[0].Agents
	if len(agents) != 2 {
		t.Fatalf("agents = %+v, want the orchestrator and the worker (remote and finished excluded)", agents)
	}
	orch := agents[0]
	if orch.Terminal != "t1" || orch.Name != "orchestrator" || orch.Role != "orchestrator" || orch.Kind != "claude" {
		t.Fatalf("orchestrator = %+v, want the verified record binding over the hc address", orch)
	}
	if orch.State != StateWorking || orch.SinceMS != nil || orch.ReportReady {
		t.Fatalf("orchestrator state/since/report = %s %v %v, want working, null since and no report-ready", orch.State, orch.SinceMS, orch.ReportReady)
	}
	if orch.StartedMS != orchMS || orch.Task == nil || *orch.Task != "Run the checkout work" {
		t.Fatalf("orchestrator start/task = %d %v, want the record values", orch.StartedMS, orch.Task)
	}
	build := agents[1]
	if build.Terminal != "t2" || build.Name != "build" || build.Kind != "claude" || build.State != StateIdle {
		t.Fatalf("worker = %+v, want the idle worker bound to the hc address", build)
	}
	if build.Model == nil || *build.Model != "example/model-1" || build.Effort == nil || *build.Effort != "high" {
		t.Fatalf("worker model/effort = %v %v, want the persisted columns", build.Model, build.Effort)
	}
	if build.SinceMS != nil {
		t.Fatalf("worker since_ms = %v, want null under the terminal server", build.SinceMS)
	}
	if build.Task == nil || *build.Task != "Fix the checkout" {
		t.Fatalf("worker task = %v, want the role prefix removed", build.Task)
	}
	body := string(mustMarshal(t, doc))
	if strings.Contains(body, "m1/t9") || strings.Contains(body, "finished") {
		t.Fatalf("the document named a remote or finished terminal:\n%s", body)
	}
}

// TestInventoryCollectHCListFailure: a failed hc listing is the stable
// generic refusal without the backend error text.
func TestInventoryCollectHCListFailure(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repo := filepath.Join(fixture, "repo")
	gitRepo(t, home, repo, "")
	rules := []fakecli.Rule{
		{Argv: []string{"ls", "--json"}, Code: 1, Stderr: `{"error":{"code":"daemon_unreachable","message":"the socket SECRET-7777 is closed"}}`},
	}
	fakeEnv, _ := withFakeHc(t, env, rules)
	fakeEnv["SOHO_TRANSPORT"] = "hc"
	_, err := collect(t, fakeEnv, 1)
	if !errors.Is(err, ErrTerminalUnavailable) {
		t.Fatalf("err = %v, want ErrTerminalUnavailable", err)
	}
	if strings.Contains(err.Error(), "SECRET-7777") {
		t.Fatalf("the refusal leaked the backend error text: %v", err)
	}
}

// TestInventoryCollectHCConflictingPhysicalIdentity: the same
// conflicting-physical-identity rejection over the real hc backend seam,
// in both row orders; identical duplicate rows stay one physical agent.
func TestInventoryCollectHCConflictingPhysicalIdentity(t *testing.T) {
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
	good := hcTerminal("p1", "build", "running", "idle", "claude", root, "ws")
	conflictName := hcTerminal("p1", "other", "running", "idle", "claude", root, "ws")
	// Same name, unknown app: a conflicting physical identity, never a
	// name-history blessing of the good row.
	conflictKind := hcTerminal("p1", "build", "running", "idle", "weird", root, "ws")
	// A different name on a cwd with no resolvable root.
	conflictRoot := hcTerminal("p1", "other", "running", "idle", "claude", plain, "ws")
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
				hcListRule(shape.entries...),
				{Argv: []string{"get", "p1", "--json"}, Stdout: `{"address":"p1","name":"build","state":"running","status":{"state":"idle"}}`},
			}
			fakeEnv, _ := withFakeHc(t, env, rules)
			fakeEnv["SOHO_TRANSPORT"] = "hc"
			doc, err := collect(t, fakeEnv, 1)
			if err != nil {
				t.Fatalf("Collect (hc %s): %v", shape.name, err)
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

// TestInventoryCollectTruncation: the literal 500/501 shown bound. The
// show project offers 501 candidates (the orchestrator plus 500 roster
// workers in roster order): the document keeps the first 500 in order and
// adds truncated=true; the hidden project's agents do not consume the
// bound and all of them are counted.
func TestInventoryCollectTruncation(t *testing.T) {
	env, fixture, home := inventoryFixture(t)
	repoShow := filepath.Join(fixture, "repoShow")
	repoHidden := filepath.Join(fixture, "repoHidden")
	gitRepo(t, home, repoShow, "")
	gitRepo(t, home, repoHidden, "")
	canonShow, canonHidden := canonical(t, repoShow), canonical(t, repoHidden)
	writeFile(t, filepath.Join(repoShow, ".agents", "soho.conf"), "inventory=show\n")
	writeFile(t, filepath.Join(repoHidden, ".agents", "soho.conf"), "inventory=hidden\n")

	const workers = 500
	var roster strings.Builder
	roster.WriteString("# roster\n")
	entries := make([]string, 0, workers+3)
	for i := 1; i <= workers; i++ {
		pane := fmt.Sprintf("w%03d", i)
		name := fmt.Sprintf("w%03d", i)
		roster.WriteString(rosterRow(name, pane, "claude", "implementer", startedStamp(), "", "") + "\n")
		entries = append(entries, herdrAgentEntry(name, pane, "claude", "wsShow", canonShow))
	}
	writeState(t, canonShow, "wsShow", roster.String(),
		map[string]any{
			"schema": 1, "project_root": canonShow, "session_id": "wsShow",
			"terminal": "p0", "name": "orchestrator", "kind": "claude",
			"started_ms": 1, "task": "run the show",
		},
		nil, nil,
	)
	writeState(t, canonHidden, "wsHidden",
		rosterRow("hidden1", "h1", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("hidden2", "h2", "claude", "implementer", startedStamp(), "", "")+"\n"+
			rosterRow("hidden3", "h3", "claude", "implementer", startedStamp(), "", "")+"\n",
		nil, nil, nil,
	)
	entries = append(entries,
		herdrAgentEntry("orchestrator", "p0", "claude", "wsShow", canonShow),
		herdrAgentEntry("hidden1", "h1", "claude", "wsHidden", canonHidden),
		herdrAgentEntry("hidden2", "h2", "claude", "wsHidden", canonHidden),
		herdrAgentEntry("hidden3", "h3", "claude", "wsHidden", canonHidden),
	)
	rules := []fakecli.Rule{
		herdrList(t, entries...),
		// Every state probe and every screen read answers the same way:
		// the prefix rules keep the 500+ workers from 1000 exact rules.
		{ArgvPrefix: true, Argv: []string{"agent", "get"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":7}}}`},
		{ArgvPrefix: true, Argv: []string{"agent", "read"}, Stdout: "typing\n"},
	}
	fakeEnv, _ := withFakeHerdr(t, env, rules)
	doc, err := collect(t, fakeEnv, 1793376000000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !doc.Truncated {
		t.Fatalf("truncated = false, want true for %d shown candidates", workers+1)
	}
	body := string(mustMarshal(t, doc))
	if !strings.Contains(body, `"truncated":true`) {
		t.Fatalf("the document JSON must carry truncated=true:\n%s", head(body))
	}
	if len(doc.Projects) != 1 {
		t.Fatalf("projects = %+v, want the one show project", doc.Projects)
	}
	sessions := doc.Projects[0].Sessions
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want exactly one", len(sessions))
	}
	if len(sessions[0].Agents) != 500 {
		t.Fatalf("shown agents = %d, want exactly the 500 bound", len(sessions[0].Agents))
	}
	agents := sessions[0].Agents
	if agents[0].Role != "orchestrator" || agents[0].Name != "orchestrator" {
		t.Fatalf("first shown agent = %+v, want the orchestrator first", agents[0])
	}
	last := agents[len(agents)-1]
	if last.Name != fmt.Sprintf("w%03d", workers-1) {
		t.Fatalf("last shown agent = %q, want the 500th in roster order (w%03d dropped)", last.Name, workers)
	}
	// The hidden project: all three counted, none consuming the bound.
	if doc.Hidden.Working != 3 {
		t.Fatalf("hidden counts = %+v, want the three hidden workers counted", doc.Hidden)
	}
	if strings.Contains(body, "hidden1") || strings.Contains(body, "hidden2") || strings.Contains(body, "hidden3") {
		t.Fatalf("the hidden agents must not appear in the show document:\n%s", head(body))
	}
}

// head is a short error-context view of a large body.
func head(body string) string {
	if len(body) > 400 {
		return body[:400] + "…"
	}
	return body
}
