package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// soh234Advisory is the fixed generic missing-registration advisory the
// SOH-234 slice proves through the real inventory consumer. It is the
// literal contract message; it never carries names, roots, paths, session
// ids, counts or status text, and it is emitted at most once per
// invocation, only after a successful stdout write.
const soh234Advisory = "an apparent orchestrator is not registered; run soho init in its pane"

// soh234AdvisoryFixture builds the command fixture with an opted-in project
// (inventory=show|hidden) and one unambiguous local live known-kind
// terminal named the configured orchestrator, in the strict canonical
// project/session, with no raw roster row on its pane and no valid
// registration record. It runs under SOHO_NOWRITE and without a caller.
func soh234AdvisoryFixture(t *testing.T, mode string) (platform.Env, string, string) {
	t.Helper()
	return soh234InventoryFixture(t, "inventory="+mode+"\n", "orchestrator", nil, "working")
}

func soh234InventoryFixture(t *testing.T, conf, name string, body func(string) string, status string, extraRules ...fakecli.Rule) (platform.Env, string, string) {
	t.Helper()
	env, repo := commandFixture(t)
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if body == nil {
		body = func(root string) string { return soh234LiveAgent(name, "p234", "claude", "ws234", root) }
	}
	bin := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + body(repo) + `]}}`},
		{Argv: []string{"agent", "get", name}, Stdout: `{"result":{"agent":{"agent_status":"` + status + `","state_change_seq":1}}}`},
	}
	rules = append(rules, extraRules...)
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withHostGit(t, withFakeCLI(env, bin), bin)
	env["SOHO_TRANSPORT"] = "herdr"
	env["SOHO_NOWRITE"] = "1"
	delete(env, "HERDR_WORKSPACE_ID")
	return env, repo, bin
}

func soh234LiveAgent(name, pane, kind, session, root string) string {
	return strings.TrimSuffix(herdrAgentEntry(name, pane, kind, session, root), "}") + `,"agent_status":"working"}`
}

// These are fixture observations through existing public APIs, not a second
// implementation of the future advisory predicate.
func soh234InventoryPrerequisites(t *testing.T, env platform.Env, repo string) []transport.Terminal {
	t.Helper()
	root, err := platform.StateProjectRootWithGit(repo, func(args ...string) (string, int) {
		r := dispatch.ReferenceGit(env, append([]string{"-C", repo}, args...)...)
		if r.NotFound || r.Error != "" || r.TimedOut || r.Status == nil {
			return "", 1
		}
		return r.Stdout, *r.Status
	})
	if err != nil || root != repo {
		t.Fatalf("setup: canonical root=%q want=%q err=%v", root, repo, err)
	}
	rep := backend.New(env).Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil || !rep.Valid || len(rep.Value) == 0 {
		t.Fatalf("setup: local listing=%+v", rep)
	}
	return rep.Value
}

func soh234RequireCandidate(t *testing.T, terms []transport.Terminal, repo, name string) {
	t.Helper()
	for _, term := range terms {
		if term.Name == name && term.PaneID == "p234" && term.Cwd == repo && term.WorkspaceID == "ws234" && term.State == "working" && reflect.DeepEqual(term.ProgramHints, []string{"claude"}) {
			return
		}
	}
	t.Fatalf("setup: exact listed candidate %q missing: %+v", name, terms)
}

func soh234CLIRecord(t *testing.T, env platform.Env, repo, pane string) {
	t.Helper()
	want := sessionmeta.Record{Schema: 1, ProjectRoot: repo, SessionID: "ws234", Terminal: pane, Name: "orchestrator", Kind: "claude", StartedMS: 42, Task: ""}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	soh234CLISessionFile(t, env, sessionmeta.RecordName, string(data))
	got, found, err := (sessionmeta.Store{StateBase: env["SOHO_DIR"], SessionID: "ws234"}).Read()
	if err != nil || !found || got != want {
		t.Fatalf("setup: strict record read=%+v found=%v err=%v want=%+v", got, found, err, want)
	}
}

func soh234CLISessionFile(t *testing.T, env platform.Env, name, content string) {
	t.Helper()
	dir := filepath.Join(env["SOHO_DIR"], "ws234")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func soh234CLISource(t *testing.T, env platform.Env, wantErr bool) core.RosterSnapshot {
	t.Helper()
	source, err := core.OpenSessionSource(env["SOHO_DIR"], "ws234", env["TMPDIR"])
	if wantErr {
		if err == nil {
			_ = source.Close()
			t.Fatal("setup: unsafe source was accepted")
		}
		return core.RosterSnapshot{}
	}
	if err != nil {
		t.Fatalf("setup: safe source refused: %v", err)
	}
	snap := source.Roster()
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	return snap
}

func soh234InventoryCalls(t *testing.T, bin string) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// assertSoH234InventoryCalls is the call-log prerequisite: the unfiltered
// local listing ran, and there was no caller, current-pane or workspace
// discovery and no screen read. The fresh liveness inspection (agent get)
// is allowed; everything that would read the screen or discover the
// caller/workspace is refused.
func assertSoH234InventoryCalls(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	sawList := false
	for _, c := range calls {
		a := c.Argv
		if len(a) == 2 && a[0] == "agent" && a[1] == "list" {
			sawList = true
			continue
		}
		switch {
		case len(a) >= 2 && a[0] == "agent" && a[1] == "get":
			// the fresh liveness inspection is allowed
			continue
		default:
			t.Fatalf("forbidden caller/workspace/remote/screen call: %v", a)
		}
	}
	if !sawList {
		t.Fatalf("the unfiltered local listing did not run: %v", calls)
	}
}

// Registered controls retain the existing terminal-only status resolver's
// local screen read. Only these exact calls are allowed, after setup calls
// have been sliced off; unregistered controls keep their screen-read ban.
func assertSoH234RegisteredLocalCalls(t *testing.T, calls []fakecli.Call, host string, allowGoneProbe bool) {
	t.Helper()
	var list, status, screen []string
	switch host {
	case "herdr":
		list = []string{"agent", "list"}
		status = []string{"agent", "get", "orchestrator"}
		screen = []string{"agent", "read", "orchestrator", "--source", "visible"}
	case "hc":
		list = []string{"ls", "--json"}
		status = []string{"get", "orchestrator", "--json"}
		screen = []string{"read", "orchestrator", "--lines", "0", "--json"}
	default:
		t.Fatalf("unknown registered-control host: %q", host)
	}
	lists, statuses, screens := 0, 0, 0
	for _, call := range calls {
		switch {
		case reflect.DeepEqual(call.Argv, list):
			lists++
		case reflect.DeepEqual(call.Argv, status):
			statuses++
		case reflect.DeepEqual(call.Argv, screen):
			screens++
		case host == "herdr" && allowGoneProbe && reflect.DeepEqual(call.Argv, []string{"agent", "get", "orchestrator-2"}):
			// Only the mixed fresh-gone case may inspect this known local
			// observation; its screen is still forbidden.
		default:
			t.Fatalf("unexpected registered-local %s call: %v", host, call.Argv)
		}
	}
	if lists != 1 || statuses == 0 || screens != 1 {
		t.Fatalf("registered-local %s calls did not reach listing/status/screen: %+v", host, calls)
	}
}

// assertSoH234InventoryResult is the shared prerequisite suite of the
// qualifying-candidate inventory runs: the unchanged single JSON document,
// no fixture-tree write, the caller-free call log and no private leak. The
// missing-registration advisory itself is asserted by the caller, after
// these prerequisites, so the red is attributed to the missing warning
// rather than a setup failure.
func assertSoH234InventoryResult(t *testing.T, env platform.Env, repo, bin, out, stderr string, before map[string]string, secrets []string) {
	t.Helper()
	doc := decodeInventoryCLI(t, out)
	if string(doc["projects"]) != "[]" {
		t.Fatalf("the document changed: projects=%s", doc["projects"])
	}
	if after := inventoryCLITree(t, filepath.Dir(repo)); !reflect.DeepEqual(before, after) {
		t.Fatal("the inventory changed the fixture tree")
	}
	assertSoH234InventoryCalls(t, soh234InventoryCalls(t, bin))
	for _, s := range secrets {
		if s != "" && strings.Contains(stderr, s) {
			t.Fatalf("the advisory leaked a private value %q: stderr=%q", s, stderr)
		}
	}
}

// TestSoh234InventoryShowAdvisesMissingRegistration is the show-mode red:
// an opted-in project with a qualifying orchestrator-named terminal that is
// not registered and not rostered yields exit0, the unchanged document and
// exactly one fixed advisory on stderr.
func TestSoh234InventoryShowAdvisesMissingRegistration(t *testing.T) {
	env, repo, bin := soh234AdvisoryFixture(t, "show")
	soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "orchestrator")
	if rows := soh234CLISource(t, env, false).Rows; len(rows) != 0 {
		t.Fatalf("setup: missing session must be safe absence: %+v", rows)
	}
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 {
		t.Fatalf("exit=%d want 0: stderr=%q", code, stderr)
	}
	assertSoH234InventoryResult(t, env, repo, bin, out, stderr, before, []string{repo, env["HOME"], env["SOHO_DIR"], "p234", "ws234"})
	if got := strings.Count(stderr, soh234Advisory); got != 1 {
		t.Fatalf("the missing-registration advisory appears %d times, want 1: stderr=%q", got, stderr)
	}
}

// TestSoh234InventoryHiddenAdvisesMissingRegistration is the hidden-mode
// red: the same qualifying candidate in a hidden project still yields the
// one fixed advisory, with the hidden counts and document unchanged.
func TestSoh234InventoryHiddenAdvisesMissingRegistration(t *testing.T) {
	env, repo, bin := soh234InventoryFixture(t, "inventory=hidden\norchestrator_name=conductor\n", "conductor-2", nil, "working")
	soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "conductor-2")
	configured, err := core.LoadReadOnlyConfig(env, repo, "ws234", core.UserSelection{})
	if err != nil || core.Cfg(&configured.Config, "orchestrator_name", "", env) != "conductor" {
		t.Fatalf("setup: custom orchestrator configuration was not loaded: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(env["SOHO_DIR"], "ws234"), 0o700); err != nil {
		t.Fatal(err)
	}
	if rows := soh234CLISource(t, env, false).Rows; len(rows) != 0 {
		t.Fatalf("setup: missing roster must be safe absence: %+v", rows)
	}
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 {
		t.Fatalf("exit=%d want 0: stderr=%q", code, stderr)
	}
	assertSoH234InventoryResult(t, env, repo, bin, out, stderr, before, []string{repo, env["HOME"], env["SOHO_DIR"], "p234", "ws234", "conductor", "conductor-2"})
	if got := strings.Count(stderr, soh234Advisory); got != 1 {
		t.Fatalf("the missing-registration advisory appears %d times, want 1: stderr=%q", got, stderr)
	}
}

// TestSoh234InventoryAdvisoryAggregatesOnce proves the advisory is an
// aggregate: two qualifying candidates in one invocation yield exactly one
// warning, not one per candidate.
func TestSoh234InventoryAdvisoryAggregatesOnce(t *testing.T) {
	env, repo := commandFixture(t)
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte("inventory=show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + herdrAgentEntry("orchestrator", "p234", "claude", "ws234", repo) + "," + herdrAgentEntry("orchestrator-2", "p235", "claude", "ws234", repo) + `]}}`},
		{Argv: []string{"agent", "get", "orchestrator"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`},
		{Argv: []string{"agent", "get", "orchestrator-2"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withHostGit(t, withFakeCLI(env, bin), bin)
	env["SOHO_TRANSPORT"] = "herdr"
	env["SOHO_NOWRITE"] = "1"
	delete(env, "HERDR_WORKSPACE_ID")
	terms := soh234InventoryPrerequisites(t, env, repo)
	if len(terms) != 2 || terms[0].PaneID != "p234" || terms[1].PaneID != "p235" {
		t.Fatalf("setup: aggregate candidates=%+v", terms)
	}
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 {
		t.Fatalf("exit=%d want 0: stderr=%q", code, stderr)
	}
	assertSoH234InventoryResult(t, env, repo, bin, out, stderr, before, []string{repo, env["HOME"], env["SOHO_DIR"], "p234", "p235", "ws234"})
	if got := strings.Count(stderr, soh234Advisory); got != 1 {
		t.Fatalf("the two qualifying candidates did not aggregate to one advisory (count=%d): stderr=%q", got, stderr)
	}
}

// soh234FailWriter is the synthetic failing stdout of the success-only
// emission control: every write is refused, so a success advisory must not
// have been emitted before the write.
type soh234FailWriter struct{ calls int }

func (w *soh234FailWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("the stdout write is refused")
}

// TestSoh234InventoryNoSuccessAdvisoryOnFailedStdout is the success-only
// emission control: a qualifying candidate plus a failing stdout write
// yields the existing generic exit4 failure and NO success advisory, which
// the initial-list failure test alone cannot discriminate.
func TestSoh234InventoryNoSuccessAdvisoryOnFailedStdout(t *testing.T) {
	env, repo, bin := soh234AdvisoryFixture(t, "show")
	soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "orchestrator")
	start := len(soh234InventoryCalls(t, bin))
	before := inventoryCLITree(t, filepath.Dir(repo))
	oldOut, oldErr := platform.Stdout, platform.Stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	writer := &soh234FailWriter{}
	platform.Stdout = writer
	var stderr bytes.Buffer
	platform.Stderr = &stderr
	code := Run([]string{"inventory", "--json"}, env)
	if writer.calls != 1 {
		t.Fatalf("stdout was attempted %d times, want 1 (collection must succeed)", writer.calls)
	}
	assertSoH234InventoryCalls(t, soh234InventoryCalls(t, bin)[start:])
	if code != 4 {
		t.Fatalf("exit=%d want 4 (the generic failure): stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "soho: inventory: unavailable") {
		t.Fatalf("the generic failure diagnostic is missing: stderr=%q", stderr.String())
	}
	if strings.Contains(stderr.String(), soh234Advisory) {
		t.Fatalf("a success advisory was emitted before the stdout write failed: stderr=%q", stderr.String())
	}
	if after := inventoryCLITree(t, filepath.Dir(repo)); !reflect.DeepEqual(before, after) {
		t.Fatal("the failed inventory changed the fixture tree")
	}
}

func TestSoh234InventoryRawInvalidWorkerPaneSuppressesAdvisory(t *testing.T) {
	env, repo, bin := soh234AdvisoryFixture(t, "show")
	soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "orchestrator")
	soh234CLISessionFile(t, env, "agents.tsv", "orchestrator\tp234\t\t\n")
	snap := soh234CLISource(t, env, false)
	if len(snap.Rows) != 1 || snap.Rows[0].Valid || len(snap.Rows[0].Fields) != 4 || snap.Rows[0].Fields[1] != "p234" {
		t.Fatalf("setup: invalid raw worker pane claim=%+v", snap.Rows)
	}
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("raw-pane exclusion exit=%d stderr=%q", code, stderr)
	}
	assertSoH234InventoryResult(t, env, repo, bin, out, stderr, before, nil)
}

func TestSoh234InventoryExactRegistrationSuppressesAdvisory(t *testing.T) {
	env, repo, bin := soh234InventoryFixture(t, "inventory=show\n", "orchestrator", nil, "working", fakecli.Rule{
		Argv: []string{"agent", "read", "orchestrator", "--source", "visible"}, Stdout: "planning the next move\n",
	})
	soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "orchestrator")
	soh234CLIRecord(t, env, repo, "p234")
	prerequisites := soh234InventoryCalls(t, bin)
	assertSoH234InventoryCalls(t, prerequisites)
	start := len(prerequisites)
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("registered consumer exit=%d stderr=%q", code, stderr)
	}
	doc := decodeInventoryCLI(t, out)
	var projects []struct {
		Sessions []struct {
			Agents []struct{ Terminal, Name, Role, Kind string }
		}
	}
	if err := json.Unmarshal(doc["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || len(projects[0].Sessions) != 1 || len(projects[0].Sessions[0].Agents) != 1 {
		t.Fatalf("exact registration was not consumed: %s", out)
	}
	a := projects[0].Sessions[0].Agents[0]
	if a.Terminal != "p234" || a.Name != "orchestrator" || a.Role != "orchestrator" || a.Kind != "claude" {
		t.Fatalf("registered agent=%+v", a)
	}
	assertSoH234RegisteredLocalCalls(t, soh234InventoryCalls(t, bin)[start:], "herdr", false)
	if !reflect.DeepEqual(before, inventoryCLITree(t, filepath.Dir(repo))) {
		t.Fatal("registered inventory changed fixture tree")
	}
}

func TestSoh234InventoryPrivacyAndSourceRefusals(t *testing.T) {
	for _, shape := range []string{"project-off", "session-off", "malformed-roster", "malformed-record", "missing-base", "unsafe-session"} {
		t.Run(shape, func(t *testing.T) {
			mode := "show"
			if shape == "project-off" {
				mode = "off"
			}
			env, repo, bin := soh234AdvisoryFixture(t, mode)
			soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "orchestrator")
			switch shape {
			case "project-off", "session-off":
				if shape == "session-off" {
					soh234CLISessionFile(t, env, "session.conf", "inventory=off\n")
				}
				soh234CLISource(t, env, false)
			case "malformed-roster":
				soh234CLISessionFile(t, env, "agents.tsv", "worker\tother-pane\tclaude\timplementer\nworker\tother-pane\tclaude\timplementer\n")
				soh234CLISource(t, env, true)
			case "malformed-record":
				soh234CLISessionFile(t, env, sessionmeta.RecordName, "{corrupt-private-record")
			case "missing-base":
				env["SOHO_DIR"] = filepath.Join(env["SOHO_DIR"], "missing")
				soh234CLISource(t, env, true)
			case "unsafe-session":
				if err := os.WriteFile(filepath.Join(env["SOHO_DIR"], "ws234"), []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				soh234CLISource(t, env, true)
			}
			if shape == "malformed-record" {
				_, found, err := (sessionmeta.Store{StateBase: env["SOHO_DIR"], SessionID: "ws234"}).Read()
				if err == nil || found {
					t.Fatalf("setup: corrupt record found=%v err=%v", found, err)
				}
			}
			before := inventoryCLITree(t, filepath.Dir(repo))
			// Retry the same bad source to prove it remains refused and untouched.
			for attempt := 0; attempt < 2; attempt++ {
				start := len(soh234InventoryCalls(t, bin))
				code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
				if code != 0 || stderr != "" {
					t.Fatalf("attempt=%d exit=%d stderr=%q", attempt, code, stderr)
				}
				assertSoH234InventoryResult(t, env, repo, bin, out, stderr, before, nil)
				assertSoH234InventoryCalls(t, soh234InventoryCalls(t, bin)[start:])
				for _, call := range soh234InventoryCalls(t, bin)[start:] {
					if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "get" {
						t.Fatalf("refused source reached status: %v", call.Argv)
					}
				}
			}
		})
	}
}

func TestSoh234InventoryFreshStatusRefusals(t *testing.T) {
	for _, state := range []string{"unavailable", "gone", "unknown"} {
		t.Run(state, func(t *testing.T) {
			env, repo, bin := soh234InventoryFixture(t, "inventory=show\n", "orchestrator", nil, state)
			soh234RequireCandidate(t, soh234InventoryPrerequisites(t, env, repo), repo, "orchestrator")
			fresh := backend.New(env).Runtime.Get(context.Background(), transport.Target{ID: "orchestrator"}, transport.Call{})
			if fresh.Value.State != state || (state == "unavailable" && fresh.Err == nil) {
				t.Fatalf("setup: fresh observation=%+v want=%s", fresh, state)
			}
			before := inventoryCLITree(t, filepath.Dir(repo))
			code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("status=%s exit=%d stderr=%q", state, code, stderr)
			}
			assertSoH234InventoryResult(t, env, repo, bin, out, stderr, before, nil)
		})
	}
}

func TestSoh234InventoryMixedUnverifiableObservations(t *testing.T) {
	for _, shape := range []string{"unknown-kind", "ambiguous-pane", "fresh-gone"} {
		t.Run(shape, func(t *testing.T) {
			freshState := "working"
			if shape == "fresh-gone" {
				freshState = "gone"
			}
			env, repo, bin := soh234InventoryFixture(t, "inventory=show\n", "orchestrator", func(root string) string {
				body := soh234LiveAgent("orchestrator", "p234", "claude", "ws234", root)
				extra := soh234LiveAgent("orchestrator-2", "p235", "claude", "ws234", root)
				if shape == "unknown-kind" {
					extra = soh234LiveAgent("orchestrator-2", "p235", "notakind", "ws234", root)
				}
				if shape == "ambiguous-pane" {
					extra += "," + soh234LiveAgent("conflict", "p235", "codex", "ws234", root)
				}
				return body + "," + extra
			}, "working", fakecli.Rule{
				Argv: []string{"agent", "get", "orchestrator-2"}, Stdout: `{"result":{"agent":{"agent_status":"` + freshState + `"}}}`,
			}, fakecli.Rule{
				Argv: []string{"agent", "read", "orchestrator", "--source", "visible"}, Stdout: "planning the next move\n",
			})
			terms := soh234InventoryPrerequisites(t, env, repo)
			soh234RequireCandidate(t, terms, repo, "orchestrator")
			if len(terms) < 2 || terms[1].PaneID != "p235" {
				t.Fatalf("setup: mixed observations missing: %+v", terms)
			}
			if shape == "unknown-kind" && !reflect.DeepEqual(terms[1].ProgramHints, []string{"notakind"}) {
				t.Fatalf("setup: unknown kind lost: %+v", terms[1])
			}
			if shape == "ambiguous-pane" && (len(terms) != 3 || terms[2].PaneID != "p235" || terms[1].Name == terms[2].Name) {
				t.Fatalf("setup: conflicting pane observations lost: %+v", terms)
			}
			fresh := backend.New(env).Runtime.Get(context.Background(), transport.Target{ID: "orchestrator-2"}, transport.Call{})
			if fresh.Err != nil || fresh.Value.State != freshState {
				t.Fatalf("setup: extra observation fresh status=%+v want=%s", fresh, freshState)
			}
			soh234CLIRecord(t, env, repo, "p234")
			prerequisites := soh234InventoryCalls(t, bin)
			assertSoH234InventoryCalls(t, prerequisites)
			start := len(prerequisites)
			before := inventoryCLITree(t, filepath.Dir(repo))
			code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("mixed observations exit=%d stderr=%q", code, stderr)
			}
			decodeInventoryCLI(t, out)
			if strings.Contains(out, "p235") || !strings.Contains(out, "p234") {
				t.Fatalf("mixed observations changed registered inventory: %s", out)
			}
			assertSoH234RegisteredLocalCalls(t, soh234InventoryCalls(t, bin)[start:], "herdr", shape == "fresh-gone")
			if !reflect.DeepEqual(before, inventoryCLITree(t, filepath.Dir(repo))) {
				t.Fatal("mixed inventory changed fixture tree")
			}
		})
	}
}

func TestSoh234InventoryLocalRegistrationPlusRemoteObservation(t *testing.T) {
	env, repo := commandFixture(t)
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte("inventory=show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	local := hcRecord("p234", "orchestrator", repo, "claude", "ws234")
	remote := hcRecord("remote/p235", "orchestrator-2", repo, "claude", "ws234")
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[` + local + "," + remote + `]}`},
		{Argv: []string{"get", "orchestrator", "--json"}, Stdout: local},
		{Argv: []string{"read", "orchestrator", "--lines", "0", "--json"}, Stdout: `{"text":"Busy on the task 30%\n"}`},
	}); err != nil {
		t.Fatal(err)
	}
	env = withHostGit(t, withFakeCLI(env, bin), bin)
	env["SOHO_TRANSPORT"], env["SOHO_NOWRITE"] = "hc", "1"
	delete(env, "HERDR_WORKSPACE_ID")
	terms := soh234InventoryPrerequisites(t, env, repo)
	if len(terms) != 1 || terms[0].PaneID != "p234" {
		t.Fatalf("setup: remote was not excluded from local listing: %+v", terms)
	}
	soh234CLIRecord(t, env, repo, "p234")
	prerequisites := inventoryCLICalls(t, bin, "hc")
	if len(prerequisites) != 1 || !reflect.DeepEqual(prerequisites[0].Argv, []string{"ls", "--json"}) {
		t.Fatalf("unexpected local-list prerequisites: %+v", prerequisites)
	}
	start := len(prerequisites)
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, "p234") || strings.Contains(out, "p235") {
		t.Fatalf("local/remote exit=%d stdout=%s stderr=%q", code, out, stderr)
	}
	decodeInventoryCLI(t, out)
	assertSoH234RegisteredLocalCalls(t, inventoryCLICalls(t, bin, "hc")[start:], "hc", false)
	if !reflect.DeepEqual(before, inventoryCLITree(t, filepath.Dir(repo))) {
		t.Fatal("local/remote inventory changed fixture tree")
	}
}

func TestSoh234InitDoesNotAdviseBeforeRegistration(t *testing.T) {
	env, repo := commandFixture(t)
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte("inventory=show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	rules := append(herdrInitRules(repo, soh234LiveAgent("orchestrator", "p1", "claude", "ws", repo)), fakecli.Rule{
		Argv: []string{"agent", "get", "orchestrator"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`,
	})
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withHostGit(t, withFakeCLI(env, bin), bin)
	env["HERDR_ENV"], env["HERDR_PANE_ID"], env["SOHO_TRANSPORT"] = "1", "p1", "herdr"
	terms := soh234InventoryPrerequisites(t, env, repo)
	caller := backend.New(env).Runtime.Caller()
	if !caller.Inside || caller.PaneID != "p1" || caller.WorkspaceID != "ws" || len(terms) != 1 || terms[0].PaneID != "p1" || terms[0].WorkspaceID != "ws" || terms[0].Cwd != repo {
		t.Fatalf("setup: init caller=%+v listing=%+v", caller, terms)
	}
	fresh := backend.New(env).Runtime.Get(context.Background(), transport.Target{ID: "orchestrator"}, transport.Call{})
	if fresh.Err != nil || fresh.Value.State != "working" {
		t.Fatalf("setup: init fresh status=%+v", fresh)
	}
	if _, found, err := (sessionmeta.Store{StateBase: env["SOHO_DIR"], SessionID: "ws"}).Read(); err != nil || found {
		t.Fatalf("setup: init must start without a record: found=%v err=%v", found, err)
	}
	code, out, stderr := runInitCaptured(t, env)
	if code != 0 || !strings.Contains(out, `"pane_id": "p1"`) || strings.Contains(stderr, soh234Advisory) || strings.Contains(out, soh234Advisory) {
		t.Fatalf("init exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	rec, found, err := (sessionmeta.Store{StateBase: env["SOHO_DIR"], SessionID: "ws"}).Read()
	if err != nil || !found || rec.ProjectRoot != repo || rec.Terminal != "p1" || rec.SessionID != "ws" || rec.Name != "orchestrator" || rec.Kind != "claude" {
		t.Fatalf("init did not actually register: record=%+v found=%v err=%v", rec, found, err)
	}
}
