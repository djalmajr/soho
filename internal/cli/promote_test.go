package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const promotePane = "ws:pS"

// promotePaneCurrent is the native caller context the default fixtures
// report: the same pane the inherited env names (tab ws:tS, workspace ws).
const promotePaneCurrent = `{"result":{"pane":{"pane_id":"ws:pS","tab_id":"ws:tS","workspace_id":"ws"}}}`

// promoteFixture is a hermetic project whose caller pane is ws:pS. The
// herdr fake answers the native pane current with the canonical caller
// context, the agent get with liveName/liveStatus, and the unique-name
// list with an empty agent list; the extra rules (placed first, so they
// win) carry the verb behavior the scenario needs. Rules are matched
// first-come.
func promoteFixture(t *testing.T, liveName, liveStatus string, extra ...fakecli.Rule) (*copiesFixture, string) {
	t.Helper()
	f := newCopiesFixture(t, nil)
	fakeDir := t.TempDir()
	rules := append([]fakecli.Rule{}, extra...)
	rules = append(rules,
		fakecli.Rule{Argv: []string{"pane", "current", "--current"}, Stdout: promotePaneCurrent},
		fakecli.Rule{Argv: []string{"agent", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"agent":{"name":%q,"pane_id":%q,"workspace_id":"ws","cwd":%q,"agent_status":%q}}}`, liveName, promotePane, f.repo, liveStatus)},
		fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`})
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_FAKECLI_CONFIG"] = fakeDir
	f.env["PATH"] = fakeDir + string(os.PathListSeparator) + f.env.Get("PATH")
	f.env["HERDR_PANE_ID"] = promotePane
	// Production state initialization (creates the subdirs and the roster
	// header the same way StateDir does at runtime).
	ctx := core.LoadConfig(f.env, f.cwd)
	core.StateDir(&ctx, f.env, f.cwd)
	return f, "orchestrator: " + filepath.Base(f.repo)
}

// standard promote verb rules (the happy-path behavior): the rename
// succeeds, the pane title is empty, and the title set succeeds. The
// report-metadata rule is a prefix rule (its exact title argument is only
// known once the fixture exists); the tests assert the exact title from the
// recorded call log.
func promoteVerbRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "rename", promotePane, "orchestrator"}},
		{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":""}}}`, promotePane)},
		{Argv: []string{"pane", "report-metadata", promotePane, "--source", "soho"}, ArgvPrefix: true},
	}
}

// promoteRoster appends the sub-orchestrator row the tests promote and a
// sibling worker row that must survive every scenario.
func promoteRoster(t *testing.T, sd, callerName string) {
	t.Helper()
	core.RosterAppend(sd, []string{callerName, promotePane, "codex", "sub-orchestrator", "openai", "0", "/repo", "sub-start", "gpt-5.4", "ask", "sub-orchestrator", "-"})
	core.RosterAppend(sd, []string{"impl-1", "ws:p1", "claude", "implementer", "anthropic", "0", "/repo", "impl-start", "claude-opus-5-5", "ask", "implementer", "build"})
}

// runPromote executes the promote handler directly (root registers the
// command in Run after review), with the fixture's cwd and captured stdout/
// stderr, recovering the platform.Die boundary the same way Run does.
func runPromote(t *testing.T, f *copiesFixture, env platform.Env, argv ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	ctx := core.LoadConfig(env, f.cwd)
	code := 0
	func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			exitErr, ok := value.(*platform.ExitError)
			if !ok {
				panic(value)
			}
			if exitErr.Msg != "" {
				_, _ = fmt.Fprintf(&errOut, "soho: %s\n", exitErr.Msg)
			}
			code = exitErr.Code
		}()
		code = cmdPromote(argv, &ctx, env, f.cwd)
	}()
	return code, out.String(), errOut.String()
}

// promoteCalls returns the recorded herdr calls; a missing log is the zero-
// calls case (the handler probed nothing).
func promoteCalls(t *testing.T, env platform.Env) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(env.Get("SOHO_FAKECLI_CONFIG"), "herdr.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return calls
}

func hasCall(calls []fakecli.Call, argv ...string) bool {
	for _, c := range calls {
		if len(c.Argv) == len(argv) {
			same := true
			for i := range argv {
				if c.Argv[i] != argv[i] {
					same = false
					break
				}
			}
			if same {
				return true
			}
		}
	}
	return false
}

// assertOnlyScopeCalls proves the promotion only probes and updates the
// caller's own scope: the only herdr verbs it may invoke are the scope and
// identity reads (pane current, agent get, agent list) and the caller's own
// rename, pane read and title set. No pane close, no pane/agent focus (a
// move), no tab operation, and nothing else (no gc, no stop, no release)
// may appear in the call log.
func assertOnlyScopeCalls(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	allowed := map[string]bool{
		"pane\tcurrent": true, "agent\tget": true, "agent\tlist": true,
		"agent\trename": true, "pane\tget": true, "pane\treport-metadata": true,
	}
	for _, c := range calls {
		if len(c.Argv) == 0 {
			t.Fatalf("promote logged an empty herdr call")
		}
		verb := c.Argv[0]
		if len(c.Argv) > 1 {
			if !allowed[verb+"\t"+c.Argv[1]] {
				t.Fatalf("promote invoked an out-of-scope herdr verb: %v", c.Argv)
			}
			continue
		}
		if verb != "pane" && verb != "agent" {
			t.Fatalf("promote invoked an out-of-scope herdr verb: %v", c.Argv)
		}
	}
}

func rosterRaw(t *testing.T, sd string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sd, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// promoteOut is the handler's stdout JSON.
type promoteOut struct {
	PaneID          string `json:"pane_id"`
	TabID           string `json:"tab_id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Role            string `json:"role"`
	RosterRow       string `json:"roster_row"`
	Title           string `json:"title"`
	TitleKept       bool   `json:"title_kept"`
	AlreadyPromoted bool   `json:"already_promoted"`
}

func parsePromoteOut(t *testing.T, out string) promoteOut {
	t.Helper()
	var res promoteOut
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unparseable promote output: %q (%v)", out, err)
	}
	return res
}

// registryFile writes a copies.tsv or procs.tsv body into the state dir.
func registryFile(t *testing.T, sd, name, body string) string {
	t.Helper()
	path := filepath.Join(sd, name)
	if err := os.WriteFile(path, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPromoteSuccessWorkingCaller(t *testing.T) {
	f, wantTitle := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	code, out, stderr := runPromote(t, f, f.env)
	if code != 0 {
		t.Fatalf("a working caller must be promoted: code=%d stderr=%q", code, stderr)
	}
	res := parsePromoteOut(t, out)
	if res.Name != "orchestrator" || res.Role != "orchestrator" || res.RosterRow != "removed" || res.Title != wantTitle || res.TitleKept || res.AlreadyPromoted {
		t.Fatalf("unexpected result: %+v (want name orchestrator, row removed, title %q)", res, wantTitle)
	}
	if res.Status != "working" {
		t.Fatalf("the report must carry the caller's working state, got %q", res.Status)
	}
	if res.PaneID != promotePane || res.TabID != "ws:tS" {
		t.Fatalf("the report must carry the actual native pane and tab: %+v", res)
	}
	raw := rosterRaw(t, sd)
	if strings.Contains(raw, "sub-orch\t"+promotePane) {
		t.Fatalf("the caller's worker row survived:\n%s", raw)
	}
	if !strings.Contains(raw, "impl-1\tws:p1") || !strings.HasPrefix(raw, "# name") {
		t.Fatalf("the sibling row or the header was lost:\n%s", raw)
	}
	calls := promoteCalls(t, f.env)
	if !hasCall(calls, "agent", "rename", promotePane, "orchestrator") {
		t.Fatalf("the generic name was not renamed: %v", calls)
	}
	if !hasCall(calls, "pane", "report-metadata", promotePane, "--source", "soho", "--title", wantTitle) {
		t.Fatalf("the orchestrator title was not set: %v", calls)
	}
	assertOnlyScopeCalls(t, calls)
	if _, err := os.Stat(filepath.Join(sd, "agents.lock")); !os.IsNotExist(err) {
		t.Fatalf("the roster lock was left behind: %v", err)
	}
}

func TestPromoteSuccessKeepsAnOrchestratorName(t *testing.T) {
	for _, name := range []string{"orchestrator", "orchestrator-2"} {
		t.Run(name, func(t *testing.T) {
			f, wantTitle := promoteFixture(t, name, "idle",
				fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":""}}}`, promotePane)},
				fakecli.Rule{Argv: []string{"pane", "report-metadata", promotePane, "--source", "soho"}, ArgvPrefix: true})
			sd := f.stateDir(t)
			promoteRoster(t, sd, name)
			code, out, stderr := runPromote(t, f, f.env)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			res := parsePromoteOut(t, out)
			if res.Name != name || res.RosterRow != "removed" || res.Title != wantTitle {
				t.Fatalf("unexpected result: %+v", res)
			}
			if !hasCall(promoteCalls(t, f.env), "pane", "report-metadata", promotePane, "--source", "soho", "--title", wantTitle) {
				t.Fatalf("the orchestrator title was not set exactly: %v", promoteCalls(t, f.env))
			}
			if hasCall(promoteCalls(t, f.env), "agent", "rename", promotePane, name) {
				t.Fatal("an already-matching name must not be renamed")
			}
		})
	}
}

func TestPromoteTitleFromAnExistingWorkerTitle(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working",
		fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}},
		fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":"sub-orchestrator: Build the thing"}}}`, promotePane)},
		fakecli.Rule{Argv: []string{"pane", "report-metadata", promotePane, "--source", "soho"}, ArgvPrefix: true})
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	code, out, stderr := runPromote(t, f, f.env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	res := parsePromoteOut(t, out)
	if res.Title != "orchestrator: Build the thing" || res.TitleKept {
		t.Fatalf("the worker title must be relabeled under orchestrator: %+v", res)
	}
	if !hasCall(promoteCalls(t, f.env), "pane", "report-metadata", promotePane, "--source", "soho", "--title", "orchestrator: Build the thing") {
		t.Fatalf("the relabeled title was not set: %v", promoteCalls(t, f.env))
	}
}

func TestPromoteTitleKeepsAnOrchestratorTitle(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working",
		fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}},
		fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":"orchestrator: Ship it"}}}`, promotePane)})
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	code, out, stderr := runPromote(t, f, f.env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	res := parsePromoteOut(t, out)
	if res.Title != "orchestrator: Ship it" || !res.TitleKept {
		t.Fatalf("an existing orchestrator title must be kept untouched: %+v", res)
	}
	if hasCallPrefix(promoteCalls(t, f.env), "pane", "report-metadata") {
		t.Fatal("a kept title must not be rewritten")
	}
}

func TestPromoteTitleFromACustomTitle(t *testing.T) {
	for _, tc := range []struct{ existing, want string }{
		{"Build the thing", "orchestrator: Build the thing"},
		{strings.Repeat("x", 80), "orchestrator: " + strings.Repeat("x", 60)},
		{"line\nbreak\ttitle", "orchestrator: line break title"},
	} {
		t.Run(tc.existing, func(t *testing.T) {
			f, _ := promoteFixture(t, "sub-orch", "working",
				fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}},
				fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":%q}}}`, promotePane, tc.existing)},
				fakecli.Rule{Argv: []string{"pane", "report-metadata", promotePane, "--source", "soho"}, ArgvPrefix: true})
			sd := f.stateDir(t)
			promoteRoster(t, sd, "sub-orch")
			code, out, stderr := runPromote(t, f, f.env)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			res := parsePromoteOut(t, out)
			if res.Title != tc.want || res.TitleKept {
				t.Fatalf("the custom title must be preserved under orchestrator: got %+v want %q", res, tc.want)
			}
			if !hasCall(promoteCalls(t, f.env), "pane", "report-metadata", promotePane, "--source", "soho", "--title", tc.want) {
				t.Fatalf("the preserved title was not set: %v", promoteCalls(t, f.env))
			}
		})
	}
}

func TestPromoteIdempotentAlreadyPromoted(t *testing.T) {
	f, _ := promoteFixture(t, "orchestrator", "working",
		fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":"orchestrator: existing"}}}`, promotePane)})
	sd := f.stateDir(t)
	// No row for the caller pane; a sibling worker row that must survive.
	core.RosterAppend(sd, []string{"impl-1", "ws:p1", "claude", "implementer", "anthropic", "0", "/repo", "impl-start", "claude-opus-5-5", "ask", "implementer", "build"})
	before := rosterRaw(t, sd)
	code, out, stderr := runPromote(t, f, f.env)
	if code != 0 {
		t.Fatalf("an already-promoted caller must be verified, not refused: code=%d stderr=%q", code, stderr)
	}
	res := parsePromoteOut(t, out)
	if !res.AlreadyPromoted || res.RosterRow != "absent" || res.Name != "orchestrator" || res.Title != "orchestrator: existing" || !res.TitleKept {
		t.Fatalf("unexpected result: %+v", res)
	}
	if rosterRaw(t, sd) != before {
		t.Fatal("the idempotent path wrote the roster")
	}
	if hasCallPrefix(promoteCalls(t, f.env), "agent", "rename") || hasCallPrefix(promoteCalls(t, f.env), "pane", "report-metadata") {
		t.Fatal("the idempotent path renamed the agent or overwrote the title")
	}
	assertOnlyScopeCalls(t, promoteCalls(t, f.env))
}

func TestPromoteRefusesAForeignCaller(t *testing.T) {
	f, _ := promoteFixture(t, "worker-x", "working")
	sd := f.stateDir(t)
	core.RosterAppend(sd, []string{"impl-1", "ws:p1", "claude", "implementer", "anthropic", "0", "/repo", "impl-start", "claude-opus-5-5", "ask", "implementer", "build"})
	before := rosterRaw(t, sd)
	code, _, stderr := runPromote(t, f, f.env)
	if code != 3 || !strings.Contains(stderr, "is not the orchestrator") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if rosterRaw(t, sd) != before {
		t.Fatal("a refused caller must not touch the roster")
	}
	if hasCallPrefix(promoteCalls(t, f.env), "agent", "rename") || hasCallPrefix(promoteCalls(t, f.env), "pane", "report-metadata") {
		t.Fatal("a refused caller got a rename or title")
	}
	assertOnlyScopeCalls(t, promoteCalls(t, f.env))
}

func TestPromoteRefusesANonSubOrchestratorRow(t *testing.T) {
	f, _ := promoteFixture(t, "impl-1", "working")
	sd := f.stateDir(t)
	core.RosterAppend(sd, []string{"impl-1", promotePane, "claude", "implementer", "anthropic", "0", "/repo", "impl-start", "claude-opus-5-5", "ask", "implementer", "build"})
	code, _, stderr := runPromote(t, f, f.env)
	if code != 3 || !strings.Contains(stderr, "registered as 'impl-1' with role 'implementer'") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// TestPromoteRefusesAStaleInheritedContext covers the moved-pane evidence:
// the inherited env still names the old pane/workspace while the native
// server reports the new one. The cross-workspace case is a refusal before
// any mutation; the same-workspace stale pane case proceeds with the
// actual native pane and tab.
func TestPromoteRefusesAStaleInheritedContext(t *testing.T) {
	t.Run("cross-workspace is a refusal", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working",
			fakecli.Rule{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"pane_id":"w1H:p1","tab_id":"w1H:t1","workspace_id":"w1H"}}}`})
		env := f.env.Clone()
		env["HERDR_PANE_ID"] = "w14:p8E"
		env["HERDR_WORKSPACE_ID"] = "w14"
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		before := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, env)
		if code != 4 || !strings.Contains(stderr, "stale cross-workspace context") || !strings.Contains(stderr, "'w14'") || !strings.Contains(stderr, "'w1H'") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != before {
			t.Fatal("a stale cross-workspace context must not write the roster")
		}
		calls := promoteCalls(t, f.env)
		assertOnlyScopeCalls(t, calls)
		if hasCallPrefix(calls, "agent", "rename") || hasCallPrefix(calls, "pane", "report-metadata") {
			t.Fatal("a stale cross-workspace context must not rename or retitle")
		}
	})
	t.Run("a stale pane in the same workspace uses the actual pane", func(t *testing.T) {
		f, wantTitle := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		env := f.env.Clone()
		env["HERDR_PANE_ID"] = "ws:pOld"
		env["HERDR_TAB_ID"] = "ws:tOld"
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		code, out, stderr := runPromote(t, f, env)
		if code != 0 {
			t.Fatalf("a stale inherited pane in the same workspace must not refuse: code=%d stderr=%q", code, stderr)
		}
		res := parsePromoteOut(t, out)
		if res.PaneID != promotePane || res.TabID != "ws:tS" {
			t.Fatalf("the success must use the actual native pane and tab: %+v", res)
		}
		if res.Title != wantTitle {
			t.Fatalf("unexpected title: %+v", res)
		}
		calls := promoteCalls(t, f.env)
		for _, c := range calls {
			for _, a := range c.Argv {
				if a == "ws:pOld" || a == "ws:tOld" {
					t.Fatalf("the stale inherited pane/tab was used for a mutation: %v", c.Argv)
				}
			}
		}
		if !hasCall(calls, "agent", "rename", promotePane, "orchestrator") {
			t.Fatalf("the actual pane was not renamed: %v", calls)
		}
	})
	t.Run("missing context is a refusal", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working",
			fakecli.Rule{Argv: []string{"pane", "current", "--current"}, Code: 1, Stderr: "pane gone"})
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		before := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "cannot resolve the native caller context") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != before {
			t.Fatal("an unresolvable context must not write the roster")
		}
		calls := promoteCalls(t, f.env)
		assertOnlyScopeCalls(t, calls)
		if hasCallPrefix(calls, "agent", "get") || hasCallPrefix(calls, "agent", "rename") || hasCallPrefix(calls, "pane", "get") {
			t.Fatalf("an unresolvable context must not probe the agent or pane: %v", calls)
		}
	})
	t.Run("an empty pane id is a refusal", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working",
			fakecli.Rule{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"pane_id":"","tab_id":"ws:tS","workspace_id":"ws"}}}`})
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "no pane id or workspace id") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("the inherited workspace may be absent", func(t *testing.T) {
		f, wantTitle := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		env := f.env.Clone()
		delete(env, "HERDR_WORKSPACE_ID")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		code, out, stderr := runPromote(t, f, env)
		if code != 0 {
			t.Fatalf("an absent inherited workspace must not refuse: code=%d stderr=%q", code, stderr)
		}
		if parsePromoteOut(t, out).Title != wantTitle {
			t.Fatalf("unexpected output %q", out)
		}
	})
}

func TestPromoteRefusesAStaleOrMismatchedScope(t *testing.T) {
	t.Run("workspace mismatch", func(t *testing.T) {
		f := staleFixture(t, `{"result":{"agent":{"name":"sub-orch","pane_id":"ws:pS","workspace_id":"other-ws","cwd":%s,"agent_status":"working"}}}`)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "reports workspace other-ws") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("pane mismatch", func(t *testing.T) {
		f := staleFixture(t, `{"result":{"agent":{"name":"sub-orch","pane_id":"ws:pX","workspace_id":"ws","cwd":%s,"agent_status":"working"}}}`)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "reports pane ws:pX") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("different project root", func(t *testing.T) {
		f := staleFixture(t, `{"result":{"agent":{"name":"sub-orch","pane_id":"ws:pS","workspace_id":"ws","cwd":"/elsewhere","agent_status":"working"}}}`)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "different project root") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("identity changed", func(t *testing.T) {
		f, _ := promoteFixture(t, "imposter", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "identity changed") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("agent get fails", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working",
			fakecli.Rule{Argv: []string{"agent", "get", promotePane}, Code: 1, Stderr: "socket gone"})
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		before := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "cannot verify the live agent") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != before {
			t.Fatal("an unverifiable scope must not write the roster")
		}
	})
}

// staleFixture is a fixture whose agent get answers with the given JSON
// (the %s placeholder is filled with the fixture repo path) and whose
// roster holds the sub-orchestrator row.
func staleFixture(t *testing.T, agentGetTemplate string) *copiesFixture {
	t.Helper()
	f := newCopiesFixture(t, nil)
	fakeDir := t.TempDir()
	stdout := agentGetTemplate
	if strings.Contains(stdout, "%s") {
		repoJSON, err := json.Marshal(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		stdout = fmt.Sprintf(stdout, string(repoJSON))
	}
	rules := []fakecli.Rule{
		{Argv: []string{"pane", "current", "--current"}, Stdout: promotePaneCurrent},
		{Argv: []string{"agent", "get", promotePane}, Stdout: stdout},
	}
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_FAKECLI_CONFIG"] = fakeDir
	f.env["PATH"] = fakeDir + string(os.PathListSeparator) + f.env.Get("PATH")
	f.env["HERDR_PANE_ID"] = promotePane
	ctx := core.LoadConfig(f.env, f.cwd)
	core.StateDir(&ctx, f.env, f.cwd)
	sd := f.stateDir(t)
	core.RosterAppend(sd, []string{"sub-orch", promotePane, "codex", "sub-orchestrator", "openai", "0", "/repo", "sub-start", "gpt-5.4", "ask", "sub-orchestrator", "-"})
	return f
}

func TestPromoteRefusesAnActiveCollaboration(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working")
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	a := collaboration.Assignment{
		Generation: 1, Phase: collaboration.Reviewing, Round: 1, MaxRounds: 3,
		Author:    collaboration.Participant{Name: "sub-orch", Pane: promotePane, Role: "sub-orchestrator", Started: "2026-01-01T00:00:00", Session: "s1", Cwd: f.repo, Family: "openai"},
		Reviewer:  collaboration.Participant{Name: "reviewer", Pane: "ws:pR", Role: "reviewer", Started: "2026-01-01T00:00:00", Session: "s2", Cwd: f.repo, Family: "anthropic"},
		Workspace: "ws", Root: f.repo, BriefHash: "x",
		Paths: []string{filepath.Join(f.repo, "a.go")},
	}
	if _, err := (collaboration.Store{StateDir: sd}).Create(a); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runPromote(t, f, f.env)
	if code != 3 || !strings.Contains(stderr, "active collaboration "+a.ID) {
		t.Fatalf("code=%d stderr=%q (assignment %s)", code, stderr, a.ID)
	}
}

func TestPromoteRefusesAnUnreadableCollaboration(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working")
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	dir := filepath.Join(sd, "collaboration", "c-0123456789abcdef")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assignment.json"), []byte("{ broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runPromote(t, f, f.env)
	if code != 4 || !strings.Contains(stderr, "collaboration state is unreadable") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestPromoteRefusesAnUnresolvedTaskOrReport(t *testing.T) {
	t.Run("no marker, no report", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing")
		code, _, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "not resolved (no completion marker, no report)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("marker but empty report pointer", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		code, _, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "report pointer is empty") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("resolved task with a report is not a refusal", func(t *testing.T) {
		f, wantTitle := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		report := filepath.Join(f.root, "report.md")
		if err := os.WriteFile(report, []byte("report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runPromote(t, f, f.env)
		if code != 0 {
			t.Fatalf("a resolved task must not refuse: code=%d stderr=%q", code, stderr)
		}
		if parsePromoteOut(t, out).Title != wantTitle {
			t.Fatalf("unexpected output %q", out)
		}
	})
}

func writeTask(t *testing.T, sd, agent, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sd, "task-"+agent), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPromoteRefusesCallerOwnedResources proves the GC-orphan guard: while
// the caller's registered copies still exist or its processes still run,
// the promotion is refused and the roster, the name, the title and the
// registries are all left untouched, with no gc/release/stop invoked.
func TestPromoteRefusesCallerOwnedResources(t *testing.T) {
	t.Run("a present copy is a refusal", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		copyPath := filepath.Join(f.root, "copies", "c1")
		if err := os.MkdirAll(copyPath, 0o700); err != nil {
			t.Fatal(err)
		}
		registryFile(t, sd, "copies.tsv", "path\towner\tpane\tcreated\tsource\torigin\n"+copyPath+"\tsub-orch\tws:pS\t2026-10-06T00:00:00Z\tmutation-copy\tmutation-copy\n")
		copiesBefore := registryBody(t, sd, "copies.tsv")
		rosterBefore := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "caller-owned copies still exist: "+copyPath) {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != rosterBefore {
			t.Fatal("a refused promotion removed the roster row")
		}
		if registryBody(t, sd, "copies.tsv") != copiesBefore {
			t.Fatal("a refused promotion touched the copies registry")
		}
		calls := promoteCalls(t, f.env)
		if hasCallPrefix(calls, "agent", "rename") || hasCallPrefix(calls, "pane", "report-metadata") {
			t.Fatalf("a refused promotion renamed or retitled: %v", calls)
		}
		assertOnlyScopeCalls(t, calls)
	})
	t.Run("a queued copy joins the registry and is a refusal (DJA-323)", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		copyPath := filepath.Join(f.root, "copies", "c1")
		if err := os.MkdirAll(copyPath, 0o700); err != nil {
			t.Fatal(err)
		}
		queue, err := core.QueueCopy(f.env, filepath.Base(sd), sd, core.CopyRow{Path: copyPath, Owner: "sub-orch", Pane: "ws:pS", Created: "2026-10-06T00:00:00Z", Source: "-", Origin: "mutation-copy"})
		if err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "caller-owned copies still exist: "+copyPath) {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rows, err := core.ReadCopies(sd); err != nil || len(rows) != 1 || rows[0].Path != copyPath {
			t.Fatalf("registry rows=%#v err=%v; want the queued copy", rows, err)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue stays: %v", err)
		}
	})
	t.Run("a proven-missing copy keeps its record and passes", func(t *testing.T) {
		f, wantTitle := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		missing := filepath.Join(f.root, "copies", "gone")
		registryFile(t, sd, "copies.tsv", "path\towner\tpane\tcreated\tsource\torigin\n"+missing+"\tsub-orch\tws:pS\t2026-10-06T00:00:00Z\tmutation-copy\tmutation-copy\n")
		copiesBefore := registryBody(t, sd, "copies.tsv")
		code, out, stderr := runPromote(t, f, f.env)
		if code != 0 {
			t.Fatalf("a proven-missing copy must not refuse: code=%d stderr=%q", code, stderr)
		}
		if parsePromoteOut(t, out).Title != wantTitle {
			t.Fatalf("unexpected output %q", out)
		}
		if registryBody(t, sd, "copies.tsv") != copiesBefore {
			t.Fatal("the historical copy record was deleted")
		}
	})
	t.Run("an unreadable copy registry fails closed", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		registryFile(t, sd, "copies.tsv", "path\towner\tpane\tcreated\tsource\torigin\nbroken\tline\n")
		rosterBefore := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "copies registry is unreadable") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != rosterBefore {
			t.Fatal("an unreadable registry must not write the roster")
		}
	})
	t.Run("a running process is a refusal", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		pid := os.Getpid()
		started, live := platform.ReadProc(pid, f.env)
		if live != platform.ProcRunning {
			t.Fatalf("the test process must read as running for the preflight (live=%v)", live)
		}
		registryFile(t, sd, "procs.tsv", "pid\tstarted\tname\towner\tpane\tcreated\n"+fmt.Sprintf("%d\t%s\tworker\tsub-orch\tws:pS\t2026-10-06T00:00:00Z\n", pid, started))
		procsBefore := registryBody(t, sd, "procs.tsv")
		rosterBefore := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "processes are still running: "+fmt.Sprintf("%d (worker)", pid)) {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != rosterBefore {
			t.Fatal("a refused promotion removed the roster row")
		}
		if registryBody(t, sd, "procs.tsv") != procsBefore {
			t.Fatal("a refused promotion touched the procs registry")
		}
		calls := promoteCalls(t, f.env)
		if hasCallPrefix(calls, "agent", "rename") || hasCallPrefix(calls, "pane", "report-metadata") {
			t.Fatalf("a refused promotion renamed or retitled: %v", calls)
		}
		assertOnlyScopeCalls(t, calls)
	})
	t.Run("a proven-gone process keeps its record and passes", func(t *testing.T) {
		f, wantTitle := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		registryFile(t, sd, "procs.tsv", "pid\tstarted\tname\towner\tpane\tcreated\n99999\tMon Oct  5 00:00:00 2026\tworker\tsub-orch\tws:pS\t2026-10-06T00:00:00Z\n")
		procsBefore := registryBody(t, sd, "procs.tsv")
		code, out, stderr := runPromote(t, f, f.env)
		if code != 0 {
			t.Fatalf("a proven-gone process must not refuse: code=%d stderr=%q", code, stderr)
		}
		if parsePromoteOut(t, out).Title != wantTitle {
			t.Fatalf("unexpected output %q", out)
		}
		if registryBody(t, sd, "procs.tsv") != procsBefore {
			t.Fatal("the historical process record was deleted")
		}
	})
	t.Run("an unreadable process state fails closed", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working", promoteVerbRules()...)
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		// An unavailable native process snapshot must refuse the command on
		// every OS. Windows reads the kernel directly, so a fake ps cannot
		// supply this failure. Inject only the snapshot dependency, keeping
		// the real promotion checks and roster mutation path.
		oldReader := promoteProcRowState
		promoteProcRowState = func(core.ProcRow, platform.Env) string { return "unknown" }
		t.Cleanup(func() { promoteProcRowState = oldReader })
		env := f.env.Clone()
		registryFile(t, sd, "procs.tsv", "pid\tstarted\tname\towner\tpane\tcreated\n"+fmt.Sprintf("%d\tMon Oct  5 00:00:00 2026\tworker\tsub-orch\tws:pS\t2026-10-06T00:00:00Z\n", os.Getpid()))
		rosterBefore := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, env)
		if code != 4 || !strings.Contains(stderr, "unreadable; their state cannot be verified") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != rosterBefore {
			t.Fatal("an unverifiable process state must not write the roster")
		}
	})
	t.Run("an unreadable procs registry fails closed", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		registryFile(t, sd, "procs.tsv", "pid\tstarted\tname\towner\tpane\tcreated\nnot-a-pid\tx\ty\tz\tw\tv\n")
		rosterBefore := rosterRaw(t, sd)
		code, _, stderr := runPromote(t, f, f.env)
		if code != 4 || !strings.Contains(stderr, "procs registry is unreadable") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if rosterRaw(t, sd) != rosterBefore {
			t.Fatal("an unreadable registry must not write the roster")
		}
	})
}

func registryBody(t *testing.T, sd, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sd, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPromotePartialOnRenameFailure(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working",
		fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}, Code: 1, Stderr: "rename failed: name taken"})
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	code, _, stderr := runPromote(t, f, f.env)
	if code != 6 || !strings.Contains(stderr, "partial — the worker roster row was removed, but the rename to 'orchestrator' failed (rename failed: name taken)") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	// The row is gone (the promotion state is forward, visible and
	// retryable) and the title step was skipped.
	if strings.Contains(rosterRaw(t, sd), "sub-orch\t"+promotePane) {
		t.Fatal("the worker row survived a partial rename")
	}
	if hasCallPrefix(promoteCalls(t, f.env), "pane", "report-metadata") {
		t.Fatal("the title step ran after a failed rename")
	}
	assertOnlyScopeCalls(t, promoteCalls(t, f.env))
}

func TestPromotePartialOnTitleFailure(t *testing.T) {
	t.Run("title set fails", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working",
			fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}},
			fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":""}}}`, promotePane)},
			fakecli.Rule{Argv: []string{"pane", "report-metadata", promotePane, "--source", "soho"}, ArgvPrefix: true, Code: 1, Stderr: "metadata rejected"})
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		code, _, stderr := runPromote(t, f, f.env)
		if code != 6 || !strings.Contains(stderr, "partial — the roster row is removed and the name is set, but the title set failed") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if strings.Contains(rosterRaw(t, sd), "sub-orch\t"+promotePane) {
			t.Fatal("the worker row survived a partial title set")
		}
		assertOnlyScopeCalls(t, promoteCalls(t, f.env))
	})
	t.Run("title unreadable", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working",
			fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}},
			fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Code: 1, Stderr: "pane query failed"})
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		code, _, stderr := runPromote(t, f, f.env)
		if code != 6 || !strings.Contains(stderr, "partial — the roster row is removed, but the pane title could not be read") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
}

func TestPromoteRefusesACorruptRoster(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working")
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	// A duplicate row for the same pane: the roster is corrupt.
	core.RosterAppend(sd, []string{"sub-orch", promotePane, "codex", "sub-orchestrator", "openai", "0", "/repo", "sub-start", "gpt-5.4", "ask", "sub-orchestrator", "-"})
	code, _, stderr := runPromote(t, f, f.env)
	if code != 4 || !strings.Contains(stderr, "roster is corrupt") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, c := range promoteCalls(t, f.env) {
		// The scope read (pane current) happens first; a corrupt roster
		// must fail before the agent probe and every mutation.
		if len(c.Argv) >= 2 && (c.Argv[0] == "agent" || (c.Argv[0] == "pane" && c.Argv[1] != "current")) {
			t.Fatalf("a corrupt roster must fail before the agent probe: %v", c.Argv)
		}
	}
}

func TestPromoteUsageAndWorkspaceResolution(t *testing.T) {
	t.Run("extra arguments are a usage error", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		code, _, stderr := runPromote(t, f, f.env, "--target", "w14:p9")
		if code != 2 || !strings.Contains(stderr, "takes no arguments") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if calls := promoteCalls(t, f.env); len(calls) != 0 {
			t.Fatalf("a usage error must probe nothing: %v", calls)
		}
	})
}

func TestPromoteSubsequentInitRetainsTheOrchestratorIdentity(t *testing.T) {
	for _, name := range []string{"orchestrator", "orchestrator-2"} {
		t.Run(name, func(t *testing.T) {
			f, _ := promoteFixture(t, name, "idle")
			ctx := core.LoadConfig(f.env, f.cwd)
			got := spawn.EnsureOrchestratorName(&ctx, f.env, f.cwd, "init")
			if got != name {
				t.Fatalf("init renamed a retained name: got %q want %q", got, name)
			}
			if calls := promoteCalls(t, f.env); hasCall(calls, "agent", "rename", promotePane, name) {
				t.Fatalf("init issued a rename for a retained name: %v", calls)
			}
		})
	}
	t.Run("a generic name without a row is renamed like init does", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "idle",
			fakecli.Rule{Argv: []string{"agent", "rename", promotePane, "orchestrator"}})
		ctx := core.LoadConfig(f.env, f.cwd)
		got := spawn.EnsureOrchestratorName(&ctx, f.env, f.cwd, "init")
		if got != "orchestrator" {
			t.Fatalf("init did not rename the generic name: %q", got)
		}
		if !hasCall(promoteCalls(t, f.env), "agent", "rename", promotePane, "orchestrator") {
			t.Fatal("init did not use the herdr rename")
		}
	})
}

// makeUnreadableForTest blocks actual file reads and restores them in cleanup.
func makeUnreadableForTest(t *testing.T, path string) func() {
	t.Helper()
	return testutil.DenyFileReads(t, path)
}

// assertRefusedNothing proves a refusal mutated nothing on the herdr side:
// no rename, no title set, no success JSON.
func assertRefusedNothing(t *testing.T, env platform.Env, out string) {
	t.Helper()
	if out != "" {
		t.Fatalf("a refusal must not print success JSON: %q", out)
	}
	calls := promoteCalls(t, env)
	if hasCallPrefix(calls, "agent", "rename") || hasCallPrefix(calls, "pane", "report-metadata") {
		t.Fatalf("a refusal renamed or retitled: %v", calls)
	}
	assertOnlyScopeCalls(t, calls)
}

// TestPromoteRefusesAnUnreadableRoster is the fail-closed roster read:
// an existing but unreadable agents.tsv refuses exit 4 before the live
// agent probe, before every obligation, before the title and before any
// success JSON. It is never an empty roster and never already-promoted.
func TestPromoteRefusesAnUnreadableRoster(t *testing.T) {
	t.Run("an orchestrator-shaped name is not already-promoted", func(t *testing.T) {
		f, _ := promoteFixture(t, "orchestrator", "idle")
		sd := f.stateDir(t)
		core.RosterAppend(sd, []string{"impl-1", "ws:p1", "claude", "implementer", "anthropic", "0", "/repo", "impl-start", "claude-opus-5-5", "ask", "implementer", "build"})
		path := filepath.Join(sd, "agents.tsv")
		before := rosterRaw(t, sd)
		releaseReadDenial := makeUnreadableForTest(t, path)
		code, out, stderr := runPromote(t, f, f.env)
		releaseReadDenial()
		if code != 4 || !strings.Contains(stderr, "roster is unreadable") {
			t.Fatalf("code=%d stderr=%q (an unreadable roster is not an empty roster and not already-promoted)", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if hasCallPrefix(promoteCalls(t, f.env), "agent", "get") {
			t.Fatalf("an unreadable roster must refuse before the live agent probe: %v", promoteCalls(t, f.env))
		}
		if rosterRaw(t, sd) != before {
			t.Fatal("an unreadable roster was rewritten")
		}
	})
	t.Run("a generic name is a refusal, not an empty roster", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		path := filepath.Join(sd, "agents.tsv")
		before := rosterRaw(t, sd)
		releaseReadDenial := makeUnreadableForTest(t, path)
		code, out, stderr := runPromote(t, f, f.env)
		releaseReadDenial()
		if code != 4 || !strings.Contains(stderr, "roster is unreadable") {
			t.Fatalf("code=%d stderr=%q (an unreadable roster refuses exit 4, not the empty-roster exit 3)", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if hasCallPrefix(promoteCalls(t, f.env), "agent", "get") {
			t.Fatalf("an unreadable roster must refuse before the live agent probe: %v", promoteCalls(t, f.env))
		}
		if rosterRaw(t, sd) != before {
			t.Fatal("an unreadable roster was rewritten")
		}
	})
}

// TestPromoteMissingRosterIsAnEmptyRoster is the control for the strict
// read: an absent agents.tsv is an empty roster, so an orchestrator-named
// caller takes the idempotent already-promoted path and the roster file is
// never created.
func TestPromoteMissingRosterIsAnEmptyRoster(t *testing.T) {
	f, _ := promoteFixture(t, "orchestrator", "idle",
		fakecli.Rule{Argv: []string{"pane", "get", promotePane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"title":"orchestrator: existing"}}}`, promotePane)})
	sd := f.stateDir(t)
	core.RosterAppend(sd, []string{"impl-1", "ws:p1", "claude", "implementer", "anthropic", "0", "/repo", "impl-start", "claude-opus-5-5", "ask", "implementer", "build"})
	rosterPath := filepath.Join(sd, "agents.tsv")
	if err := os.Remove(rosterPath); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runPromote(t, f, f.env)
	if code != 0 {
		t.Fatalf("an absent roster is an empty roster, not a refusal: code=%d stderr=%q", code, stderr)
	}
	res := parsePromoteOut(t, out)
	if !res.AlreadyPromoted || res.RosterRow != "absent" || res.Name != "orchestrator" || res.Title != "orchestrator: existing" || !res.TitleKept {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(rosterPath); !os.IsNotExist(err) {
		t.Fatalf("the idempotent path created the roster file: %v", err)
	}
	if hasCallPrefix(promoteCalls(t, f.env), "agent", "rename") || hasCallPrefix(promoteCalls(t, f.env), "pane", "report-metadata") {
		t.Fatalf("the idempotent path renamed or retitled: %v", promoteCalls(t, f.env))
	}
}

// TestPromoteRefusesAnUnreadableTask is the fail-closed task read: an
// existing but unreadable task file refuses exit 4 (never "no task"), with
// the row, the name and the title untouched.
func TestPromoteRefusesAnUnreadableTask(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working")
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
	taskPath := filepath.Join(sd, "task-sub-orch")
	before := rosterRaw(t, sd)
	releaseReadDenial := makeUnreadableForTest(t, taskPath)
	code, out, stderr := runPromote(t, f, f.env)
	releaseReadDenial()
	if code != 4 || !strings.Contains(stderr, "task file of 'sub-orch' is unreadable") {
		t.Fatalf("code=%d stderr=%q (an unreadable task is not an absent task)", code, stderr)
	}
	assertRefusedNothing(t, f.env, out)
	if rosterRaw(t, sd) != before {
		t.Fatal("a refused promotion removed the roster row")
	}
}

// TestPromoteRefusesAMalformedReportPointer is the pointer-validation
// consumer: a resolved task only resolves when the pointer names an
// absolute, existing, readable, nonempty, regular report file. Every
// other pointer refuses exit 3 with the row, the name and the title
// untouched.
func TestPromoteRefusesAMalformedReportPointer(t *testing.T) {
	t.Run("whitespace-only pointer is empty", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte("   \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rosterRaw(t, sd)
		code, out, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "report pointer is empty") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if rosterRaw(t, sd) != before {
			t.Fatal("a refused promotion removed the roster row")
		}
	})
	t.Run("a relative pointer is not absolute", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte("report.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rosterRaw(t, sd)
		code, out, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "not an absolute path") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if rosterRaw(t, sd) != before {
			t.Fatal("a refused promotion removed the roster row")
		}
	})
	t.Run("a missing absolute target is not a report", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte(filepath.Join(f.root, "no-such-report.md")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rosterRaw(t, sd)
		code, out, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "does not name an existing file") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if rosterRaw(t, sd) != before {
			t.Fatal("a refused promotion removed the roster row")
		}
	})
	t.Run("an empty report file is not a report", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		empty := filepath.Join(f.root, "empty-report.md")
		if err := os.WriteFile(empty, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte(empty+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rosterRaw(t, sd)
		code, out, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "names an empty report") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if rosterRaw(t, sd) != before {
			t.Fatal("a refused promotion removed the roster row")
		}
	})
	t.Run("a directory target is not a regular report", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		dirReport := filepath.Join(f.root, "dir-report")
		if err := os.MkdirAll(dirReport, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte(dirReport+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rosterRaw(t, sd)
		code, out, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "is not a regular file") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if rosterRaw(t, sd) != before {
			t.Fatal("a refused promotion removed the roster row")
		}
	})
	t.Run("an unreadable report target is not readable", func(t *testing.T) {
		f, _ := promoteFixture(t, "sub-orch", "working")
		sd := f.stateDir(t)
		promoteRoster(t, sd, "sub-orch")
		writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
		locked := filepath.Join(f.root, "locked-report.md")
		if err := os.WriteFile(locked, []byte("report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		makeUnreadableForTest(t, locked)
		if err := os.WriteFile(core.LastReportPath(sd, "sub-orch"), []byte(locked+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rosterRaw(t, sd)
		code, out, stderr := runPromote(t, f, f.env)
		if code != 3 || !strings.Contains(stderr, "is not readable") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		assertRefusedNothing(t, f.env, out)
		if rosterRaw(t, sd) != before {
			t.Fatal("a refused promotion removed the roster row")
		}
	})
}

// TestPromoteRefusesAnUnreadableReportPointer is the pointer-read
// consumer: an existing but unreadable last-report file refuses exit 4
// (fail closed), never "empty pointer".
func TestPromoteRefusesAnUnreadableReportPointer(t *testing.T) {
	f, _ := promoteFixture(t, "sub-orch", "working")
	sd := f.stateDir(t)
	promoteRoster(t, sd, "sub-orch")
	writeTask(t, sd, "sub-orch", "sub-orch: Build the thing ✓")
	report := filepath.Join(f.root, "report.md")
	if err := os.WriteFile(report, []byte("report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pointerPath := core.LastReportPath(sd, "sub-orch")
	if err := os.WriteFile(pointerPath, []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := rosterRaw(t, sd)
	releaseReadDenial := makeUnreadableForTest(t, pointerPath)
	code, out, stderr := runPromote(t, f, f.env)
	releaseReadDenial()
	if code != 4 || !strings.Contains(stderr, "report pointer") || !strings.Contains(stderr, "is unreadable") {
		t.Fatalf("code=%d stderr=%q (an unreadable pointer file is not an empty pointer)", code, stderr)
	}
	assertRefusedNothing(t, f.env, out)
	if rosterRaw(t, sd) != before {
		t.Fatal("a refused promotion removed the roster row")
	}
}
