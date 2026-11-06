package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
// SOH-234 slice proves through the real doctor consumer. It is the literal
// contract message; it never carries names, roots, paths, session ids,
// counts or status text, and it is emitted through the normal Say.Warning
// stream at most once.
const soh234Advisory = "an apparent orchestrator is not registered; run soho init in its pane"

// soh234DoctorAgent is one agent-list entry of the synthetic native fixture:
// the exact five-field protocol the herdr listing reports.
func soh234DoctorAgent(name, pane, agent, session, cwd string) string {
	body, _ := json.Marshal(map[string]string{
		"name": name, "pane_id": pane, "agent": agent, "workspace_id": session, "cwd": cwd,
	})
	return string(body)
}

// soh234DoctorFixture builds a hermetic doctor environment whose cwd is a
// git repo (so the current strict canonical project is well-defined), with
// the project's opt-in conf, a fake herdr answering the doctor's probes and
// the agent list, and the state base. agentListBody is built from the
// resolved root so the qualifying terminal's cwd is the canonical project.
func soh234DoctorFixture(t *testing.T, conf string, agentListBody func(root string) string, agentGetStatus string) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state")
	skill := filepath.Join(root, "skill")
	for _, dir := range []string{home, bin, state, skill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// make root a git repo so the current strict canonical project is
	// well-defined (the advisory binds to the canonical root, not the cwd).
	init := exec.Command("git", "init", "-q")
	init.Dir = root
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	body := agentListBody(root)
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"pane", "get", "p234"}, Stdout: `{"result":{"pane":{"workspace_id":"ws234","title":"fixture"}}}`},
		{Argv: []string{"pane", "current", "--current"}, Code: 1, Stderr: "workspace discovery unavailable\n"},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + body + `]}}`},
		{Argv: []string{"agent", "get", "orchestrator"}, Stdout: `{"result":{"agent":{"agent_status":"` + agentGetStatus + `","state_change_seq":1}}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME": home, "USERPROFILE": home, "PATH": bin,
		"XDG_CONFIG_HOME": filepath.Join(home, "config"), "TMPDIR": root,
		"HERDR_ENV":           "1",
		"HERDR_PANE_ID":       "p234",
		"HERDR_WORKSPACE_ID":  "ws234",
		"SOHO_TRANSPORT":      "herdr",
		"SOHO_NOWRITE":        "1",
		"SOHO_CALLER_CHECK":   "off",
		"HERDR_SOCKET_PATH":   filepath.Join(root, "missing.sock"),
		"SOHO_DIR":            state,
		"SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR":      skill,
	}
	// the doctor's canonical-root seam runs real read-only git in the fixture
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("the host has no git: %v", err)
	}
	env["PATH"] = bin + string(filepath.ListSeparator) + filepath.Dir(gitPath)
	if conf != "" {
		if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return env, root
}

// soh234DoctorOutput runs the public DoctorCheck entry with the project
// configuration loaded from root (mirroring the doctor command's production
// load) and returns the normal output stream.
func soh234DoctorOutput(t *testing.T, env platform.Env, root string) string {
	t.Helper()
	ctx := core.LoadConfig(env, root)
	before := soh234DoctorState(t, env["SOHO_DIR"])
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	if !reflect.DeepEqual(before, soh234DoctorState(t, env["SOHO_DIR"])) {
		t.Fatal("doctor changed the state tree")
	}
	if env["HERDR_ENV"] == "1" && env["HERDR_WORKSPACE_ID"] == "ws234" && !strings.HasPrefix(out.String(), "ok     inside Herdr (HERDR_ENV=1)\n") {
		t.Fatalf("setup: native context stage failed:\n%s", out.String())
	}
	return out.String()
}

func soh234DoctorState(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		snap[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func soh234DoctorPrerequisites(t *testing.T, env platform.Env, root string) {
	t.Helper()
	caller := backend.New(env).Runtime.Caller()
	if !caller.Inside || caller.PaneID != "p234" || caller.WorkspaceID != "ws234" {
		t.Fatalf("setup: native caller=%+v", caller)
	}
	canonical, err := platform.StateProjectRootWithGit(root, func(args ...string) (string, int) {
		r := dispatch.ReferenceGit(env, append([]string{"-C", root}, args...)...)
		if r.NotFound || r.Error != "" || r.TimedOut || r.Status == nil {
			return "", 1
		}
		return r.Stdout, *r.Status
	})
	if err != nil || canonical != root {
		t.Fatalf("setup: canonical root=%q want=%q err=%v", canonical, root, err)
	}
	rep := backend.New(env).Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil || !rep.Valid || len(rep.Value) != 1 {
		t.Fatalf("setup: listing=%+v", rep)
	}
	term := rep.Value[0]
	if term.Name != "orchestrator" || term.PaneID != "p234" || term.WorkspaceID != "ws234" || term.Cwd != root || !reflect.DeepEqual(term.ProgramHints, []string{"claude"}) {
		t.Fatalf("setup: exact candidate=%+v", term)
	}
	ctx := core.LoadConfig(env, root)
	if core.Cfg(&ctx, "inventory", "", env) != "show" {
		t.Fatal("setup: current project is not opted in")
	}
}

func soh234QualifyingBody(root string) string {
	return soh234DoctorAgent("orchestrator", "p234", "claude", "ws234", root)
}

func soh234WriteSessionFile(t *testing.T, state, session, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(state, session), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, session, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func soh234RecordJSON(t *testing.T, root, session, pane string) []byte {
	t.Helper()
	rec, err := json.Marshal(sessionmeta.Record{Schema: 1, ProjectRoot: root, SessionID: session, Terminal: pane, Name: "orchestrator", Kind: "claude", StartedMS: 42, Task: ""})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func soh234AssertDoctorRecord(t *testing.T, env platform.Env, root, pane string) {
	t.Helper()
	got, found, err := (sessionmeta.Store{StateBase: env["SOHO_DIR"], SessionID: "ws234"}).Read()
	want := sessionmeta.Record{Schema: 1, ProjectRoot: root, SessionID: "ws234", Terminal: pane, Name: "orchestrator", Kind: "claude", StartedMS: 42, Task: ""}
	if err != nil || !found || got != want {
		t.Fatalf("setup: strict record read=%+v found=%v err=%v want=%+v", got, found, err, want)
	}
}

func soh234DoctorSource(t *testing.T, env platform.Env, wantErr bool) core.RosterSnapshot {
	t.Helper()
	source, err := core.OpenSessionSource(env["SOHO_DIR"], "ws234", env["TMPDIR"])
	if wantErr {
		if err == nil {
			_ = source.Close()
			t.Fatal("setup: unsafe source accepted")
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

// TestSoh234DoctorAdvisesMissingRegistration is the doctor red: the current
// strict canonical project and current session hold a qualifying
// orchestrator-named terminal that is not registered and not rostered, so
// the normal warning stream carries exactly one fixed advisory.
func TestSoh234DoctorAdvisesMissingRegistration(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	soh234DoctorPrerequisites(t, env, root)
	if rows := soh234DoctorSource(t, env, false).Rows; len(rows) != 0 {
		t.Fatalf("setup: missing session is not safe absence: %+v", rows)
	}
	out := soh234DoctorOutput(t, env, root)
	if n := strings.Count(out, soh234Advisory); n != 1 {
		t.Fatalf("the missing-registration advisory appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "warn   "+soh234Advisory+"\n") {
		t.Fatalf("the advisory is not a normal warn line:\n%s", out)
	}
}

// TestSoh234DoctorRecordOnAnotherPaneDoesNotSuppress proves the exact-pane
// binding: a valid record bound to another pane does not suppress this
// pane's advisory.
func TestSoh234DoctorRecordOnAnotherPaneDoesNotSuppress(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	soh234WriteSessionFile(t, env["SOHO_DIR"], "ws234", "orchestrator.json", string(soh234RecordJSON(t, root, "ws234", "p1")))
	soh234AssertDoctorRecord(t, env, root, "p1")
	soh234DoctorPrerequisites(t, env, root)
	if rows := soh234DoctorSource(t, env, false).Rows; len(rows) != 0 {
		t.Fatalf("setup: missing roster is not safe absence: %+v", rows)
	}
	out := soh234DoctorOutput(t, env, root)
	if n := strings.Count(out, soh234Advisory); n != 1 {
		t.Fatalf("a record on another pane suppressed this pane's advisory (count=%d):\n%s", n, out)
	}
}

// TestSoh234DoctorRegistrationSuppressesAdvisory: a valid live registration
// bound to the exact pane suppresses the advisory.
func TestSoh234DoctorRegistrationSuppressesAdvisory(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	soh234WriteSessionFile(t, env["SOHO_DIR"], "ws234", "orchestrator.json", string(soh234RecordJSON(t, root, "ws234", "p234")))
	soh234AssertDoctorRecord(t, env, root, "p234")
	soh234DoctorPrerequisites(t, env, root)
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("a valid registration on the exact pane still advised:\n%s", got)
	}
}

// TestSoh234DoctorRosterRowOnPaneSuppressesAdvisory: a raw roster row
// claiming the pane suppresses the advisory even when the row is not valid.
func TestSoh234DoctorRosterRowOnPaneSuppressesAdvisory(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	// an invalid row (empty kind/role) so row.Valid=false, but the raw pane
	// claim is present and must still suppress the advisory.
	roster := "# name\tpane\tkind\trole\norchestrator\tp234\t\t\n"
	soh234WriteSessionFile(t, env["SOHO_DIR"], "ws234", "agents.tsv", roster)
	snap := soh234DoctorSource(t, env, false)
	if len(snap.Rows) != 1 || snap.Rows[0].Valid || len(snap.Rows[0].Fields) != 4 || snap.Rows[0].Fields[1] != "p234" {
		t.Fatalf("setup: raw invalid pane claim=%+v", snap.Rows)
	}
	soh234DoctorPrerequisites(t, env, root)
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("a raw roster row claiming the pane still advised:\n%s", got)
	}
}

// TestSoh234DoctorOffSuppressesAdvisory checks project opt-out silence;
// it does not count filesystem reads.
func TestSoh234DoctorOffSuppressesAdvisory(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=off\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("an off project still advised:\n%s", got)
	}
}

// TestSoh234DoctorSessionOffSuppressesAdvisory: off at the session level
// (which may only lower the project mode) also stops the advisory.
func TestSoh234DoctorSessionOffSuppressesAdvisory(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	soh234WriteSessionFile(t, env["SOHO_DIR"], "ws234", "session.conf", "inventory=off\n")
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("a session-off project still advised:\n%s", got)
	}
}

// TestSoh234DoctorGoneTerminalNeverAdvises: a freshly gone terminal is
// excluded by the terminal-status fold and never advises.
func TestSoh234DoctorGoneTerminalNeverAdvises(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "gone")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("a freshly gone terminal still advised:\n%s", got)
	}
}

// TestSoh234DoctorUnknownKindNeverAdvises: a terminal without a recognized
// catalog kind is never a qualifying candidate.
func TestSoh234DoctorUnknownKindNeverAdvises(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", func(root string) string {
		return soh234DoctorAgent("orchestrator", "p234", "notakind", "ws234", root)
	}, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("an unknown-kind terminal still advised:\n%s", got)
	}
}

// TestSoh234DoctorAmbiguousPaneNeverAdvises: two conflicting observations
// of one pane make it unverifiable, so it never advises.
func TestSoh234DoctorAmbiguousPaneNeverAdvises(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", func(root string) string {
		return soh234DoctorAgent("orchestrator", "p234", "claude", "ws234", root) + "," +
			soh234DoctorAgent("other", "p234", "codex", "ws234", root)
	}, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("an ambiguous pane still advised:\n%s", got)
	}
}

// TestSoh234DoctorCorruptRecordSuppressesAdvisory: a corrupt registration
// record is an acquisition error, never a safe absence, so it suppresses
// the advisory rather than inventing one.
func TestSoh234DoctorCorruptRecordSuppressesAdvisory(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	soh234WriteSessionFile(t, env["SOHO_DIR"], "ws234", "orchestrator.json", "{corrupt")
	soh234DoctorPrerequisites(t, env, root)
	if _, found, err := (sessionmeta.Store{StateBase: env["SOHO_DIR"], SessionID: "ws234"}).Read(); err == nil || found {
		t.Fatalf("setup: corrupt record found=%v err=%v", found, err)
	}
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("a corrupt record was treated as a safe absence:\n%s", got)
	}
}

// TestSoh234DoctorOutsideCallerNoAdvisory: outside a native session the
// doctor keeps its existing diagnostics without this advice.
func TestSoh234DoctorOutsideCallerNoAdvisory(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
	env["HERDR_WORKSPACE_ID"] = "ws234"
	env["HERDR_ENV"] = ""
	got := soh234DoctorOutput(t, env, root)
	if strings.Contains(got, soh234Advisory) {
		t.Fatalf("an outside-caller doctor still advised:\n%s", got)
	}
}

func TestSoh234DoctorUnavailableStatusNeverAdvises(t *testing.T) {
	env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "unavailable")
	soh234DoctorPrerequisites(t, env, root)
	fresh := backend.New(env).Runtime.Get(context.Background(), transport.Target{ID: "orchestrator"}, transport.Call{})
	if fresh.Value.State != "unavailable" || fresh.Err == nil {
		t.Fatalf("setup: fresh unavailable status=%+v", fresh)
	}
	if got := soh234DoctorOutput(t, env, root); strings.Contains(got, soh234Advisory) {
		t.Fatalf("unavailable status manufactured liveness:\n%s", got)
	}
}

func TestSoh234DoctorUnknownSourceNeverAdvises(t *testing.T) {
	for _, shape := range []string{"unknown-session", "missing-base", "malformed-roster", "unsafe-session"} {
		t.Run(shape, func(t *testing.T) {
			env, root := soh234DoctorFixture(t, "inventory=show\n", soh234QualifyingBody, "working")
			soh234DoctorPrerequisites(t, env, root)
			switch shape {
			case "unknown-session":
				env["HERDR_WORKSPACE_ID"] = ""
				if source, err := core.OpenSessionSource(env["SOHO_DIR"], "", env["TMPDIR"]); err == nil {
					_ = source.Close()
					t.Fatal("setup: unknown session accepted")
				}
			case "missing-base":
				env["SOHO_DIR"] = filepath.Join(env["SOHO_DIR"], "missing")
				soh234DoctorSource(t, env, true)
			case "malformed-roster":
				soh234WriteSessionFile(t, env["SOHO_DIR"], "ws234", "agents.tsv", "worker\tother-pane\tclaude\timplementer\nworker\tother-pane\tclaude\timplementer\n")
				soh234DoctorSource(t, env, true)
			case "unsafe-session":
				if err := os.WriteFile(filepath.Join(env["SOHO_DIR"], "ws234"), []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				soh234DoctorSource(t, env, true)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if got := soh234DoctorOutput(t, env, root); strings.Contains(got, soh234Advisory) {
					t.Fatalf("attempt=%d unknown/unsafe source treated as absence:\n%s", attempt, got)
				}
			}
		})
	}
}

func TestSoh234DoctorCurrentProjectIsolation(t *testing.T) {
	other := ""
	env, root := soh234DoctorFixture(t, "inventory=show\n", func(string) string {
		var err error
		other, err = filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		init := exec.Command("git", "init", "-q")
		init.Dir = other
		if out, err := init.CombinedOutput(); err != nil {
			t.Fatalf("setup: other project's git init: %s: %v", out, err)
		}
		if err := os.MkdirAll(filepath.Join(other, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, ".agents", "soho.conf"), []byte("inventory=show\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return soh234QualifyingBody(other)
	}, "working")
	if other == root {
		t.Fatal("setup: project isolation needs different canonical roots")
	}
	for _, project := range []string{root, other} {
		canonical, err := platform.StateProjectRootWithGit(project, func(args ...string) (string, int) {
			r := dispatch.ReferenceGit(env, append([]string{"-C", project}, args...)...)
			if r.NotFound || r.Error != "" || r.TimedOut || r.Status == nil {
				return "", 1
			}
			return r.Stdout, *r.Status
		})
		ctx := core.LoadConfig(env, project)
		if err != nil || canonical != project || core.Cfg(&ctx, "inventory", "", env) != "show" {
			t.Fatalf("setup: project=%q canonical=%q err=%v", project, canonical, err)
		}
	}
	rep := backend.New(env).Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil || !rep.Valid || len(rep.Value) != 1 || rep.Value[0].Cwd != other || rep.Value[0].WorkspaceID != "ws234" || rep.Value[0].PaneID != "p234" {
		t.Fatalf("setup: other project's matching terminal=%+v", rep)
	}
	if got := soh234DoctorOutput(t, env, root); strings.Contains(got, soh234Advisory) {
		t.Fatalf("another opted-in project caused current-project advice:\n%s", got)
	}
}
